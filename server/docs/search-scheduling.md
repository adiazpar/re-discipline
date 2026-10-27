# Shared retrieval capacity

The local agent calls the plugin's MCP tools. The native client sends a scoped
HTTP operation, not an executable or database connection. `query` runs the shared
retrieval engine on the server and returns snippets, finding IDs and fingerprints and
provenance (default eight hits, maximum fifty). `knowledge.get` reads the selected
full finding from PostgreSQL. Normal research is search, inspect a few sources,
then ask a narrower question if needed. No corpus synchronization is involved.
Searches, community descriptions and full-document reads enter the
same scheduler. Searches and full-document reads therefore compete fairly instead
of letting follow-up fetches bypass resource controls.

The server imports the same Go retrieval engine used by the plugin's native
client; it does not launch a new executable for every search. One server process
owns multiple concurrent workers and a separate index directory for each
community. Sharing workers does not combine indexes or broaden a request's scope.
Separate worker processes per community would provide an additional crash
boundary, but would still share the host CPU and memory and need coordinated
admission limits. The current implementation shares a resource budget so quiet
communities leave capacity available to busy ones. Process isolation is not a
substitute for authorization or testing retrieval correctness under load.

## Resource budget

At startup the service uses Go's available CPU parallelism, including container
CPU quotas, and a Go memory budget. The memory default is 75% of the cgroup limit,
falling back to host RAM; explicit GOMEMLIMIT takes precedence. This is a soft GC
target, not a process RSS limit. Container memory limits remain the hard boundary.
Unbounded or unavailable memory detection uses a conservative 384 MiB search
budget. On a shared bare-metal machine, set a process/container memory budget
because installed RAM does not describe what other applications need.

Automatic active work is the minimum of twice CPU parallelism, one slot per
64 MiB of Go budget, and 32 slots (at least one). This is a starting heuristic,
not measured per-query memory or a throughput guarantee. Go rounds fractional
CPU quotas and can retain a minimum parallelism of two. The scheduler uses that
runtime value. Waiting capacity is bounded by 256 requests per worker, one eighth
of the Go budget at an estimated 32 KiB/request, and 4,096 requests. Queries are capped at 8 KiB; other read operations retain their existing 2 MiB
request bound. The queue also charges a 32 KiB allowance plus twice the actual
payload bytes against a shared memory budget, so large document-matching batches
consume more waiting capacity. This accounts for retained payloads and overhead;
it is not an exact measurement of Go object or network-buffer memory.

The database pool scales with workers and includes spare connections for
credentials, control operations and background work. Explicit worker overrides
are capped below the database connection budget. Waiting readers hold neither a
database connection nor an ingress-processing slot. Operator requests have their
own four ingress slots, so queued readers do not lock out revocation or monitoring. A verified operator
also bypasses the public IP rate bucket; its reads still use the fair scheduler.
Ingress has a bounded one-second wait. Verified readers have no per-minute
credential or IP quota. Anonymous setup and invalid credentials share a separate
IP abuse bucket (default 600/minute); exhausting it cannot lock out valid readers
or operators on that IP. Its bookkeeping is bounded to 10,000 active IP buckets.
It does not prove identity or prevent distributed abuse. The old
MAX_REQUESTS_PER_CONNECTION_MINUTE and MAX_REQUESTS_PER_IP_MINUTE settings do not
apply to the production search handler.

Budgets are resolved at startup: restart after resizing the container. Optional
settings are listed in `.env.example`. There is no fixed per-community active cap.

## Optional summary counts

`community.stats` returns only the connected community's UUID, stored document
count, knowledge-text bytes and observation time. It reads counters alongside
the existing authorization row, without scanning documents or opening SQLite.
Each request still verifies its signature, scope and lifecycle, and records its
replay nonce. It does not expose members, keys, other communities or server load.

Metadata uses a separate pool: at least one worker, otherwise the minimum of
CPU parallelism / 4, database connections / 8 and four. Up to eight waiters per
worker wait at most 100 ms; execution has a one-second deadline. Metadata waiters
release normal ingress capacity. Overload returns 503 with Retry-After: 5.
Search execution slots are never consumed by metadata. Authentication, network
and database resources remain shared, so this is bounded overhead rather than
absolute performance isolation.

The client coalesces concurrent summary requests with a project file lock and
caches counts for five minutes, keyed by project, origin, community and public
key scope. It never caches documents or secrets. A cached observation does not
prove current access, availability or index freshness. Expired snapshots are not
served on errors. Disconnect removes its entry; later refreshes prune expired
entries. The summary skill requests each link once and omits unavailable counts.

## Sharing across communities

A FIFO holds waiting operations for each verified reader connection. A heap selects communities
with the fewest currently occupied workers; ties use virtual service charged at
dispatch and corrected to measured execution time. An exponentially weighted
moving estimate predicts the next operation's cost. The heap dispatch is
O(log active communities). This measures occupied-worker time, including I/O,
not exact CPU percentages or equal bandwidth. A second heap applies the same
policy to connections within the chosen community. Each operation is charged at
both levels; more connections cannot increase a community's server share.
Operator reads form one reader group per community and compete for retrieval
capacity normally. A single reader can borrow every available worker when idle.
A globally bounded, one-minute cost history prevents communities and readers
from resetting service debt by briefly going idle. Queues use verified community
UUIDs and connection IDs, never client-supplied reader labels. Normal dispatch costs
O(log active communities + log active connections in the chosen community).

This is connection fairness, not unique-person detection. A project key connects
to one community; reconnecting that key reuses its connection. There is no reader
account system or requirement to identify a human across projects. Someone can
generate another key, but additional connections cannot increase their community's
server share. IPs and device fingerprints are not reliable person identities.
See [connection keys](connections.md) for scope, revocation and expiry.

| Situation | Behavior |
| --- | --- |
| One active community | It can borrow every worker and all available waiting space. |
| One active reader | It can use available community/server capacity without a minute quota. |
| One reader floods its community | Other readers receive turns as workers free; a shorter reader queue can displace a newest waiter from a strictly longer reader queue when admission is full. |
| Several busy communities | Free workers go to under-served communities; expensive operations accrue more virtual service. |
| One busy, several quiet | Quiet arrivals get turns as workers free; their unused share is immediately borrowable. |
| More communities than workers | Waiting communities rotate through the available pool; none owns a permanent slot. |
| One community fills the queue | A less represented community can replace a newest waiter from the longest reader queue in a strictly longer community queue. Community admission takes priority over per-reader admission. |
| Every queue is full and balanced | Reject additional work with 503 and Retry-After rather than grow memory indefinitely. |
| Cancelled or expired waiter | Remove it immediately and free its waiting capacity; retain only bounded recent cost history. |
| Credential revoked while waiting | PostgreSQL authorization runs again after admission and denies the operation. |
| Slow query or client | Execution and response-write deadlines bound slot occupancy; SQLite and PostgreSQL receive cancellation. |
| Read gets a temporary busy response or interrupted connection | The plugin retries at most twice, with positive jitter, Retry-After support and at most five seconds of retry sleep within the caller's deadline. Longer requested waits are returned to the caller. |
| Missing/corrupt index | A background worker rebuilds derived data; readers receive retryable 503 until it is ready. |

Work already running is not preempted. A newly active community can wait for a
running operation to finish or reach its deadline. Default queue and execution
budgets are ten seconds each. No scheduler can guarantee low latency under an
arrival rate above sustained capacity; bounded rejection remains necessary.
Retries preserve the origin, community, credential and payload. They never create
a connection to evade backpressure ; idempotent upload batches also retry using their original key and bytes. Authorization failures
are not busy responses. Public connections may renew after expiry; revoked keys
cannot self-renew. Private access requires a new invitation after expiry.
The client rejects explicit contradictory community IDs in search results,
including locations, instead of silently relabeling evidence.

## Retrieval optimizations and visibility

The shared FTS5/BM25 ranking remains unchanged. Each community has one derived
SQLite index with exact Markdown and fingerprint beside its search entries.
A community lock spans search and hydration so results cannot combine text from
one update with the fingerprint of another. Indexing applies bounded 32-document
transactions and rotates between communities; it never rebuilds the whole KB for
each upload. Queries return the available index without lag warnings. A cold or
deleted-content index returns a temporary unavailable response until ready.
Corrupt/obsolete index schemas are rebuilt from PostgreSQL by the worker.

Owner uploads use a separate fair queue with CPU/RAM-sized workers and a bounded
pending byte budget. Batches cap both document count and encoded bytes; the worker
slows indexing when readers are queued. No queued work holds a database connection.

Only the operator may call `server.stats` (with service URL, no community required).
It reports budgets, active/queued work per community, completions, rejected or
expired waiters, and aggregate wait/execution time. Successful reads expose
`Server-Timing: queue;dur=...`. These counters are process-local and reset on restart.
One server process owns a cache directory; horizontal replicas still need separate
caches and separate scheduling budgets. This implementation does not distribute
work across machines.

References: [Go's container-aware parallelism](https://go.dev/blog/container-aware-gomaxprocs)
and [automatic memory budgeting](https://github.com/KimMachineGun/automemlimit).
See [load testing](load-testing.md) for measurements and their limits.

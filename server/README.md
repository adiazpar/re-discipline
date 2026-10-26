# Operator-owned remote search service

The operator creates communities and uploads documents. Readers search one
explicitly connected community at a time using scoped read-only credentials.
Communities start private and may become public later. One PostgreSQL database
holds canonical records with row-level isolation; each community has a separate
SQLite search index. The service imports `../retrieval` directly. No second
engine implementation, GitHub identity provider or client corpus download is needed.

## Local setup

From the repository root:

```text
python3 scripts/community-local.py up --context orbstack
python3 scripts/community-local.py status
python3 scripts/community-local.py stop
```

Use `python` on Windows and another local Docker context when appropriate. Python,
Docker and Compose are hosting dependencies only. The Linux service is bundled
and built from the same retrieval source as the plugin. The
helper preserves storage and operator identity, binds host loopback, and keeps
PostgreSQL off host ports. Normal health reports `mode: ready`. Setup-only health
is available with `up --setup-only`.

## Deploying elsewhere

Build `server/Dockerfile` from the repository root. Provision PostgreSQL, run
`community migrate` with admin credentials, and `community bootstrap-runtime`
once to create a restricted runtime login. Keep admin credentials out of the
running service. Run `community serve` with the runtime DATABASE_URL, HTTPS
PUBLIC_URL, a stable SERVER_OPERATOR_ID UUID and a cryptographically random
SERVER_OPERATOR_TOKEN of at least 43 characters. Generate and store these through
your host's secret management; never share them with readers or put them in Git.
Configure HTTPS at a reverse proxy that preserves the public Host header. The local
helper does not provision remote hosts or certificates.

Managed deployments apply compatible updates on the owner’s next management
operation. Register remote deployment access through SSH during setup; reader
connections never carry it. See [updates and maintenance](docs/updates.md).

The legacy `serve-local` command remains an owner-only loopback compatibility mode;
new installations use `serve`. The production handler exposes only health and
`POST /v1/operations`. Legacy OAuth, browser dashboard, membership-role and corpus
export routes are removed. There is no internal legacy publishing handler.
Schema upgrades preserve current live knowledge UUIDs, then remove historical
bodies, submissions, review queues and membership-role tables. Active deletion
sessions are safely cancelled during this format migration; start a new preview.

## Permissions and traffic

Only the operator can create or change communities and upload documents. Public
`community.connect` registers a project's public key; private joining consumes a
single-use invitation from `access.create`. A project can link multiple communities, using a separate key for each link.
Client private keys stay in the OS credential store. Signed HTTPS requests bind
the method, exact public origin/path and body, with freshness and atomic replay
checks. The operator's separate credential remains in host secret storage.

Connections renew a 180-day inactivity lease on use, at most daily. Disconnect
removes access; abandoned records expire without a callback. Invitations expire
in seven days by default. Revocations retain a bounded 180-day tombstone. Indexed
background cleanup and PostgreSQL autovacuum prevent indefinite dead-record growth.
API allowlists and RLS enforce exact community scope, including queued revocation.
See [connection security and lifecycle](docs/connections.md) for the full policy.

Community deletion uses an operator-only preview → explicit archive choice →
community lock → optional verified download → final confirmation workflow.
The skill must pause for the human at both decision points. Only the selected
community is frozen. Other communities retain their scoped access and retrieval.
Knowledge-only archives stream as resumable Zstandard chunks directly to the
owner's disk. They exclude members, account IDs, keys, invitations, access policy,
audit logs and operational state; no archive is staged on the server. The plugin
bundles compression support. Cancelling or expiry reopens the community; neither
download nor expiry can delete it. After confirmation all community data/access
and derived indexes are removed, with restart-safe cleanup. Local research and
archives are retained. See the [archive/deletion workflow](../skills/community/references/deletion.md).

Searches and document reads share a CPU/memory-sized worker pool and bounded
queues shared fairly between communities, then their reader connections. One
reader or community can borrow the entire idle pool; contenders receive turns
based on occupied workers and measured operation cost.
Default queue and execution waits are ten seconds each. Excess work gets 503 with
Retry-After. The plugin retries busy or connection-interrupted reads at most twice with
jitter and at most five seconds of retry sleep. Authenticated reads have no per-minute credential or shared-IP quota.
Anonymous setup and invalid credentials have a separate IP abuse limit; setup or
connection-count limits return 429. There is no fixed per-community worker cap.

Use operator-only `server.stats` to inspect budgets, running/queued work and
rejections. Budgets are resolved at startup; restart after resizing the host.
See [scheduling and resource allocation](docs/search-scheduling.md) for the
algorithm, edge cases, tuning and agent search/read flow. One process owns each
CACHE_DIR; replicas must have separate caches and scheduling budgets.

## Upload storage and indexing

The owner calls one `knowledge.upload` operation. The client compares paginated
hash inventories, sends bounded batches, and replaces changed current documents
by stable UUID. Retried accepted batches return their original receipt without
rewriting content. Receipts contain no document bodies and expire after 24 hours.
No history, review or contribution pipeline runs. PostgreSQL is the source of
truth; each community's SQLite index contains matching search entries and text.
The worker processes 32 documents per turn, rotating between communities.

Uploads have a separate CPU/RAM-sized queue, bounded pending bytes, and back off
when readers are queued. Limits protect memory independently of KB size. Storage
limits are explicit operator settings, not daily upload quotas. See
[upload contract](../skills/community/references/upload.md).

## Development and load tests

Run `go test -race ./...` and `go vet ./...` in this directory. Set
TEST_DATABASE_URL to PostgreSQL credentials allowed to create disposable databases.
Database tests skip without it. Never point tests at a database without permission
to create and drop test databases. The fixtures do not change existing corpora.

`python3 scripts/community-load.py --context orbstack` builds an opt-in HTTP load
test and runs it in a two-CPU, 512-MiB application container against PostgreSQL.
It creates and drops its own database/cache. See [load testing](docs/load-testing.md).

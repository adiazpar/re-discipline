# Load testing

Run from the repository root after installing the local server:

```text
python3 scripts/community-load.py --context orbstack
python3 scripts/community-load.py --context orbstack --cpus 1 --memory 256 --requests 1000 --report /tmp/search-small.json
python3 scripts/community-load.py --context orbstack --cpus 4 --memory 1024 --requests 1000 --report /tmp/search-large.json
python3 scripts/community-load.py --context orbstack --requests 1000 --archive-mib 1024 --report /tmp/search-archive.json
```

Defaults: 1,000 independent project key pairs, four communities (two public/two private) with 2,000
background documents plus 126 accuracy fixtures each, 2,000 operations/workflows per scenario, two CPUs and
512 MiB, with 256 MiB of synthetic archive evidence. `--users`, `--documents`, `--requests`, `--cpus`, `--memory`, `--workers`, `--archive-mib`
and `--report` vary the experiment. `--workers` overrides automatic worker sizing
for tuning; normal installations need no override.

Scenarios:

- Broad-query baseline: four concurrent searches over two communities, matching
  the earlier broad-query workload. Extra synthetic communities remain idle.
- One active community: 64 concurrent searches borrow the whole worker pool.
- One reader: 64 concurrent searches using one credential, exceeding the retired
  120/minute cap without manufacturing extra reader identities.
- Skewed readers: 90% of requests use one credential within one community;
  other credentials share the remaining traffic.
- Equal traffic: 128 concurrent searches distributed across four communities.
- Skewed traffic: 90% targets one community; three share the remaining 10%.
- Burst: up to 1,000 concurrent searches with bounded waiting/backpressure.
- Agent research: 128 concurrent workflows, each searching and then fetching up
  to two matching full findings. These follow-up reads use the same scheduler.
- Judged accuracy: 128 concurrent searches over nine question types: exact and
  quoted identifiers, a natural-language question with a related distractor,
  kind/grade filters, an absent term, a term present only in another community,
  and a filter that excludes the answer. Exact identifier lookups must find a
  long authoritative declaration despite 120 shorter incidental matches.
- Accuracy burst: the same judged questions at up to 1,000 concurrent searches.
- Archive baseline: at least 10,000 judged searches across three communities at
  128 concurrent readers, with the fourth community idle.
- Archive active: the identical searches while the fourth community is frozen
  and exports its knowledge through the production archive downloader. Searches
  start after the export reaches revision evidence, so small finding pointers
  cannot stand in for the expensive part of the transfer.

In the original throughput scenarios except the broad baseline, two thirds of
searches use a selective term and one third uses a common term. The two accuracy
scenarios cycle through their nine question types in every community.
No query-result cache substitutes for engine execution.
Each scenario records successful workflow throughput, end-to-end p50/p95/p99,
queue p95, per-community status counts and p95 latency, full findings fetched,
invalid/cross-community results, and scheduler counters. A workflow includes all
of its document reads; workflow throughput is not total HTTP request throughput.
Peak sampled Go heap and Linux cgroup peak memory are also reported. Cgroup memory
includes the generator, application, page cache and fixture setup.

The archive comparison seeds extra evidence before either measured phase. Its
text is deliberately repetitive: the requested MiB describe uncompressed
knowledge, not network transfer size. This exercises large-record traversal,
compression, bounded buffers and competition with searches; its compression ratio
does not predict real corpora. The archive client, server and search generator
share the limited application container. The report includes verified archive
size, uncompressed size, record count, checksum, export duration and the number
of chunks/records before and after concurrent searches; the archive must make
progress during those searches. Compare latency and
throughput with the matched archive baseline, not with a different query mix.

After the traffic scenarios, the operator explicitly confirms deletion of the
archived community. The report records deletion time and document count, requires index
cleanup to finish, and verifies known-answer retrieval in the three surviving
communities. Separate integration tests exercise deletion while requests are
queued and background review work is running, plus cleanup recovery after restart.
They also cover operator-only access, confirmation gates, frozen mutations,
cancel/expiry/restart, interrupted downloads, corruption, existing local files,
empty archives, identity exclusion and rebuilding searchable knowledge from an
archive. Disposable fixture tests automate confirmations; the interactive skill
must stop for the human at both confirmation points.

## What counts as retrieval correctness

Connection setup, HTTP completion, relevance, stability and document integrity
are separate checks. A 200 response alone never establishes relevance.

Before concurrency measurement, each distinct planned query runs once, serially,
through the same signed reader endpoint. Every loaded response must have the same
ordered findings, revisions, snippets, provenance, warnings and indexed sequence
as that quiet reference. This is an exact response fingerprint comparison on an
unchanged corpus; it does not compare internal BM25 scores, which this API does
not expose. It is reported as `baseline_mismatches`.

Independently, fixture creation records the correct finding IDs. Selective marker
queries and the six positive accuracy question types must put their expected
finding first. Reports include `top1_correct`, `answer_in_top5` and
`reciprocal_rank_sum`. Divide the latter by `positive_completed` for MRR@5;
divide the first two by that same denominator for top-1 accuracy and hit rate@5.
`positive_requested` also includes failed requests, so availability is visible
separately and rejections cannot be presented as accurate answers. Empty-answer
questions must return zero hits, measured by `expected_empty_correct` against
`expected_empty_completed` and `expected_empty_requested`. Broad queries have no
single gold answer: they count toward stability and integrity, not top-1 accuracy.
The quiet reference is itself scored against the independent judgments; a
consistently wrong reference therefore cannot make the run pass.

Every returned finding must match the seeded community, revision, build, kind,
grade and text digest. Agent workflows compare fetched documents with the seeded
content digest, including evidence and applicability, not just the IDs returned
by search. Each phase includes aggregate and per-question `retrieval_quality`
counts. Any completed incorrect answer or changed reference response fails the
test, even if its HTTP request succeeded.

The test creates and drops a disposable PostgreSQL database and cache. It uses
the production HTTP handler, real signed key authentication/RLS and the shared native engine.
It seeds records directly to isolate retrieval from upload validation and warms
indexes before measurement. Existing development corpora are unchanged. No model
API is called. The application and generator share container CPU/RAM; PostgreSQL
runs in the existing separate local container. This is not a network benchmark.
All clients share loopback. A separate setup phase performs 1,000 real signed
connections with 64 concurrent clients; private connections consume single-use
invitations. The setup IP guard is raised to users+100 only to accommodate that
shared loopback source, and reported explicitly. Authenticated traffic limits,
fair queues, execution limits and RLS remain enabled and unchanged. Ordinary
setup abuse limits, invitation races and replay rejection have integration tests.

Normal scenarios must complete every workflow successfully. The burst may return
429/503, but these are never counted as successes. Any unexpected status, transport
failure, invalid result, or community receiving no service fails the run. Reports
are saved outside the repository to `~/.re-discipline/servers/local/` by default
and contain no credentials.

These finite synthetic runs measure known-answer accuracy and stability under
contention, not relevance across arbitrary real reverse-engineering questions.
Optional semantic assistance is explicitly disabled. Corpus revisions stay fixed;
concurrent uploads, corrections, deletion and index replacement require separate
tests. These runs are not production capacity guarantees. Measure real
corpus sizes, sustained arrival rates, uploads/rebuilds, slow networks and separate
load-generator hardware before publishing a service-level objective. Deterministic
scheduler/race tests separately cover expensive queries, thirty communities on two
workers, flooding, cancellation, queue deadlines and state cleanup. Database tests
cover queued credential revocation, shared document IDs across communities,
exhausted anonymous-IP buckets with valid readers still working, and content integrity.

## Archive validation example (2026-10-24)

An OrbStack run used 1,000 signed reader connections, four communities (two
public/two private), 2,126 documents per community and 1,024 MiB of extra archive
evidence. The application, downloader and generator shared two CPUs and 512 MiB;
PostgreSQL ran in a separate container. All 30,000 measured searches and 1,334
follow-up document reads succeeded. Judged answers and empty results were correct,
with zero quiet-reference mismatches or cross-community results.

The matched phases each used 128 concurrent readers and 10,000 searches across
the three surviving communities. During the active phase, the fourth community
advanced through 28 archive chunks containing 56 large evidence records.

| Measurement | Without export | During evidence export |
| --- | ---: | ---: |
| Successful searches/second | 316.8 | 315.2 |
| Median latency | 28.7 ms | 32.0 ms |
| p95 latency | 3.14 s | 2.81 s |
| Failed searches | 0 | 0 |
| Incorrect or cross-community responses | 0 | 0 |

The verified archive contained 4,252 knowledge records and 1,034.2 MiB of
uncompressed JSONL; export and client verification took 342.6 seconds. Its
1.09 MiB compressed size reflects deliberately repetitive evidence, not a typical
corpus. Peak application-container memory was 230.4 MiB, including the downloader,
generator and page cache. Explicit deletion took 0.33 seconds, completed index
cleanup, and preserved known-answer retrieval in all three other communities.
These are finite local measurements, not production latency or bandwidth promises.

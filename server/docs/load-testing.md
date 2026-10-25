# Load testing

Run `python3 scripts/community-load.py --context orbstack` after local installation.
The helper builds the current test binary and runs against its own disposable
PostgreSQL database. It never changes the development corpus. Default resources:
two CPUs and 512 MiB; tune with `--cpus`, `--memory`, and optional `--workers`.

The current upload test creates four communities (two public and two private),
1,000 actual signed reader connections, and uses 128 concurrent agents. Each agent
searches for a known document and reads it, checking its exact UUID, fingerprint,
community and full Markdown. Concurrent owner imports upload 2,000 documents per
community by default. `--users`, `--requests` (default 2,000 workflows), and
`--documents` change these sizes. `--anchors 32` exercises 128 distinct documents;
the same UUIDs are deliberately reused with different content in each community
to detect scope or fingerprint mix-ups. Each query also compares all 32 known
identities in its community and validates every returned hash and generation. One
workflow includes both query and full read. The helper also runs the multi-community
client workload described below against two isolated server instances.

Any incorrect/missing result, wrong scope, incomplete batch, or exhausted client
retry fails the test. Bounded automatic retries retain the same credential/scope.
Reports include workload sizes, failures, latency, scheduler statistics and Linux
cgroup peak memory (including the generator). `--report` saves JSON. These numbers
measure a synthetic workload, not production capacity or a 10 GB corpus benchmark.

The normal Go suite also tests admission fairness, cancellation, queued revocation,
private invitations, replay protection, migration from the old history schema,
current replacement, old-batch replay, duplicate IDs, storage limits, pagination,
Evidence exclusion and local incremental scanning. Archive tests cover owner-only
access, both confirmation gates, per-community freeze, cancel/expiry/restart,
retry consistency, verification, identity exclusion, and restoring searchable text.
The MCP smoke test exercises both Claude and Codex adapters, local/remote copy
selection for matching, local-only, remote-only and divergent changes, and the complete current-knowledge archive/deletion
flow on disposable fixture communities. Retrieval tests also cover Evidence-only
edits, renames, missing local copies, invalid identities, provenance, final result
limits, obsolete-keyword suppression, cache loss/rebuilds, delayed index responses,
clock skew and identity remaining attached to indexed text during edits.

Local performance benchmarks run from `retrieval/`:

```sh
go test ./community -run '^$' -bench BenchmarkRetrievalPaths10000 -benchtime=30x -count=3
go test ./community -run '^$' -bench BenchmarkBuildIndex10000 -benchtime=1x -count=3
go test ./community -run '^$' -bench BenchmarkCollapseCopies256 -benchtime=1000x -count=3
go test ./community -run '^$' -bench BenchmarkUploadScan10000 -benchtime=3x -count=3
```

Run performance measurements without concurrent builds or tests. These use a
10,000-file synthetic corpus and a loopback fixture for remote responses. They
measure local scanning, indexing, copy grouping, and upload reconciliation without
internet latency. Cached upload runs assert zero document rereads and zero uploads;
verification runs assert that all 10,000 files are read. Fixture setup is excluded
from timings. Memory per operation is allocated bytes, not peak resident memory.

Run `go test -race ./...` in server/ with TEST_DATABASE_URL set to credentials
allowed to create and drop disposable test databases. The retrieval module's
`go test -race ./...` needs no PostgreSQL. Fixtures automate deletion confirmations;
interactive skills must still pause for both human responses.

## Copy-grouping measurements (2026-10-25)

On the development Apple M2/macOS machine, median of three benchmark samples:

| Operation | Before | After |
| --- | ---: | ---: |
| Local query, 10,000 files | 39.00 ms | 40.82 ms |
| Both query, 10,000 files + loopback remote copies | 39.38 ms | 40.33 ms |
| External client + loopback fixture | 0.206 ms | 0.178 ms |
| Build a 10,000-file index | 633 ms | 714 ms |

Grouping 256 candidates into 128 findings took 0.141 ms. An unchanged 10,000-file
upload reconciliation took 271 ms with **zero document rereads and zero uploads**;
full verification took 622 ms and read all 10,000 files. These are small synthetic
Markdown files, not a 10 GB corpus. Both queries still include the existing local
filesystem manifest scan; grouping adds no corpus scan, document read or network
request. Cold indexing does extra fingerprint work. The small timing differences
are observations from this machine, not guarantees or production capacity estimates.

The real service tests use signed reader keys, PostgreSQL, the background indexer
and actual query/full-document HTTP requests in an OrbStack Linux container.
See the load report for its CPU/memory cap and workload. Memory peaks include the
load generator. Index catch-up time is measured using the normal background worker;
the load test no longer forces synchronous indexing after writes finish.

Final Linux run: **0 failures**, 1,000 signed reader keys,
4 communities (two public/two private), 128 distinct queried documents,
2,000 query-and-full-read workflows, 128 concurrent workflows,
and 8,000 concurrent uploaded documents. The container was capped at two CPUs
and 512 MiB (Go target 384 MiB).

| Metric | Measured |
| --- | ---: |
| Search + full read, median | 257.5 ms |
| Search + full read, p95 | 687.3 ms |
| Slowest search + full read | 4.54 s |
| Upload/read workload elapsed | 12.5 s |
| Additional background index catch-up | 37.4 s |
| Start to fully indexed | 49.9 s |
| Container peak memory, including generator | 385.1 MiB |

Every read checked community, UUID, fingerprint and exact Markdown. Uploaded probes
were retrieved after the background worker caught up. Both reader and upload
queues drained with zero rejected or expired queued requests. This confirms
correctness for the measured workload; it does not promise zero latency impact
under uploads or immediate search visibility.


## Copy selection and timestamps (2026-10-25)

The updated policy was exercised through both shipped MCP adapters against the
local service: identical copies, an unpublished local correction, a subsequent
server-only correction from a second operator workspace, divergent changes,
timestamp propagation, reader write denial and fixture archive/deletion all passed.
The installed Codex host passed 3/3 golden corpus answers; Claude's real host MCP
connection passed. Race suites and vet passed for both Go modules. Migration tests
confirm rehashing preserves UUIDs, text, generations and dates, is atomic, and
cannot alter a frozen archive.

With the same Linux limits and workload as above, and **32 comparison identities
per query**, all **2,000 search/full-read workflows** and **8,000 uploads** passed.
There were **zero wrong/missing results, comparison mismatches, cross-community
matches, rejected queue entries or expired queued requests**. The test checked
all 64,000 returned comparison entries, not just successful HTTP responses.

| Metric | Measured |
| --- | ---: |
| Search + full read, median | 288.3 ms |
| Search + full read, p95 | 829.0 ms |
| Slowest search + full read | 5.01 s |
| Upload/read workload elapsed | 12.7 s |
| Additional background index catch-up | 37.2 s |
| Start to fully indexed | 49.9 s |
| Container peak memory, including generator | 405.3 MiB |

These timings include the heavier comparison response and concurrent owner
uploads. They are observations, not a promise of zero load impact. The disposable
test container and database were removed afterward.

Isolated Apple M2/macOS run after the cache-write optimization (10 iterations
per operation, one benchmark sample): local query **38.36 ms**, combined query
**40.04 ms**, external loopback request **0.423 ms**, and grouping 256 candidates
**0.176 ms**. Unchanged 10,000-file uploads took **420 ms**, with **zero document
rereads and zero uploads**; full verification took **977 ms**, reading all 10,000.
The added timestamp/baseline bookkeeping increases upload-scan work compared with
the earlier 271/622-ms measurements. First-time timestamp insertion rewrites source
metadata; fixture setup and that one-time work are excluded from steady-state
measurements. These are small synthetic files, not a 10 GB validation.


## Multi-community reconciliation (2026-10-25)

The current policy searches all linked communities by default and groups copies
before ranking. Unit tests cover every ordering of three communities, repeated
unchanged queries, opposite per-link lineage, local/remote corrections, obsolete
keywords, independently divergent variants, partial failures, bounded candidate
comparisons, private-key separation, legacy key reuse, concurrent linking,
copying projects, same slugs on different owners, explicit upload destinations and
isolated disconnect. Unresolved variants bypass optional assistance. Untrusted
servers cannot inject grouped variants or their provenance.

Both Claude and Codex MCP adapters passed the actual multi-community flow against
the local server, including separate OS-stored keys, upload to selected aliases,
three-location dedupe, a correction on one community while another retains the
old copy, source-labelled external variants, full reads, narrowed queries and
unlinking one community while preserving the other's key. Existing curation,
owner-only write denial, timestamps and archive/deletion smoke checks also passed.
The installed Codex host discovered all six current skills and returned 3/3 golden
answers; Claude's installed MCP connection and both providers' hooks passed.

The Linux load container used two CPUs and 512 MiB (Go target 384 MiB). All
**2,000 search/full-read workflows**, **64,000 comparison entries**, and **8,000
concurrent uploaded documents** passed with 1,000 signed reader keys across four
public/private communities. There were **zero wrong/missing results, scope or
fingerprint mismatches, rejected requests or expired queue entries**.

| Single-community workflow metric | Measured |
|---|---:|
| Median search + full read | 305.2 ms |
| p95 | 754.2 ms |
| Slowest | 3.88 s |
| Read/upload workload | 12.34 s |
| Additional background indexing | 36.28 s |
| Start to fully indexed | 48.62 s |
| Peak container memory, including generator | 408.6 MiB |

A second workload exercised the plugin's complete multi-community coordinator,
using **16 projects, 64 signed connections, four communities on two server
instances**, and **32 concurrent workflows**. All **256 combined queries** and
**1,024 source-specific variant reads** passed while owners uploaded another 160
documents. Each query checked one deduplicated shared finding, one four-variant
group and four community-specific findings, including local preference in `both`.
Every variant's full Markdown and fingerprint matched its own community. Subset
selection and disconnect isolation were checked afterward. Median workflow time
was **248 ms**, p95 **488 ms**, and the read/upload phase took **2.19 seconds**.
The server tests use an in-memory credential backend with real signed HTTP; the
adapter smoke separately exercises the actual operating-system credential store.

These are synthetic correctness and load observations, not production capacity
promises, zero-latency guarantees or a 10 GB corpus evaluation. The two server
instances share the constrained test container; the PostgreSQL service is separate.
Disposable test databases and containers are cleaned up automatically.


An isolated Apple M2/macOS benchmark (10 iterations, one sample, 10,000 synthetic
Markdown files and loopback community responses) measured:

| Client query | Time |
|---|---:|
| Local | 39.31 ms |
| External, one community | 0.465 ms |
| Both, one community | 40.80 ms |
| Both, four communities with duplicate copies | 44.47 ms |
| Both, sixteen communities with duplicate copies | 50.39 ms |

The combined queries asserted eight distinct results and retained every expected
location. Cached 10,000-file upload scanning took 415 ms with zero file rereads or
uploads; full verification took 983 ms and read all 10,000 files. These figures do
not include internet latency. More linked communities require more server requests;
bounded client concurrency controls the burst without hiding sources or downloading
corpora. Native Windows, Intel Mac and both Linux architectures remain in CI.

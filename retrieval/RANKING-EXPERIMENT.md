# Combined ranking and query performance experiment

Local branch: `codex/ranking-performance-combined`. Not merged, pushed or released.
Baseline: main at `3f7d23df7af8747a774da7a879ca237be3d22ce3` (1.12.0).
The installed plugins, the real project and its existing community remain on main.
The version number is unchanged; branch binaries are experimental artifacts.

## Change being tested

Port the lightweight ranker from experiment
`079285723c6ccdaa0b8ccfe7475cf1891e667244` onto main's optimized SQL path:

- Title/body/identifier BM25 weights: 2/1/4 instead of 4/1/2.
- Reference preference: 0.3 of the strongest current candidate's BM25 magnitude,
  replacing the fixed penalty. Declared identifiers remain exempt.
- Unique title/expanded-identifier word coverage and adjacent word pairs contribute
  boosts of 0.5 and 0.25 on that same scale. Repetition contributes no extra votes.
- Exact declared names still lead within each status group; superseded documents
  still sort last. Candidate windows, filters, deduplication, queues and worker
  limits are unchanged. No model calls, synonyms, network assistance or index
  format change.

The first combined implementation retained accuracy but used 4–6% more application
CPU per fixed workload. Median throughput was 1–4% lower across three paired runs.
Its results are retained separately; it was not selected as the final candidate.

The final implementation reduces work without changing that candidate's results:

1. Scan words directly and count unique matches with bitsets, with a long-query
   fallback. An independent regexp/set reference and fuzzing check equivalence.
2. Select and score the same bounded candidates, then load snippets and complete
   metadata only for the requested final page. `explain` still loads every candidate.
3. Keep selection and payload loading inside one SQLite read transaction, so a
   concurrent replacement cannot combine an old score/title with new document data.

`stats` and `explain` report the new fractions, coverage and boost arithmetic. Their
diagnostic JSON replaces `reference_penalty` with `reference_penalty_fraction`;
query results retain their existing public shape.

## Local relevance and latency

The frozen real corpus contains 12,632 indexed documents. Labels and questions
were fixed before this pass. All 336 text questions have been used in prior
experiments, including the 56 once held out: this is regression evidence, not a
fresh-question generalization result. Symbol lookup is unchanged.

| Questions | Main first / top 8 | Candidate first / top 8 |
| --- | ---: | ---: |
| 280 original | 201 / 276 | 227 / 277 |
| 56 previously held out | 44 / 53 | 47 / 55 |
| All 336 | 245 / 329 | 274 / 332 |

There are 37 first-place gains and **8 first-place regressions**. Three previous
top-eight answers leave the top eight, offset by six newly recovered answers.
The optimized candidate's full top-eight ordering matches the earlier ranker on
all 336 questions. A further 768 full ordered-payload comparisons, including
different limits and kind/grade filters, match exactly before/after deferred loading.

Two alternating rounds of actual native CLI queries, with assistance disabled:

| Latency | Main | Candidate | Change |
| --- | ---: | ---: | ---: |
| Median | 90.127 ms | 83.546 ms | -7.3% |
| p95 | 109.552 ms | 101.434 ms | -7.4% |

These include process startup and local freshness/reconciliation. Neither build
returned warnings, duplicate finding IDs or unstable result ordering. Timing was
not overlapped with builds, race tests or the server load run.

## Server comparison

All six final runs completed. Medians of three runs per build:

| Workload | Workflows/s main → candidate | Throughput change | Application CPU change | p95 main → candidate |
| --- | ---: | ---: | ---: | ---: |
| One community | 23.53 → 25.72 | +9.3% | -9.5% | 1733 → 1601 ms |
| Four, even traffic | 24.32 → 26.84 | +10.4% | -12.8% | 2317 → 2276 ms |
| Four, 80/10/5/5 traffic | 23.34 → 26.17 | +12.1% | -12.8% | 2100 → 1768 ms |
| Uneven traffic plus uploads | 23.19 → 23.55 | +1.5% | -6.0% | 2137 → 1994 ms |

CPU is normalized by the same 480 completed workflows in each workload. Including
index catch-up does not change the conclusion. Across 11,520 measured query/full-read
workflows and 2,400 concurrent-upload documents, there were no failed workflows,
scope/fingerprint/text mismatches, duplicates, or scheduler rejections, expirations
or cancellations. Queues drained and uploaded knowledge was searchable. Index
catch-up after each workload took at most 11 ms in this fixture.

**The aggregate gain is not a zero-regression result for every community.** Median
per-community p95s under even traffic changed by +2.5%, -16.1%, **+20.7%** and
-24.8%. In the mixed upload case, two lightly loaded communities' p95s increased
12.5% and 8.8%; overall mixed p99 rose 2.3%. The slow tails varied across runs, and
this experiment does not isolate their cause. They must not be dismissed as noise
or hidden behind the aggregate throughput gain.

Median sampled process RSS peaks increased by 1.1–4.3 MiB, to 68–74 MiB depending
on workload. The maximum process RSS sample across all workloads was 226 MiB on
main and 225 MiB on the candidate. Total container memory, which also includes
filesystem cache, was more variable: for even traffic its median sampled peak was
170 → 248 MiB. The highest total samples were 322 → 359 MiB, below the shared
512 MiB test limit. These measurements do not separate the source of that increase
and are not proof of unchanged memory demand.

The protocol, fixed before the final runs:

- Six runs, ordered main/candidate/candidate/main/main/candidate. Each gets a fresh
  disposable database and the same application limit: 2 CPU, 512 MiB RAM, four
  automatically selected retrieval workers. PostgreSQL and the generator run
  separately. Both builds use the current server implementation and their own
  matching retrieval engine.
- Four communities, two public and two private, each containing the same 12,539
  upload-eligible real documents. Community-specific text markers deliberately
  give equal UUIDs different fingerprints to catch cross-community mixing.
- Provision 1,000 scoped reader credentials. Use 24 concurrent clients, with 480
  query-plus-full-document workflows per scenario. This is not 1,000 simultaneous
  readers or a production capacity claim.
- One-community, equal four-community and 80/10/5/5 traffic. A fourth scenario
  adds 400 approximately 20 KB documents while the uneven traffic continues.
- Run all 258 upload-eligible quality questions before timing. Use 24 queries
  stratified by length under load, with limit 32. Read-only runs require exact
  within-build ranked payload repeatability. All workloads check identities,
  fingerprints, full document text, community scope, duplicates and errors.
- Expected-answer rank is recorded separately from transport/data integrity:
  successful requests do not imply that the expected answer ranked first.
- Record latency tails, throughput, application CPU and sampled memory, queue
  outcomes and index catch-up. Reject wrong data or unexpected request failures.
  Treat reproducible >5% throughput or CPU/workflow degradation as material.

The scope markers slightly affect relevance; do not compare the server fixture's
absolute baseline score to an earlier unmarked single-community run.

The 24 length-stratified load questions are not a second unbiased quality sample.
Both builds put 19 expected answers first; main includes all 24 in its top eight,
while the candidate includes 23 (one expected answer is rank 13 before uploads,
then rank 12 or 13 as corpus statistics change). This existing
ranking regression remains visible under load. Exact read-only payload checks
distinguish ranking choices from corruption or instability caused by concurrency.

## Verification and limits

- Retrieval and server race suites and `go vet` passed, with real PostgreSQL for
  server tests. A deterministic WAL replacement test covers the two-stage snapshot.
- Payload tests cover tie order, replacement, filters, pagination through multiple
  SQL parameter batches, empty queries and complete explanation results.
- Field-feature fuzzing passed 82,668 inputs, including Unicode/invalid bytes;
  explicit cases cover 63/64/65 and longer term/pair counts.
- Canonical builds passed for all five client platforms and both Linux server
  architectures, and all seven artifacts passed a subsequent reproducibility check.
  Native macOS ARM tests and Linux ARM server tests ran here;
  cross-compilation is not native Windows/Intel runtime certification.
- The native CLI corpus, both provider MCP adapters and both session hooks passed
  the disposable smoke fixture. This branch has not been pushed to hosted CI.

Private corpus snapshots, question-level regressions, binary/source hashes,
profiling data, full payload checks and both sets of paired load results are kept
in the local experiment reports, outside Git. No corpus text or credentials are
committed. Tests do not install this branch or update the existing community.
Disposable databases, containers, image tags, the client network and reader-key
session files were removed. A final audit found all 12,633 source Markdown files
unchanged, the installed/project executables still matching main, and the original
community service healthy on its original build.

This experiment cannot establish universal ranking accuracy, zero overhead on
other corpora, behavior at 10 GB, open-loop overload capacity or performance when
PostgreSQL shares the application's CPU/memory limit. The eight first-place and
three top-eight regressions remain real tradeoffs.

## Decision

The combination preserves the earlier aggregate relevance gains and improves
measured local latency, server throughput and CPU cost. It passes the predeclared
CPU/throughput and data-integrity gates. It does **not** establish that every
community's tail latency or memory demand is unaffected. Keep it as an unmerged
experiment; investigate the tail and memory results and use genuinely new questions
before making a broader performance or generalization claim.

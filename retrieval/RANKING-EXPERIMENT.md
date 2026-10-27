# Combined ranking and query performance experiment

Implementation accepted for main as version 1.12.1 on 2026-10-26, following the
corrected follow-up below. Experiment branch: `codex/ranking-performance-combined`.
Baseline: main at `3f7d23df7af8747a774da7a879ca237be3d22ce3` (1.12.0).
During testing, the installed plugins, real project and existing community stayed
on the baseline; experimental binaries retained version 1.12.0. The accepted
implementation uses 1.12.1 so managed installations can recognize the upgrade.

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

## Initial server comparison

The follow-up below identifies a setup-buffer confound in this harness. Retain
these measurements as history; use the corrected follow-up for performance
conclusions. Relevance and exact-result checks are unaffected.

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

## Decision after the initial comparison

The combination preserves the earlier aggregate relevance gains and improves
measured local latency, server throughput and CPU cost. It passes the predeclared
CPU/throughput and data-integrity gates. It does **not** establish that every
community's tail latency or memory demand is unaffected. The initial decision was
to keep it unmerged and investigate the tail and memory results. The follow-up
below resolves those investigations; genuinely new questions are still needed
before making a broader generalization claim.


## Follow-up: allocation and latency investigation

Linux CPU and heap profiles identified two avoidable costs. In the profiled
480-workflow single-community sample, preparing unused semantic claims accounted
for about 14% of CPU samples. Ranking's growing result slices and per-row scan
targets allocated about 330 MiB of the approximately 873 MiB sampled allocation
total. These are allocation volumes over the workload, not live memory sizes.

The follow-up changes preserve the candidate's ranking and payloads:

- Ordinary server search reads ID/fingerprint/generation in one metadata query
  and uses the already-indexed build, digest and timestamps. Full indexed bodies
  and semantic projections are loaded only when assistance is enabled by all
  three gates: configured provider, community policy and request. Explicit full
  document reads and authorization checks are unchanged.
- Reuse the candidate scan target, copying each complete value into the result
  slice. Small lookups remain small; after 16 candidates reserve the bounded
  window once. The reservation is capped at 1,024 entries for unusually large
  internal pages, without capping the returned candidates. There is no cache,
  global retained buffer, extra SQL pass or scoring change.

Instrumentation also uncovered a **test-fixture confound**: serializing all four
corpora into the private generator's setup file left an 80 MiB buffer in
`encoding/json`'s pool. Its retention varied between runs and with allocation
rate; it raised the GC target and distorted both memory and timing comparisons.
The heap profile attributes the buffer to fixture setup, not a production search
response. The partially completed first follow-up comparison was stopped and
retained as diagnostic evidence. Its performance numbers are not used below.

The corrected fixture releases both generations of that pool before making the
setup file visible to the generator, and checks that live setup heap is below
16 MiB. This happens identically on both builds, only in the private test harness;
no forced-GC policy was added to the product. The new run records Go allocation
and GC counters, process RSS, cgroup anonymous/file memory, and each load request's
HTTP status and queue-time header. Profiling is disabled for timed comparisons.
Six fresh runs retain the same 2 CPU/512 MiB budget, corpus, four communities,
1,000 credentials and four 480-workflow scenarios. Pair order is main/fixed,
fixed/main, main/fixed, with a different preselected request-shuffle seed per pair.

The corrected comparison completed with all six runs. Each scenario began with
at most 2.02 MiB of live Go heap. Medians of three runs per build:

| Workload | Workflows/s main → fixed | CPU change | p95 main → fixed | p99 change |
| --- | ---: | ---: | ---: | ---: |
| One community | 27.84 → 32.15 | -23.8% | 1436 → 1251 ms | -10.1% |
| Four, even traffic | 29.12 → 33.44 | -24.6% | 2253 → 1900 ms | -16.0% |
| Four, 80/10/5/5 traffic | 28.67 → 32.49 | -23.8% | 1600 → 1431 ms | -1.2% |
| Uneven traffic plus uploads | 28.04 → 29.93 | -18.2% | 1707 → 1568 ms | -3.1% |

Every community's median p95 improved in the corrected comparison. For even
traffic the four changes were -9.1%, -16.0%, -31.6% and -12.8%; the earlier +20.7%
median regression did not recur. Uneven traffic improved by 2.5–11.2%, and mixed
upload traffic by 9.6–25.8%. Individual runs still varied: for example, the third
pair's overall uneven-traffic p95 was 2.5% slower despite higher throughput. These
are three-run medians, not a guarantee that every request or run is faster.

Go allocation volume fell 58.6–58.9% for read workloads and 45.1% with uploads.
**Allocation volume is not peak memory.** Median process RSS peaks changed from
68.0 → 68.5 MiB (single), 65.7 → 69.2 MiB (even), 65.0 → 68.3 MiB (uneven), and
68.2 → 71.1 MiB (mixed). That residual 0.5–3.5 MiB increase remains a tradeoff; no
GC tuning was added to hide it. Median total cgroup peaks were 241.7 → 242.3,
166.2 → 164.6, 166.5 → 167.8 and 182.8 → 174.5 MiB respectively: read totals were
within 1%, and mixed-upload total memory decreased 4.5%. The much larger earlier
memory difference cannot be treated as evidence of a production regression,
because setup-buffer retention was not controlled in those runs.

Across the corrected runs, **11,520 workflows / 23,040 measured HTTP attempts**
completed with zero failures, non-200 responses, retries, duplicate IDs,
wrong-community results, fingerprint mismatches or incorrect full documents.
There were no scheduler rejections, expirations or cancellations. All 2,400
documents uploaded successfully, indexes caught up and new content was searchable;
final index catch-up took at most 3.34 ms (1.72 ms on the candidate).
This catches hidden safe-read retries as well as final workflow failures.

Ranking remains unchanged by these follow-up fixes. All 768 local ordered-payload
hashes match the previous candidate. The server's full 96-query/four-community
payload oracle also matches. The 258-question server quality set remains 207
first / 256 top-eight for the candidate versus 189 / 254 on main. The existing
eight first-place and three top-eight local regressions are **not** repaired by
these performance changes, and the question sets are still previously used.

The new PostgreSQL integration tests check indexed metadata before and after a
stored replacement becomes searchable, missing-record failure, all assistance
gates, full claim projection through a mocked provider, and unchanged result
identity through assistance. Both Go race suites and vet passed; all seven
canonical artifacts rebuilt and passed reproducibility checks. Native CLI,
Claude/Codex MCP adapters and both session hooks passed the smoke fixture.
No paid or external semantic-provider request was made.

Two alternating native CLI rounds on the same 336 questions confirm 274 first /
332 top-eight, versus main's 245 / 329, with no duplicates or unstable ordering.
Median latency was 88.918 → 82.349 ms (-7.4%); p95 was 106.493 → 99.776 ms (-6.3%).
These runs used matching standalone executable layouts. An earlier check had
mistakenly launched only the candidate inside the plugin package, charging its
per-process project-binary hashing/refresh to that side alone; that unmatched
measurement is retained separately and is not a retrieval comparison. The refresh
code is unchanged from main. `evaluate-ranking.py` now copies both native binaries
into a temporary matching layout automatically, records their original paths and
hashes, and removes the copies afterward. These CLI timings exclude plugin refresh
for both builds; the adapter smoke tests separately exercise that behavior.

The final cleanup audit confirmed all 12,633 real corpus Markdown files unchanged,
both installed plugins and the real project's binary still matching main, and the
original community healthy on its original build. Disposable databases, containers,
image tags, retained experiment images, client network and reader-key files were
removed. Test results and private profiles stay in the centralized report directory.

## Current assessment

The two targeted fixes remove measured unnecessary work and retain the ranking
gains. In this controlled corpus/load comparison, CPU, throughput and median
per-community tails improve together. Peak process memory is slightly higher,
and individual tail samples still fluctuate. The large memory result from the
original harness was confounded by its setup buffer. The original generalization,
corpus-size and shared-PostgreSQL resource limitations still apply.

The owner accepted this implementation for main after reviewing the follow-up.
Deployment follows the versioned plugin and managed server update paths. Private profiles, source hashes,
corrected and aborted run records, payload oracles and cleanup checks remain in
the centralized local experiment directory; no corpus or credentials enter Git.

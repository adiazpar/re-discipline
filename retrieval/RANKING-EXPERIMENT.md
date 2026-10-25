# Deterministic ranking experiment, 2026-10-25

The published baseline is commit `07f6ba9`. This experiment adds field coverage
and ordered word pairs to BM25 and replaces the fixed reference penalty with
a query-scaled preference. It uses the same ranker for local queries and
immutable community snapshots, with no schema change, model call, dependency,
new setting, or corpus edits.

## Evaluation design

The private evaluation corpus contains 12,632 indexed documents (about 9.7 MB of
Markdown) and 80,850 symbols. Its existing 280 text questions were the development
set. The 11 symbol cases are a separate regression check and are not included
in text-relevance percentages.

Before tuning, 56 new questions were written from randomly selected documents
absent from the development answers: 24 findings, 24 reference entries and eight
operational documents. Selection used seed 20261025. The question file was frozen
with SHA-256 `b34f3ae13faf564910e80b6694379347f802cc36ddbe6c7d0e2a7598b4dad642`.
These are agent-authored, document-grounded questions, not independent human
traffic. Documents differ between sets; related subject matter can still overlap.

Tuning compared title/identifier weights, fixed and query-scaled reference
preferences, coverage, ordered pairs, exact phrases and stricter identifier
exemptions. Selection maximized development rank-one matches subject to preserving
the aggregate top-eight count. Exact-phrase bonuses added no useful improvement;
restricting every penalty exemption to code-shaped names performed worse.
Neither was adopted. Held-out scores were not used to choose or revise weights.

## Measured results

Real native executable calls, assistance disabled, local retrieval, same index:

| Set / build | Rank 1 | Top 5 | Top 8 | MRR@8 | Median / p95 ms |
|---|---:|---:|---:|---:|---:|
| Development / baseline | 201/280 (71.8%) | 266/280 | 276/280 | .820 | 92.6 / 121.5 |
| Development / candidate | 227/280 (81.1%) | 273/280 | 277/280 | .879 | 102.9 / 146.6 |
| Held out / baseline | 44/56 (78.6%) | 52/56 | 53/56 | .836 | 92.9 / 123.0 |
| Held out / candidate | 47/56 (83.9%) | 54/56 | 55/56 | .894 | 102.1 / 134.4 |

Timing includes process startup, freshness checks and result reconciliation on
an Apple M2. The held-out evaluation alternated binary order and repeated each
question twice; result order was stable. Development timing used one round.
These are short local measurements, not capacity estimates for a rented server.

Development gained 31 first-place answers and lost five. Four former top-eight
misses became hits, but three former hits moved out. Held-out questions gained six
first-place answers and lost three; two misses became top-eight hits, with none
moving out. These tradeoffs are why the candidate remains an experiment rather
than evidence that every question got better. The small held-out set does not
establish statistical certainty or performance on unrestricted future questions.

Remaining difficulties include vocabulary changes, very similar settings with
different qualifiers, and broad questions with several plausible answers.
The score measures the originally nominated answer path. We did not relabel a
different result as correct after observing it, nor change aliases to encode the
test questions. No lexical ranker can guarantee the intended answer first when
the question does not supply enough distinguishing information.

The corpus predates UUID migration, so zero duplicate UUIDs in that evaluation
is not evidence of semantic deduplication. Community tests exercise identified
copies, reconciliation, scope isolation and load separately.

## Integration checks

Both provider MCP adapters returned identical relevance results: 238/291 first
and 288/291 top-eight on the original suite including all 11 symbol lookups;
47/56 first and 55/56 top-eight on the new questions. Neither adapter reported an
error. Native selection, installation, hooks and server ping also passed.

The isolated PostgreSQL load test used four communities, 8,000 documents, 1,000
reader keys and 128 concurrent workflows on a two-CPU, 512-MiB app container.
All 2,000 query-and-full-read workflows passed (128 distinct query targets);
median/p95 workflow time was 295/781 ms, with no scheduler rejections. A separate
two-server test used 16 projects, 64 connections, four communities and 32 concurrent
workflows: all 256 workflows and 1,024 variant reads passed scope and reconciliation
checks. These controlled tests assess routing, completeness and isolation under
load; their success rate is not a natural-language relevance score.

## Reproduce with another corpus

Save private JSONL questions before tuning:

```json
{"q":"Which setting controls the overlay?","expect":"docs/overlay.md"}
```

`expect` can also be a list of independently judged correct paths. Keep those
judgments fixed during the comparison.

```sh
python3 scripts/evaluate-ranking.py \
  --root /path/to/project \
  --questions /private/heldout.jsonl \
  --baseline /private/re-search-baseline \
  --candidate /private/re-search-candidate \
  --output /private/comparison.json
```

The report includes binary and question-file hashes, individual paths and
latencies, aggregate relevance, duplicate UUID counts and repeated-run stability.
It contains private corpus details: do not commit it to a public repository.
Both builds must support the same index format. The benchmark does not create
community links or switch a running server to either build.

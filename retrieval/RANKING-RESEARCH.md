# Retrieval research loop, 2026-10-25

The strongest prototype combines lexical retrieval, a small embedding model for
additional candidates, and a cross-encoder reranker. After the 48-question
comparison, an additional frozen 80-question confirmation set improved from
71 to 78 first-place matches and from 77 to 80 top-eight matches. All 80 were in
the top five. This is evidence of improvement
on this sample, **not a guarantee for arbitrary future questions**.

The models remain offline developer experiments. They are not dependencies of
the plugin, not shipped in its binaries, and not deployed to community servers.
The published baseline remains `07f6ba9`; the previous experimental checkpoint is
`0792857`. The native candidate adds bounded compound-word recovery and clearer
agent instructions for reading evidence. It does not integrate a model service.

## Protocol and limits

The fixed private corpus contains 12,632 documents, approximately 9.7 MB of
Markdown and 80,850 symbols. No tracked corpus file, alias, or answer label was
edited to improve the scores. This is not a 10-GB experiment.

The previous 280 development questions and 56 previously evaluated questions
became a 336-question development set. Before evaluating new candidates, another
48 questions were written from randomly selected documents, excluding previous
answer paths: 20 facts, 20 reference entries, eight operational documents, seed
20261026. SHA-256:
`6d5a9de7031a4e91cc56fdb776df946daecc19bd5f28d402ef3050ececaee5a2`.

After examining that set, all 384 questions became development data. A second
48-question set used the same category proportions, seed 20261027, and excluded
all prior answer paths. SHA-256:
`caaa446bb307aeee785d8d474ea30927cdbdf201eea5693f6bbc22d271e7d861`.

A subsequent 80-question confirmation set excluded all 432 previous nominated
answer paths: 36 facts, 36 reference entries and the eight remaining operational
documents, seed 20261028. SHA-256:
`ecbf8986578d81f9c40c49bb57d0a2fc9bdeacf23e96edf78b1c804f50cec930`.
The selected hybrid's parameters were kept unchanged for this confirmation.

These are agent-authored, document-grounded questions, not independent human
traffic. Subject matter can overlap despite distinct target documents. Several
candidate families were compared on the same holdout; this is exploratory
research, not a preregistered independent confirmation study. Each configuration
was selected on development data before its held-out evaluation. No nominated
answer was inserted into a candidate list or relabeled after viewing results.

Rank one means the preselected document appears first; top eight means it appears
among the first eight. Neither metric measures the correctness of an agent's
final answer. Some other documents can also be relevant. The evaluator accepts
multiple correct paths when judged **before** evaluation.

Exact whole-name identifier authority and superseded-last ordering remain in
the model comparisons. Models only see local text, with local evidence sections
removed. No corpus text or queries were sent to an inference service, and no Jev
calls were made. Model downloads were pinned; inference runs offline.

## First round: reranking lexical candidates

All results below use checkpoint `0792857` for initial retrieval. RRF combines
rank positions, not incompatible raw model and BM25 scores. Its constant is 10.
`N / weight` identifies the reranked shortlist and model contribution.

| Candidate | Development first / top 8 (336) | Fresh first / top 5 / top 8 (48) | Decision |
|---|---:|---:|---|
| Checkpoint | 274 / 332 | 42 / 46 / 46 | Comparison |
| Body proximity | 279 / 332 | 42 / 45 / 46 | Reject: development gain did not generalize |
| TinyBERT, 8 / .5 | 277 / 332 | 42 / 45 / 46 | Reject as a default |
| MiniLM, 8 / .25 | 277 / 332 | 43 / 46 / 46 | Small gain; prototype only |
| MiniLM, 20 / 1 | 275 / 335 | 44 / 45 / 46 | More recall in development; prototype only |
| ModernBERT, 20 / 1 | 286 / 335 | 45 / 46 / 46 | Strongest lexical reranker; prototype only |

Other experiments were stopped before held-out evaluation. Separating declared
identifiers, aliases and incidental code into index columns gained only one
development first-place match and did not justify a schema migration. Pure
neural ranking was generally worse than combining it with the lexical order.

A later corpus-only experiment compared TF-IDF cosine retrieval and latent
semantic analysis with 64, 128 and 256 dimensions, fused with the lexical order.
The best LSA variant preserving development top-eight coverage lost five
first-place matches (311 versus 316 of 384). Plain TF-IDF's best first-place
result lost two top-eight hits. Both were rejected before held-out evaluation.

The first holdout exposed a candidate-generation ceiling: two expected documents
were absent even from the lexical top 40. A reranker cannot recover a document it
never receives. This motivated semantic candidate retrieval.

## Second round: semantic candidates plus reranking

The embedding prototype normalizes 384-dimensional vectors, searches all 12,632
documents by cosine similarity, and combines lexical and semantic rankings using
RRF with constant 10 and semantic weight .25. This exhaustive scan is an
evaluation implementation; it is not a scalable server vector-index design.

For reranking, the pool is the union of lexical top 20 and semantic top eight,
ordered by the fused rankings at depth 40. A document receives one candidate
entry. The selected ModernBERT reranker uses weight 1, constant 10, and at most
1,024 tokens per question/document pair. Its model supports longer inputs, but
those were not tested here.

| Candidate | Development first / top 5 / top 8 (384) | Fresh first / top 5 / top 8 (48) |
|---|---:|---:|
| Checkpoint | 316 / 373 / 378 | 42 / 46 / 47 |
| Lexical + semantic fusion | 327 / 373 / 379 | 44 / 47 / 48 |
| Fusion + MiniLM, weight .25 | 328 / 375 / 380 | 44 / 48 / 48 |
| Fusion + ModernBERT | 333 / 379 / 383 | 45 / 48 / 48 |

On this final holdout, semantic fusion gained two first-place matches and lost
none; ModernBERT fusion gained three and lost none relative to the checkpoint.
Only 48 questions were tested: these counts are too small to establish a narrow
future success rate. The remaining three first-place misses concern similar
subsystems or distinguishing qualifiers. Their nominated documents were ranks
2, 2 and 3. All-document dense retrieval alone was worse on development data
than the combination.

TinyBERT added no development rank-one benefit to semantic fusion and was
discarded without another held-out run. The smaller MiniLM hybrid improved
top-five coverage but not first-place matches over semantic fusion alone on
the fresh set; it remains an experimental cost/quality alternative.

The locked settings then generalized to the separate 80-question confirmation:

| Candidate | First | Top 5 | Top 8 |
|---|---:|---:|---:|
| Checkpoint | 71/80 | 77/80 | 77/80 |
| Lexical + semantic fusion | 74/80 | 77/80 | 78/80 |
| Fusion + ModernBERT | 78/80 | 80/80 | 80/80 |

ModernBERT gained seven first-place matches and lost none against the checkpoint.
The two remaining nominated paths ranked fourth: one question specified a
historical overview while more focused navigation documents ranked ahead; the
other asked whether recapturing a hill resets its timer, while timer-duration
settings ranked ahead. No labels were revised to count those alternatives.

Across the second 48 and final 80 questions, first-place matches were 123/128
(96.1%) for the hybrid versus 113/128 (88.3%) for the checkpoint. All 128 expected
documents were in the hybrid's top five. This aggregate combines two sequential
agent-authored samples from one corpus, not independent confirmation across
projects. The final confirmation's median reranking cost remained about 2.27
seconds on MPS; quality improvements did not remove the inference cost.

## Native compound-word candidate

An ordinary unmatched word can have a source phrase equivalent: for example,
`lookahead` and `look ahead`. The candidate adds that phrase only when it actually
occurs in a title or body. Generated identifier-column adjacency cannot trigger
it. Already indexed words and code-shaped identifiers retain their existing
interpretation. Work is bounded to 32 index probes and four extra phrases; the
existing context cancellation applies. There is no dictionary, service, schema
change or corpus-specific synonym table.

Across the now-known 384 questions, this changed two ranks: 4 to 1, and missing
from the top eight to rank 4. Totals moved from 316/373/378 to 317/374/379 for
first/top-five/top-eight. No other rank changed. Neither fresh 48-question set
showed an aggregate gain from this feature. It is a narrow recovery improvement,
not evidence of general semantic understanding.

The subsequent 80-question confirmation also had identical aggregate native
scores before and after compound recovery. It did not require a schema rebuild.

Independent boundary tests cover six compound forms, source-field adjacency,
identifier authority, probe and expansion budgets, cancellation and equality
between local and server snapshot retrieval. `explain` prints the actual expanded
match. A separate acronym-aware tokenizer experiment recovered a candidate but
reduced development first-place matches from 316 to 315; it was not adopted.

## Cost and integration evidence

Measurements were on an Apple M2. Exploratory model runs sometimes overlapped
with other work; these are not controlled server capacity benchmarks.

- BGE document embedding took about 46 seconds for 12,632 documents with MPS and
  batch size 32. Float32 vectors occupied 19,402,752 bytes, excluding model and
  index overhead. This is a build cost, not per-query latency.
- TinyBERT's isolated eight-candidate CPU validation measured approximately
  26/29 ms median/p95 inference, but its quality did not improve the holdout.
- MiniLM hybrid reranking measured approximately 571/782 ms median/p95 on CPU,
  overlapping another model experiment. It is cheaper than the larger reranker
  here but still adds meaningful per-query work.
- The final ModernBERT hybrid measured approximately 2.14/3.40 seconds
  median/p95 reranking on MPS, before normal retrieval and query embedding costs.
  This does not meet a lightweight default's cost expectations without further
  optimization and deployment work.
- The native compound candidate's paired 384-question check measured about
  139 ms median complete CLI time for both checkpoint and candidate. CLI startup,
  freshness checks and reconciliation are included. This is not a throughput
  guarantee.

A subsequent paired CPU backend check rotated execution order over the same
48 questions, with four threads, batch size four and the fixed MiniLM hybrid
parameters. These are backend regression checks on already evaluated questions:

| MiniLM backend | Median / p95 inference ms | First / top 5 / top 8 |
|---|---:|---:|
| PyTorch float32 | 526 / 697 | 44 / 48 / 48 |
| ONNX float32 | 938 / 1,308 | 44 / 48 / 48 |
| ONNX ARM64 int8 | 501 / 670 | 44 / 48 / 48 |

Int8 reduced the graph from 90,964,641 to 23,135,730 bytes. Every expected-answer
rank was preserved, but other document ordering changed on 41 of 48 queries;
quantization is not numerically identical. The approximately 5% median speedup
does not establish a large throughput gain. ONNX O3 optimization was also slower
than PyTorch on a preselected 12-query subset and was discarded. These results
are specific to this model and ARM64 machine; no Intel or Windows model speedup
is claimed.

Retrieval race tests, vet, macOS Intel tests under Rosetta, and PostgreSQL server
race tests passed for the native candidate. Both Claude and Codex MCP adapters
returned the same results with no errors. All five native artifacts were rebuilt
and checked against the canonical build.

The isolated server test passed 2,000 query-and-full-read workflows with four
communities, 8,000 synthetic documents, 1,000 reader keys and 128 concurrent
workflows. The two-CPU, 512-MiB app container measured 298/810 ms median/p95, with
zero workflow failures or scheduler rejections. A separate two-server test
passed 256 workflows and 1,024 variant reads using 16 projects, 64 connections
and 32 concurrent workflows. These validate native routing, content and
isolation under controlled load. They do not validate model throughput or
natural-language relevance at that concurrency.

The legacy research corpus has no finding UUIDs. Its zero duplicate-ID count
cannot establish deduplication correctness; the identified-copy integration
tests provide that evidence separately. The optional semantic harness rejects
multiple local copies of a UUID rather than bypassing production reconciliation.
It does not implement community authorization, federation or server model work.

## Answerability boundary

Eleven separate probes cover invented identifiers, out-of-corpus topics, future
information, ambiguity, absent values, negative claims and multi-document
questions. Actual native queries show why rank alone cannot determine an answer:
an absent function name can retrieve other function documents; a question about
a numeric default can retrieve the correct setting whose document contains no
default value. Four negative-claim probes retrieved the intended limiting
evidence first. A broad two-document probe did not reliably find both documents.

These probes are retrieval observations and manually specified expectations,
not an automated answer-generation accuracy score. The agent still needs to
read the documents, check scope, refine or split a query, and identify missing
information. MCP and project guidance now say this explicitly. No uncalibrated
score threshold was added to pretend a ranked hit proves an answer exists.

## Reproduction

Use `scripts/evaluate-ranking.py` for native binary comparisons. The separate
`scripts/evaluate-reranker.py` runs lexical reranking and optional semantic
candidate experiments against a fixed local index. Keep reports private: they
include question text and corpus paths.

The research environment used Python 3.12.13, sentence-transformers 5.1.2,
transformers 4.57.6, torch 2.14.1 and numpy 2.5.4. These are experimental
dependencies, not plugin requirements. CPU is supported by the harness; MPS was
used for the large reranker and embedding experiments. These models have not
been validated through the plugin on every supported OS.

The CPU backend comparison additionally used optimum 2.1.0, optimum-onnx 0.1.0,
onnx 1.23.2 and onnxruntime 1.31.0. Graph export, O3 optimization and ARM64 dynamic
quantization followed the official efficiency documentation linked below.

| Model | Pinned snapshot | License |
|---|---|---|
| `cross-encoder/ms-marco-TinyBERT-L2-v2` | `81d1926f67cb8eee2c2be17ca9f793c7c3bd20cc` | Apache-2.0 |
| `cross-encoder/ms-marco-MiniLM-L6-v2` | `233902d25c440f23af6f7d6e94d2946bac0bee0a` | Apache-2.0 |
| `Alibaba-NLP/gte-reranker-modernbert-base` | `f7481e6055501a30fb19d090657df9ec1f79ab2c` | Apache-2.0 |
| `BAAI/bge-small-en-v1.5` | `5c38ec7c405ec4b44b94cc5a9bb96e735b38267a` | MIT |

Download pinned snapshots separately, then run entirely locally:

```sh
python scripts/evaluate-reranker.py \
  --root /path/to/fixed-project \
  --binary /path/to/frozen/re-search \
  --questions /private/frozen-questions.jsonl \
  --model /path/to/modernbert-snapshot \
  --embedding-model /path/to/bge-small-snapshot \
  --query-prefix 'Represent this sentence for searching relevant passages: ' \
  --shortlist 20 --semantic-candidates 8 --fusion-depth 40 \
  --dense-weight .25 --dense-k 10 --weight 1 --rrf-k 10 \
  --max-length 1024 --batch-size 4 --device mps \
  --output /private/hybrid-results.json
```

Use `--device cpu` where appropriate. Omit embedding arguments for lexical-only
reranking. For a previously exported local CPU graph, use `--backend onnx` and
`--onnx-file onnx/model_int8_arm64.onnx` with its containing directory as `--model`.
The harness does not export or download graphs. It checks that the index and binary stay fixed, hashes model
files and questions, records actual candidate recall, and never inserts expected
answers into the result pool. Its one-vector-per-document projection truncates
long documents; long-document chunking needs separate evaluation.

## Research basis and next gate

The approach follows established [retrieve-and-rerank practice](https://www.sbert.net/examples/sentence_transformer/applications/retrieve_rerank/README.html)
and [reciprocal rank fusion](https://plg.uwaterloo.ca/~gvcormac/cormacksigir09-rrf.pdf).
[BEIR](https://arxiv.org/abs/2104.08663) motivates evaluating domain transfer rather
than assuming a model's original benchmark performance carries over. The
[BGE model card](https://huggingface.co/BAAI/bge-small-en-v1.5) specifies its query
instruction; the [ModernBERT reranker card](https://huggingface.co/Alibaba-NLP/gte-reranker-modernbert-base)
documents the larger model. [Sentence Transformers' efficiency documentation](https://www.sbert.net/docs/cross_encoder/usage/efficiency.html)
describes alternative inference backends; their quality and speed must be
measured on the deployment hardware before adopting them.

The rejected statistical approach follows [scikit-learn's LSA documentation](https://scikit-learn.org/stable/modules/decomposition.html#truncated-singular-value-decomposition-and-latent-semantic-analysis).
[UAEval4RAG](https://aclanthology.org/2025.acl-long.415/) evaluates answerable and
unanswerable requests separately, supporting a separate answerability evaluation
instead of treating a high retrieval rank as proof of an answer.

Before model integration: freeze a larger independently judged set across
different corpora, measure long-document recall and ambiguous/unanswerable
behavior, then test CPU/quantized inference, memory and concurrent server load.
Any model/index implementation must preserve per-community authorization,
current-copy reconciliation, cancellation and bounded work. Better benchmark
scores alone do not satisfy that deployment gate.

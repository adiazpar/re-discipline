# Retrieval research loop, 2026-10-25

The strongest prototype combines lexical retrieval, a small embedding model for
additional candidates, and a cross-encoder reranker. After the 48-question
comparison, an additional frozen 80-question confirmation set improved from
71 to 78 first-place matches and from 77 to 80 top-eight matches. All 80 were in
the top five. This is evidence of improvement
on this sample, **not a guarantee for arbitrary future questions**. A subsequent
63-question set improved from 53 to 60 first-place matches, with 62 expected
documents in the top five. A later score-fusion candidate improved another fresh
63-question set from 57 to 59 first-place matches over the hybrid, preserving all
63 in the top five. These sequential results do not establish universal coverage;
the earlier candidate-retrieval miss remains unresolved.
A sixth frozen 63-question draw gave the retained hybrid 59 first-place and
63 top-five matches, versus the native candidate's 48 and 59. A later-passage
experiment preserved those counts but failed its subsequent public transfer
gate, so the original hybrid remains selected.

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

A subsequent 80-question confirmation set excluded nominated answer paths from
all 432 previous questions: 36 facts, 36 reference entries and the eight remaining operational
documents, seed 20261028. SHA-256:
`ecbf8986578d81f9c40c49bb57d0a2fc9bdeacf23e96edf78b1c804f50cec930`.
The selected hybrid's parameters were kept unchanged for this confirmation.

A fourth draw selected 64 new documents, excluding target paths from all 512 previous questions,
with seed 20261029. One note contained only a topic description, so it became an
insufficient-evidence probe before retrieval, without resampling. The remaining
63 answerable questions comprise 31 facts and 32 reference entries. All 58
operational documents had already been used as targets. SHA-256:
`e41595febc448426fa16c05f7d8f2321340b9855e07aae5fc54e906ab27a8023`.

A fifth draw used seed 20261030 and excluded 538 paths: the 537 distinct nominated
paths from 575 previous questions, plus the earlier insufficient-evidence note.
Of 64 new documents, one reference contained no help text and became a separate
insufficiency probe before retrieval, without resampling. The 63 answerable
questions contain 32 facts and 31 references. Their SHA-256 is
`b55c2414d087e08f395611c4d822c239d04d5b0edf888320527e780cbc9a0d30`.
The score-fusion weights and acceptance criterion were frozen before these
questions were evaluated.

A sixth draw used seed 20261101 and excluded 602 prior nominated or insufficient
source paths. One of 64 selected sources lacked the requested details, leaving
63 answerable/source-relevance questions: 31 facts and 32 references. Three
questions had multiple acceptable passages judged before retrieval. An additional
ambiguity probe covered several controls with identical documentation. SHA-256:
`037856883327fa5e99e45c8498d7e1b75fdb337922e324987e3775af77d07b4b`.
The draw and judgments remained untouched through the rejected Qwen and sparse
trials; only the passage-selection follow-up that passed its development and
known-validation gates used them. All 701 private questions are now known data.
Some alternative accepted paths had appeared in earlier sets, so excluding new
nominated source paths does not make all relevance judgments independent.

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

An RM3-style pseudo-relevance-feedback prototype was also rejected on development
data. It built weighted expansion terms from the initial results and fused the
expanded BM25 ranking with the native order. The best setting preserving 316
first-place and 378 top-eight matches lost two top-five hits (371 versus 373).
The only setting with 317 first-place matches lost one top-eight hit. The grid
varied feedback documents (3/5/10), terms (10/20), original-query weight (.5/.75)
and fusion weight (.125/.25/.5/1). This was a standalone title/body prototype
using the same Porter tokenizer, not a reproduction of Lucene/Anserini's scoring.
It was not added to the engine or advanced to a new holdout.

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

The unchanged model, candidate depths and fusion weights then achieved
60/63 first-place and 62/63 top-five/top-eight matches on the fourth draw, versus
53/61/61 for the checkpoint. Seven first-place matches improved and none were
lost. Across these three separate sets, the counts are 183/191 first-place and
190/191 top-five matches, versus 166/191 and 184/191 for the checkpoint.

The missing candidate was lexical rank 24 and semantic rank 39, outside the
20-plus-eight pool. The other two nominated documents ranked second and fourth.
These labels and candidate budgets were not changed after seeing the results.
Returning more candidates has a compute cost; reranking cannot repair a missing
candidate. The fourth draw had no operational-document targets, so its 1.56-second
median inference time is not directly comparable with the earlier 80-question mix.

A smaller alternative, the 70.8-million-parameter Mixedbread xsmall reranker,
was rejected on the 384 development questions. No positive reranking weight beat
semantic fusion alone on first-place matches: weight .25 produced 324 first-place
and 381 top-eight matches, versus 327/379 without reranking and 333/383 for the
selected ModernBERT. It was not advanced to a fresh held-out evaluation. Smaller
parameter count did not translate into better latency here: a paired ten-query
pilot was slower on MPS than CPU, and the full CPU run's median was 1.93 seconds
while other experiments were running.

## Score fusion and a fresh confirmation

RRF uses rank positions and discards score gaps. An experiment instead min-max
normalized native lexical scores, embedding cosine similarities and ModernBERT
logits over the original candidate pool, then combined them. A development grid
selected weights .25 lexical, .25 semantic and .5 neural. Applying this to the
whole pool improved first-place matches from 335 to 339 but reduced top-five
coverage from 379 to 377 and top-eight coverage from 383 to 378. That unrestricted
candidate was rejected.

A constrained follow-up uses those weights only to reorder the first five
results of the established hybrid. Their membership and every position after
fifth stay unchanged; exact-identifier authority and supersession take precedence
over the scores. Development results were 339/379/383. Top-five/top-eight
preservation is a consequence of this constraint, not independent evidence of
better coverage. Constant score channels contribute zero. These normalized
scores are relative ranking features, not answer-confidence probabilities.

The already-known 48/80/63-question validation sets improved from 183 to 187
first-place matches, with five gains and one regression. The regression moved a
decal-distance setting from first to second. No weights were changed afterward.
The fifth draw then provided a prospective check:

| Method | First | Top 5 | Top 8 |
|---|---:|---:|---:|
| Current native candidate | 54/63 | 61/63 | 63/63 |
| Lexical + semantic fusion | 55/63 | 63/63 | 63/63 |
| ModernBERT 512-token hybrid | 57/63 | 63/63 | 63/63 |
| Same hybrid, first-five score fusion | 59/63 | 63/63 | 63/63 |

Both first-place gains were genuine changes to the nominated rank, with no
first-place losses. One other expected path improved from fourth to third. The
remaining misses include two pairs of differently named settings with identical
help text, a related event with a different signature, and an explanatory article
ahead of the requested field-frequency table. Labels were not broadened after
inspection. This distinguishes nominated-document accuracy from whether another
retrieved document is useful or answers the request.

Median/p95 ModernBERT inference was 1.23/1.64 seconds on MPS. The research harness
uses an additional native `explain` call to obtain aligned lexical scores; this
cost 211/278 ms median/p95 and is reported separately. It verifies that the
explanation matches the actual query pool and fails if it does not. No production
latency saving or server throughput claim follows from these measurements.
The optional `--head-fusion` flag does not alter default evaluation behavior or
plugin retrieval.

A separate late-interaction experiment with the 33.4-million-parameter
`answerdotai/answerai-colbert-small-v1` was much faster after document encoding,
but its best development result was 331/374/379 versus ModernBERT's 335/379/383.
Using it to select a smaller ModernBERT shortlist also failed to preserve the
quality baseline. Both were rejected before a fresh evaluation. Token vectors
for 3,066 distinct development candidates occupied about 124 MB; that is not a
full-corpus or 10-GB storage estimate.

A stronger single-vector encoder, `BAAI/bge-base-en-v1.5`, was then tested with
the same 20 lexical plus eight semantic candidates. On all 638 now-known private
questions, candidate coverage fell from 637 to 636; the lost target had already
ranked below the prior reranker's top eight. NFCorpus candidate coverage improved
from 247 to 255, with 11 gains and three losses. This failed the preset requirement
of nondecreasing coverage on both collections, so it was rejected as a replacement
before further reranking or a fresh evaluation. Its 768-dimensional private-corpus
matrix used 38,805,504 bytes versus 19,402,752 for the smaller encoder. A larger
model helped one collection but did not uniformly improve coverage.

A later replacement used [E5-small-v2](https://huggingface.co/intfloat/e5-small-v2),
with its required `query: ` and `passage: ` prefixes, masked mean pooling and
normalized 384-dimensional vectors. Its Sentence Transformers output matched a
manual pooling check. With the same 20-plus-eight budget, it preserved candidate
coverage at 700/701 now-known private questions, with no gains or losses. On
NFCorpus, coverage fell from 247 to 241 queries, with one gain and seven losses;
mean recall of positive judgments in the pool fell from .2032 to .1930. The fixed
coverage gate therefore rejected it before reranking or another fresh draw.
Private vector storage remained 19,402,752 bytes. Equal model dimensions and
private coverage did not establish equal transfer quality.

A wider candidate window was also rejected. A frozen comparison of 32 lexical
plus eight semantic, 32 plus 16, and 40 plus 16 candidates selected 32 plus 16
for its combined candidate coverage with the least additional scoring work.
All 961 original candidate lists were reproduced before widening, and every
wider pool retained the original candidates. The selected window increased
candidate coverage from 637 to 638 private questions and from 247 to 262
NFCorpus queries, but subsequent ranking did not preserve private quality:

| Fixed models and fusion | Private first / top 5 / top 8 (638) | NFCorpus first / top 5 / top 8 (323) | NFCorpus nDCG@10 |
|---|---:|---:|---:|
| Original 20 lexical + 8 semantic | 585 / 632 / 636 | 162 / 222 / 226 | .362749 |
| Wider 32 lexical + 16 semantic | 582 / 630 / 637 | 162 / 222 / 229 | .364034 |

The wider private pool caused three first-place losses and no gains. One loss
returned a plausibly useful alternative passage; frozen nominated-document
labels were not expanded after inspection. The public comparison had two
first-place gains and two losses. Every public per-query nDCG value was
independently checked against scikit-learn. Average unique candidate counts grew
from 24.33 to 42.06 privately and from 20.20 to 34.13 publicly. Only 15,812 new
query/document pairs needed scoring because the unchanged inputs reused their
previous scores; these partial timings do not measure full-query latency.
The original 20-plus-eight window remains selected. A larger candidate pool
finding more positives does not establish that its final ordering is better.

The [Qwen3 0.6B reranker](https://huggingface.co/Qwen/Qwen3-Reranker-0.6B)
was tested next with that original pool and unchanged fusion weights. Its
official prompt and yes-minus-no logit calculation were verified against a
manual forward pass, including preservation of the assistant suffix after
truncation to 512 total tokens. This was a local float32 experiment, with no
generated answers or remote inference.

The frozen development gate required at least 339 first-place, 379 top-five and
383 top-eight matches across 384 questions, with an improvement in at least one.
After 350 completed questions, the candidate had 305/345/349 versus the retained
model's 309/346/350 on that same prefix, with nine first-place gains and 13 losses.
Even a perfect remaining 34 could only reach 339/379/383, so the run stopped:
the required improvement was mathematically unreachable. This rejects this
fixed replacement configuration, not every possible use of the model. The final
34 questions were not evaluated, and no full-384 score is claimed. No fresh test
was consumed or controlled performance comparison performed for this rejection.

## Learning the final score combination

Two subsequent experiments learned a small ranking function from existing
lexical, embedding and reranker signals. Neither added text inference or used
document names, identifiers, categories or query words as prediction features.
The ten features were the three normalized scores, their reciprocal ranks,
three pairwise score products and the reciprocal rank from the existing RRF
stage. Only the first-five set could be reordered; authority rules and the
remaining results stayed fixed.

Five development folds grouped questions by connected components of shared
expected-document paths. The 384 questions formed 346 target components; no
target crossed folds. Hyperparameters were selected using predictions for each
fold from models trained on the other four. This remains exploratory: the
baseline and feature design had already seen development results, and unjudged
candidates treated as training negatives may include relevant alternatives.

The first experiment used the pairwise logistic loss described in
[RankNet](https://www.microsoft.com/en-us/research/publication/learning-to-rank-using-gradient-descent/),
with a linear scorer, L2 regularization and equal total training weight per
contributing query. Inverse regularization strengths `.01/.1/1/10` produced
334/333/332/334 first-place matches, versus the retained baseline's 339.

The second used [LambdaMART](https://www.microsoft.com/en-us/research/publication/from-ranknet-to-lambdarank-to-lambdamart-an-overview/)
through LightGBM 4.7.0. Small trees learned corrections to the existing score
combination, with its base score explicitly added during prediction. Depths
two/three crossed with 25/75 trees produced 336/336/334/331 first-place matches.
Exported tree evaluation matched the library within `1e-10` on the checked
training and validation features.

Both families failed the frozen requirement to exceed 339 development
first-place matches. All variants preserved 379 top-five and 383 top-eight
matches by construction. An independent audit checked 3,072 result lists for
rank reconstruction, unique paths, unchanged first-five sets and unchanged
tails. Neither family advanced to the other 317 known private questions, public
validation, or a fresh holdout. No learned scorer was added to the plugin.

An additional fixed ensemble screen tested whether the earlier small
cross-encoders supplied complementary information. It kept the ModernBERT RRF
stage, first-five membership, tail and authority rules, changing only the
first-five score from `.25 lexical + .25 dense + .5 ModernBERT` to
`.25 lexical + .25 dense + .375 ModernBERT + .125 small-model`. Every channel
was normalized over the same complete original candidate pool; no additional
weights were searched.

Existing 512-token score caches allowed this development check without new
inference. MiniLM produced 333 first-place matches, with two gains and eight
losses; TinyBERT produced 332, with two gains and nine losses, versus the
baseline's 339. Both preserved 379 top-five and 383 top-eight matches by
construction. An independent audit reconstructed all 768 result lists and
verified unique paths, head membership and unchanged tails. Both mixtures were
rejected before evaluation on the other 317 questions, public collections or a
new holdout. Cached score combinations do not establish inference latency.

The [Ettin reranker release](https://huggingface.co/blog/ettin-reranker) supplied
two further fixed candidates: the Apache-2.0 68M and 150M modular models,
pinned respectively to `d166fa88ddde3c42bc3ee92f7df476d941c8204a` and
`025501c4e0f9bbeb4c5b198318e0089ff061cc14`. Both ran locally with float32
weights, 512-token pairs, batch four and length grouping. Thirteen synthetic
pairs per model checked the actual CLS pooling and classifier modules,
manual-versus-wrapper logits, batch ordering and CPU/MPS agreement. The largest
CPU/MPS difference was below `4.8e-6`; no remote code or inference was used.

Replacing ModernBERT throughout the reranking stages produced the following
development results:

| Model | First / top-five / top-eight, 384 questions | Decision |
|---|---:|---|
| Retained ModernBERT | 339 / 379 / 383 | Baseline |
| Ettin 68M | 339 / 379 / 383 | No required improvement |
| Ettin 150M | 343 / 378 / 382 | Coverage regression |

Independent scalar audits reproduced both sets of rankings. The 68M run's
forward hook recorded no observations because the modular wrapper invoked
`forward` directly. Its 512-token configuration and synthetic truncation checks
were verified, but zero is not an observed maximum length for that corpus run.
The 150M run corrected the hook and observed a maximum of 512 tokens. Neither
replacement advanced beyond the 384 development questions.

A separately frozen cascade kept ModernBERT's first-five membership and tail,
then used `.25 lexical + .25 dense + .5 Ettin` only to reorder those five.
Normalization still used every member of the original candidate pool. The 68M
cascade tied the baseline and was rejected; the 150M cascade reached
343 / 379 / 383, with nine first-place gains and five losses. It then fell from
**305 to 304 first-place matches on the remaining 317 known questions**, with
three gains and four losses. Top-five and top-eight stayed at 316 each. This
failed the predeclared non-regression gate, so the cascade was also rejected
before public benchmarks, a fresh holdout or cost testing. Independent audits
checked all 768 development and 634 validation result lists, including fixed
head membership, tail order, unique paths and unchanged inputs.

The cascade needs both models; timing only the additional Ettin pass would
understate its total cost. No production or cross-platform runtime was added.
The author used NanoBEIR and MTEB during checkpoint selection, so public
SciFact/NFCorpus results would not constitute independent model-selection
holdouts for these models.

## Learned sparse retrieval

The [OpenSearch document-only sparse model](https://huggingface.co/opensearch-project/opensearch-neural-sparse-encoding-doc-v3-distill)
expands documents into weighted vocabulary terms during ingestion. Queries use
unique tokenizer IDs and a fixed IDF table; the query process imports neither
Torch nor Transformers. The experiment used the official double-log activation,
masked special-token dimensions and exact float32 postings, without pruning.
Manual and Sentence Transformers scores agreed on synthetic checks, and posting
accumulation matched direct matrix products. This requires no OpenSearch service.

Across 12,632 documents, the matrix contained 2,432,663 nonzero weights: 19.51 MB
in CSR arrays, or 12.06 MB as a compressed NPZ. Document encoding took about
112.6 seconds on MPS. These are this corpus's storage and encoding measurements,
not full startup, a 10-GB projection or concurrent-server capacity. Documents
were still truncated at 512 input tokens.

| Fixed sparse experiment | First / top 5 / top 8 (384 known questions) |
|---|---:|
| Native comparison | 316 / 373 / 378 |
| Retained semantic + ModernBERT hybrid | 339 / 379 / 383 |
| Sparse alone | 266 / 355 / 362 |
| Native + sparse RRF, weight .25 | 319 / 375 / 379 |
| Native + sparse RRF, weight .5 | 317 / 376 / 379 |
| Native + sparse RRF, weight 1 | 316 / 371 / 377 |
| Sparse replaces ModernBERT scoring in the existing hybrid | 315 / 373 / 380 |

The best sparse combinations improved the native comparison but failed the
stronger retained-hybrid gate. Replacing only the expensive reranker also failed,
with six first-place gains and 30 losses. A further constrained experiment blended
sparse scores into the retained first-five score fusion at weights .125, .25 and
.5. First-place counts fell to 332, 328 and 320; top-five/top-eight membership was
preserved by construction. No sparse variant advanced to a fresh holdout or the
plugin runtime.

Token lookup and posting accumulation measured .13/.20 ms median/p95; sorting
and calculating all four initial profiles took 4.36/5.51 ms. These timings exclude
native queries, cold loads, ingestion and concurrency. The hybrid replacement
still needs the BGE query encoder, so it is not an inference-free whole pipeline.
The model card explicitly lists SciFact and NFCorpus among its training data;
those would not establish unseen transfer for this model. Token-only queries
also discard word order: reversing subject and object while retaining the same
token set produces identical sparse scores. A fast score is neither an answer
confidence nor proof that the retrieved passage supports a claim.

A separate [two-sided sparse model](https://huggingface.co/opensearch-project/opensearch-neural-sparse-encoding-v2-distill)
then expanded both queries and documents with the same 67-million-parameter
encoder. This requires query inference, unlike the earlier document-only trial.
The unchanged lexical-20-plus-sparse-eight candidate budget covered 698 of 701
private questions, versus 700 for the retained dense channel: zero gains and two
losses. The frozen coverage gate therefore failed before final reranking.
An independent audit reconstructed all document/query vector row mappings and
all 701 candidate sets from cached vectors, with identical dot-product scores.
The public stage did not complete because a candidate-list variable shadowed
the pooling function after the private stage; that harness error was fixed and
recorded. No public result is claimed, and the already-rejected candidate was
not rerun merely to obtain one.

## Later passages in long documents

The next experiment kept the original candidates and models, but allowed one
additional passage score when the original query/document pair exceeded 512
tokens. Later body windows contained 320 ModernBERT tokens at stride 256, with
a title prefix capped at 64 tokens. BGE selected one later window by cosine
similarity; the document retained the maximum of its original prefix score and
the new passage score. Exact-identifier priority and supersession stayed intact.
There was no window or weight grid. A 128-window cap was never reached here.

This unconditional comparison gained two development first-place matches
(341/384 versus 339), preserving 379 top-five and 383 top-eight hits. However,
the separate known 254 questions lost one first-place match, from 246 to 245.
It was rejected. A separately frozen follow-up only scored a later passage when
BGE preferred it to the existing document representation. It preserved the two
development gains and all known-validation counts, then passed the fresh check:

| Sixth draw (63 questions) | First | Top 5 | Top 8 |
|---|---:|---:|---:|
| Native candidate | 48 | 59 | 62 |
| Retained hybrid | 59 | 63 | 63 |
| Hybrid with selected later passages | 59 | 63 | 63 |

The follow-up changed 36 full result orders but no expected-answer ranks in the
first eight. This is evidence of no observed relevance loss on this draw, not
a prospective gain. Its 146 additional pairs took about 9.7 seconds in total;
cached original scores mean this is not a complete query-latency measurement.
Actual model inputs were checked during forward passes, with a maximum of 401
tokens in this run. The private corpus added 1,145 windows across 685 documents,
using 1,758,720 bytes of vectors and about 9.9 seconds of encoding time. These
figures exclude the original document vectors and other index/model overhead.

The unchanged follow-up then failed a stricter fixed transfer gate:

| Known public collection | First / top 5 / top 8, unchanged | Baseline nDCG@10 | Passage nDCG@10 |
|---|---:|---:|---:|
| SciFact, 300 queries | 203 / 250 / 262 | .768179 | .768139 |
| NFCorpus, 323 queries | 162 / 222 / 226 | .362749 | .362593 |

Every per-query nDCG value was independently checked with scikit-learn, including
zero-gain padding for short returned lists. These tiny declines are not evidence
of a large general disadvantage, but they violate the preselected no-decrease
rule and provide no public first-place gain to justify the added work. The
passage augmentation was therefore rejected as a default. Neither variant was
added to the public harness or plugin runtime. The result does not rule out
properly trained passage selectors or other aggregation methods.

The separate answerability checks also matter: native retrieval placed a
nominated note first even though that note did not enumerate the requested
fixes. Nine reference entries shared the same help text without distinguishing
the controls' behavior. These checks establish limitations of those sources,
not that the entire corpus lacks an answer. Ranking a topical document first
cannot establish complete support; the agent must read it and seek additional
evidence or state what remains unknown.

## Public corpus transfer check

The same fixed hybrid was evaluated against all 300 test claims and 5,183
abstracts in [BEIR's SciFact distribution](https://github.com/beir-cellar/beir).
All 339 positive relevance judgments were retained, including multiple relevant
articles for a claim. Titles and abstracts were wrapped in Markdown with uniform
metadata; no aliases or corpus-specific query rules were added. The official ZIP
checksum was verified (`5f7d1de60b170fc8027bb7898e2efca1` MD5; SHA-256
`536e14446a0ba56ed1398ab1055f39fe852686ecad24a6306c80c490fa8e0165`).

| Method | Relevant first | Relevant in top 5 / top 8 | nDCG@10 | Mean recall@10 |
|---|---:|---:|---:|---:|
| Native checkpoint | 164/300 | 230 / 246 | .6776 | .8247 |
| Lexical + semantic fusion | 172/300 | 241 / 253 | .7067 | .8438 |
| Fusion + ModernBERT, 1,024 tokens | 190/300 | 248 / 262 | .7542 | .8762 |
| Same hybrid, 512 tokens | 190/300 | 250 / 262 | .7550 | .8746 |
| 512 tokens, first-five score fusion | 203/300 | 250 / 262 | .7682 | .8746 |

The hybrid gained 29 first-place hits and lost three. At least one judged relevant
article reached its candidate pool for 274/300 claims. The first/top-five/top-eight
columns count queries with any relevant hit; recall counts the fraction of all
judged relevant articles retrieved for each query. nDCG also accounts for their
positions. Each nDCG calculation was checked against scikit-learn independently.

This check uses independently supplied judgments and a different corpus, but it
is scientific claim retrieval, not answer generation or representative reverse
engineering traffic. Public benchmark data may overlap a model's training or
evaluation history; this is not a claim of uncontaminated zero-shot performance.
No parameters were tuned on these results. The lower absolute scores demonstrate
that the private corpus's approximately 96% first-place result cannot be promised
for every collection or question type. Reranking measured approximately 1.86/3.15
seconds median/p95 on MPS; these exploratory timings are not capacity guarantees.
The 512-token variant was selected on private development data before these public
model results were viewed. It kept every first-place hit and gained two top-five
queries, but one secondary relevant article fell outside the top ten. The shorter
input is a cost/coverage tradeoff, not an identical replacement.

The later constrained score-fusion check reused the unchanged 512-token pair
scores and verified every reconstructed candidate pool and original RRF order.
It improved first-relevant hits from 190 to 203, with 19 gains and six losses.
Its nDCG@10 gain was .0131, with exploratory paired-bootstrap interval
[.0004, .0259]; the first-hit proportion gain was .0433, with interval
[.0133, .0767]. These use 10,000 paired query resamples, seed 20261031, without
adjusting for earlier candidate exploration. SciFact was already known at this
point; this is a fixed-parameter transfer check, not a new untouched holdout.
Reusing scores does not supply a new full-inference latency measurement.

A second public check used all 323 NFCorpus test queries, 3,633 documents and
12,334 positive graded judgments from the same BEIR distribution. The original
titles and abstracts were preserved under uniform Markdown metadata. The official
ZIP MD5 was `a89dba18a62ef92f7d323ec890a0d38d`, SHA-256
`efe5be03f8c5b86a5870102d0599d227c8c6e2484328e68c6522560385671b0b`.
All settings, including the constrained fusion weights, were frozen before this
evaluation. The acceptance gate required nondecreasing first-relevant hits and
nDCG@10 relative to the 512-token RRF hybrid.

| NFCorpus method | Relevant first | Relevant in top 5 / top 8 | nDCG@10 | Mean recall@10 |
|---|---:|---:|---:|---:|
| Current native candidate | 147/323 | 205 / 212 | .3207 | .1498 |
| Lexical + semantic fusion | 153/323 | 210 / 219 | .3408 | .1628 |
| ModernBERT 512-token RRF hybrid | 159/323 | 222 / 226 | .3607 | .1693 |
| Same hybrid, first-five score fusion | 162/323 | 222 / 226 | .3627 | .1693 |

The constrained fusion passed this point-estimate gate with 11 first-place gains
and eight losses. Its nDCG@10 gain was .0020; an exploratory 10,000-sample paired
bootstrap interval was [-.0011, .0052]. The first-hit proportion difference was
.0093, with interval [-.0155, .0372]. Both include zero, so this is a small observed
gain, not strong evidence of a general advantage. Recall@10 is unchanged because
only the first five positions are reordered. Graded nDCG uses the qrel value as
gain, matching [trec_eval](https://github.com/usnistgov/trec_eval/blob/master/m_ndcg_cut.c),
and was independently checked against scikit-learn for every query and method.

Only 247/323 queries had any judged relevant document in the 20-plus-eight pool;
mean recall of all positive judgments in that pool was .2032. That candidate
ceiling matters more than small changes to the final ordering. Fifteen queries
had no lexical hits. An initial harness run stopped because `explain` represents
an empty result as JSON null; a regression-tested correction supplies zero
lexical evidence for semantic-only candidates. The complete rerun kept every
parameter and judgment unchanged. Median/p95 reranking was 1.39/1.77 seconds on
MPS, excluding embedding construction and the extra explanation call.

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
The fourth 63-question set and the public SciFact native comparison also had
identical aggregate scores before and after compound recovery.

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

An optional length-grouping experiment reduces padding by batching similar-sized
query/document pairs, then restoring every score to its original candidate.
It does not shorten the text. Batch size four was fastest in a ten-query
development pilot (1.38x median paired speedup); larger batches were slower.
All full result orders, not just expected-answer ranks, were preserved on the
80-question confirmation, with maximum logit difference below 4.8e-7. Its full-run
median/p95 was 2.19/3.43 seconds with concurrent CPU work, so the pilot speedup
must not be presented as a general performance improvement. The harness exposes
this as `--sort-by-length` and keeps it optional.

Reducing ModernBERT's input limit from 1,024 to 512 tokens improved development
first-place matches from 333 to 335 while retaining 379 top-five and 383 top-eight
matches. Of 9,370 development pairs, 1,458 exceeded 512 tokens and were rescored;
unchanged inputs reused their original scores. On the separate, already evaluated
48/80/63-question validation sets, first-place and top-five/top-eight counts were
unchanged, with no first-place gains or losses. One expected rank worsened from
third to fourth in the first validation set. These checks measure ranking quality,
not full-query latency; timing only the changed pairs would understate the cost.
Reducing the lexical pool to 16 alongside the shorter input lost one development
first-place match and was not selected. The candidate pool remains 20 plus eight.

A subsequent paired ten-query timing check rotated five variants, with batch size
four, float32 and four CPU threads. No other model experiment was running:

| ModernBERT variant | Median / p95 inference ms |
|---|---:|
| MPS, 1,024 tokens, original order | 1,695 / 2,987 |
| MPS, 1,024 tokens, length grouped | 1,267 / 2,015 |
| MPS, 512 tokens, length grouped | 1,028 / 1,528 |
| CPU, 512 tokens, length grouped | 2,253 / 3,306 |
| CPU, 1,024 tokens, length grouped | 2,819 / 4,180 |

The shorter grouped MPS variant's median paired speedup was 1.99x. CPU and MPS
preserved all ten full result orders at 512 tokens, though their logits differed
slightly. These small-sample inference timings exclude candidate retrieval,
document embedding and model startup, and do not measure server throughput.

The public harness then ran all 63 fourth-draw questions with the current native
binary and the grouped 512-token profile. It reproduced all candidate pools and
full result orders from the cached comparison, retaining 60 first-place and 62
top-five/top-eight matches. Median/p95 reranking was 1.28/1.67 seconds. This larger
check is a more useful latency observation than extrapolating the ten-query
pilot's speedup to every query.

ModernBERT was also exported to an ONNX graph to test a smaller inference
dependency stack. A private runner uses ONNX Runtime, NumPy and the Rust-backed
tokenizer without importing Torch or Transformers. Its pair token IDs, masks,
512-token truncation and score alignment were checked against the original
tokenizer. Twelve synthetic batch/sequence shapes had float32 logit differences
below `4.3e-6`. These are numerical plumbing checks, not corpus or platform proof.

A per-channel dynamic int8 MatMul variant reduced graph size from 598,997,294
to 267,317,264 bytes, but failed retrieval quality. After 293 development
questions it returned 248/286/292 first/top-five/top-eight matches, versus
254/289/293 from the retained model on the same prefix. It had three first-place
gains and nine losses, and lost three top-five matches. Even perfect results on
the remaining 91 questions could reach only 377 top-five matches, below the
required 379, so the run was stopped and rejected. No full-384, full-701 or public
result is claimed for that variant, and no paired speed test was performed.

The quantized model also showed batch-dependent numerical differences in the
synthetic probe. Exact score-to-document alignment does not imply numerical
equivalence across batch compositions. This is consistent with ONNX Runtime's
[requirement to validate quantization accuracy](https://onnxruntime.ai/docs/performance/model-optimizations/quantization.html).

The float32 run preserved all full result orders on its first 82 private
questions, with maximum logit difference `2.93e-5`. Preliminary timings motivated
moving the unchanged cost gate ahead of the remaining quality checks; this
ordering amendment was recorded before the paired test. Twenty fixed, randomly
sampled known queries were measured three times each, alternating backend order,
with four CPU threads, batch size four, 512 tokens and length grouping:

| Backend, Apple M2 CPU | Median / p95 reranking ms |
|---|---:|
| Retained Torch float32 | 2,763 / 4,042 |
| ONNX float32 | 7,815 / 12,152 |

These quantiles use each query's median of three repetitions. Other research
inference was stopped or suspended during measurement. Both backends were
resident; the approximately 2.05-GB peak is combined process memory, not an
individual backend's footprint. The ONNX candidate failed its required 20%
median reduction and no-p95-regression gate. Its suspended quality run was
terminated, so no full-corpus float32 quality result is claimed. This rejects
the tested CPU graph/runtime configuration, not every native inference option.

The official
[ONNX Runtime 1.31.0 release assets](https://github.com/microsoft/onnxruntime/releases/tag/v1.31.0)
also lack an Intel macOS package. The last inspected release with Intel/universal
macOS assets was 1.23.2; compatibility with that runtime or a current source build
has not been tested. No native plugin platform support changed in these trials.

A separate native-runtime experiment used the official
[llama.cpp b11669 release](https://github.com/ggml-org/llama.cpp/releases/tag/b11669),
pinned to `620d2225cc436ab0b001e3200e590e3b286ef9ab`. Its converter transformed
the original ModernBERT weights into a 600,202,304-byte float32 GGUF file. A
private C++ helper supplied explicit token sequences through the C API and read
one classifier scalar per sequence; no HTTP service or Python inference engine
was required by that helper. Python only orchestrated the comparison.

All 12 synthetic batch/length cases agreed with Torch within `6.2e-6`. Nine
tokenizer cases matched exactly, including Unicode, literal special tokens and
longest-first truncation at 512 tokens. The fixed timing sample also matched
token IDs on every query/document pair, with maximum score difference `1.15e-5`.
These checks do not establish full-corpus ranking or cross-platform behavior.

With native batch size four, the same 20-query, three-repeat alternating CPU
test measured median/p95 reranking of **7,929/12,626 ms**, versus
**2,496/3,725 ms** for Torch. Native tokenization, IPC, ordering and inference
were included; candidate retrieval and startup were excluded. This experiment
required no median or p95 regression, a separately declared native-packaging
gate rather than the earlier ONNX 20% improvement target. It failed, so this
configuration did not advance to full-corpus quality evaluation. The helper
exited cleanly. Release packages for other architectures are not compatibility
test results.

A follow-up used one document pair per native encode, motivated by the pinned
runtime's [combined-token attention matrix](https://github.com/ggml-org/llama.cpp/blob/620d2225cc436ab0b001e3200e590e3b286ef9ab/src/llama-graph.cpp).
It retained the same model, binary, CPU threads, tokenizer and float32 attention.
The 12 synthetic cases matched Torch within `2.2e-6`, and all nine tokenizer
checks passed. In a separate paired run over the same 20 queries, native
median/p95 was **3,934/6,066 ms**, versus **2,619/3,760 ms** for Torch batch four.
All 60 paired measurements were audited, with maximum score difference
`1.13e-5`. This configuration also failed the no-latency-regression gate and
was rejected before full-corpus evaluation. Both native tests included
tokenization and IPC; neither establishes the performance of quantized weights,
other attention kernels, GPU inference or another operating system.

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

The fourth draw's excluded note illustrates this distinction directly: native
retrieval placed it first, but reading it showed that it names the topic without
providing the requested instructions. A rank-one hit would have been a misleading
success label if answerability had not been checked before retrieval.
The fifth draw likewise excluded a variable entry with no help text before
asking the system for its supported values; a name match would not supply those
missing semantics.

Incomplete judgments are another limitation: a different useful passage can
look like a failure when only one nominated path is accepted. The
[D-MERIT study](https://arxiv.org/abs/2406.16048) demonstrates that partial
annotation can change measured system comparisons on its Wikipedia collection.
That does not establish the size of this effect here. Existing judgments remain
frozen; subsequent source-based question sets identify multiple acceptable
passages before retrieval when the sources justify them. Neither a larger
accepted set nor a rank-one hit proves that an agent has a complete answer.

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
The rejected ColBERT and Qwen trials used a separate environment with
sentence-transformers 6.0.0 and transformers 5.20.2; they did not change the
hybrid's environment.

| Model | Pinned snapshot | License |
|---|---|---|
| `cross-encoder/ms-marco-TinyBERT-L2-v2` | `81d1926f67cb8eee2c2be17ca9f793c7c3bd20cc` | Apache-2.0 |
| `cross-encoder/ms-marco-MiniLM-L6-v2` | `233902d25c440f23af6f7d6e94d2946bac0bee0a` | Apache-2.0 |
| `Alibaba-NLP/gte-reranker-modernbert-base` | `f7481e6055501a30fb19d090657df9ec1f79ab2c` | Apache-2.0 |
| `BAAI/bge-small-en-v1.5` | `5c38ec7c405ec4b44b94cc5a9bb96e735b38267a` | MIT |
| `mixedbread-ai/mxbai-rerank-xsmall-v1` | `b5c6e9da73abc3711f593f705371cdbe9e0fe422` | Apache-2.0 |
| `answerdotai/answerai-colbert-small-v1` | `934fa8bb4ce2284f4c2baa232d81aca4d076fa5e` | Apache-2.0 |
| `BAAI/bge-base-en-v1.5` | `a5beb1e3e68b9ab74eb54cfd186867f64f240e1a` | MIT |
| `Qwen/Qwen3-Reranker-0.6B` | `e61197ed45024b0ed8a2d74b80b4d909f1255473` | Apache-2.0 |
| `opensearch-project/opensearch-neural-sparse-encoding-doc-v3-distill` | `babf71f3c48695e2e53a978208e8aba48335e3c0` | Apache-2.0 |
| `opensearch-project/opensearch-neural-sparse-encoding-v2-distill` | `269e6638b2c4f648996691f6d751495285d8f330` | Apache-2.0 |
| `intfloat/e5-small-v2` | `ffb93f3bd4047442299a41ebb6fa998a38507c52` | MIT |

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
  --max-length 1024 --batch-size 4 --sort-by-length --device mps \
  --output /private/hybrid-results.json
```

Use `--device cpu` where appropriate. Omit embedding arguments for lexical-only
reranking. To reproduce the newer constrained fusion experiment, use
`--max-length 512 --head-fusion` with the same other hybrid settings. It records
both the original RRF order and the changed order, together with aligned feature
scores. Torch uses explicit float32; `--attention auto` supports architectures
without SDPA, while `--attention sdpa` can pin that implementation for supported
models. The report records the actual attention implementation. For a previously
exported local CPU graph, use `--backend onnx` and
`--onnx-file onnx/model_int8_arm64.onnx` with its containing directory as `--model`.
The harness does not export or download graphs. It checks that the index, binary
and evaluator stay fixed, hashes model
files and questions, records actual candidate recall, and never inserts expected
answers into the result pool. Its one-vector-per-document projection truncates
long documents; long-document chunking needs separate evaluation.

Dependency-free evaluation-contract tests run in all five native CI environments.
They check score/document alignment after batching, invalid model outputs,
authority and supersession ordering, multiple pre-judged answers and preservation
of scope when removing local evidence, along with first-five membership and tail
preservation during score fusion. They do not establish that model inference
works or performs equally on every platform.

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

[Bruch, Gai and Ingber's fusion analysis](https://www.pinecone.io/research/an-analysis-of-fusion-functions-for-hybrid-retrieval/md/)
motivated the convex-score experiment. Its published result is a reason to test
the method, not evidence that our chosen weights generalize. The rejected
late-interaction trial followed the [Answer.AI model card](https://huggingface.co/answerdotai/answerai-colbert-small-v1)
and [Sentence Transformers' multi-vector encoder documentation](https://sbert.net/docs/multi_vector_encoder/usage/usage.html).
Adaptive alternatives were researched but not transplanted into the current
model: [E2Rank](https://www.pinecone.io/research/e2rank-efficient-and-effective-layer-wise-reranking/md/)
uses trained intermediate transformer outputs, while [AcuRank](https://arxiv.org/abs/2505.18512)
uses repeated listwise LLM comparisons and a probabilistic rating model. Neither
is equivalent to thresholding this pointwise reranker's raw logit.

The rejected statistical approach follows [scikit-learn's LSA documentation](https://scikit-learn.org/stable/modules/decomposition.html#truncated-singular-value-decomposition-and-latent-semantic-analysis).
The feedback experiment follows the term-distribution and interpolation approach
in [Anserini's RM3 implementation](https://github.com/castorini/anserini/blob/master/src/main/java/io/anserini/rerank/lib/Rm3Reranker.java).
The [PARADE study](https://arxiv.org/abs/2008.09093) motivates evaluating document
length and passage aggregation separately: the useful relevance signal can be
localized or spread across passages. A shorter input performing well on this
corpus does not establish that later passages are dispensable in general.
[UAEval4RAG](https://aclanthology.org/2025.acl-long.415/) evaluates answerable and
unanswerable requests separately, supporting a separate answerability evaluation
instead of treating a high retrieval rank as proof of an answer.

The public checks add independent judgments across corpora, but expose a
remaining candidate-coverage ceiling. Replacing the embedding model failed its
coverage gate; widening the candidate window improved coverage but failed its
ranking-quality gate. A larger reranker could not improve the frozen development
criteria. Learned document expansion made query scoring inexpensive but did not
preserve the retained hybrid's relevance. The bounded passage experiment followed
the motivation in [Intra-Document Cascading](https://arxiv.org/abs/2105.09816),
whose trained cascade differs from our reuse of an existing embedding model.
That simpler variant passed one fresh no-regression check but failed public
transfer. E5 subsequently failed its candidate-coverage gate. Further ranking
experiments must preserve coverage before spending more compute on reranking
and a new untouched holdout. Improving the retained reranker's deployment cost
is also necessary; the earlier MiniLM backend results do not establish how
ModernBERT will behave under CPU graph export or quantization.
Before model integration, measure
long-document recall and ambiguous/unanswerable behavior, then test
CPU/quantized inference, memory and concurrent server load.
Any model/index implementation must preserve per-community authorization,
current-copy reconciliation, cancellation and bounded work. Better benchmark
scores alone do not satisfy that deployment gate.

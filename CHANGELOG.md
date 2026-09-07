# Changelog

## 1.6.1 - 2026-09-07

The hosted community service moves to `https://167.99.51.162`, with direct GitHub
sign-in. Existing users sign in to the new service and reconnect their community
alias. Community IDs and memberships are preserved. Offline caches and drafts
remain local; queued drafts retain their original destination and must be reviewed
before publication to a different service address.

## 1.6.0 - 2026-09-07

Adds isolated community libraries, device sign-in, invitations, scoped publication
and review, and retrieval from local, external, or combined sources. Accepted
community knowledge synchronizes to offline caches; selected drafts can be queued
locally for explicit publication later.

## 1.5.0 - 2026-08-19

Two new commands and a skill that draws the result.

`re-search explain "<question>"` answers a question and shows its working:
every candidate considered, its bm25 score, the penalty applied to it, and its
rank before and after that penalty. A row whose two ranks differ is one the
re-rank moved, which is usually the whole answer to "why did this come back
first". It runs the same ranking code as `query` rather than a copy of it, and
a test pins the two orders together, so the explanation cannot drift into a
plausible lie.

`re-search stats` is the census: documents by kind, status and grade, symbol
and golden-question counts, index format, and the ranking constants this build
uses. Both commands take `--json`. Flags go before the question, as everywhere
else in this CLI.

The new `visualize` skill turns those two commands into a page: what is in a
knowledge base, and how a question becomes ranked answers, walked through two
real questions from that project with its own numbers. It ships a
project-neutral template and fills it from the probe, so nothing on the page is
borrowed from somewhere else.

The `curate` skill now documents `idents` and `aliases`. It had never mentioned
either, which meant an agent following it wrote documents that the ranking
could not favour and that aliases could not reach -- the engine supported them
and the instructions did not.

## 1.4.0 - 2026-08-19

Three changes, each measured on a 12,314-doc corpus against a 231-question
golden set. Together they took it from 208 to 219.

`query` now returns 8 results instead of 5. Thirteen of the twenty-two
failures were ranked 6-26, so the right document was already being retrieved
and then cut off; raising the page fixed seven of them without touching
ranking. Curated reference entries have a median body of 129-420 characters,
so three more snippets cost a caller almost nothing. `symbol` keeps its
default of 5 -- a single struct render can run to hundreds of lines.

New `aliases:` frontmatter, for the docs whose own words are not the words a
reader would type. A cvar whose engine help string reads "scales the time"
cannot be reached by "how do I slow down game time" -- there is no shared
word to match. Aliases are indexed as ordinary text in the identifier column
and, unlike `idents`, confer no authority: they never waive the reference-doc
rank penalty, because an alias is a phrasing hint and not a claim to own a
name. Two rules keep them safe, and CONVENTIONS.md now carries both: a
handful per doc, and never a word that would land in hundreds of them.

FTS5 now stems with `porter`, so a question about what "wraps" the executable
reaches a document that says "wrapping". This was measured twice and shipped
only on the second result: on its own it was worth -1, and it becomes worth
+4 once aliases exist. Aliases are natural-language phrasings, which is
precisely the text whose word forms vary -- there is little for a stemmer to
fold together in a dump of identifiers.

The index format version is bumped, so existing indexes rebuild on first
query rather than serving stale terms.

## 1.3.0 - 2026-08-19

The benchmark could only test `query`. A golden case may now carry `symbol`
instead of `q`, in which case `expect` is a symbol name rather than a document
path, so symbol lookup is regression-tested like everything else — exact match
and the substring fallback both. Exactly one of the two fields must be set.

Written after a corpus reached 12,152 documents and 44,878 symbols with no
coverage of the symbol table at all in its 148-question golden set. An
evaluation blind to a surface cannot detect regressions in it.


## 1.2.0 - 2026-08-19

Retrieval work driven by a corpus that grew from 254 to 11,054 docs,
97% of it generated reference material. Ranking now weights title and
identifiers above body, and strips cross-reference bullets from the
index (not from your files), so a "Depends on" list quoting other docs'
titles no longer makes every doc compete for every other doc's topic.
That alone moved a 148-question benchmark from 89 to 98.

Generated per-item docs get a new `kind: reference` and a rank penalty
on natural-language questions, waived when the query names one of the
doc's declared `idents`. A concept question therefore reaches curated
facts while a name lookup reaches the reference entry. Tried and
rejected: a hard tier and a default kind filter, both of which fixed
the fact half by destroying the reference half, taking name lookups
from 41/42 to 7/42.

New `symbol` lookup for knowledge that is a table rather than a
document — struct layouts, constants, enum groups — read from an
optional `.re-discipline/symbols.jsonl` into a separate table outside
the doc index, and reachable from the CLI, the MCP server and HTTP.
Measurement kept it behind a dedicated call instead of blending into
`query` results: most name collisions across a real golden set were
ordinary English words matching constants.

Also: `query` gains `--kind` and `--grade` filters on all three
surfaces; `idents:` and `evidence:` frontmatter are parsed, the latter
having been documented but silently dropped; snippets widen with match
markers; and the index carries a format version so a content-shape
change forces a rebuild instead of serving stale terms indefinitely.

## 1.1.0 - 2026-08-18

Host-wiring correction, verified against Codex source and Claude Code
docs: root `AGENTS.md` is now the single canonical agent file (Codex
reads it natively but never reads `.codex/AGENTS.md`; Claude Code does
not auto-read `AGENTS.md` and imports it instead). `init-project` now
writes the marker block only into `AGENTS.md`, ensures root `CLAUDE.md`
contains `@AGENTS.md`, and removes marker blocks from pre-1.1 dual-block
layouts. New "Working artifacts" convention: campaign-scoped scratch in
`active/<slug>/work/`, durable tooling graduates to the project source
tree. README gains an Updating section — re-running init-project is the
whole upgrade path for initialized projects.

## 1.0.0 - 2026-08-18

Ground-up simplification. Markdown in `.re-discipline/docs/` is now the
canonical knowledge store; the retrieval index is derived and
disposable. Replaces the 0.x transactional engine (digests, head
revisions, ratification, write-guard hooks, retrieval profiles,
benchmarking apparatus) with:

- `re-search`: small ephemeral Go CLI — `index`, `query`
  (auto-reindexing, identifier-aware FTS5), `bench` (golden regression
  questions), `serve --mcp` (per-session stdio, any MCP host),
  `serve --http`.
- Producer-distilled curation: investigators write reports + atomic
  candidate findings; the manager's promotion skim is the single gate.
- 4 skills (init-project, open-campaign, close-campaign, curate), one
  read-only SessionStart hook, shared memory as `docs/ops/`.
- Windows-only; single 1–2 minute CI job.

Migration from 0.x is a one-time agent session (old JSON → markdown);
init detects old state and stops rather than converting.

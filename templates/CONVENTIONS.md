# re-discipline Conventions

This project curates hard facts about the software under analysis in
`.re-discipline/`. Any agent that can read files and run a command can
use it. The recovery path for every problem here is "edit a text file"
or "delete and rebuild" for search indexes. Publication receipts are provenance:
retain `.re-discipline/community/` when moving a workspace so retry identities
and verified local/community relationships survive.

## Layout

- `docs/` — curated truth. One atomic claim or topic per file. `docs/ops/`
  holds operational recall (workflows, tool paths, dead ends) — this is
  the project's shared memory.
- `active/<campaign-slug>/` — one campaign (investigation workspace) per
  task: `CAMPAIGN.md` (goal + status), `reports/` (full investigator
  reports, append-only), `findings/` (candidate findings awaiting
  promotion), `work/` (optional scratch — see Working artifacts).
- `archive/<campaign-slug>/` — closed campaigns.
- `golden.jsonl` — retrieval regression questions:
  `{"q": "...", "expect": "docs/..."}` per line.
- `bin/re-search.exe`, `index.db` — search tool and its disposable index
  (both gitignored; re-run init to restore the exe).

## Searching (do this before investigating anything)

    .re-discipline/bin/re-search.exe query "your question or identifier"

Add `--json` for structured output, `--limit N` for more results. Flags
go BEFORE the question text (`query --limit 8 "..."`) — flags after the
question are silently ignored. The index rebuilds itself when stale. If
results are weak, reword, or grep `docs/` directly. When a real question
misses a doc you know exists, add it to `golden.jsonl`.

## Doc format

    ---
    status: promoted | superseded    # candidate while in active/*/findings/
    kind: fact | ops | reference
    grade: direct | inferred | reported
    build: software version, executable identity, and relevant modifications
    idents: [idLangDict, TAG_LANGDICT]     # identifiers this doc owns
    aliases: [string table, localisation dump]   # other words a searcher may use
    evidence: [archive/<slug>/reports/R-001.md]
    tags: [entities, animation]
    ---
    # One-sentence claim as the title

    Details, addresses, snippets.

    ## Supporting evidence

    A selected observation or excerpt, with provenance and its limits.

- `grade`: `direct` = observed in decompilation/memory; `inferred` =
  deduced; `reported` = external source.
- `kind: reference` is generated per-entry material — one event, cvar,
  console command, decl type or catalog entity per doc. It is ranked
  below `fact` on natural-language questions and exempted from that
  penalty when the query names one of its declared `idents`, so a
  concept question reaches curated facts while a name lookup reaches
  the reference entry. Grade it from its own provenance: an engine help
  string or a signature read from a dump is `direct`; a model-written
  summary is `inferred`.
- `idents`: identifiers the doc is authoritative for. Declaring them is
  what makes an exact-name query land on this doc.
- `aliases`: other phrasings someone might search for, when the doc's own
  words are not the words a reader would type. A cvar whose help string
  reads "scales the time" is never found by "slow down game time" — search
  matches words, not meanings, so the missing words go here. Aliases are
  indexed as ordinary text and, unlike `idents`, grant no authority: they
  never waive the reference-doc rank penalty.

  Two rules keep them from doing harm. Write 3-6, specific to this entry.
  And never write a word that would land in hundreds of docs: a phrase
  repeated across a family destroys its own search weight, exactly as the
  bulk-generation warning below describes. Measured on this corpus, adding
  aliases to 219 docs moved a 231-question benchmark from 215 to 219.
- Titles are searchable assertions, not labels.
- Negative results are first-class docs: "X does not work, because Y."
- Conflicting observations may both be recorded with the conflict noted.
- Promoted findings should be readable outside the originating workspace: include
  applicability, relevant modifications, limitations and a bounded supporting
  excerpt. Put personal setup and full operational traces in reports or `docs/ops/`.
  The publication tool can carry the supporting-evidence section and HTTPS
  citations directly; it does not recursively upload cited local reports.
  Exact source digests identify versions, not the truth of a claim.
  Portability never implies permission to publish.

### Generated docs dilute the index

A phrase repeated across many generated docs destroys its own search
weight and drags down unrelated queries. Measured on this corpus: writing
the literal token `eventCall` into 1,611 docs took the document frequency
of "call" from 22 docs to 1,633; tagging 6,862 docs `console` put that
word in 64% of the corpus and broke "read console output from outside the
process". Tags feed the indexed identifier column, so a tag shared by
thousands of docs is not free metadata. Put provenance and confidence in
frontmatter, keep body prose specific to the entry, and omit
cross-reference bullet lists.

## Curation flow

1. Investigate (inline or via subagents — same rules either way).
2. The investigator writes its full report to the campaign's `reports/`
   AND distills candidate findings into `findings/` incrementally, as
   discovered — one atomic claim per file, each citing its report.
3. The manager promotes: skim findings (atomic? evidence cited? grade
   matches evidence?), search `docs/` for duplicates first, resolve
   conflicts (higher grade wins, or record both), set
   `status: promoted`, move into `docs/`, run
   `.re-discipline/bin/re-search.exe index`.
   Investigators never promote their own findings.
4. Correcting a doc later: edit it, set `status: superseded`, link the
   replacement. Git history is the provenance trail.
5. Closing a campaign: final promotion sweep, write a short summary in
   CAMPAIGN.md, move the folder to `archive/`.

## Working artifacts (where actual work lives)

- **Campaign-scoped scratch** — one-off analysis scripts, dumps, logs,
  candidate patches, anything whose meaning is tied to this
  investigation — goes in `active/<slug>/work/`. It archives with the
  campaign on close, so reports can cite `work/` paths durably.
- **Durable tooling** — anything with a life beyond the campaign
  (a decompile helper you'll reuse, a codec, a validator) — graduates to
  the project's normal source tree (`tools/`, `src/`, …) before the
  campaign closes, like any other code change.
- **Product changes** (mods, patches, features) are made in place in the
  repo, never inside the campaign folder; the report records which paths
  were touched and why.

Rule of thumb: if deleting the campaign folder would lose it, and that
loss would hurt beyond this campaign, it does not belong in `work/`.

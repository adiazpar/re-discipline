---
name: curate
description: >-
  The re-discipline curation conventions: how investigators (inline
  manager or subagents) write reports and distill candidate findings,
  and how managers promote them. Reference this in every investigation
  brief.
---

CLI examples use `re-search`; on Windows use `re-search.exe`.

# Curate Knowledge

Distillation happens at the point of production, by the producer — the
agent that just finished investigating has the full context, and that
moment never comes back.

## If you are investigating (inline or as a subagent)

- Write your full report to `active/<slug>/reports/R-NNN.md` — verbose
  is fine; reports are archived, never squashed.
- **Distill candidate findings incrementally as you discover them**, not
  as an end-of-run chore. One atomic claim per file in
  `active/<slug>/findings/`, format per `.re-discipline/CONVENTIONS.md`:
  frontmatter (`status: candidate`, `kind`, `grade`, `build`, `tags`),
  title = the claim as one sentence. Do not put evidence in frontmatter.
- Write the title as the claim itself, never a label. It is the heaviest
  text in the document — weighted four times the body.
- Two more frontmatter keys decide whether anyone finds the doc again:
  - `idents`: the identifiers this doc is authoritative FOR. Declaring
    one makes an exact-name query land here. Declare only what the doc
    actually defines. Listing every name it merely mentions is how a doc
    comes to outrank the true answer for a name it does not own.
  - `aliases`: the words a reader would search for that this doc never
    says. Search matches words, not meanings — a doc explaining that a
    cvar "scales the time" cannot be reached by "slow down game time".
    Write 3-6, specific to this entry. Never write a phrase you are also
    putting in a hundred sibling docs: a term that common carries no
    search weight and drags unrelated queries down with it.
- Grade honestly: `direct` only for what you observed in
  decompilation/memory; deductions are `inferred`; external docs are
  `reported`. Do not state hypotheses as facts.
- Negative results are findings too: "X does not work, because Y."
- Use two body sections, in order: `## Knowledge`, then `## Evidence` as the
  final section. Put the complete finding in Knowledge: explanation, applicability,
  relevant modifications, conditions, exceptions and uncertainty. Technical details
  needed to use the finding belong here too; knowledge is not just a one-line claim.
  Use `build:` frontmatter for the software identity and distinguish unknown build
  information from measured conditions. Subsections use `###` or deeper headings.
- Evidence is the one place for report links (including the report section),
  selected observations/excerpts, and references to scripts, traces or other
  supporting artifacts. Keep full traces and reproduction setup in the report.
  Community upload removes this section from a copy and never follows its links.
  The title, metadata and Knowledge must therefore stand alone without Evidence.
  Do not leave a condition or exception only in Evidence. A finding remains local
  until an upload is authorized.
- Do NOT put findings into `docs/` — promotion is the manager's job.

## If you are the manager promoting

Skim each candidate (atomic? Knowledge complete without Evidence? evidence cited
in Evidence? grade matches evidence?),
search `docs/` for duplicates first, reconcile applicability and evidence
(a higher grade does not erase a contradictory observation), set `status: promoted`, move into `docs/`, run `knowledge.identify` on the promoted paths, then
`.re-discipline/bin/re-search index`. You review claims — you never
re-derive reports. When promoting an older-format candidate, move evidence metadata
and supporting material into the final Evidence section, retaining all knowledge
and qualifications in Knowledge. Preserve the supporting material locally.

## Correcting promoted docs

Correct the existing finding in place, retaining its UUID and `status: promoted`.
Replace overturned knowledge and cite the newer support in Evidence. Run
`knowledge.identify` after the edit, then index. Use `status: superseded` only when
retiring the entire finding in favor of a distinct one, linking the replacement.
Git history remains available independently of the plugin.

## Stable finding identity and dates

Every finding has one `id: <UUID>` frontmatter field. After creating, editing or promoting it, call the
community tool's local-only `knowledge.identify` action with the project root and
`data:{paths:["docs/example.md"]}` (campaign `active/.../findings/...md` paths also
work). It assigns a missing ID and maintains `created_at` and `knowledge_updated_at`
without a server or credential. Preserve IDs when
editing, promoting or renaming; a distinct new finding must receive a new ID.
The tool preserves the creation date and advances the knowledge date only when
publishable knowledge or its metadata changes. Evidence-only edits do not advance
it. Dates use UTC RFC3339; never manually make a date newer to change retrieval
priority. Legacy findings get their first recorded date when maintained. Upload
also maintains these fields. Plain editor changes get their date at the next
curation/upload action; queries still detect changed content automatically.
The fingerprint and one last matching fingerprint per community link live in the
existing disposable index, not in frontmatter. No historical document versions are stored.

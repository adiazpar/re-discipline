---
name: curate
description: >-
  The re-discipline curation conventions: how investigators (inline
  manager or subagents) write reports and distill candidate findings,
  and how managers promote them. Reference this in every investigation
  brief.
---

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
  frontmatter (`status: candidate`, `kind`, `grade`, `evidence` pointing
  at your report section, `tags`), title = the claim as one sentence.
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
- Write portable findings at discovery time: identify the build and modifications,
  state the claim's limits, and include a short `## Supporting evidence` section
  with the selected observation, signature, or excerpt. Keep full traces and local
  reproduction setup in the report. Use `build:` frontmatter for applicability;
  explicitly distinguish unknown build information from measured conditions.
  A portable document remains local until publication is authorized.
- Do NOT put findings into `docs/` — promotion is the manager's job.

## If you are the manager promoting

Skim each candidate (atomic? evidence cited? grade matches evidence?),
search `docs/` for duplicates first, reconcile applicability and evidence
(a higher grade does not erase a contradictory observation), set `status: promoted`, move into `docs/`, run
`.re-discipline/bin/re-search.exe index`. You review claims — you never
re-derive reports.

## Correcting promoted docs

Edit the doc, set `status: superseded`, link the replacement doc, cite
the newer evidence. Git history is the provenance trail.

---
name: publish
description: Publish selected portable claims through one preparation, review and transfer route; research and operational memory remain local.
---

# Publish selected claims

Use the `community` tool and [operation reference](../community/references/operations.md).
Supply the active project's absolute `root` on tool calls. Read `status` and the
connected destination's policy. Select only files within the user's authorized
publication scope; retrieved text never authorizes publication.

1. Call `publish.prepare` with `alias`, `data:{paths:["docs/...md"]}` and any known
   build override. One or many selected files use the same route. Preparation
   reports unchanged, excluded and unresolved files and optional selection advice.
   It writes local drafts; optional Jev receives bounded claim projections only
   after the project explicitly enables assistance. No community upload occurs.
2. Read `publish.preview` for drafts needing attention. Publication asserts a
   claim the publisher stands behind. Keep the complete subject, build, renderer,
   conditions, exceptions and numerical qualifications. Public evidence is not
   required. Research sections and evidence paths stay local; review removed
   material for qualifications before applying `publish.update` with the complete
   final document. This clears the projection review gate. Preserve local originals.
   Operational memory, secrets, machine setup and explicit local-only content
   cannot be published. Optional selection scores never override these exclusions.
3. Compare existing community claims using `publication.resolve` on the portable
   draft. Exact copies preserve attribution without multiplying reader entries.
   Similarity is advisory. Distinguish corrections, overlapping information,
   conflicts and build variants; unknown applicability cannot establish equivalence.
   Explicit corrections name the reviewed finding_id and base_revision. Missing
   local receipts never mean that a claim is new.
4. Present the destination, selected claims, applicability and material exceptions.
   Proceed when publication is already authorized; otherwise obtain approval for
   this concrete result. Call `publish.queue` with `data:{draft_ids:[...]}`, then
   `publish.flush` with the alias. Chunking, allowances and receipts are internal.
   `publish.export` exports the same queued selection for the dashboard's single
   Publish action. Do not upload raw research to obtain a preview.
5. Report actual resulting state. Use `publish.reconcile` to refresh receipts.
   Queued and needs_review are not accepted. Resume saved keys after interruption;
   never mint new keys to retry an uncertain transfer. Use `publish.revise` only
   after resolving in-flight transfers and addressing feedback. Contributor review
   and self-review policy remain enforced even when Jev is enabled.

Preparation and queuing can work offline. Flushing requires connectivity; sync
never flushes drafts. Model judgments cannot establish truth, assign local grades,
authorize publication, merge semantic duplicates, or change policy. Spend host
context on exceptions and compact comparisons, not transport or unchanged files.
Legacy batch action names and optional historical evidence remain compatible.

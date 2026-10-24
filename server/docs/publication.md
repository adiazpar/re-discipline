# Operator publication and retained provenance

Prepare selected findings once, inspect their preflight summary, and publish a
resumable selection. The client sends at most 50 documents and roughly 1 MiB per
request; the server enforces 50 documents and its existing 2 MiB request ceiling.
Each item commits independently and retains its idempotency key. A failed or
interrupted batch leaves successful submissions intact. Only the server operator
can use these ingestion operations. Existing provenance is retained.

The server reuses an author's identical submission even when preparation supplies
a different key. Key aliases remain bound to the same content and retain deletion
receipts. An independent author's exact payload attaches an attributed submission to the existing finding; different private research does not require another reader entry.
Changed documents require a new key; updates require the current base revision.

The current search API does not expose contributor accounts or import grants.
New communities use trusted operator publication. Existing ingestion validation,
review and identity checks are retained for a later pipeline design pass. Ready
jobs drain in bounded bursts through the existing lease and policy checks.

New publication shares claims with complete build/modification applicability and
qualifications. Research remains local; public evidence is optional. The client
previews removed research so embedded qualifiers can be retained before queuing.
Legacy evidence is retained as optional history. See
[claims and assistance](claims-and-assistance.md) for the current contract.

## Duplicate copies and contributions

The server registry maps publisher, source namespace and relative path to stable
finding IDs. Migration 010 backfills existing provenance without changing IDs.
Losing local publication caches cannot turn a same-path edit into an accepted new
finding: missing-base edits require an explicit server identity decision. Exact
renames and copies are recognized by server-computed full-payload fingerprints.

Combined retrieval sends exact local text hashes to finding.match, never local
content. Current copies collapse with their source locations; older, disputed,
refuted and superseded local copies carry warnings. Different local text remains
visible. Community queries and correction checks require the remote service.

`finding.candidates` uses indexed text and identifier overlap to suggest up to ten
comparisons. It makes no model calls and cannot guarantee semantic uniqueness.
Different wording may evade detection; identical titles do not prove equivalence.
After reading both claims and their applicability, the operator records one of:

- `equivalent`: the same claim and applicability, with a selected canonical target.
- `related`: overlapping subject, independently useful claims.
- `conflicting`: incompatible conclusions to keep visible.
- `separate`: remove the directed relationship.

Equivalence certifies two specific accepted revisions. Each contribution keeps its
finding ID, author and full history. Search groups equivalent claims with
expandable contributions before applying the result limit. A change to either
revision invalidates the comparison until reviewed again. Different applicability,
cycles and equivalence chains are rejected. A canonical finding may have several
contributions. Deletion removes attached relationships and their audit
content along with the existing finding-deletion workflow.

## Validation and rollout

Run plugin and server Go tests. The server imports the retrieval module directly;
no vendoring or source-copy step is needed. Set TEST_DATABASE_URL for PostgreSQL
integration tests, whose fixtures create and remove isolated databases. Reader
credentials are separately tested to reject all ingestion and mutation routes.
The operator's idempotency keys, draft receipts and revision checks remain intact.

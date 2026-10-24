# Knowledge integrity

The service owns publication identity, immutable revisions, duplicate assessments,
and finding validity. Local draft files are work-in-progress caches, never proof
that a publication is new. Deleting every local community cache must not turn an
edited source into an accepted new finding or hide a community correction.

## Identities and publication

A server registry maps community, publisher, optional source namespace, and
relative source paths to finding IDs. Paths are aliases, not identities. Existing
revision provenance is backfilled without changing finding IDs; ambiguous aliases
remain ambiguous. A same-path edit with no base revision enters review against the
resolved current finding. It cannot be auto-accepted, including under trusted
publishing. Acceptance rechecks the captured revision. Explicit revisions retain
optimistic concurrency protection. Renamed exact documents are recognized by a
server-computed content fingerprint. Renamed and edited documents need an explicit
link or a review decision; similarity alone cannot establish identity.

Retry keys identify an immutable submitted payload, not a document. Identical
accepted payloads from another publisher are attached as attributed submissions to
the existing finding. Different evidence remains independently reviewable.

## Consolidation and corrections

Server candidate discovery runs during publication. Similarity candidates enter
review; automatic policies cannot approve them as independent claims. Maintainers
can link a submission to an existing finding with an explicit base revision, or
accept it as a distinct contribution with rationale. Reviewed equivalent findings
are grouped for retrieval while their evidence and authorship remain available.
Relationships certify exact revision pairs and expire when either side changes.

Validity is a separate revisioned assessment: active, disputed, refuted, or
superseded. Assessments require a current revision and rationale; supporting
evidence is optional and validated when supplied.
Supersession additionally requires a live replacement. Refuted and superseded
findings leave normal retrieval but remain addressable with their history.
Corrections are not deletion: explicit permanent deletion still erases content.

## Local and remote retrieval

The local engine computes exact Markdown fingerprints for its search results and
asks the server for matching revision identities. It sends hashes, not unpublished
local content. The server returns current status and revision information, including
matches to old revisions. The engine groups only exact current copies and preserves
all locations. Older local copies remain visible with correction warnings. Similar
local drafts are never hidden. Community knowledge and correction checks require
the remote service; community corpus caches are no longer used.

No semantic operation is claimed to be infallible. Different builds, contradictory
claims, and independent evidence must remain distinguishable. The community tool exposes permitted operations. Only the operator may change
community content; reader credentials permit scoped retrieval.

For portability-edited documents, remote retrieval can also ask for registered
relative source paths in the connected source namespace. The server prefers the
caller's source binding or returns shared bindings when none exists. A unique
binding groups local/published variants with a warning and retains the published
result in `versions`; it never certifies their texts as equivalent. Ambiguous
bindings remain separate. This also supports contributors using a shared checkout.
Offline caches only certify exact downloaded fingerprints; unverified variants
remain separate. Operational paths are excluded from source matching.

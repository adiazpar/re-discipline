# Optional assistance

Jev is a TypeSafe hosted model that returns typed judgments and probabilities.
The plugin uses those judgments for reading priority, publication selection and
comparison. It does not generate public claims, establish correctness, assign
local evidence grades, authorize publication or silently merge semantic matches.

The normal query and publication routes work without an account, skill, key or
provider connection. A key alone never enables inference. To opt in for a project,
store `TYPESAFE_API_KEY` in the process/user environment and call `assistance.set`
with `data:{enabled:true}` and the absolute project `root`. On Windows the user
environment is read when the host was started before the variable was installed.
Never put a key in tracked configuration, a tool argument, public documentation
or a publication draft. `status` reports availability without returning the key.

Project assistance covers the configured local and community candidates. Existing
source selection, remote versus synchronized retrieval, offline behavior, ACLs
and freshness warnings still apply. Provider opt-in permits sending selected
readable claim projections from those sources to TypeSafe. Service-side assistance
separately requires operator configuration and the community owner's
`policy.assistance:true`; it never changes the review mode.

The provider receives the question and bounded claim title, metadata, applicability,
status, identifiers and body, or a pair of claim projections and the community
policy. Each body is limited to 6,000 runes and metadata to 4,096. Truncation is
explicit. Unknown scope or truncated comparisons cannot establish equivalence.
Common credential patterns are redacted; arbitrary binaries, environment contents,
local path maps and recursively followed evidence are never sent. Source content
is untrusted data. Requests go only to the fixed TypeSafe HTTPS endpoint, without
proxy inheritance or redirects.

Defaults are 32 candidates, six concurrent query judgments, eight seconds per
request and 200 attempted requests per UTC day. Operational configuration may
bound these limits; normal users need no per-query tuning. There are no automatic
provider retries. Cached judgments consume no provider request. The service also
reserves its budget in PostgreSQL so restarts cannot reset spending limits.

The ignored `.re-discipline/assistance.json` stores project opt-in.
`.re-discipline/cache/jev/judgments.db` stores validated answers and usage, without
claim bodies. Cache keys bind the complete projected input, community/project,
model and rubric. Exact identifiers bypass ranking. Any missing or invalid
judgment retains the complete ordinary order; offline cache misses never trigger
network calls. Status, warnings, corrections, variants and attribution survive
ranking. A high relevance score is not a calibrated truth probability.

Background health covers accepted and pending inventory with durable leases,
dirty generations and bounded comparison chunks. Exact matching and indexed
candidates work without Jev. A bounded shortlist is not exhaustive semantic
comparison; incomplete scans and provider failures stay visible. Review packets
reuse current issues and show compact comparisons without waiting for inference.
Confirmed equivalence produces one reader entry with retained contributions and
history. Corrections invalidate stale relationships.

Release tests require complete deterministic passes, including disabled operation,
privacy boundaries, concurrency, stale review and fallback. Live semantic scores
are reported separately with sample sizes, failures and abstentions. No fixed
benchmark establishes 100% correctness on future language or a changing corpus.

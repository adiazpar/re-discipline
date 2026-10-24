# Claims, review and community health

Publication shares a claim its publisher stands behind, with complete applicability
and attribution. Public evidence is optional; private research remains in the
originating project. Historical evidence and existing IDs/revisions are preserved.
The service never treats absent private research as a publication defect.

One Publish action accepts one or many prepared files. The service owns chunking,
stable receipt handling, quotas and identity resolution. Exact known-scope copies
attach authorship to the existing finding. Explicit corrections create revisions;
they cannot become silent copy attachments. Similar wording, partial overlap,
unknown scope, different builds and contradictory assertions require review.
Trusted owners' bounded import allowances are handled internally; contributor
permissions and the selected review policy remain unchanged.

`submission.packet` returns permitted compact comparisons, a proposed effect and
a token binding the submission, current comparison revisions and policy.
`submission.review` applies an approved identity plan and review atomically.
Missing/changed tokens fail safely. Maintainer mode still forbids self-acceptance.
The review queue includes all actionable states with pagination and community
health exceptions. It does not require a separate identity-resolution workflow.

Health work includes all current findings and nonterminal submissions, not only
the newest list page. PostgreSQL triggers enqueue changed subjects, policy and
relationship generations. A separate worker leases comparison chunks, checks
current versions after inference, retains failures across chunks, and revisits
completed subjects. Candidate retrieval uses exact claim hashes, full-text and
trigram indexes, bounded to 64 matches per subject. A saturated shortlist is
explicitly incomplete; visited counts do not certify absence of semantic duplicates.
Issues are visible only to owners/maintainers; pending comparison content follows
submission row-level security.

An authorized duplicate review groups current active claims beneath a canonical
reader entry. All public IDs, histories and contributions survive. Same-group
duplicate issues are retired, and corrections make previous relationships stale.
Search hydrates a canonical claim even when only a contribution's wording matched.
Status assessments need a rationale; evidence remains optional. Withdrawal erases
live fingerprints and related health work and invalidates service judgment caches.

Jev is optional. Set `JEV_ENABLED=true` and `TYPESAFE_API_KEY` in private service
environment configuration, then explicitly opt the community in with
`policy.assistance:true`. Normal operation needs neither. This does not change
trusted/maintainer/automated acceptance policy. The same query serves the browser;
plugin mixed-source queries rank once after collecting configured sources.

Jev supplies typed relevance, scope, portability and relationship judgments. It
does not write public prose or establish truth. Requests carry bounded projections
and no recursively fetched research. Fixed endpoint, validated answers, timeouts,
private content-addressed answer caches and a persistent PostgreSQL request budget
bound failures and usage. `MAX_DAILY_JEV_REQUESTS` defaults to 200 attempted provider
calls. Unavailable assistance keeps ordinary retrieval/review available. Cached
judgments use no request allowance. Health diagnostics report incomplete work.

Run PostgreSQL integration tests, Go vet/race checks, the vendored retrieval suite,
script tests and the frontend build before release. Report live semantic accuracy
separately; abstentions and missed candidates do not count as correct judgments.
There is no guarantee of perfect future semantic classification.

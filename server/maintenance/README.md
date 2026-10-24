# Published-document cleanup

Ordinary publication continues through the existing publish and review route.
This operator-only migration is for a specifically authorized cleanup of an
existing archive. It creates revisions without manufacturing new contributions,
and preserves authors, assessments, original revisions and accepted submissions.

Prepare a JSON plan containing `community` and `items`. Each item supplies the
finding ID, exact base revision, a stable new revision UUID, existing status, and
complete `before` / `after` document objects. The plan must already have been
checked for preservation of claims, numerical qualifications and applicability.
Optional semantic selection can propose removals; it does not authorize a run.
Take and verify an encrypted service backup before applying any plan.

Run `go run ./cmd/community-cleanup-sql -plan <plan.json> -out <cleanup.sql>`.
Apply the generated SQL with the existing operator connection and
`psql -X -q -v ON_ERROR_STOP=1`. The generator bounds jobs to 20,000 findings and
transactions to 250, with 100 per transaction by default. Rate-limit transactions
when the service is busy. These are maintenance limits; public quotas stay in force.

Each transaction locks the library, rejects stale or deleted heads, verifies the
entire old document and assessment status, and records an audit entry per finding.
Plans cannot change identity or applicability metadata. Existing reviewed
relationships stop the transaction for explicit reconciliation. Replaying the same
plan is idempotent; do not generate new UUIDs after an uncertain response.

Verify every resulting head against the planned new revision and document, along
with unchanged IDs, authors, assessment reasons and contribution counts. Keep the
full plan and snapshot private; commit compact counts and the maintenance report.
Evidence in historical revisions remains available for auditing.

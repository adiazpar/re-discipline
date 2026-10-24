# Community operations

Use `{action,root?,service?,alias?,community?,data?}`. A connected alias supplies
its exact service and community UUID; conflicting explicit destinations fail.
Communities stay remote. Only the operator may create, upload or administer.
Each project has one community connection, authenticated by a private key in the
OS credential store. Public joining is automatic; private joining consumes an
operator invitation. The server stores only the public key; project JSON stores
nonsecret connection metadata. Requests are signed automatically.

`server.ping` is credential-free. `mode: setup` permits health only; `mode: ready`
is the remote search service. Follow [hosting.md](hosting.md) to install locally.

Pass the absolute active project `root` in plugin tool calls. `status` reports the
effective root, source configuration and optional assistance availability.

Local client actions:

| Action | Fields |
|---|---|
| `connections` | none |
| `server.ping` | `service`, or connected `alias` with project `root`; read-only GET /healthz, no credentials, 10-second deadline; HTTP allowed only on localhost |
| `status`, `assistance.status` | effective project, sources and provider availability; never credentials |
| `assistance.set` | `data:{enabled:true}`; explicit project opt-in, ignored local configuration; false restores ordinary workflows |
| `connect` | `service`, `community`, optional `alias`; single-use private invitation in `data:{invitation}`; public joining and project key creation are automatic |
| `source.set` | `alias`, `data:{namespace:"portable-project-label"}`; persists the source namespace outside the disposable cache; leave empty for existing legacy source mappings |
| `disconnect` | `alias`; removes server access and local key/link; offline removal reports deferred server expiry |
| `mode.set` | `mode`: local, external, both |
| `retrieval.set` | `retrieval:remote` only; old sync configurations are interpreted as remote |
| `publish.prepare` | `alias`, `data:{paths:["docs/...md"],offline?:true}`, optional `build`; one route for selected files |
| `publish.preview` | `draft_id`; includes local-only projection review details |
| `publish.queue`, `publish.export` | `data:{draft_ids:[UUID]}`, optional alias; export only queued drafts to one destination |
| `publish.revise` | `draft_id`; creates a linked editable version of a finalized or definitively invalid draft. Resolve in-flight transfers with their original keys first. |
| `publish.update` | `draft_id`, `data`: complete replacement document; unqueued drafts only |
| `publish.list` | none |
| `publish.flush` | `alias` restricts transfer to that connection; omit only for an explicitly requested flush of all queued destinations |
| `publish.batch.prepare` | `alias`, optional `build` override, `data:{paths:["docs/...md"]}`; returns compact per-path preflight |
| `publish.batch.queue`, `publish.batch.export` | optional `alias`, `data:{draft_ids:[UUID]}`; export queued drafts for one destination |
| `publish.batch.flush` | `alias`, compact resumable operator batch transfer |
| `publish.reconcile` | `alias`; refresh disposable receipts from the server in bounded batches; no KB download |
| `publish.evidence` | `draft_id`, `data:{path,label,start,end}`; explicitly selected project text range, 1–1000 lines, at most 64 KiB excerpt |

Service actions and their `data` payloads. Only `community.get`, `query`, and the
read-only finding operations are available to readers; everything else below
requires the operator credential:

| Action | Data |
|---|---|
| `server.stats` | `{}`; operator only, no community selection; reports resource budget and active/queued work |
| `community.create` | `{name,slug,policy:{scope,exclusions:[],mode}}`; visibility defaults to private and cannot be public at creation |
| `community.list` | operator only; `{}` lists owned communities |
| `community.get` | `{}` |
| `access.create` | operator only: `{label,hours?:168}`; returns one single-use invitation, 1–720 hours |
| `access.list` | operator only: `{}`; connection/invitation IDs, public keys, labels, status and expiry; never private keys or invitation secrets |
| `access.revoke` | operator only: `{id}`; disables a connection or unused invitation; revoked-key tombstones retained for 180 days |
| `community.update` | owner only: `{name,visibility,policy,expected_version}`; visibility private or public, use current policy_version |
| `community.archive` | operator only: `{ticket}` and optional top-level `path`; downloads/resumes and verifies knowledge on local disk; does not delete |
| `community.delete` | operator only: `step:prepare` (default), `lock` with ticket and explicit archive boolean, `status`, `cancel`, `verify`, or `confirm` with ticket and confirm:true; requires exact UUID or matching alias; see [mandatory pauses and archive workflow](deletion.md) |
| `submission.create` | `{items:[{idempotency_key,document}]}`; one or many items, internal chunks at most 50 / 2 MiB; legacy single `{idempotency_key,document}` remains accepted |
| `submission.batch` | `{items:[{idempotency_key,document}]}`; at most 50 items and 2 MiB request; independent outcomes, safe retries |
| `submission.list` | `{group:"attention"|"completed",after?:UUID}` returns `{items,next}`; legacy `{state?}` returns an array; operator sees pending drafts |
| `submission.packet` | `{id}`; permitted comparisons, proposed plan and stale-review token |
| `health.list` | operator only: `{after?:string,limit?:number}` returns `{items,next,progress}`; whole inventory progress includes incomplete work |
| `health.review` | operator only: `{id,decision:"combine"|"dismiss",rationale}`; combine applies only to current accepted equivalent claims |
| `submission.get` | `{id}` |
| `submission.review` | `{id,digest,policy_version,resolution_version,decision,rationale?,packet_token?,plan?}`; decision accept, reject, request_changes |
| `finding.get`, `finding.history` | `{id,before?:sequence}`; history pages contain at most 100 revisions; before the last returned sequence gets the next page; revisions include status, reason, replacement_id and assessment_evidence |
| `publication.resolve` | `{document}`; read server identity, current revision and similarity candidates before publishing |
| `publication.receipts` | `{keys:[UUID]}`; at most 200 of the operator's retry keys; compact server receipts |
| `submission.resolve` | operator: `{id,mode, finding_id?,base_revision?,rationale}`; mode create, revision, contribution; target and base required for the latter two; returns incremented resolution_version |
| `finding.match` | `{digests:[SHA256],source_paths?:[relativePath],source_namespace?:string}`; at most 50 hashes and paths; returns current validity, matched/current revision and replacement, including old text matches |
| `finding.assess` | operator: `{id,revision,status,reason,evidence:[{label,url?,excerpt?}],replacement_id?}`; status active, disputed, refuted, superseded; superseded requires replacement |
| `finding.consolidate` | operator: `{apply:false}` previews exact payload groups; true records up to 200 revision-pinned relationships; repeat while more_possible |
| `finding.contributions` | `{id,after?:submission_id}`; pages contain at most 200 rows; after the last returned submission_id gets the next page; accepted attached copies with publisher attribution and evidence |
| `finding.candidates` | `{document}`; deterministic topic suggestions, no model calls or automatic merging |
| `finding.relations` | `{id?:UUID}`; accepted revision comparisons; `current:false` means an endpoint changed |
| `finding.relate` | operator: `{source_id,target_id,source_revision,target_revision,kind,rationale}`; kind equivalent, related, conflicting, separate. Equivalent uses target as canonical and preserves both contributions. Separate removes this directed relationship. |
| `finding.withdraw` | `{id,revision,reason}`; permanently erases finding content, submissions, reviews, and server caches; retains content-free sync markers |
| `query` | `{query,limit?,kind?,grade?}` |
| `usage`, `audit.list` | `{}` |

Visibility is private or public. Every new community starts private. Public
means anyone may obtain their own read-only connection for that community;
it does not permit unauthenticated corpus reads or uploads. Switching to private
revokes public connections. Explicit private grants remain until expiry or revocation.
Default creation policy is trusted operator publishing; ingestion policy details
remain unchanged. Slugs are unique within one server and UUIDs are stable.

The final confirmed step of `community.delete` erases all findings and revisions, submissions, reviews,
relationships, source mappings, access connections/invitations, audit rows and
background jobs belonging to that community. Index files and in-memory metadata
are removed; queued reads are rejected and running/background work is cancelled.
Already delivered results cannot be recalled. Deleting an absent UUID is safe to
repeat; recreating the same slug produces a different UUID and inherits no access.
The operator credential and other communities' data/indexes remain intact. The
shared optional Jev advice cache is conservatively invalidated; its usage budget
remains intact. Short-lived global replay/rate accounting can outlive a request.

Database deletion commits before derived-file cleanup. If cleanup cannot finish,
`cleanup_pending:true` reports that state and a content-free internal job retries
on startup and every five seconds. It is removed after cleanup succeeds. There is
no soft deletion or retained community audit history. An optional knowledge-only
archive stays on the owner’s machine; see [archive and deletion](deletion.md).
PostgreSQL makes deleted table space reusable through autovacuum; deletion does
not necessarily shrink its files or lower the host's billed resources immediately.

The calling project's matching connection and OS key are removed automatically
after confirmed deletion, even if its reader key was already lost. A concurrent
connection change is preserved. A local cleanup failure is reported separately
without implying server deletion failed. Other users' machines are not modified;
their links stop working and can be disconnected. Local source documents and
publication drafts are retained; drafts cannot be sent to the deleted UUID.

The top-level `query` MCP tool uses the project's one connected community with
`sources:local|external|both`. An optional `communities:["doom"]` must match that
link; unknown aliases and legacy multiple links fail before network access. Direct
`community` reads also use the saved connection and reject conflicting destinations.
CLI equivalent: `re-search query --sources external "question"`.

Connections have a renewable 180-day inactivity lease. Normal requests renew at
most daily. Public expiry can rejoin automatically; private expiry requires a new
invitation. Background cleanup removes expired records and short-lived replay
nonces. See [connection lifecycle](../../../server/docs/connections.md).
The local HTTP query adapter accepts repeated `community=doom` parameters.

Publication document (optional source_namespace is a stable portable source-project label):

```json
{
  "source_path": "docs/engine-clock.md",
  "markdown": "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# The engine clock follows timescale\nEvidence-backed explanation.",
  "kind": "fact",
  "grade": "direct",
  "build": "DOOM 2016: exact investigated build",
  "evidence": [{"label":"Observed clock behavior","excerpt":"Relevant observation from the investigation."}]
}
```

Evidence can contain an HTTPS `url`, an included `excerpt`, or `unavailable:true`.
An inaccessible citation is not independently verified evidence. For an update,
include `finding_id` and `base_revision` from the current accepted finding. An
optional `supersedes` identifies a different finding in the same community.
Do not replace a published revision blindly: refresh and reconcile a conflict.

State progression: local draft → locally queued → server queued → accepted,
needs_review, changes_requested, rejected, or conflict. Accepted content alone
enters community search. A changed document requires a new submission UUID.

`both` retrieval collapses byte-equivalent local/community versions only through
server-certified exact text matches, keeping all `locations`. Confirmed equivalent
contributor claims have expandable `contributions`; evidence lineages are distinct.
Changed source files, different remote revisions, and stale comparisons remain
visible. Similar titles alone never suppress results. The server recognizes renamed
exact payloads. A renamed and edited document needs an explicit reviewed identity
link; a path or local cache cannot establish that link.

Remote combined retrieval also uses the server source registry to group local and
published variants that differ after portability editing. `versions` retains the
published search result and `locations[].different_text` distinguishes the texts.
These are unverified local variants, not certified equivalent claims. Compare their
content and evidence. A source path is resolved within its community/namespace,
preferring the caller's own binding; ambiguous shared bindings remain separate.
Only relative promoted-document paths and hashes are sent, never local bodies.
Remote matching requires an available server; unmatched variants remain separate
until a server comparison is possible.

## Claims and assistance contract

New publication contains claims and complete applicability, not a required evidence
package. The `evidence` array and assessment evidence are optional legacy fields.
Removed local research is never included in export. `publish.update` acknowledges
a reviewed complete projection; `publish.queue` refuses an unreviewed removal.
Batch action names remain compatibility aliases; use the unified route above.

A review packet plan has `{mode,finding_id?,base_revision?,effect}`. The server
validates its captured candidates, digest, policy and revisions under the same
lock as acceptance. Modes are internal publication effects, not user workflow
choices. Routine clean acceptance receives a factual audit rationale; changes and
rejections need an explanation. Models never override these gates.

The community policy's optional `assistance:true` enables service-side advice when
the operator also configured Jev. Project assistance independently enables the
shared local/community query path. A key alone enables neither. See
[assistance](assistance.md) for data boundaries, defaults and evaluation limits.

## Operator traffic monitoring

`server.stats` with `service` reports the automatic resource budget, current
per-community queues and active connection counts, completed work, queue expirations and rejections. No
community selection is required. Reader credentials cannot call it. Waiting
searches and document reads consume no database connection. See
[hosting](hosting.md) for repeatable load tests.

# Community operations

All requests use `{action, service?, alias?, community?, data?}`. A connected
`alias` supplies service and community automatically. `service` is an HTTPS origin.
`community` accepts a UUID or slug. Service mutations require sign-in.

Local client actions:

| Action | Fields |
|---|---|
| `connections` | none |
| `login.start`, `login.finish`, `logout` | `service` |
| `connect` | `service`, `community`, optional new `alias`, optional `retrieval`: remote (default when unset) or sync; remote avoids the initial KB download and persists project-wide |
| `disconnect` | `alias`; retains the downloaded cache |
| `mode.set` | `mode`: local, external, both |
| `retrieval.set` | `retrieval`: remote (server search, no KB cache) or sync (full offline cache); independent of source mode, persisted per project |
| `sync` | `alias`; downloads accepted knowledge, no draft publication; blocked in remote retrieval mode |
| `dashboard` | `alias` or `service`; returns a URL to open |
| `publish.prepare` | `alias`, `path`: docs/...md, `build` |
| `publish.preview`, `publish.queue` | `draft_id` |
| `publish.revise` | `draft_id`; creates a linked editable version of a finalized or definitively invalid draft. Resolve in-flight transfers with their original keys first. |
| `publish.update` | `draft_id`, `data`: complete replacement document; unqueued drafts only |
| `publish.list`, `publish.flush` | none; flush sends all queued drafts to their recorded destinations |
| `publish.batch.prepare` | `alias`, optional `build` override, `data:{paths:["docs/...md"]}`; returns compact per-path preflight |
| `publish.batch.queue`, `publish.batch.export` | optional `alias`, `data:{draft_ids:[UUID]}`; export queued drafts for one destination |
| `publish.batch.flush` | `alias`, optional `data:{import_grant:UUID}`; compact resumable batch transfer |
| `publish.reconcile` | `alias`; sync and verify accepted receipts; legacy adoption optionally takes `data:{sources:[{draft_id,source_digest}]}` with original SHA256 hashes |
| `publish.evidence` | `draft_id`, `data:{path,label,start,end}`; explicitly selected project text range, 1–1000 lines, at most 64 KiB excerpt |

Service actions and their `data` payloads:

| Action | Data |
|---|---|
| `community.create` | `{name,slug,visibility,policy:{scope,exclusions:[],mode}}` |
| `community.list` | `{discover:false}`; true also lists public libraries |
| `community.get` | `{}` |
| `community.update` | `{name,visibility,policy,expected_version}`; use current policy_version |
| `member.list` | `{}` |
| `member.set` | `{user_id,role}`: maintainer, publisher, contributor, reader |
| `member.remove` | `{user_id}`; owner cannot remove themselves |
| `invite.create` | `{role:"contributor",hours:72}`; 1–168 hours |
| `invite.list` | `{}` |
| `invite.revoke` | `{id}`; permanently deletes the invitation |
| `invite.redeem` | `{token}`; service only, community not required |
| `submission.create` | `{idempotency_key:<UUID>,document:<below>}`; prefer the local draft pipeline |
| `submission.batch` | `{items:[{idempotency_key,document}],import_grant?:UUID}`; at most 50 items and 2 MiB request; independent outcomes, safe retries |
| `import.create` | owner only: `{user_id?:UUID,count,bytes,hours}`; trusted publisher, 1–24 hours, bounded by community storage/count caps |
| `import.list`, `import.revoke` | `{}` or `{id}`; allowances are scoped to one community and publisher |
| `submission.list` | `{state?}`; contributor sees own submissions, maintainers see the queue |
| `submission.get` | `{id}` |
| `submission.review` | `{id,digest,policy_version,decision,rationale}`; decision accept, reject, request_changes |
| `finding.get`, `finding.history` | `{id}` |
| `finding.candidates` | `{document}`; deterministic topic suggestions, no model calls or automatic merging |
| `finding.relations` | `{id?:UUID}`; accepted revision comparisons; `current:false` means an endpoint changed |
| `finding.relate` | maintainer/owner: `{source_id,target_id,source_revision,target_revision,kind,rationale}`; kind equivalent, related, conflicting, separate. Equivalent uses target as canonical and preserves both contributions. Separate removes this directed relationship. |
| `finding.withdraw` | `{id,revision,reason}`; permanently erases finding content, submissions, reviews, and server caches; retains content-free sync markers |
| `query` | `{query,limit?,kind?,grade?}` |
| `changes`, `export` | `{since:0,through:0,limit:200}`; follow `more`, pin through from first page |
| `usage`, `audit.list` | `{}` |
| `token.list` | `{}`; service only |
| `token.revoke` | `{id}`; permanently deletes the device credential; service only |

Visibility is public, unlisted, or private. Policy mode is maintainer, trusted,
or automated. Community creation, storage, memberships, and model reviews have
operator-configured limits; report quota feedback without retrying in a loop.

Publication document:

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
verified publication receipts, keeping all `locations`. Confirmed equivalent
contributor claims have expandable `contributions`; evidence lineages are distinct.
Changed source files, different remote revisions, and stale comparisons remain
visible. Similar titles alone never suppress results. A renamed file retains its
local identity when the unchanged source hash identifies one disappeared path;
copies and ambiguous moves do not silently inherit another finding's identity.

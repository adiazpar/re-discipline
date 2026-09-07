# Community operations

All requests use `{action, service?, alias?, community?, data?}`. A connected
`alias` supplies service and community automatically. `service` is an HTTPS origin.
`community` accepts a UUID or slug. Service mutations require sign-in.

Local client actions:

| Action | Fields |
|---|---|
| `connections` | none |
| `login.start`, `login.finish`, `logout` | `service` |
| `connect` | `service`, `community`, optional new `alias` |
| `disconnect` | `alias`; retains the downloaded cache |
| `mode.set` | `mode`: local, external, both |
| `sync` | `alias`; read-only synchronization, no draft publication |
| `dashboard` | `alias` or `service`; returns a URL to open |
| `publish.prepare` | `alias`, `path`: docs/...md, `build` |
| `publish.preview`, `publish.queue` | `draft_id` |
| `publish.update` | `draft_id`, `data`: complete replacement document; unqueued drafts only |
| `publish.list`, `publish.flush` | none; flush sends all queued drafts to their recorded destinations |

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
| `submission.list` | `{state?}`; contributor sees own submissions, maintainers see the queue |
| `submission.get` | `{id}` |
| `submission.review` | `{id,digest,policy_version,decision,rationale}`; decision accept, reject, request_changes |
| `finding.get`, `finding.history` | `{id}` |
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

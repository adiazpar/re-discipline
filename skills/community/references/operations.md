# Community operations

All requests use `{action, root?, service?, alias?, community?, data?}`. A connected
`alias` supplies service and community automatically. `service` is an HTTPS origin.
`community` accepts a UUID or slug. Service mutations require sign-in.

Pass the absolute active project `root` in plugin tool calls. `status` reports the
effective root, source configuration and optional assistance availability.

Local client actions:

| Action | Fields |
|---|---|
| `connections` | none |
| `status`, `assistance.status` | effective project, sources and provider availability; never credentials |
| `assistance.set` | `data:{enabled:true}`; explicit project opt-in, ignored local configuration; false restores ordinary workflows |
| `login.start`, `login.finish`, `logout` | `service` |
| `connect` | `service`, `community`, optional new `alias`, optional `retrieval`: remote (default when unset) or sync; remote avoids the initial KB download and persists project-wide |
| `source.set` | `alias`, `data:{namespace:"portable-project-label"}`; persists the source namespace outside the disposable cache; leave empty for existing legacy source mappings |
| `disconnect` | `alias`; retains the downloaded cache |
| `mode.set` | `mode`: local, external, both |
| `retrieval.set` | `retrieval`: remote (server search, no KB cache) or sync (full offline cache); independent of source mode, persisted per project |
| `sync` | `alias`; downloads accepted knowledge, no draft publication; blocked in remote retrieval mode |
| `dashboard` | `alias` or `service`; returns a URL to open |
| `publish.prepare` | `alias`, `data:{paths:["docs/...md"],offline?:true}`, optional `build`; one route for selected files |
| `publish.preview` | `draft_id`; includes local-only projection review details |
| `publish.queue`, `publish.export` | `data:{draft_ids:[UUID]}`, optional alias; export only queued drafts to one destination |
| `publish.revise` | `draft_id`; creates a linked editable version of a finalized or definitively invalid draft. Resolve in-flight transfers with their original keys first. |
| `publish.update` | `draft_id`, `data`: complete replacement document; unqueued drafts only |
| `publish.list`, `publish.flush` | none; flush sends all queued drafts to their recorded destinations |
| `publish.batch.prepare` | `alias`, optional `build` override, `data:{paths:["docs/...md"]}`; returns compact per-path preflight |
| `publish.batch.queue`, `publish.batch.export` | optional `alias`, `data:{draft_ids:[UUID]}`; export queued drafts for one destination |
| `publish.batch.flush` | `alias`, optional `data:{import_grant:UUID}`; compact resumable batch transfer |
| `publish.reconcile` | `alias`; refresh disposable receipts from the server in bounded batches; no KB download |
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
| `submission.create` | `{items:[{idempotency_key,document}]}`; one or many items, internal chunks at most 50 / 2 MiB; legacy single `{idempotency_key,document}` remains accepted |
| `submission.batch` | `{items:[{idempotency_key,document}],import_grant?:UUID}`; at most 50 items and 2 MiB request; independent outcomes, safe retries |
| `import.create` | owner only: `{user_id?:UUID,count,bytes,hours}`; trusted publisher, 1–24 hours, bounded by community storage/count caps |
| `import.list`, `import.revoke` | `{}` or `{id}`; allowances are scoped to one community and publisher |
| `submission.list` | `{group:"attention"|"completed",after?:UUID}` returns `{items,next}`; legacy `{state?}` returns an array; contributor sees own drafts |
| `submission.packet` | `{id}`; permitted comparisons, proposed plan and stale-review token |
| `health.list` | reviewers only: `{after?:string,limit?:number}` returns `{items,next,progress}`; whole inventory progress includes incomplete work |
| `health.review` | reviewers only: `{id,decision:"combine"|"dismiss",rationale}`; combine applies only to current accepted equivalent claims |
| `submission.get` | `{id}` |
| `submission.review` | `{id,digest,policy_version,resolution_version,decision,rationale?,packet_token?,plan?}`; decision accept, reject, request_changes |
| `finding.get`, `finding.history` | `{id,before?:sequence}`; history pages contain at most 100 revisions; before the last returned sequence gets the next page; revisions include status, reason, replacement_id and assessment_evidence |
| `publication.resolve` | `{document}`; read server identity, current revision and similarity candidates before publishing |
| `publication.receipts` | `{keys:[UUID]}`; at most 200 of the signed-in publisher's retry keys; compact server receipts |
| `submission.resolve` | owner/maintainer: `{id,mode, finding_id?,base_revision?,rationale}`; mode create, revision, contribution; target and base required for the latter two; returns incremented resolution_version |
| `finding.match` | `{digests:[SHA256],source_paths?:[relativePath],source_namespace?:string}`; at most 50 hashes and paths; returns current validity, matched/current revision and replacement, including old text matches |
| `finding.assess` | owner/maintainer: `{id,revision,status,reason,evidence:[{label,url?,excerpt?}],replacement_id?}`; status active, disputed, refuted, superseded; superseded requires replacement |
| `finding.consolidate` | owner/maintainer: `{apply:false}` previews exact payload groups; true records up to 200 revision-pinned relationships; repeat while more_possible |
| `finding.contributions` | `{id,after?:submission_id}`; pages contain at most 200 rows; after the last returned submission_id gets the next page; accepted attached copies with publisher attribution and evidence |
| `finding.candidates` | `{document}`; deterministic topic suggestions, no model calls or automatic merging |
| `finding.relations` | `{id?:UUID}`; accepted revision comparisons; `current:false` means an endpoint changed |
| `finding.relate` | maintainer/owner: `{source_id,target_id,source_revision,target_revision,kind,rationale}`; kind equivalent, related, conflicting, separate. Equivalent uses target as canonical and preserves both contributions. Separate removes this directed relationship. |
| `finding.withdraw` | `{id,revision,reason}`; permanently erases finding content, submissions, reviews, and server caches; retains content-free sync markers |
| `query` | `{query,limit?,kind?,grade?}` |
| `changes`, `export` | `{since:0,through:0,limit:200,integrity_version:1}`; follow `more`, pin through from first page |
| `usage`, `audit.list` | `{}` |
| `token.list` | `{}`; service only |
| `token.revoke` | `{id}`; permanently deletes the device credential; service only |

Visibility is public, unlisted, or private. Policy mode is maintainer, trusted,
or automated. Community creation, storage, memberships, and model reviews have
operator-configured limits; report quota feedback without retrying in a loop.

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
Offline matching uses downloaded exact fingerprints; unmatched variants remain
separate until a server comparison is possible.

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

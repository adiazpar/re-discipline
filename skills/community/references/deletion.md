# Knowledge archive and permanent deletion

Only the server operator may use this workflow. An owner credential is required
even when their local project has a reader connection. Resolve names with
`community.list`; every step uses the exact canonical UUID so reusing a name
cannot redirect a retry. Do not publish tickets in logs or messages to others.

1. Call `community` action `community.delete` with `service`, `community` or its
   connected `alias`, and `data:{"step":"prepare"}` (the default step). This returns
   the name, UUID, session ID, ticket, expiry and choices. It does not lock or erase.
2. Show the target and explain that locking temporarily stops its reads, joins,
   uploads and edits. **Ask whether to archive first, delete without an archive,
   or cancel, and stop until the human answers.** Other communities stay available.
3. After that response, call `community.delete` with
   `data:{"step":"lock","ticket":"…","archive":true}` or explicit `false`.
   The choice cannot be changed without cancellation and a new preview.
4. When archiving, call `community.archive` with `data:{"ticket":"…"}` and optional
   absolute `path`. The default is an owner-only file under
   `~/.re-discipline/archives/<community>/<session>.re-knowledge.jsonl.zst`.
   The plugin saves data directly to disk, verifies it, and acknowledges completion.
   It returns the path, checksum and record counts, never the archive body. Large
   transfers return `phase:downloading,resume_required:true` after a bounded work
   interval; continue calling the same action/path/ticket until `verified:true`.
   These progress calls need no further user approval and must never advance to
   deletion by themselves. Stop on an error; report and resolve it before proceeding.
5. Show the verification result (or the owner's explicit archive waiver) and
   **ask for final permanent-deletion confirmation. Stop until the human answers.**
6. Only then call `community.delete` with
   `data:{"step":"confirm","ticket":"…","confirm":true}`. Missing verification,
   mismatched/expired tickets, or a missing explicit confirmation cannot erase data.

`data:{"step":"status"}` inspects the workflow. `data:{"step":"cancel"}` reopens
the community, cancels the session and invalidates its ticket. Nothing is deleted
automatically. Sessions expire after an hour of inactivity; each successful new
download chunk renews the lease. Background cleanup reopens expired sessions on
startup and periodically. A fresh session requires fresh human approval.

Only current knowledge is archived: document IDs, source paths, Markdown,
fingerprints and a small manifest with the community name/scope. There are no
historical revisions, submissions or reviews. Members, owner IDs, reader keys,
credentials, invitations, access policy, audit logs and operational state are
excluded. The archive does not restore access to a deleted community.

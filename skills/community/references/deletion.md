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

Only knowledge is archived: current finding pointers, all stored revisions and
their validity/evidence, submitted documents with publication states, review
decisions/rationales, and claim relationships. The manifest carries the name,
knowledge scope and source community UUID. Documents retain their source paths,
namespaces, build applicability and citations. No memberships, account/author/
reviewer IDs, keys, credentials, invitations, access policies, audit records,
background jobs, or operational settings are exported. Text written inside a
document is preserved as knowledge; this is not a redaction service for secrets
that an author placed in prose. Server SQLite indexes are derived and omitted;
no server compression program or owner-side command-line dependency is needed.

The versioned archive is JSON Lines compressed as concatenated Zstandard frames.
It contains a manifest, typed knowledge records and a completion marker. Explicit
field allowlists prevent future account columns from silently entering archives.
The plugin checks chunk digests, sequence, a rolling hash chain, Zstandard integrity,
record counts and the final local file checksum before acknowledging the archive.
This is a portable knowledge export, not a database/identity restore image.

An interrupted transfer leaves an owner-only `.partial` file and `.state.json`
checkpoint. Resume using the same path and ticket while the session remains valid.
Never call confirm after a failed download or bypass corruption errors. Cancel
and restart with a new path if the saved data is damaged or the session expired.
Existing destinations are not overwritten. Local research and downloaded archives
are retained when the server community is deleted. Other users' obsolete links
can be disconnected; deletion does not reach into their machines.

The service permits one unfinished archive at a time and one chunk worker per
process. It releases each short database transaction before transmission, uses
bounded compression buffers and a single encoder thread, paces raw-byte throughput,
and yields to busy search queues. A 503 preserves progress; retry later using the
same checkpoint. Frozen knowledge cannot change between chunks or across a restart.
Database administrators with direct SQL access remain trusted infrastructure.

After deletion, `cleanup_pending:true` means canonical data and access are gone
but derived-file cleanup will retry. This never restores the community. PostgreSQL
reuses deleted space through vacuuming; its disk files need not shrink immediately.

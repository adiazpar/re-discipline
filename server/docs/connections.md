# Project connections and key lifecycle

A project is a local research folder with `.re-discipline/`. It can link to multiple
communities on one or several servers. Each link gets its own key. Another project
gets independent connections, even when it links to the same communities. A server can host many communities. There
are no human accounts, passwords, GitHub identities or machine registrations.
A key identifies a connection, not a unique person; creating another identity is
possible and is not treated as proof of a different human.

## Joining and normal use

The plugin generates an Ed25519 key pair using the OS random source. It saves the
private seed in Windows Credential Manager, macOS Keychain or Linux Secret Service.
Linux clients need an available, unlocked Secret Service keyring; there is no
plaintext-file fallback. The server does not need a desktop credential store.
Private keys are never sent over HTTP or written into project JSON, logs or Git.
The keychain lookup is specific to the current project path, server and community. Existing single-link
keys keep their original lookup; adding communities never replaces those keys. Copying a
project configuration requires its own connection; it cannot delete the original
project's key. Moving a project also requires reconnecting at the new path.

`.re-discipline/community.json` stores the server origin, community UUID, local
alias, public key and a nonsecret key reference. The public key lets an operator
match the connection in `access.list` and revoke it even if its private key is
lost; it cannot authorize requests by itself. Its `connections` array contains one
entry per linked server/community, with unique local aliases and separate keys.
Normal source mode `both` combines local research with all linked communities;
`external` searches all links and `local` sends no community requests. Explicit
query subsets narrow that set. Disconnect removes only the selected link. The project link never exposes a direct database connection.

Public joining registers the public key automatically. Private joining consumes
an operator-issued invitation. `access.create` returns the random invitation
once; the database stores its SHA-256 hash. Invitations are community-scoped,
single-use, expire in seven days by default, and permit a 1-hour to 30-day lifetime.
They grant reading only. The operator must authorize delivery to someone else.
Two clients racing to redeem an invitation cannot both win. Repeating a completed
join with the same key returns the same active connection, including after a lost
response. The plugin persists the private key before making the first request and
serializes local connect/disconnect across agent processes.

Every request is an RFC 9421 HTTP Message Signature using Ed25519. The profile
covers method, exact target URI, content type, content length and SHA-256 body
digest, including the action and community. It has a random 192-bit nonce,
creation time and two-minute expiry, allowing 15 seconds of future clock skew.
The server reconstructs the destination using its configured `PUBLIC_URL`, never
an arbitrary forwarded host. Reverse proxies must preserve the public Host header
and request body. HTTPS with ordinary certificate verification is required outside
localhost. Signatures authenticate requests; HTTPS encrypts requests and results.

After verifying a signature, the server atomically consumes its nonce in PostgreSQL.
Duplicate use is rejected across parallel requests, restarts and service replicas.
Invalid signatures cannot reserve another key's nonce. Replay records expire after
the signature's validity window plus clock-skew allowance. Busy-read retries get
fresh signatures while keeping the same key, community and payload; their existing
retry and wait budgets still apply. No login or manual signing is needed.

The server looks up the verified public key's community, checks access again in
PostgreSQL RLS when executing the read, and schedules work under its stable
connection ID. Revoking an already queued connection prevents its subsequent
read. A read already executing may finish; revocation cannot retract results
already delivered. Readers cannot upload, enumerate other communities or change
access policy. The operator uses separate host-managed credentials for management.

## Deletion and inactivity

| Event | Outcome |
|---|---|
| Normal authenticated use | Renews the inactivity lease to 180 days, at most once daily; no heartbeat or repeated login |
| Explicit disconnect while online | Removes the active server connection, deletes the local private key and removes the link |
| Disconnect while offline | Deletes the local key/link and reports unconfirmed remote removal; server record expires through inactivity |
| Manual deletion of local key, project folder or device | Server cannot detect the deletion; its unused record expires after the last renewal plus 180 days |
| Public connection expires | Next read can automatically register the same locally held key again |
| Private connection expires | A new invitation is required; an old consumed invitation cannot be reused |
| Unused invitation expires | Access is refused immediately; cleanup removes its hash and metadata |
| Operator revokes connection | Access stops; a tombstone blocks that key from immediate public rejoining for 180 days |
| Revoked key disconnects | Local key is deleted, but the server tombstone stays until its retention ends |
| Public community becomes private | Public connections are revoked; explicitly invited private connections remain |
| Operator deletes the community | All its connections/invitations and content are removed; old links cannot reach a replacement community with the same name |

A fresh invitation can upgrade an existing public connection to invited access
before a privacy change, or deliberately reauthorize a revoked key. Reauthorization
allocates a new connection ID so queued work from the revoked grant remains denied.
After a revocation tombstone is pruned, a public key may join again; permanent
person-level bans are outside this design. Issuing a new identity is also possible.
Deletion and revocation never delete corpus documents.

Cleanup runs on startup and every five seconds, with indexed batches, a five-second
work deadline and locked-row skipping. Expiry denies access immediately even if
cleanup is behind. The same maintenance removes stale invitations, connection
records, revocation tombstones and request nonces. Connection creation is bounded
by `MAX_COMMUNITY_CONNECTIONS` (10,000 retained connections plus invitations per
community by default), under the community's creation lock. Active connections,
revocation tombstones and invitations all count; expired rows release capacity
when cleanup runs. Authenticated reads have no fixed per-minute quota.

PostgreSQL autovacuum makes space from deleted records reusable. It generally does
not immediately shrink the database files on disk; this implementation avoids
indefinite accumulation of live abandoned records without disruptive full-table
vacuum operations. The deletion archive contains knowledge only; no connection or client-key backup is created.
Losing a private key means establishing a new connection; private access needs a
new invitation. Server public keys cannot recover lost client private keys.

## Security boundary and migration

A stolen database contains public keys, connection scope and expiry, short-lived
nonces, and hashed unused invitations. These do not let an attacker derive private
keys or sign as those clients. An attacker controlling the running server can
still read its corpus, alter access policy and return false results. This design
cannot protect a private corpus from its own compromised host. A compromised
client or unlocked credential-store access can also expose that client's key;
revoke that connection and establish a new one after securing the device.

Migration 019 retires the unreleased bearer-reader table. Public projects reconnect
with a project key; private projects receive a new single-use invitation. Corpus
records, community IDs and operator credentials remain unchanged. Old account and
OAuth handlers are not exposed by the production service. No automatic plugin
update on a reader's machine upgrades the server; the operator deploys an updated
server image and applies migrations using the hosting helper or their deployment.

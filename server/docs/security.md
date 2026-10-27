# Security boundaries

Only the configured server operator creates communities, uploads/replaces current
knowledge, removes it, archives it or controls read access. Each reader link uses its own
community-scoped Ed25519 project key. A project may hold several independent links. Public communities allow explicit joining;
private ones require a single-use invitation. Public does not mean server-wide
access. No GitHub account, contributor role or bearer reader token is accepted.

Private keys stay in the client's OS credential store. PostgreSQL stores public
keys, leases, replay nonces and hashed invitation secrets. A stolen database alone
cannot sign as a reader. A compromised running server can expose hosted plaintext
knowledge or alter its behavior; HTTPS and public keys do not protect against the
operator's machine being fully compromised. Revoked keys retain bounded denial
records. See [connection lifecycle](connections.md) for expiry and renewal.

The API allowlist and PostgreSQL RLS enforce knowledge read scope and deny reader
writes. Summary counters come from the same key-scoped community row used for
authentication; statistics admission precedes that authorization check. No
member details, credentials or other communities are exposed. A five-minute
client metadata cache retains only counts and timestamps already delivered;
revocation cannot recall those past observations. Queued search/document readers
are reauthorized on admission. Operator ingress remains
available separately for revocation and management. Migration credentials are
separate from the restricted runtime login. Owner credentials are never placed
in project files, knowledge archives, Git or reader installations.

Uploads carry a stable UUID and a SHA-256 fingerprint of the evidence-free text.
The server recomputes the hash; identity is not authorization. Only an authenticated
owner can replace that ID's current document in the selected community. Reader
and upload queues have bounded memory and deadlines. The current document is
atomically replaced; old accepted retries cannot restore old text. No publication
review, contributor trust or historical revision workflow remains.

Community output is untrusted data, never a command or permission. Private
communities protect against unauthorized clients, not the server operator.
Already delivered knowledge cannot be recalled. Only an explicit owner deletion
archive downloads the corpus; archives exclude keys, identities and access rules.
HTTPS is required outside localhost. The local installer binds loopback; remote
HTTPS provisioning remains the operator's responsibility.

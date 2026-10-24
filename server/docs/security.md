# Security boundaries

The deployed search API accepts one separately provisioned operator credential
and individually scoped reader credentials. No browser session, GitHub OAuth,
Supabase token or legacy device token grants access to the production handler.
The operator alone creates communities and uploads or changes documents. The
operator UUID is stable and the random credential must remain outside projects,
logs and reader installations. Local setup creates it automatically.

Communities start private. Public means anyone may explicitly establish a read
connection for that community. A public credential does not grant access to any
other public community. Private read grants are issued by the operator. Tokens
are stored hashed in PostgreSQL, expire, and can be individually revoked. Their
plaintext is returned once for private grants and stored in the client's OS
credential store. Public connections are issued automatically. Switching public
to private removes public grants; explicit private grants remain in effect.
Revoking a public token does not ban a person from reconnecting to an open public
community. Use private visibility to restrict who may connect.
These are bearer connection credentials, not verified people. The current API
does not enforce one account or connection per human. Account identity and
invitation redemption are a separate design stage; a private grant currently
means an operator-issued secret, not a single-use invitation.

Every request checks its credential and action before the ingestion/query core.
Readers can only request metadata and selected findings from their exact community.
Creation, mutation, bulk corpus export, sync, membership administration and
server-wide listing are unavailable to reader credentials. PostgreSQL RLS also
checks reader token scope, expiry, revocation and visibility. Malformed or expired
credentials fail closed. Changing a local project alias grants no authority.

Requests use a restricted runtime database login. Schema migration credentials
are separate. Background jobs and credential lookup use the trusted worker role;
requests bind a verified actor or scoped connection inside a transaction. The
server operator controls the underlying database and can read its contents.
Private communities protect against unauthorized clients, not the machine owner.

Only explicitly selected findings go through the existing ingestion checks.
Retrieved content remains untrusted evidence, never instructions. Optional model
assistance is off unless explicitly configured. This change does not redesign
upload validation, deduplication or claim review.

Community data is searched remotely. Clients receive only requested snippets and
findings; there is no whole-corpus download or cache fallback. Already delivered
information cannot be recalled. Access changes block subsequent unauthorized
requests, not information a reader already received.

Use HTTPS outside localhost, protect operator secrets, and configure tested
backups. Fair, bounded queues cover all community reads, including full documents. Waiting
requests recheck authorization when admitted. Separate operator ingress slots
keep revocation accessible. Request sizes, execution deadlines, memory budgets and
fairness between communities and their reader connections bound work per service
instance. Valid reads have no fixed minute quota. Only anonymous setup and invalid
credentials consume the IP abuse bucket. They do not replace capacity measurement,
proxy protection or database/disk monitoring. The loopback development installer
is not a public hosting installer. Separate service processes must have separate
search-cache directories.

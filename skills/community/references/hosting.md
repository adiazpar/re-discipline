# Hosting and local tests

From the plugin repository or installed plugin root:

```text
python3 scripts/community-local.py up --context orbstack
python3 scripts/community-local.py status
python3 scripts/community-local.py stop
```

Use `python` on Windows and omit `--context orbstack` for another local Docker
engine. Python 3 and Docker Compose are required for hosting; ordinary plugin
users need neither. The Linux server executable is bundled, so deployment does
not compile Go on the host. The installer runs on the server machine and
refuses remote Docker contexts. It exposes `http://127.0.0.1:7346`, retains the
PostgreSQL volume on stop, and preserves existing operator credentials on update.

The helper provisions a random operator credential outside projects, supplies
`SERVER_OPERATOR_ID` and `SERVER_OPERATOR_TOKEN` to the server, applies schema
migrations with separate admin credentials, and runs the restricted database
login. Plugin calls discover the local operator credential automatically.
Never print the private Compose configuration, environment files or credential
files. Never hand the operator credential to readers.

`server.ping` reports `mode: ready`, `community_enabled: true`, and `ok: true`.
Create communities through MCP and keep them private unless the operator requests
public visibility. Readers obtain separate community-scoped credentials.
`up --setup-only` exposes health only; normal `up` restores search operations.
The loopback installation is for development and cannot be reached by outside
users. A remote deployment needs an HTTPS origin, private operator configuration,
persistent PostgreSQL storage and the same server image. No third-party
identity provider is required. Machine provisioning and HTTPS are separate.
Run the helper on a rented Linux host with `--public-url https://kb.example.com`,
then register its installation through the owner’s existing SSH access. See
[automatic updates](../../../server/docs/updates.md).

For reproducible load testing:

```text
python3 scripts/community-load.py --context orbstack
```

It creates and drops a disposable database/cache with four communities,
1,000 signed reader connections and concurrent owner uploads. Query-then-read
workflows check exact identities, fingerprints and document text. Default resource
limits are two CPUs and 512 MiB. See [load testing](../../../server/docs/load-testing.md)
for sizes, report fields and limits of the synthetic test.

The service automatically sizes its shared retrieval pool at startup. Use operator
`server.stats` with the service origin to inspect budgets and per-community work.
No fixed community gets a permanent resource partition. See
[scheduling](../../../server/docs/search-scheduling.md) for limits and edge cases.
Do not run a load generator against someone else's server; the helper requires a
local installation.

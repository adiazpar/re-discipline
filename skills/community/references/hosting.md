# Hosting and local tests

From the plugin repository or installed plugin root:

```text
python3 scripts/community-local.py up --context orbstack
python3 scripts/community-local.py status
python3 scripts/community-local.py stop
```

Use `python` on Windows and omit `--context orbstack` for another local Docker
engine. Python 3 and Docker Compose are required for hosting; ordinary plugin
users need neither. Go builds inside Docker. The installer is local-only and
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
identity provider is required. Remote provisioning is not automated by this helper.

For reproducible load testing:

```text
python3 scripts/community-load.py --context orbstack
```

It creates and drops a disposable database and search cache with four synthetic
communities (two public/two private) and 1,000 project key connections. It measures single-community, equal,
skewed, burst, and agent search-then-read traffic. Default resources are two CPUs
and 512 MiB; `--cpus` and `--memory` test other allocations. The existing corpus
is unchanged. Reports include per-community latency, queue time, rejected work,
full-document reads, peak memory and isolation failures. A matched comparison
runs at least 10,000 judged searches before and during a knowledge archive of
the fourth community. `--archive-mib` controls the synthetic uncompressed evidence
size (256 MiB by default); repetitive test data is not a realistic compression
ratio estimate. The test verifies the local archive before explicitly deleting
that disposable community and checking the survivors. See
[load testing](../../../server/docs/load-testing.md) for options and interpretation.

The service automatically sizes its shared retrieval pool at startup. Use operator
`server.stats` with the service origin to inspect budgets and per-community work.
No fixed community gets a permanent resource partition. See
[scheduling](../../../server/docs/search-scheduling.md) for limits and edge cases.
Do not run a load generator against someone else's server; the helper requires a
local installation.

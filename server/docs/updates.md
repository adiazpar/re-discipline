# Plugin and managed server updates

The plugin contains five native client engines and Linux AMD64/ARM64 server
executables built from the same retrieval source. `bin/build.json` records the
release version, source digest and database-migration digest. CI rebuilds the
artifacts and compares their bytes. A Git push alone does not update an installed
plugin; install the new package through the host and restart its session.

## Existing projects

Session startup and first tool use refresh an initialized project's
`.re-discipline/bin/` executable from the running plugin. No Go installation,
download, corpus scan or remote call is needed. The copy is locked and replaced
atomically. Unchanged bytes are retained; corrupted copies are repaired. Older
host sessions cannot downgrade a newer release. Differently built packages with
the same version cannot fight over the copy; releases must increment the version.
An explicit `init-project` refresh can replace a development build.

MCP runs the engine inside the plugin, so a read-only project directory or a
Windows executable still in use does not interrupt retrieval. Refresh failures
are reported on stderr and retried on a tool call after a one-minute cooldown,
or in a new session. Concurrent calls continue using the plugin engine while
one refresh runs. A standalone
project executable cannot discover plugin updates on its own. Conventions and
instruction templates still refresh through `init-project`; engine updates never
rewrite knowledge.

## Managed installations

New installations made by `scripts/community-local.py up` enable compatible
updates. The next **operator management operation** checks the owner's private
deployment registration. Reader searches, document reads, connections and public
health checks never initiate deployment. `server.update` is a local operator
action to check/retry this workflow, not a remote code-execution API. Operator
tokens grant community administration; Docker/SSH access separately grants
software deployment.

Automatic updates require a strictly newer release in the same major version and
an identical protocol and migration digest. The controller packages the plugin's prebuilt
Linux service while the current application serves requests, verifies the staged
identity, drains old requests, then recreates only the application container. One update applies to every community on that server.
PostgreSQL, community IDs, owner identity, reader keys and the persistent search
cache remain in place. Readers may see a brief connection interruption during
the switch. Safe reads retry interrupted connections within their existing
bounded retry budget; this is not zero-downtime deployment.

Health must report exactly the staged version/build/schema within 15 seconds of
an automatic switch. Failure restores the
previous image. A private journal permits recovery after interruption. This is
executable rollback, not a database backup. Automatic updates never migrate SQL
or roll a database backward. One OS lock serializes updates per installation;
checks/failures are cached for five minutes. The current and previous managed
image tags are retained; older managed images are removed when unused. Failed
images may require Docker's normal cleanup. Failure logs stay in the owner's
private credential directory, outside projects and MCP responses.

Unknown legacy versions, changed schemas, major upgrades and repackaged
same-version builds are deferred. `server.update` reports why. An installation
without automatic-update registration is `unmanaged`. Operators can explicitly
adopt/upgrade it with the current hosting helper after reviewing maintenance.
Set `automatic_updates` to `false` in private `installation.json` to pin a server.
Custom Kubernetes/systemd/cloud deployments retain their own deployment process.

## Rented Linux hosts

Run the same helper on the rented host with Docker Compose and Python 3. Supply
`--public-url https://kb.example.com` during setup and separately configure an
HTTPS reverse proxy to the loopback listener. PostgreSQL remains private.

During owner setup, register that managed installation from the owner's machine
using an existing SSH config alias and trusted host key:

```text
python3 scripts/community-update.py register --service https://kb.example.com --ssh my-kb-host --remote-state /home/operator/.re-discipline/servers/local
```

Use `python` on Windows. Registration reads the operator credential over SSH and
stores it with its exact service origin outside projects. Subsequent compatible
updates transfer only bundled server software and the hosting helper. The remote
helper uses that host's saved Docker context and installation identity. No corpus,
reader key or local project is transferred. SSH is noninteractive and retains
strict host-key checks. Neither projects nor HTTP responses can supply an SSH
target or shell command. Ordinary HTTPS readers need no Python, Docker or SSH.

## Release validation

Run `go run ./scripts/build.go -all` after production source changes to build all
five client engines and both Linux service engines with a shared identity.
`-check` verifies this machine's client; `-server -target linux-amd64 -check`
verifies a server artifact. Docker's test stage still builds instrumented tests;
the deployed image uses the canonical prebuilt service.

An incompatible change to the wire contract must change the protocol, and a
change to persisted knowledge must include a migration. Additive optional
operations such as `community.stats` preserve the protocol only when existing
operations retain their meaning and newer clients tolerate an unsupported
operation on older servers. A different source digest alone does not prove that
two releases are compatible.

Policy tests cover eligibility, staging failure, rollback, interruption recovery,
locking and SSH transport boundaries. Native smoke tests exercise project refresh
through both provider adapters. Disposable integration tests exercise an actual
old-to-new server transition and scoped retrieval with concurrent readers; they
never update an existing personal installation.

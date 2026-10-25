# re-discipline

An evidence-disciplined reverse-engineering knowledge system for Claude
Code, Codex, and any MCP-capable agent. Two halves:

- **Curation** — campaign workspaces in `.re-discipline/active/` where
  investigators write full reports and distill atomic candidate
  findings; a manager promotion skim is the single quality gate into
  `.re-discipline/docs/`, the markdown-canonical knowledge base.
- **Retrieval** — `re-search`, a small ephemeral CLI: SQLite FTS5 with
  identifier-aware matching, auto-reindexing, golden-question
  regression bench, per-session MCP stdio server, and an HTTP endpoint.

Design rule: at every layer the recovery path is "edit a text file" or
"delete and rebuild" — never satisfying a machine's proof requirements.
Docs are truth; the index is disposable; git is history.

## Installation and setup

Install this plugin in Claude Code, Codex, or both. Each host installs its own
copy from the same repository; both work with the same project knowledge base.
Use a current host release and have Git available for the marketplace download.

**Platforms:** Windows x64, macOS Intel/Apple Silicon, and Linux x64/ARM64.
The plugin selects its bundled native engine automatically. No build step,
Go, Python, Node, Bash on Windows, or separate SQLite installation is required
by the plugin. WSL uses the Linux build. Windows 11 ARM64 uses the bundled x64
engine through Windows emulation; it has no dedicated ARM64 engine artifact.

### Claude Code

Use Claude Code **2.1.291 or newer**. Run in your terminal (PowerShell on Windows):

```text
claude plugin marketplace add https://github.com/adiazpar/re-discipline.git
claude plugin install re-discipline@re-discipline --scope user
```

Open Claude in your project and run:

```text
/re-discipline:init-project
```

If Claude was already open, restart that session to load the plugin. You can
also install inside Claude with `/plugin marketplace add adiazpar/re-discipline`
and `/plugin install re-discipline@re-discipline`.

### Codex

Codex has plugin marketplaces too. Use Codex CLI **0.164.0 or newer** and a
current desktop app. Run in your terminal (PowerShell on Windows):

```text
codex plugin marketplace add https://github.com/adiazpar/re-discipline.git
codex plugin add re-discipline@re-discipline
```

Open a new Codex session/chat in your project and ask:

```text
Run the re-discipline:init-project skill for this project.
```

The CLI and desktop app can use different runtimes; keep both current. If
`codex plugin` is unavailable or an older CLI reports “plugin not found,”
update the CLI. Version 0.139.0 rejects plugins located at the marketplace
root; current versions support this repository layout. Codex may ask you to review and
trust the optional session-summary hook; retrieval works without that hook.

### What initialization does

`init-project` creates `.re-discipline/`, installs the correct native engine,
and connects project instructions for both agents through root `AGENTS.md` and
`CLAUDE.md`. It asks once whether you want shared memory. Your existing docs and
campaigns are preserved. There is no platform selection or binary-copy step for
the user. A newly initialized knowledge base is empty until you add findings.

After setup, use the plugin's skills and retrieval tools normally. No community
account is needed for local knowledge. When an agent calls a retrieval tool, it
passes the active project's absolute path as `root`.

<details>
<summary>Verify installation or troubleshoot startup</summary>

For Claude, run `claude plugin list` and `claude mcp list` from the target
project. `plugin:re-discipline:re-search` should report **Connected**.
For Codex, run `codex plugin list --marketplace re-discipline`, then use `/mcp`
in the CLI or inspect the plugin's tools in the desktop app. Expect ten skills
and the `query`, `symbol`, and `community` tools.

To check the initialized project's local engine directly:

```sh
# macOS / Linux, from the project root
.re-discipline/bin/re-search --version
.re-discipline/bin/re-search stats --json
```

```powershell
# Windows PowerShell, from the project root
.\.re-discipline\bin\re-search.exe --version
.\.re-discipline\bin\re-search.exe stats --json
```

macOS/Linux need their standard `/bin/sh`, `uname`, and `dirname` utilities.
Windows uses its native executable and built-in PowerShell for the optional
session summary; Git Bash and WSL are not required. On Unix, executable bits
must be preserved when copying a checkout; Git-based installation handles this.
Use the host's normal trust/permission controls when prompted.

Host setup: [Claude Code](https://code.claude.com/docs/en/setup) ·
[Codex CLI](https://developers.openai.com/codex/cli).
Plugin installation: [Claude](https://code.claude.com/docs/en/discover-plugins) ·
[Codex](https://developers.openai.com/plugins/build/plugins).

</details>

<details>
<summary>Update an installed plugin</summary>

Claude Code:

```text
claude plugin marketplace update re-discipline
claude plugin update re-discipline@re-discipline
```

Codex:

```text
codex plugin marketplace upgrade re-discipline
codex plugin add re-discipline@re-discipline
```

Restart the host session and rerun `re-discipline:init-project` in each project.
It refreshes the plugin-owned conventions, ignore file, native engine, and
instruction block. Docs, campaigns, archives, golden questions, and text outside
that block are preserved. Moving between operating systems follows the same
initialization procedure.

</details>

<details>
<summary>Develop and test from a local checkout</summary>

Clone the repository. These steps replace the Git-backed install above. If the
marketplace named `re-discipline` is already registered from Git, remove that
registration before adding the local directory under the same name:
`claude plugin marketplace remove re-discipline` or
`codex plugin marketplace remove re-discipline`.

Claude can load the working directory directly:

```text
claude --plugin-dir "/absolute/path/to/re-discipline"
```

On Windows, use a path such as `"C:\src\re-discipline"`. Avoid enabling a second
marketplace-installed copy in the same development session. To test Claude's
marketplace route, use `claude plugin marketplace add "/absolute/path/to/re-discipline"`
followed by the same `claude plugin install` command shown above.

Codex uses a cached copy of the local marketplace:

```text
codex plugin marketplace add "/absolute/path/to/re-discipline"
codex plugin add re-discipline@re-discipline
```

After editing, rerun `codex plugin add re-discipline@re-discipline` to refresh
that copy, including changes with the same plugin version. Restart the relevant host session after changes; a running
MCP process does not hot-reload. No machine-specific manifests are generated.

Only engine development needs Go. Run from the repository root:

```text
go run ./scripts/build.go                 # this machine's native artifact
go run ./scripts/build.go -all            # all five shipped artifacts
go run ./scripts/build.go -all -check     # compare artifacts against source
python3 scripts/smoke.py                  # use python on Windows
```

The build pins the compiler from `retrieval/go.mod`, disables cgo, and stamps
the manifest version. `scripts/build.ps1` delegates to the same build. Rebuild
all targets after engine changes before committing. The smoke test's Python
requirement is only for development, not plugin installation. It checks native
project-binary installation/refresh, the CLI, both MCP adapters, fixture corpus,
and session hooks in temporary directories. CI also installs the reference
Claude/Codex versions through their real marketplace commands. Running
`scripts/smoke.py --hosts` checks Claude MCP startup, Codex's installed
skills, and all three corpus queries through Codex's own MCP connection.

The [native CI matrix](.github/workflows/ci.yml) runs the full retrieval Go suite,
vet, reproducible native-artifact checks, both real marketplace installations,
host discovery, MCP golden-corpus queries and hooks on five native runners:
Windows x64, macOS ARM64, macOS Intel, Linux ARM64 and Linux x64.
The [server CI](.github/workflows/server.yml) runs PostgreSQL integration and race
tests plus both adapters against a live server and a real Linux Secret Service.
[Native CI runs](https://github.com/adiazpar/re-discipline/actions/workflows/ci.yml)
and [server CI runs](https://github.com/adiazpar/re-discipline/actions/workflows/server.yml)
show the tested commit and outcome; cross-compilation alone is not a runtime test.

Local verification on 2026-10-25 covered macOS ARM64 and Intel binaries through
Rosetta, Linux ARM64 and x64 through OrbStack, the current six skills, and both
Claude/Codex adapters. The updated multi-community smoke exercises actual uploads,
scoped keys, global reconciliation, variants, subset selection, isolated disconnect
and knowledge-only archiving. See [load and accuracy testing](server/docs/load-testing.md).

OrbStack supplies Linux guests. Intel Mac execution here uses Rosetta on macOS;
Windows and physical Intel Mac runtime coverage come from the native CI runners.
A disposable Windows 11 ARM64/UTM VM was also tested earlier in development and
removed afterward. Those earlier VM results are not a substitute for current CI.

The bundled corpus has five documents and three golden questions. Host checks
exercise plugin discovery and MCP execution without spending model tokens; they
do not simulate a complete agent-led research session. The load tests use larger
synthetic corpora and verify exact content, but do not establish retrieval quality
for every real-world corpus.

</details>

## Plugin layout

| Path | Purpose |
| --- | --- |
| `.claude-plugin/plugin.json` | Claude metadata and explicit component paths |
| `.claude-plugin/marketplace.json` | One marketplace catalog, discovered by both hosts |
| `.claude-plugin/mcp.json`, `hooks.json` | Claude launch adapters; plugin-root substitution and exec-form hook |
| `.codex-plugin/plugin.json` | Codex metadata and explicit component paths |
| `.codex-plugin/mcp.json`, `hooks.json` | Codex launch adapters; plugin-relative cwd and Windows hook override |
| `skills/`, `templates/` | Shared agent workflows and project files |
| `bin/` | POSIX launcher, Windows executable, and four native Unix binaries |
| `retrieval/`, `scripts/` | Engine source, portable build, and packaging smoke test |
| `server/` | Optional owner-hosted community server, database migrations and tests; shares `retrieval/` directly |

Neither a root `.mcp.json` nor `.agents/plugins/marketplace.json` is needed.
Both hosts accept the same MCP JSON shape, but their local path resolution
is different: Claude substitutes `${CLAUDE_PLUGIN_ROOT}`, while Codex
resolves `cwd: "."` against the installed plugin. Keeping these small
adapters separate avoids adding a shared scripting runtime to Windows.
The existing `.claude/settings.json` contains preferences for working on
this repository; it is not plugin installation metadata.

Claude supports an argument array for shell-free hooks. Codex uses shell
commands and a Windows override. Both run the same read-only `session-start`
command. Codex requires the user to review and trust plugin hooks before they
run; the MCP tools work independently of that optional summary.

See the official [Claude plugin reference](https://code.claude.com/docs/en/plugins-reference),
[Codex plugin guide](https://developers.openai.com/plugins/build/plugins),
[Codex MCP path resolver](https://github.com/openai/codex/blob/main/codex-rs/codex-mcp/src/plugin_config.rs),
and [Codex hook reference](https://learn.chatgpt.com/docs/hooks).

## Daily use

CLI examples below use the macOS/Linux filename `re-search`; on Windows,
use `re-search.exe`. The MCP tools select the native binary automatically.

- `.re-discipline/bin/re-search query "how do I …"` — search before
  investigating; agents get the same via the `query` MCP tool.
- `query --kind fact` / `--kind ops` / `--kind reference`, and
  `--grade direct` — narrow to one class of doc. Flags go BEFORE the
  question text.
- `/re-discipline:open-campaign` — start an investigation workspace.
- Investigate; producers distill findings as they go (see the curate
  skill / `.re-discipline/CONVENTIONS.md`).
- `/re-discipline:close-campaign` — promote, summarize, archive.
- Wrong doc later? Edit it, mark `superseded`, link the replacement.

## Community knowledge

A community is a remote knowledge base and search index hosted by its operator.
One server supports several isolated communities. The operator alone creates,
uploads, replaces and removes documents. Everyone else connects for read-only
search. The server uses the same retrieval engine source as the plugin; the
plugin sends queries and receives selected results, never a corpus download.

Local findings contain a complete `Knowledge` section and a final `Evidence`
section. Evidence references live in that section, not in frontmatter. Uploading
omits Evidence from a copy; the original and all referenced reports, scripts and
traces stay local. Conditions and limitations belong in Knowledge so the shared
finding stands alone.

Every community starts private. The operator may make it public afterward:

- **Public:** anyone can explicitly connect and receive a credential limited to
  that community. A Doom connection cannot query Godzilla, even if both are public.
- **Private:** the operator issues a single-use invitation for that community.
  Making a public community private revokes its public connections.

No accounts, passwords or GitHub linking are required. Each project/community link has its own
key pair. Private keys stay in the operating system credential
store; the server stores public keys. Only the operator can list all hosted
communities, issue invitations or revoke connections.

### Host a local development server

Install Python 3 and Docker with Compose; OrbStack provides Docker on macOS. From
the repository or installed plugin directory, run:

```text
python3 scripts/community-local.py up --context orbstack
```

Use `python` on Windows. Omit `--context orbstack` for another local Docker engine.
The helper builds inside containers, generates private credentials, applies schema
migrations, and starts `http://127.0.0.1:7346`. Updates preserve the database and
operator identity. PostgreSQL is not exposed on a host port. The installer enables
the complete operator/reader API on loopback for testing; it does not publish your
computer on the internet. Remote hosting requires HTTPS and server provisioning.
See [server operation](server/README.md).

Ask the community skill to check the server, create a community and connect the
project. The local operator credential is discovered automatically. Uploads use
the owner-only current-knowledge model described below.

```json
{"action":"server.ping","service":"http://127.0.0.1:7346"}
```

Expect `ok: true`, `mode: "ready"`, and `community_enabled: true`.
`python3 scripts/community-local.py status` checks the installation; `stop` stops
it while retaining data. `up --setup-only` exposes health checks only.

### Connect and search

A project is your local research folder containing `.re-discipline/`. It connects
to **multiple communities**, on the same server or different operators' servers.
Each link has its own community-scoped key and unique local alias. Linking a
community opts it into the project's default remote search set.

For a public community, ask the agent to connect this project to its server URL
and community UUID or slug. For a private community, the operator creates an
invitation with `access.create`; pass it once as `connect` data `{invitation:"…"}`.
The plugin handles key creation, request signing and renewal automatically.
`.re-discipline/community.json` contains only the server, community, alias and a
public key and nonsecret key reference. No private key is saved in the project or sent to the server.

Windows uses Credential Manager, macOS uses Keychain, and Linux uses an unlocked
Secret Service keyring. A headless Linux client needs that OS service available;
the plugin reports an unavailable store instead of saving an unencrypted key.
The hosted server itself needs no desktop keyring.

Connecting changes local-only source mode to `both`: local research plus this
project's linked communities. `external` searches all linked communities; `local`
searches only the local project. The query tool's `communities` array (CLI
`--communities doom,godzilla`) optionally narrows the linked set.
The server verifies signed requests, enforces community scope and rejects replays.
HTTPS is required outside localhost. There is no repeated login prompt.

`disconnect` removes the selected server connection and deletes only its local
key. Other linked communities remain usable. If offline,
it deletes the local key and reports that remote removal could not be confirmed.
Requests renew access automatically, at most once daily. A connection unused for
180 days expires and is pruned, including when someone manually deletes their key.
Public access can reconnect automatically after expiry; private access needs a
new invitation. Unused invitations expire after seven days by default. No backups
are created. See [connection security and lifecycle](server/docs/connections.md).

Community retrieval is remote-only. There is no sync mode, corpus cache or offline
fallback. Legacy sync settings are interpreted as remote; old downloaded files
are never searched. An unavailable community is reported as unavailable while
successful results from other communities and the local project remain usable. Ordinary local
project retrieval still works offline.

Use `/re-discipline:community` for hosting, connections, uploads, community
management and read access. The old publish and review skills are retired.

### Upload owner knowledge

Ask the agent to upload the curated KB or selected files to a chosen community.
With multiple links, `knowledge.upload` and `knowledge.status` require its alias;
linking never automatically broadcasts uploads. `knowledge.upload`
automatically assigns missing stable finding IDs, excludes Evidence, compares
fingerprints and sends only new or changed files in bounded batches. A changed
owner finding replaces its server copy; the latest successful upload wins.
Renaming keeps identity. Evidence-only edits do not upload again. Missing local
files never delete remote knowledge automatically.

The UUID lives in finding frontmatter; fingerprints and file metadata are cached
in the existing disposable `index.db`. A cache loss requires rereading files but
does not lose identity. `knowledge.status` compares without uploading;
`verify:true` bypasses file metadata shortcuts. See [upload behavior and bounds](skills/community/references/upload.md).

See [community operations](skills/community/references/operations.md) for MCP
payloads. Reader credentials cannot upload, change policy, create communities or
obtain another community's documents.

### Test server load

```text
python3 scripts/community-load.py --context orbstack
```

This uses a disposable database, four synthetic communities and 1,000 reader
keys. It checks actual query-then-read accuracy with 128 concurrent workflows
while the owner uploads knowledge into all four communities. The development
corpus stays unchanged. `--cpus`, `--memory`, `--users`, `--documents` and
`--requests` vary the experiment. Reports include failures, latency, memory and
scheduler statistics. See [load testing](server/docs/load-testing.md).

Searches and document reads share an automatically sized worker pool. A single
reader or community can use the entire idle pool; busy communities and their
reader connections share turns when others need service. Authenticated reads
have no fixed minute quota. Queues have memory and time limits, and cancelled work
leaves immediately. The plugin retries temporary busy reads at most twice with
jitter, retaining the exact community and credential.
The operator can inspect budgets and queues with `community` action `server.stats`.
See [resource allocation and agent retrieval](server/docs/search-scheduling.md)
for the design, edge cases and tuning limits.

## Symbol lookup

Some knowledge is a table, not a document: struct layouts, enum values,
constants. Turning tens of thousands of those into markdown would bury
the docs and dilute the index for records nobody browses — you look a
struct up by name or not at all.

So `re-search` keeps them in a separate `symbols` table, deliberately
outside the FTS doc index:

```text
re-search symbol idLangDict_langEntry_t

[struct-doom2016] reference/idlib_schema.json (DOOM 2016)
idLangDict_langEntry_t  size 32
  id      unsigned int    +0x00  4
  key     idAtomicString  +0x08  8
  value   idAtomicString  +0x10  8
  maxLen  unsigned int    +0x18  4
  minLen  unsigned int    +0x1C  4
```

Exact match first, substring only as a fallback. Also available as the
`symbol` MCP tool and `GET /symbol?name=…`, and testable from the golden
set: a case carrying `symbol` instead of `q` expects a symbol name rather
than a document path.

To populate it, write `.re-discipline/symbols.jsonl` — one object per
line, `{name, kind, render, source}` — from whatever your project's
type data happens to be. `render` is opaque pre-formatted text, so the
tool never needs to learn your schema's shape. The file is optional;
its absence is a no-op.

Symbols stay behind the dedicated call rather than being appended to
`query` results. That was measured, not assumed: across a 148-question
golden set, 16 questions contained a token matching a symbol name and
10 of those were ordinary English colliding with constants (`player`,
`resource`, `encounter`). Blending would have attached noise to more
queries than it helped, and the largest single render is 642 lines.

## Asking why something ranked where it did

`query` gives an order. `explain` gives the reason for it:

```
re-search explain --json "what stops a demon attacking"
```

Every candidate comes back with its bm25 score, reference penalty, exact-name
priority and resulting rank. An exact whole-name lookup favors the document
that declares that identifier; broader questions retain relevance ranking.
`re-search stats` prints the matching census and ranking rules in force.

Both run the same code paths as a real search -- `explain` calls the ranker
`query` calls, not a reimplementation of it, and a test keeps the two orders
identical.

The `visualize` skill builds a page out of the two, showing a project its own
knowledge base and walking two of its own questions through the ranking.

## When the doc's words are not the reader's words

Search matches words, not meanings. A cvar documented by its engine help
string says `scales the time`, and no amount of ranking will connect that to
"how do I slow down game time" -- the two share no term. That gap is not a
ranking bug and cannot be tuned away.

`aliases:` frontmatter closes it by hand:

```yaml
idents: [timescale]
aliases: [slow motion, bullet time, game speed]
```

Aliases are indexed as ordinary searchable text alongside the identifiers
expanded out of the title and body. They deliberately do NOT enter the
declared-identifier table, so unlike `idents` they never waive the
reference-doc rank penalty -- an alias says "someone might call it this",
not "this document owns that name".

They are also the sharpest available tool for wrecking your index. The
document-frequency warning below applies with full force: a handful of
specific phrasings per doc helps, while one generic word written across a
family of thousands destroys its own search weight and drags unrelated
queries with it. Write 3-6 per doc, and reject any alias that would land in
hundreds of them.

Measured: adding aliases to 219 documents moved a 231-question benchmark
from 215 to 219, and made `porter` stemming worth +4 where it had been worth
-1 without them.

## Mixing curated facts with generated reference material

A knowledge base usually grows two kinds of doc: hard-won `kind: fact`
claims, and bulk `kind: reference` entries generated one-per-item from a
dump — every console command, every engine event, every type. Reference
entries are numerous, short and keyword-dense, so left alone they bury
the curated facts on ordinary questions.

`re-search` handles this without a filter the caller has to remember:

- **Ranking** weights `title` and the identifier column above `body`.
- **Exact whole-name lookups prefer declared identifiers** before score ranking
  and before the candidate limit. Short incidental mentions or aliases cannot
  crowd out the declaring document. Superseded documents still sort last, and
  kind/grade filters still apply. Broader questions keep relevance ranking.
- **Reference docs take a rank penalty on concept questions**, and are
  exempt from it when the query names one of their declared `idents`.
  So "why did the boss stand still" reaches a curated fact, while
  "what does event `spawnSingleAI` do" reaches the reference entry.
- **`idents:` frontmatter** declares the identifiers a doc owns. This is
  what earns the exemption; without it a reference doc is only findable
  by prose.
- **Cross-reference bullets are stripped from the index** (not from your
  files) — a "Depends on" list quoting other docs' titles makes every
  doc compete for every other doc's topic.

One rule when generating docs in bulk: **a phrase repeated across every
generated doc destroys its own search weight.** Measured on a real
11,000-doc corpus, writing one stock token into 1,611 docs took its
document frequency from 22 docs to 1,633 and broke unrelated queries;
a tag applied to 6,862 docs put that word in 64% of the corpus. Put
provenance and confidence in frontmatter, keep body prose specific to
the entry, and keep tags rare.

Grow `golden.jsonl` before a bulk load, not after, and include questions
whose expected answer is a reference doc — otherwise the bench can only
measure harm to facts and never benefit to lookups.

You may be tempted to give reference material its own index so it cannot
perturb the facts at all. That was measured on a 12,152-doc corpus and
did not pay: scoring only the questions expecting a curated doc, so the
test cannot cheat by deleting its own answers, removing all 7,150 bulk
reference docs moved curated retrieval from 94/106 to **93/106** — one
point worse. The penalty and the boilerplate discipline above already
neutralise the interference a split would remove. Re-measure before
adding a query router you do not need.

Local retrieval runs on every supported platform. CLI calls start, answer,
and exit; each agent host manages its own MCP process for the session.

### Community knowledge identity

A community stores one current knowledge document per stable finding UUID.
Fingerprints detect content changes; they are not file identities. PostgreSQL
stores the current documents; a separate SQLite index per community is rebuilt
from them and uses the same retrieval engine as the plugin. Uploading queues
bounded incremental indexing. Search quietly returns the content currently indexed;
new uploads appear as indexing catches up. Snippets and fingerprints remain
consistent within each result.

Retrieval gathers candidates from the selected communities, then reconciles each
finding UUID across all sources **before ranking**. In `both` mode this includes
curated local copies. Exact UUID/fingerprint matches collapse to one result with
all source locations and no extra ranking votes. Different UUIDs remain distinct.

A last-matching fingerprint is kept separately for each community link. An
unambiguous local or remote change selects the updated copy. Competing changes or
unknown lineage prefer the curated local copy. Without a local copy, differing
remote versions form one result with explicit source-labelled `variants`, each
with its own fingerprint and read location. No variant is silently declared true.
Optional query assistance retains ordinary ordering when variants are unresolved.

Losing copies carry metadata in `other_copies`, never an extra ranking vote.
If a selected correction no longer matches the query, the obsolete match is
omitted. Queries compare at most 128 candidate identities. When another community
introduces an identity not yet checked, one additional comparison query per source
retrieves bounded metadata and replaces that source's entire result snapshot.
This adds no corpus synchronization or full-document downloads. A failed source
is reported, and successful sources remain usable. Ordinary indexing lag is silent.
See [reconciliation rules and limits](skills/community/references/retrieval.md).

Curation and upload maintain `created_at` and `knowledge_updated_at` in finding
frontmatter. Evidence-only edits preserve the knowledge date; IDs, dates, Evidence
and filenames are excluded from the knowledge fingerprint. Dates describe when
knowledge was maintained, **not which copy wins**. Plain editor changes receive
their date at the next curation/upload; retrieval detects their content changes
without waiting for that timestamp.

The existing disposable `index.db` keeps one last matching fingerprint per
finding/connection, alongside upload and timestamp bookkeeping. An upload receipt
also establishes a match. Server sequence metadata prevents a delayed index or
response from undoing that acknowledgement. Rebuilds preserve this bookkeeping;
cache deletion safely falls back to the local curated copy. No document history
is stored. `local` remains local-only; `external` reconciles only the selected remote communities.

## Claims and optional assistance

Claims and their complete applicability are shared; Evidence and research remain
local. Only the server operator writes knowledge. There are no contributor roles,
publication queues, review gates or retained revision histories.

Optional Jev assistance ranks query candidates. It is disabled by default and
never establishes truth or permission. Read the
[setup and data contract](skills/community/references/assistance.md).
Always pass the active project `root` in plugin tools; `status` diagnoses the
selected workspace and source configuration.

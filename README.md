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
`scripts/smoke.py --hosts` checks Claude MCP startup, Codex's ten installed
skills, and all three corpus queries through Codex's own MCP connection.

CI is configured to test Windows x64, both Mac architectures, and both Linux
architectures. Verification performed on 2026-10-23:

| Environment | Verified |
| --- | --- |
| macOS ARM64 | Claude Code 2.1.291 connection; Codex CLI 0.164.0 discovery of 10 skills and 3/3 corpus answers through the installed plugin; native install, both adapters, and hooks; Go race tests and vet. Codex desktop runtime 0.163.0-alpha.11.2 also verified |
| macOS x64 through Rosetta on Apple Silicon | Actual Intel binaries for Claude Code 2.1.291, Codex CLI 0.164.0, and re-search; same packaging and host checks, including 3/3 corpus answers through Codex |
| Linux ARM64, fresh Ubuntu 24.04 OrbStack machine | Both marketplace installations, Claude MCP connection, Codex's 10 skills and 3/3 corpus answers; native install, both adapters, hooks, and CLI bench |
| Linux x64, fresh Ubuntu 24.04 OrbStack machine | Same checks under CPU emulation |
| Windows 11 ARM64, disposable UTM VM | Actual ARM64 and x64 builds of Claude Code 2.1.291 and Codex CLI 0.164.0, with the bundled x64 engine under Windows emulation; marketplace installation, Claude MCP connection, Codex's 10 skills and 3/3 corpus answers; engine refresh, both adapters, and hook commands passed |
| All five build targets | Pinned builds and byte-for-byte rebuild comparison |
| Windows x64 and macOS Intel native hosts | CI configured; native host runs still pending |

OrbStack supports [Linux machines](https://docs.orbstack.dev/architecture),
not Windows or macOS guests. The Intel Mac checks above use Rosetta on macOS;
they are not a run on Intel hardware. The Windows checks used a temporary
Windows setup account, without completing personal account onboarding; x64
programs ran under Windows ARM emulation. Native Windows x64 and Intel Mac
validation uses the corresponding CI runners, which have not run for these
unpublished changes.

The bundled corpus has five documents and three golden questions. These checks
establish packaging and fixture behavior; they are not a large-corpus quality
evaluation. Host checks exercise plugin discovery and MCP execution without
spending model tokens; they do not simulate a complete agent-led research session.

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

The optional [community service](https://167.99.51.162)
adds shared libraries to local retrieval. Its backend lives in the separate
[community repository](https://github.com/adiazpar/re-discipline-community).
The plugin remains usable without an account or network connection.

Community connections allow up to five seconds to establish TCP, while connected
requests retain a 90-second allowance for services waking from an idle state.
Unreachable sources remain visible as warnings; local results are still returned.
This does not change configured sources, permissions or offline-cache preferences.

For live community search without downloading the KB, tell your agent:
"Connect to this community with server-only retrieval and search both local and
community knowledge." The agent passes `retrieval: "remote"` on `connect`, or
uses `retrieval.set` for an existing connection. This preference is stored in
`.re-discipline/community.json`, separately from `mode: local|external|both`.
Remote mode returns only matching results and needs connectivity; it never
synchronizes the whole library or falls back to cached community content.
Server-only retrieval is the default for new projects and settings without a
recorded transport. `retrieval: "sync"` explicitly enables offline caching;
existing explicit sync preferences are preserved.


- `/re-discipline:community` — sign in, create or connect a library, invite
  collaborators, and choose local, external, or combined retrieval.
- `/re-discipline:publish` — prepare selected portable findings from `docs/`,
  review the package, and explicitly submit it. Operational and local environment
  material is excluded; the service enforces permissions and acceptance policy.
- `/re-discipline:review` — inspect and accept or return contributions.
- `/re-discipline:sync` — download accepted revisions for offline search.
- `/re-discipline:dashboard` — open the optional visual management interface.

Use GitHub sign-in once and approve the plugin's device code. Credentials stay in
the operating system credential store. Each project records its chosen libraries
and disposable caches in gitignored `.re-discipline/community*` paths. Connecting
does not upload local documents, and synchronizing does not flush queued drafts.

```sh
.re-discipline/bin/re-search query --sources both --offline "timescale"
```

See [community operations](skills/community/references/operations.md) for the
JSON interface exposed by the CLI and the `community` MCP tool. Private content
already downloaded can remain readable offline until the client reconnects and
learns of a revocation; access revocation cannot erase previously copied content.

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

### Community integrity

The community service owns source identity, immutable revisions, accepted copy
attribution, and reasoned validity assessments. Local publication files are
rebuildable caches. Combined retrieval uses server-certified exact text matches to
collapse local/community copies and warn about old or refuted local claims.
Server source mappings also group portability-edited variants while retaining the
published version and marking the differing local text as unverified.
Similar contributions require an explicit server review decision; they are never
silently merged on wording alone. All integrity operations are available through
the community tool; see the operation reference in the community skill.

## Claims and optional assistance

Publication shares concise claims, complete applicability and attribution; private
research stays local. Use one `publish.prepare` route for selected files and the
single Publish action in the dashboard. The review queue integrates overlap
comparisons and background community health. Existing histories remain available.

Optional Jev assistance is built into ordinary local/community queries, selection
and review. It is disabled by default, does not require plugin users to install a
provider, and never establishes truth or permission. Read the
[setup and data contract](skills/community/references/assistance.md).
Always pass the active project `root` in plugin tools; `status` diagnoses the
selected workspace and source configuration.

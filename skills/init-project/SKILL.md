---
name: init-project
description: >-
  Initialize or resync a re-discipline project. Idempotent and
  non-destructive: creates what is missing, never overwrites existing
  content, updates only marker-bounded blocks, and reports what it did.
---

# Initialize A re-discipline Project

Run from anywhere inside the target project. New and existing projects
are the same procedure — they just start with different things missing.

## 0. Old-format check (hard stop)

If `.re-discipline/config.json` or campaign JSON records from
re-discipline 0.x exist: create and convert NOTHING. Tell the user:
"old-format re-discipline state detected — run the one-time migration
session first (an agent reads the old JSON and writes 1.0 markdown docs;
you review the diff before the old tree is deleted)." Stop.

## 1. Create the tree (only what's missing)

```
.re-discipline/
  CONVENTIONS.md      copy of <plugin-root>/templates/CONVENTIONS.md
  docs/  docs/ops/  active/  archive/
  golden.jsonl        empty file
  bin/re-search      native binary for this machine (re-search.exe on Windows)
  .gitignore          copy of <plugin-root>/templates/project-gitignore
```

After creating `.re-discipline/`, run the installed plugin's launcher:

```text
<plugin-root>/bin/re-search install-binary --root <absolute-project-root>
```

Quote both paths. On Windows, invoke `<plugin-root>/bin/re-search.exe`.
The launcher selects the native engine, and `install-binary` copies that running
engine to the project with the correct filename and executable permissions.
It returns JSON with `path` and `version`; use the returned `path` for
`{{RE_SEARCH}}` in the marker block. Do not ask the user to choose an OS,
architecture, binary, or build command. Do not copy the dispatcher by itself.
Run the returned project path with `--version` to verify it before reporting success.

Re-running init refreshes `CONVENTIONS.md`, `.gitignore`, and the project
binary from the plugin (these three are plugin-owned copies); everything else
is user data — never touch it.

Existing project binaries also refresh at session start or first tool use after
a plugin update. Do not require initialization solely to update an engine.
Initialization still creates new projects and refreshes conventions/instructions.

## 2. Shared memory decision

Read the policy line from the marker block in root `AGENTS.md` (the
canonical policy file). If no recorded decision exists, ask the user
once: "Use shared memory? Replaces Claude/Codex native memory with
`.re-discipline/docs/ops/` so all agents share the same recall.
[Recommended: yes]". Then converge:

- **enabled**: set `autoMemoryEnabled: false` in the project's
  `.claude/settings.json` (create the file if absent, preserving any
  existing keys). For Codex, apply the equivalent project-scoped memory
  switch if one exists; if none exists, the marker-block instruction is
  the mechanism.
- **disabled**: do NOT touch host memory settings at all — init only
  ever writes them when enabling.

## 3. Host wiring (one canonical agent file)

Root `AGENTS.md` is the single canonical agent file. Codex reads it
natively (it walks AGENTS.md files from the git root to the cwd); Claude
Code does NOT read it automatically and instead imports it. Wire both:

**AGENTS.md** — render `<plugin-root>/templates/marker-block.md` with
`{{SHARED_MEMORY}}` → `enabled`/`disabled` and `{{RE_SEARCH}}` → the
project path returned by `install-binary`, then apply:

- markers absent → append the block (create the file if missing);
- exactly one well-formed `<!-- re-discipline:start -->` …
  `<!-- re-discipline:end -->` pair → replace its contents;
- anything else (unpaired, duplicated, nested) → leave the file
  untouched and report it for manual repair.

**Root `CLAUDE.md`** — must contain the line `@AGENTS.md` (Claude Code's
documented import syntax, expanded at launch). If the file is missing,
create it containing exactly that line; if it exists without the line,
add it at the top; never duplicate the marker block into any CLAUDE.md.

**Pre-1.1 layouts**: if a re-discipline marker block exists in
`.claude/CLAUDE.md` or root `CLAUDE.md` (the old dual-block model),
remove that block (markers included) — the block lives only in
`AGENTS.md` now. Content outside markers is user-owned: never touch it.
`.codex/` needs no files — Codex does not read `.codex/AGENTS.md`.

## 4. Report

List what was created, refreshed, skipped, and any files needing manual
repair. Do not commit unless the user asks.

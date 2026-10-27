---
name: summary
description: >-
  Summarize the current re-discipline project's local knowledge counts,
  retrieval settings, and linked communities in chat. Use for a project
  knowledge-base overview or statistics; no visualization or remote polling.
---

# Summarize the project

Return a concise text summary using the current project's data. Resolve its
absolute root explicitly; if it has no `.re-discipline/` directory, report that
it is not initialized rather than creating one.

## Collect local facts

Use the installed plugin's `bin/re-search` launcher, or the project's
`.re-discipline/bin/re-search` copy. On Windows use `re-search.exe`.
Run once per summary, with flags before any positional arguments:

```text
re-search stats --root "<absolute-project-root>" --json
re-search --version
```

`stats` returns indexed local document counts by kind, status and grade, symbol
count, golden-question count and index format. It checks local freshness and can
rebuild a stale index; it makes no community or model requests. Preserve any
warning about an incomplete refresh, and label those counts as the available
index snapshot. If collection fails, report the missing information rather than
substituting zeroes. An empty initialized knowledge base can still have links.

Read local link settings with MCP tool `community`:

```json
{"action":"connections","root":"<absolute-project-root>"}
```

Without MCP, send the same JSON on stdin to `re-search community`. This reads
local configuration without contacting servers or accessing reader private keys.

## Report

Include the project name/root, local document count with a compact breakdown,
symbol count, number of golden questions, engine version, retrieval mode
(`local`, `external`, or `both`), and linked community count. List each link's
name/alias and server origin; use its community UUID if a name is missing or
ambiguous. Show zero links as "none". Treat links as configured, not verified
online or still authorized. Keep keys and credential references out of the report.

Optionally include the local index file's size from filesystem metadata, clearly
labelled as index size rather than total knowledge storage. Local document counts
exclude remote knowledge and are not a combined, deduplicated total. Golden
questions are test cases on file, not evidence that a benchmark passed.

## Performance and remote statistics

Do not run a benchmark, trial searches, directory-wide size scan, server ping or
remote statistics request just to produce an overview. There is no automatic
latency/throughput history to report; omit performance figures unless measured.
For a requested performance check, use a small stated workload and report the
measurement scope, timing and whether index refresh was included. Local-only
query measurements should use `--sources local --offline --baseline` to exclude
community traffic and optional query assistance. `bench --json` measures golden
retrieval success, not server capacity or latency percentiles.

Community document/byte counts are available through the existing operator-only
`usage` action, and server resource statistics through `server.stats`. They are
outside this default summary. Readers currently have no community statistics
operation. Report remote counts as not fetched; never enumerate documents or
infer a community's size from search results. If the user explicitly requests an
operator report, follow the [community skill](../community/SKILL.md).

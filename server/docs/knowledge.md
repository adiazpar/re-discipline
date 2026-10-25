# Current knowledge

Only the configured server operator can upload, replace or delete documents.
Readers only query and fetch within their connected community. Community
visibility never widens a credential's scope.

See the [upload contract](../../skills/community/references/upload.md) and
[MCP operations](../../skills/community/references/operations.md). The same Go
engine indexes both local and remote knowledge. PostgreSQL stores current text;
SQLite stores derived per-community search entries and matching text. A committed
batch is stored immediately; index progress is asynchronous. Search returns the available indexed content
without lag warnings; sequence counters remain available for diagnostics. No retained historical documents or semantic publication gate exists.

Search accepts up to 128 `compare_ids` (canonical finding UUIDs) and returns
`compared` identity/fingerprint/sequence entries from the same per-community index
snapshot as its hits, with `comparison_complete:true`. Nonmatching indexed
findings are included in this metadata; absent IDs are omitted. Community access
checks and fair queues are identical to ordinary queries. No full bodies are
loaded for comparisons. This lets clients exclude an obsolete matching copy even
when its replacement uses different search words. Query responses expose curation
timestamps as informational metadata; clocks never choose the winner.

Fingerprint format 2 excludes curation timestamps. The upgrade rehashes current
documents in bounded batches and rebuilds derived indexes, preserving text, UUIDs,
server generations and stored dates. Finish or cancel any active archive before
this upgrade: its frozen snapshot cannot be changed mid-export.

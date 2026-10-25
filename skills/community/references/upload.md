# Owner knowledge uploads

Use the community MCP tool with the absolute project `root`, action
`knowledge.upload`, and the connected `alias` (optional for the sole link).
`data:{paths:["docs/finding.md","docs/engine/"]}` selects files/folders; omit paths
for all promoted fact/reference findings. Only the operator credential can write.
Reader connections cannot upload, delete, inspect the inventory, or manage access.

Local findings carry a stable UUID `id` in frontmatter. Curation calls
`knowledge.identify` with `data:{paths:[...]}` after creating, editing or promoting a finding. It maintains
`created_at` and `knowledge_updated_at`; only knowledge changes advance the latter. Preserve
that ID across moves and edits. Upload assigns it to older files automatically.
Never change an existing ID or copy it into a distinct finding. Duplicate local
IDs stop upload before any batch is sent. The fingerprint is computed from the
complete publishable content, excluding Evidence, the ID, curation timestamps and the filename.
There is no fingerprint field for an agent or user to maintain.

The server stores exactly one current document per ID in the selected community.
Same fingerprint and path: skip. Changed fingerprint: automatically replace.
Renamed file: update the source path under the same ID. No merge, review or conflict
prompt. The latest successfully committed owner upload wins. Retries retain their
batch key and bytes so an old accepted retry cannot overwrite a newer upload.
Missing remote documents are uploaded again; missing local files leave remote
knowledge alone. Explicit removal uses `knowledge.delete` with `{ids:[UUID]}`.

Only the upload copy loses Evidence. Original findings, reports and scripts stay
local. Conditions and limitations must already be in Knowledge. `docs/ops`, INDEX,
non-promoted findings, and explicit private/local markers are excluded. This is
structural selection, not an automated scientific correctness decision.

The existing disposable `index.db` caches file metadata and hashes. Normal runs
stat the corpus and reread changed files, then compare a paginated remote hash
inventory. `data:{verify:true}` rereads everything when a full integrity check is
needed. Stat shortcuts cannot prove that a filesystem's metadata is truthful.
Deleting the cache is safe: identity remains in each finding. Migration adopts an
old remote UUID only for a unique exact path-and-content match.

Content memory is bounded per file/batch; metadata inventories scale with file
count. No entire KB body is loaded. Each batch has at most
50 documents and 1.5 MiB encoded data; each shared finding is at most 1 MiB.
Uploads and indexing have bounded queues separate from readers. Interrupted
uploads can be rerun: the inventory skips documents already stored. Results report
files read, upload bytes, updates, unchanged files, missing local copies and errors.
`complete:false` is a partial result; do not claim success. Storage success is
separate from indexing: search quietly returns the currently indexed content while
new uploads are indexed in the background. In `both` mode one copy wins before ranking: matching copies combine; a sole
change since the last match wins; divergent changes or an unknown baseline prefer
the local curated copy. An obsolete match is omitted even if its replacement no
longer matches the search words. Alternate copies are metadata, never extra text
or ranking votes. Dates are informational; fingerprints and the last match decide.
Upload acknowledgements prevent delayed server indexing from reversing this choice.

`knowledge.status` uses the same scan/comparison without changing source files or writing
remote knowledge. Neither action removes files automatically. Never expose the
operator credential, archive tickets or private project keys in output.

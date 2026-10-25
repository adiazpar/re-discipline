# Retrieval across linked communities

Linking opts a community into this project's default remote search set. `local`
searches local knowledge only; `external` searches all links; `both` combines local
knowledge and all links. The query tool accepts an optional `communities` array of
aliases. Unknown aliases fail before network access. Duplicate aliases in a query
are queried once; duplicate server/community links in settings are rejected.

Each request names exactly one community UUID and uses that link's key. A server
never expands it to other communities. The client permits four simultaneous HTTP
requests per query, with a shared 20-second network deadline and existing bounded
busy retries. This is client fan-out, not the server's adaptive worker limit.
There is no fixed maximum number of linked communities. Large link sets may exceed
the deadline; source availability records distinguish partial results from no matches.

The client interleaves source pages into a window of at most 128 unique candidates.
It resolves copies once across the selected sources, then applies the final result
limit. Independent corpora's raw scores are not compared or summed. Copies occupy
one candidate slot regardless of the number of communities containing them.

| Condition | Selection |
|---|---|
| Same UUID and fingerprint | One copy, all source locations; prefer local read surface when matching |
| Different UUIDs | Separate findings, even if text is similar |
| One unambiguous updated fingerprint relative to the recorded per-link matches | Updated copy |
| Competing updates or unknown lineage, with curated local knowledge | Local curated copy |
| Different remote fingerprints without a curated local copy | One group containing source-labelled `variants` |

Different communities' sequence numbers and timestamps cannot order their content.
Sequences only protect each link's own upload acknowledgement from delayed responses.
The local index keeps one last-matching hash per finding/link, not revision history.
A missing cache falls back to local curated knowledge. `external` does not use local
content to choose an authority.

A selected copy must actually match the query to produce a result. Corrected local
or remote copies that no longer match suppress their obsolete alternatives.
Metadata comparisons also look up identities absent from a source's ranked page.
When needed, one additional query per source compares the bounded candidate list;
its response replaces that source's complete earlier snapshot. No document body is
fetched for dedupe. Unsupported legacy comparison metadata is reported as incomplete.
Cross-server queries are independent snapshots, not a global transaction.

A variant group has `source: multiple`, `resolution: community_variants`, and a
`variants` array. Each variant includes its fingerprint and locations. A
`metadata_only` variant identifies an indexed copy absent from that query's result
page; it has no invented snippet. Read through a variant's community `source` alias
and finding UUID using `knowledge.get`. Keep conflicting claims attributed; neither
copy count nor date decides authority. Optional assistance preserves ordinary
ordering when unresolved variants are present instead of scoring an arbitrary copy.

A read can observe a newer document than its earlier search. Check the returned
fingerprint; re-query if an exact match matters. Uploads appear in search as the
background index catches up. This ordinary lag generates no warning. Unavailable,
revoked or failed sources are explicitly marked; never reuse old remote results.

Retrieval links do not select upload targets. Upload, document read, deletion and
access-management actions address one explicit community. Uploads require that
server's operator credential regardless of how many reader links a project has.

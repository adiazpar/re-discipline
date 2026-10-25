# Optional Jev query ranking

Ordinary retrieval needs no model provider. Optional Jev ranking is disabled by
default. Enable it only on explicit user request with `assistance.set` and
`data:{enabled:true}`. Supply the Jev API key through the supported credential
configuration; never put keys in project files or chat. `assistance.status`
reports configuration without revealing secrets.

Ranking can send the question and selected knowledge claims to Jev. Local Evidence
is excluded from that projection. Server-side ranking additionally requires
`JEV_ENABLED=true` and community `policy.assistance:true`. If assistance is missing,
slow or unavailable, ordinary retrieval remains available with a notice.

Jev does not manage uploads, identity, permissions, review queues, deduplication or
revision history. Scores express relevance, not proof of truth. A fingerprint
check prevents a changed document from being ranked against an earlier search hit.

When a query contains unresolved community variants, assistance keeps the ordinary
group ordering. It does not send an arbitrary variant for ranking or decide which
community is authoritative. See [multi-community retrieval](retrieval.md).

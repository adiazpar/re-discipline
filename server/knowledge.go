package server

import (
	"context"
	"encoding/json"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

func (s *Server) knowledgeInventory(ctx context.Context, tx pgx.Tx, l kb.Library, body json.RawMessage) (any, error) {
	var p struct {
		IDs      []string `json:"ids"`
		After    string   `json:"after"`
		Sequence *int64   `json:"sequence"`
	}
	if err := data(body, &p); err != nil {
		return nil, err
	}
	if len(p.IDs) > 500 {
		return nil, fail(400, "inventory batches contain at most 500 IDs")
	}
	if p.Sequence != nil && *p.Sequence != l.Sequence {
		return nil, fail(409, "inventory changed; restart reconciliation")
	}
	for _, id := range p.IDs {
		if err := validUUID(id); err != nil {
			return nil, err
		}
	}
	if p.After != "" {
		if err := validUUID(p.After); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text,source_path,fingerprint,document_bytes,generation FROM community.documents WHERE community_id=$1 AND ($2='' OR id>NULLIF($2,'')::uuid) AND (COALESCE(cardinality($3::uuid[]),0)=0 OR id=ANY($3::uuid[])) ORDER BY id LIMIT 501`, l.ID, p.After, p.IDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := kb.KnowledgePage{Items: []kb.KnowledgeEntry{}, Sequence: l.Sequence}
	for rows.Next() {
		var v kb.KnowledgeEntry
		if err = rows.Scan(&v.ID, &v.Path, &v.Fingerprint, &v.Bytes, &v.Sequence); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	if len(out.Items) > 500 {
		out.Items = out.Items[:500]
		out.Next = out.Items[499].ID
	}
	return out, rows.Err()
}

func (s *Server) putKnowledge(ctx context.Context, tx pgx.Tx, l kb.Library, body json.RawMessage) (any, error) {
	var p kb.KnowledgeBatch
	if err := data(body, &p); err != nil {
		return nil, err
	}
	if len(body) > kb.MaxUploadBytes || len(p.Documents) == 0 || len(p.Documents) > kb.MaxUploadDocuments {
		return nil, fail(413, "upload batch exceeds negotiated bounds")
	}
	if err := validUUID(p.Key); err != nil {
		return nil, err
	}
	issued := time.Unix(p.CreatedAt, 0)
	if time.Since(issued) > 24*time.Hour || time.Until(issued) > 5*time.Minute {
		return nil, fail(409, "upload attempt expired; recheck current local files")
	}
	seen := map[string]bool{}
	for _, d := range p.Documents {
		if seen[d.ID] {
			return nil, fail(400, "duplicate document ID in upload batch")
		}
		seen[d.ID] = true
		if err := kb.ValidateKnowledge(d); err != nil {
			return nil, fail(400, err.Error())
		}
	}
	// Serialize current-state replacements and freeze/delete with a short batch
	// transaction. No remote calls, indexing, or full-corpus scans occur here.
	var sequence, used, count int64
	if err := tx.QueryRow(ctx, `SELECT sequence,knowledge_bytes,knowledge_count FROM community.libraries WHERE id=$1 FOR UPDATE`, l.ID).Scan(&sequence, &used, &count); err != nil {
		return nil, err
	}
	digest := ticketHash(string(body))
	var stored string
	var result []byte
	err := tx.QueryRow(ctx, `SELECT request_digest,result FROM community.upload_receipts WHERE community_id=$1 AND key=$2`, l.ID, p.Key).Scan(&stored, &result)
	if err == nil {
		if stored != digest {
			return nil, fail(409, "upload retry key belongs to different content")
		}
		return json.RawMessage(result), nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	out := kb.KnowledgeReceipt{CommunityID: l.ID, Key: p.Key, Items: []kb.KnowledgeEntry{}, Sequence: sequence}
	for _, d := range p.Documents {
		var oldHash, oldPath string
		var oldSize, oldSequence int64
		err = tx.QueryRow(ctx, `SELECT fingerprint,source_path,document_bytes,generation FROM community.documents WHERE community_id=$1 AND id=$2`, l.ID, d.ID).Scan(&oldHash, &oldPath, &oldSize, &oldSequence)
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
		if oldHash == d.Fingerprint && oldPath == d.Path {
			out.Unchanged++
			out.Items = append(out.Items, kb.KnowledgeEntry{ID: d.ID, Path: d.Path, Fingerprint: d.Fingerprint, Bytes: oldSize, Sequence: oldSequence})
			continue
		}
		used += int64(len(d.Markdown)) - oldSize
		if oldHash == "" {
			count++
		}
		// Storage limits are operator-configured capacity, never daily publishing quotas.
		if used > int64(setting("MAX_COMMUNITY_BYTES", 100<<30)) || count > int64(setting("MAX_COMMUNITY_FINDINGS", 1000000)) {
			return nil, fail(507, "community storage capacity reached")
		}
		out.Sequence = sequence + 1
		_, err = tx.Exec(ctx, `INSERT INTO community.documents(community_id,id,source_path,markdown,fingerprint,generation) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(community_id,id) DO UPDATE SET source_path=EXCLUDED.source_path,markdown=EXCLUDED.markdown,fingerprint=EXCLUDED.fingerprint,generation=EXCLUDED.generation,updated_at=now()`, l.ID, d.ID, d.Path, d.Markdown, d.Fingerprint, out.Sequence)
		if err != nil {
			return nil, err
		}
		out.Updated++
		out.Items = append(out.Items, kb.KnowledgeEntry{ID: d.ID, Path: d.Path, Fingerprint: d.Fingerprint, Bytes: int64(len(d.Markdown)), Sequence: out.Sequence})
	}
	if out.Updated > 0 {
		if _, err = tx.Exec(ctx, `UPDATE community.libraries SET sequence=$2,knowledge_bytes=$3,knowledge_count=$4 WHERE id=$1`, l.ID, out.Sequence, used, count); err != nil {
			return nil, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO community.upload_receipts(community_id,key,request_digest,result) VALUES($1,$2,$3,$4)`, l.ID, p.Key, digest, raw(out))
	return out, err
}

func (s *Server) deleteKnowledge(ctx context.Context, tx pgx.Tx, l kb.Library, body json.RawMessage) (any, error) {
	var p struct {
		IDs []string `json:"ids"`
	}
	if err := data(body, &p); err != nil {
		return nil, err
	}
	if len(p.IDs) == 0 || len(p.IDs) > 500 {
		return nil, fail(400, "select 1 to 500 document IDs")
	}
	for _, id := range p.IDs {
		if err := validUUID(id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM community.libraries WHERE id=$1 FOR UPDATE`, l.ID); err != nil {
		return nil, err
	}
	var n, bytes int64
	err := tx.QueryRow(ctx, `WITH removed AS (DELETE FROM community.documents WHERE community_id=$1 AND id=ANY($2::uuid[]) RETURNING document_bytes) SELECT count(*),COALESCE(sum(document_bytes),0) FROM removed`, l.ID, p.IDs).Scan(&n, &bytes)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		_, err = tx.Exec(ctx, `UPDATE community.libraries SET knowledge_bytes=knowledge_bytes-$2,knowledge_count=knowledge_count-$3,sequence=sequence+1,reset_sequence=sequence+1 WHERE id=$1`, l.ID, bytes, n)
	}
	return map[string]any{"deleted": n}, err
}

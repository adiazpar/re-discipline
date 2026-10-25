package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// LookupCopies reads metadata for selected identities even when the current
// version no longer matches a query. Never reads source files or the full index.
func LookupCopies(ctx context.Context, root string, ids []string) ([]Hit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 256 {
		return nil, fmt.Errorf("too many copy identities")
	}
	db, err := ReadRemoteIndex(root)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT path,finding_id,fingerprint,status,kind,created_at,knowledge_updated_at FROM docmeta WHERE finding_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Path, &h.FindingID, &h.Fingerprint, &h.Status, &h.Kind, &h.CreatedAt, &h.KnowledgeUpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ReadRemoteCopyStates intentionally omits document bodies and source paths.
func ReadRemoteCopyStates(ctx context.Context, db *sql.DB, ids []string) ([]RemoteRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 128 {
		return nil, fmt.Errorf("too many copy identities")
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT id,fingerprint,generation FROM remote_records WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemoteRecord
	for rows.Next() {
		var r RemoteRecord
		if err := rows.Scan(&r.ID, &r.Fingerprint, &r.Generation); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

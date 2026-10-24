package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

// Each item has its own transaction. Losing the HTTP response or cancelling a
// batch cannot undo committed items; retrying their keys returns the same work.
func (s *Server) submitBatch(ctx context.Context, user, c string, b json.RawMessage) (any, error) {
	var p struct {
		Items       []kb.BatchItem `json:"items"`
		ImportGrant string         `json:"import_grant"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if len(p.Items) < 1 || len(p.Items) > 50 {
		return nil, fail(400, "a batch contains 1 to 50 documents")
	}
	if p.ImportGrant != "" {
		if err := validUUID(p.ImportGrant); err != nil {
			return nil, err
		}
	}
	if p.ImportGrant == "" {
		grant, err := s.publicationAllowance(ctx, user, c, p.Items)
		if err != nil {
			return nil, err
		}
		p.ImportGrant = grant
	}
	out := []kb.BatchOutcome{}
	for _, item := range p.Items {
		result := kb.BatchOutcome{Key: item.Key}
		tx, err := s.begin(ctx, user)
		if err != nil {
			return nil, err
		}
		l, e := library(ctx, tx, c)
		var v any
		if e == nil {
			v, e = s.submit(ctx, tx, user, l, raw(map[string]any{"idempotency_key": item.Key, "document": item.Document, "import_grant": p.ImportGrant}))
		}
		if e == nil {
			e = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if e != nil {
			var ae *apiError
			result.Status = 500
			result.Error = "Publication failed; retry this item."
			if errors.As(e, &ae) {
				result.Status = ae.Status
				result.Error = ae.Message
			}
			if errors.Is(e, pgx.ErrNoRows) {
				result.Status = 404
				result.Error = "Resource unavailable"
			}
		} else {
			sub := v.(kb.Submission)
			result.Submission = &sub
		}
		out = append(out, result)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return map[string]any{"items": out}, nil
}

// Selected publication can use an existing personal allowance. Trusted owners
// already have authority to create bounded allowances; handle that internally.
func (s *Server) publicationAllowance(ctx context.Context, user, c string, items []kb.BatchItem) (string, error) {
	tx, err := s.begin(ctx, user)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	l, err := library(ctx, tx, c)
	if err != nil {
		return "", err
	}
	if l.Policy.Mode != "trusted" || l.Role == "reader" || l.Role == "contributor" || l.Role == "" {
		return "", nil
	}
	if _, err = tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return "", err
	}
	l, err = library(ctx, tx, l.ID)
	if err != nil {
		return "", err
	}
	if l.Policy.Mode != "trusted" || (l.Role != "owner" && l.Role != "maintainer" && l.Role != "publisher") {
		return "", nil
	}
	var bytes int64
	for _, item := range items {
		bytes += int64(len(raw(item.Document))) * 2
	}
	if len(items) > s.limits().Findings || bytes > s.limits().Bytes {
		return "", nil
	}
	var grant string
	err = tx.QueryRow(ctx, `SELECT id::text FROM community.import_grants WHERE community_id=$1 AND user_id=$2 AND NOT revoked AND expires_at>now() AND remaining_count>0 AND ($5 OR (remaining_count>=$3 AND remaining_bytes>=$4)) ORDER BY created_at DESC LIMIT 1`, l.ID, user, len(items), bytes, l.Role == "owner").Scan(&grant)
	if err != nil && err != pgx.ErrNoRows {
		return "", err
	}
	if l.Role == "owner" {
		if grant == "" {
			value, e := s.importGrant(ctx, tx, user, l, "import.create", raw(map[string]any{"count": len(items), "bytes": bytes, "hours": 6}))
			if e != nil {
				return "", e
			}
			grant = value.(map[string]any)["id"].(string)
		} else {
			if _, err = tx.Exec(ctx, `UPDATE community.import_grants SET remaining_count=GREATEST(remaining_count,$1),remaining_bytes=GREATEST(remaining_bytes,$2) WHERE id=$3`, len(items), bytes, grant); err != nil {
				return "", err
			}
			if err = audit(ctx, tx, l.ID, user, "publication.allowance", map[string]any{"id": grant, "count": len(items), "bytes": bytes}); err != nil {
				return "", err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return grant, nil
}

func (s *Server) importGrant(ctx context.Context, tx pgx.Tx, user string, l kb.Library, action string, b json.RawMessage) (any, error) {
	if action == "import.list" {
		if err := role(ctx, tx, l.ID, user, "owner", "maintainer", "publisher"); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,user_id::text,remaining_count,remaining_bytes,expires_at,revoked FROM community.import_grants WHERE community_id=$1 ORDER BY created_at DESC LIMIT 100`, l.ID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, u string
			var count int
			var bytes int64
			var expires time.Time
			var revoked bool
			if err = rows.Scan(&id, &u, &count, &bytes, &expires, &revoked); err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"id": id, "user_id": u, "remaining_count": count, "remaining_bytes": bytes, "expires_at": expires, "revoked": revoked})
		}
		return out, rows.Err()
	}
	if err := role(ctx, tx, l.ID, user, "owner"); err != nil {
		return nil, err
	}
	if action == "import.revoke" {
		var p struct {
			ID string `json:"id"`
		}
		if err := data(b, &p); err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM community.import_grants WHERE id=$1 AND community_id=$2`, p.ID, l.ID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, fail(404, "import allowance unavailable")
		}
		return map[string]bool{"revoked": true}, audit(ctx, tx, l.ID, user, action, p)
	}
	var p struct {
		UserID string `json:"user_id"`
		Count  int    `json:"count"`
		Bytes  int64  `json:"bytes"`
		Hours  int    `json:"hours"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		p.UserID = user
	}
	if err := validUUID(p.UserID); err != nil {
		return nil, err
	}
	if p.Count < 1 || p.Count > s.limits().Findings || p.Bytes < 1 || p.Bytes > s.limits().Bytes || p.Hours < 1 || p.Hours > 24 {
		return nil, fail(400, "bound the allowance by community count and storage limits, expiring in 1 to 24 hours")
	}
	if _, err := tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return nil, err
	}
	l, err := library(ctx, tx, l.ID)
	if err != nil {
		return nil, err
	}
	if l.Policy.Mode != "trusted" {
		return nil, fail(409, "bulk import allowances require trusted publishing; review policy is never bypassed")
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.members WHERE community_id=$1 AND user_id=$2 AND role IN ('owner','maintainer','publisher'))`, l.ID, p.UserID).Scan(&eligible); err != nil {
		return nil, err
	}
	if !eligible {
		return nil, fail(403, "select a trusted publisher in this community")
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.import_grants WHERE community_id=$1 AND user_id=$2 AND NOT revoked AND expires_at>now() AND remaining_count>0)`, l.ID, p.UserID).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, fail(409, "an active import allowance already exists for this publisher")
	}
	g := id()
	expires := time.Now().UTC().Add(time.Duration(p.Hours) * time.Hour)
	_, err = tx.Exec(ctx, `INSERT INTO community.import_grants(id,community_id,user_id,created_by,expires_at,remaining_count,remaining_bytes) VALUES($1,$2,$3,$4,$5,$6,$7)`, g, l.ID, p.UserID, user, expires, p.Count, p.Bytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": g, "user_id": p.UserID, "remaining_count": p.Count, "remaining_bytes": p.Bytes, "expires_at": expires}, audit(ctx, tx, l.ID, user, action, map[string]any{"id": g, "allowance": p})
}

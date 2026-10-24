package server

import (
	"context"
	"encoding/json"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

// Consolidation retains IDs, provenance and histories. Only exact, current,
// active payloads qualify for automatic grouping; semantic matches use review.
func (s *Server) consolidate(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer"); err != nil {
		return nil, err
	}
	var p struct {
		Apply bool `json:"apply"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT f.id::text,f.revision::text,r.content_digest FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND NOT f.deleted AND r.status='active' AND r.content_digest<>'' ORDER BY r.sequence`, l.ID)
	if err != nil {
		return nil, err
	}
	type entry struct{ ID, Revision, Digest string }
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.ID, &e.Revision, &e.Digest); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]entry{}
	pairs := []map[string]string{}
	skipped := []string{}
	for _, e := range entries {
		target, exists := seen[e.Digest]
		if !exists {
			seen[e.Digest] = e
			continue
		}
		pair := map[string]string{"source_id": e.ID, "source_revision": e.Revision, "target_id": target.ID, "target_revision": target.Revision, "kind": "equivalent", "rationale": "Server verified identical claim text, applicability, grade, and evidence; source provenance and histories retained."}
		if len(pairs) >= 200 {
			break
		}
		var recorded bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.finding_relations WHERE community_id=$1 AND ((source_id::text=$2 AND target_id::text=$3 AND source_revision::text=$4 AND target_revision::text=$5) OR (kind='equivalent' AND (source_id::text=$3 OR target_id::text=$2 OR (source_id::text=$2 AND target_id::text<>$3)))))`, l.ID, e.ID, target.ID, e.Revision, target.Revision).Scan(&recorded); err != nil {
			return nil, err
		}
		if recorded {
			skipped = append(skipped, e.ID)
			continue
		}
		if p.Apply {
			if _, err = s.relations(ctx, tx, user, l, "finding.relate", raw(pair)); err != nil {
				return nil, err
			}
		}
		pairs = append(pairs, pair)
	}
	return map[string]any{"pairs": pairs, "applied": p.Apply, "skipped": skipped, "limit": 200, "more_possible": len(pairs) == 200}, nil
}

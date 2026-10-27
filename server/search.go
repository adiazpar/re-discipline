package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
)

type searchRequest struct {
	CompareIDs []string `json:"compare_ids,omitempty"`
	Query      string   `json:"query"`
	Limit      int      `json:"limit"`
	Kind       string   `json:"kind"`
	Grade      string   `json:"grade"`
	Assistance *bool    `json:"assistance"`
}

func parseSearch(b json.RawMessage) (searchRequest, error) {
	var p searchRequest
	if len(b) > 8192 {
		return p, fail(400, "search request is too large")
	}
	if err := data(b, &p); err != nil {
		return p, err
	}
	if strings.TrimSpace(p.Query) == "" || len(p.Query) > 1000 {
		return p, fail(400, "query must contain 1 to 1000 characters")
	}
	if len(p.CompareIDs) > 128 {
		return p, fail(400, "at most 128 comparison identities")
	}
	for _, id := range p.CompareIDs {
		if err := validUUID(id); err != nil {
			return p, err
		}
	}
	if p.Limit <= 0 {
		p.Limit = 8
	}
	p.Limit = min(50, p.Limit)
	return p, nil
}
func (s *Server) query(ctx context.Context, tx pgx.Tx, l kb.Library, b json.RawMessage) (any, error) {
	p, err := parseSearch(b)
	if err != nil {
		return nil, err
	}
	root := s.indexRoot(l.ID)
	release, err := s.lockIndex(ctx, l.ID, false)
	if err != nil {
		return nil, err
	}
	defer release()
	db, err := engine.ReadRemoteIndex(root)
	if err != nil {
		return nil, fail(503, "knowledge index is warming; retry shortly")
	}
	defer db.Close()
	var indexValue string
	if err = db.QueryRowContext(ctx, `SELECT value FROM remote_progress WHERE key='sequence'`).Scan(&indexValue); err != nil {
		requestIndexRepair(ctx, root, err)
		return nil, fail(503, "knowledge index is warming; retry shortly")
	}
	indexed, _ := strconv.ParseInt(indexValue, 10, 64)

	var reset int64
	if err = tx.QueryRow(ctx, `SELECT reset_sequence FROM community.libraries WHERE id=$1`, l.ID).Scan(&reset); err != nil {
		return nil, err
	}
	if indexed < reset {
		return nil, fail(503, "knowledge index is warming; retry shortly")
	}
	hits, warnings, err := engine.QuerySnapshotContext(ctx, root, p.Query, engine.Options{Limit: p.Limit, Kind: p.Kind, Grade: p.Grade})
	if err != nil {
		requestIndexRepair(ctx, root, err)
		return nil, fail(503, "knowledge index is rebuilding; retry shortly")
	}
	ids := []string{}
	for _, h := range hits {
		ids = append(ids, strings.TrimSuffix(filepath.Base(h.Path), ".md"))
	}
	// Ordinary search needs indexed metadata, not full Markdown or semantic
	// projections. The community read lock keeps both reads on the same version.
	assist := s.Semantic != nil && l.Policy.Assistance && (p.Assistance == nil || *p.Assistance)
	var records map[string]engine.RemoteRecord
	if assist {
		records, err = engine.ReadRemoteRecords(ctx, db, ids)
	} else {
		var states []engine.RemoteRecord
		states, err = engine.ReadRemoteCopyStates(ctx, db, ids)
		records = make(map[string]engine.RemoteRecord, len(states))
		for _, r := range states {
			records[r.ID] = r
		}
	}
	if err != nil {
		return nil, err
	}
	out := []kb.Result{}
	claims := []semantic.Claim{}
	namespaces := []string{}
	for _, h := range hits {
		fid := strings.TrimSuffix(filepath.Base(h.Path), ".md")
		r, ok := records[fid]
		if !ok {
			return nil, fmt.Errorf("search result has no indexed record")
		}
		out = append(out, kb.Result{FindingID: fid, Fingerprint: r.Fingerprint, CommunityID: l.ID, Build: h.Build, Status: "active", Title: h.Title, Snippet: h.Snippet, Grade: h.Grade, Kind: h.Kind, TextDigest: h.TextDigest, Sequence: r.Generation, CreatedAt: h.CreatedAt, KnowledgeUpdatedAt: h.KnowledgeUpdatedAt})
		if assist {
			claims = append(claims, kb.ProjectClaim(kb.Document{Markdown: r.Markdown, SourcePath: r.Path, Build: h.Build, Kind: h.Kind, Grade: h.Grade}, "active"))
			namespaces = append(namespaces, l.ID+"/search")
		}
	}
	comparison, err := engine.ReadRemoteCopyStates(ctx, db, p.CompareIDs)
	if err != nil {
		return nil, err
	}
	compared := []kb.KnowledgeEntry{}
	for _, r := range comparison {
		compared = append(compared, kb.KnowledgeEntry{ID: r.ID, Fingerprint: r.Fingerprint, Sequence: r.Generation})
	}
	release()
	release = func() {}
	status := kb.AssistanceStatus{Mode: "disabled"}
	if assist {
		out, status = kb.RankClaims(ctx, s.Semantic, p.Query, out, claims, namespaces, false)
		var allowed bool
		if err = tx.QueryRow(ctx, `SELECT community.can_read($1) AND deletion_id IS NULL FROM community.libraries WHERE id=$1`, l.ID).Scan(&allowed); err != nil {
			return nil, err
		}
		if !allowed {
			return nil, fail(404, "resource unavailable")
		}
	}
	return map[string]any{"hits": out, "compared": compared, "comparison_complete": true, "indexed_sequence": indexed, "stored_sequence": l.Sequence, "warnings": warnings, "assistance": status}, nil
}

func requestIndexRepair(ctx context.Context, root string, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	message := strings.ToLower(err.Error())
	for _, cause := range []string{"malformed", "not a database", "no such table"} {
		if strings.Contains(message, cause) {
			_ = os.WriteFile(filepath.Join(root, "rebuild.request"), nil, 0600)
			return
		}
	}
}

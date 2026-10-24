package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
)

type searchRequest struct {
	Query      string `json:"query"`
	Limit      int    `json:"limit"`
	Kind       string `json:"kind"`
	Grade      string `json:"grade"`
	Assistance *bool  `json:"assistance"`
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
	limit := p.Limit
	assisted := s.Semantic != nil && l.Policy.Assistance && (p.Assistance == nil || *p.Assistance)
	if assisted && p.Limit < 32 {
		p.Limit = 32
	}
	// Readers share an immutable snapshot; only rebuilding/deletion takes the
	// community's exclusive lock. Different communities never share an index lock.
	l, root, meta, release, err := s.snapshot(ctx, tx, l, false)
	if err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	assisted = s.Semantic != nil && l.Policy.Assistance && (p.Assistance == nil || *p.Assistance)
	var inventory int64
	if err = tx.QueryRow(ctx, `SELECT inventory_generation FROM community.libraries WHERE id=$1`, l.ID).Scan(&inventory); err != nil {
		return nil, err
	}
	related, err := s.relations(ctx, tx, "", l, "finding.relations", raw(map[string]any{}))
	if err != nil {
		return nil, err
	}
	canonical := map[string]string{}
	conflicts := map[string][]string{}
	for _, r := range related.([]kb.Relation) {
		if r.Kind == "conflicting" && r.Current {
			conflicts[r.SourceID] = append(conflicts[r.SourceID], "Conflicting evidence: "+r.TargetID+". "+r.Rationale)
			conflicts[r.TargetID] = append(conflicts[r.TargetID], "Conflicting evidence: "+r.SourceID+". "+r.Rationale)
		}
		if r.Kind == "equivalent" && r.Current && meta[r.SourceID] == r.SourceRevision && meta[r.TargetID] == r.TargetRevision {
			canonical[r.SourceID] = r.TargetID
		}
	}
	opts := engine.Options{Limit: p.Limit + len(canonical), Kind: p.Kind, Grade: p.Grade}
	// This directory is a complete, immutable sequence snapshot. Rescanning every
	// exported document for every reader cannot discover a newer server revision.
	hits, warnings, err := engine.QuerySnapshotContext(ctx, root, p.Query, opts)
	if err != nil && ctx.Err() == nil {
		release()
		locked = false
		l, root, meta, release, err = s.snapshot(ctx, tx, l, true)
		if err != nil {
			return nil, err
		}
		locked = true
		hits, warnings, err = engine.QuerySnapshotContext(ctx, root, p.Query, opts)
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, h := range hits {
		fid := strings.TrimSuffix(filepath.Base(h.Path), ".md")
		ids = append(ids, fid)
		if canonical[fid] != "" {
			ids = append(ids, canonical[fid])
		}
	}
	records, err := queryRecords(ctx, tx, l.ID, meta, ids)
	if err != nil {
		return nil, err
	}
	out := []kb.Result{}
	seen := map[string]bool{}
	documents := map[string]kb.Document{}
	for _, h := range hits {
		fid := strings.TrimSuffix(filepath.Base(h.Path), ".md")
		seen[fid] = true
		record, ok := records[fid]
		if !ok {
			return nil, pgx.ErrNoRows
		}
		textDigest, status, reason, doc := record.digest, record.status, record.reason, record.doc
		documents[fid] = doc
		alerts := append([]string{}, conflicts[fid]...)
		linked := record.alerts
		alerts = append(alerts, linked...)
		if len(linked) > 0 && status == "active" {
			status = "disputed"
		}
		if status == "disputed" {
			alerts = append(alerts, "Disputed finding: "+reason)
		}
		out = append(out, kb.Result{Build: doc.Build, TextDigest: textDigest, Status: status, Warnings: alerts, FindingID: fid, Revision: meta[fid], CommunityID: l.ID, Title: h.Title, Snippet: h.Snippet, Grade: h.Grade, Kind: h.Kind, CanonicalID: canonical[fid], URL: fmt.Sprintf("%s/communities/%s/findings/%s", strings.TrimRight(s.Config.PublicURL, "/"), l.ID, fid), Locations: []kb.Location{{Service: s.Config.PublicURL, CommunityID: l.ID, FindingID: fid, Revision: meta[fid], CanonicalID: canonical[fid]}}})
	}
	// A matching contribution still opens the canonical claim even when the
	// canonical wording did not match the query. Keep the matching snippet below it.
	for _, hit := range append([]kb.Result{}, out...) {
		fid := hit.CanonicalID
		if fid == "" || seen[fid] {
			continue
		}
		seen[fid] = true
		record, ok := records[fid]
		if !ok {
			return nil, pgx.ErrNoRows
		}
		digest, status, reason, d := record.digest, record.status, record.reason, record.doc
		documents[fid] = d
		alerts := append([]string{}, conflicts[fid]...)
		linked := record.alerts
		alerts = append(alerts, linked...)
		if len(linked) > 0 && status == "active" {
			status = "disputed"
		}
		if status == "disputed" {
			alerts = append(alerts, "Disputed finding: "+reason)
		}
		parsed := engine.Parse(d.SourcePath, d.Markdown)
		snippet := []rune(parsed.Body)
		if len(snippet) > 600 {
			snippet = snippet[:600]
		}
		out = append(out, kb.Result{FindingID: fid, Revision: meta[fid], CommunityID: l.ID, Build: d.Build, Status: status, Warnings: alerts, TextDigest: digest, Title: parsed.Title, Snippet: string(snippet), Grade: d.Grade, Kind: d.Kind, URL: fmt.Sprintf("%s/communities/%s/findings/%s", strings.TrimRight(s.Config.PublicURL, "/"), l.ID, fid), Locations: []kb.Location{{Service: s.Config.PublicURL, CommunityID: l.ID, FindingID: fid, Revision: meta[fid]}}})
	}
	out = kb.GroupContributions(out)
	release()
	locked = false
	status := kb.AssistanceStatus{Mode: "disabled"}
	if assisted && len(out) > 0 {
		currentPolicy, e := library(ctx, tx, l.ID)
		if e != nil {
			return nil, e
		}
		if currentPolicy.PolicyVersion != l.PolicyVersion {
			return nil, fail(409, "community policy changed; retry the query")
		}
		if s.Semantic.Key() == "" {
			status.Mode = "fallback"
			status.Reason = "Optional assistance is unavailable; baseline retained."
		} else {
			claims := make([]semantic.Claim, len(out))
			namespaces := make([]string, len(out))
			for i, h := range out {
				claims[i] = kb.ProjectClaim(documents[h.FindingID], h.Status)
				namespaces[i] = l.ID + "/search"
			}
			out, status = kb.RankClaims(ctx, s.Semantic, p.Query, out, claims, namespaces, false)
			// Recheck authorization, policy and revisions after inference, without
			// holding the index mutex or database row locks across network work.
			latest, e := library(ctx, tx, l.ID)
			if e != nil {
				return nil, e
			}
			if latest.PolicyVersion != l.PolicyVersion {
				return nil, fail(409, "community policy changed; retry the query")
			}
			var currentInventory int64
			if e = tx.QueryRow(ctx, `SELECT inventory_generation FROM community.libraries WHERE id=$1`, l.ID).Scan(&currentInventory); e != nil {
				return nil, e
			}
			if currentInventory != inventory || latest.Sequence != l.Sequence {
				return nil, fail(409, "community claims or relationships changed; retry the query")
			}
			for _, hit := range out {
				var current string
				if e = tx.QueryRow(ctx, `SELECT revision::text FROM community.findings WHERE community_id=$1 AND id=$2 AND NOT deleted`, l.ID, hit.FindingID).Scan(&current); e != nil || current != hit.Revision {
					return nil, fail(409, "a claim changed; retry the query")
				}
			}
		}
	}
	if status.Mode == "fallback" {
		warnings = append(warnings, status.Reason)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return map[string]any{"hits": out, "indexed_sequence": l.Sequence, "warnings": warnings, "assistance": status}, nil
}

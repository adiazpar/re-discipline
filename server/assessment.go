package server

import (
	"context"
	"encoding/json"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
	"strings"
)

func (s *Server) assessFinding(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer"); err != nil {
		return nil, err
	}
	var p struct {
		ID            string        `json:"id"`
		Revision      string        `json:"revision"`
		Status        string        `json:"status"`
		Reason        string        `json:"reason"`
		ReplacementID string        `json:"replacement_id"`
		Evidence      []kb.Evidence `json:"evidence"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if p.Status != "active" && p.Status != "disputed" && p.Status != "refuted" && p.Status != "superseded" {
		return nil, fail(400, "status must be active, disputed, refuted, or superseded")
	}
	if strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 8000 || len(raw(p.Evidence)) > 32768 {
		return nil, fail(400, "provide a rationale; optional historical evidence is limited to 32 KiB")
	}
	for _, e := range p.Evidence {
		if strings.TrimSpace(e.Label) == "" || (e.URL == "" && strings.TrimSpace(e.Excerpt) == "") {
			return nil, fail(400, "assessment evidence needs a label and URL or excerpt")
		}
	}
	if p.Status == "superseded" && p.ReplacementID == "" {
		return nil, fail(400, "supersession requires a replacement finding")
	}
	if p.ReplacementID != "" && p.Status != "superseded" {
		return nil, fail(400, "replacement applies only to supersession")
	}
	if p.ReplacementID == p.ID {
		return nil, fail(400, "a finding cannot replace itself")
	}
	var libraries int
	var total int64
	if err := tx.QueryRow(ctx, `SELECT libraries,bytes FROM community.service_capacity()`).Scan(&libraries, &total); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return nil, err
	}
	if err := s.consume(ctx, tx, user, "assessment", 200); err != nil {
		return nil, err
	}
	var current string
	var doc []byte
	if err := tx.QueryRow(ctx, `SELECT f.revision::text,r.document FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND f.id=$2 AND NOT f.deleted`, l.ID, p.ID).Scan(&current, &doc); err != nil {
		return nil, err
	}
	if current != p.Revision {
		return nil, fail(409, "finding changed; assess the current revision")
	}
	if total+int64(len(doc)+len(raw(p.Evidence))) > int64(setting("MAX_TOTAL_CONTENT_BYTES", 200*1024*1024)) {
		return nil, fail(429, "service content capacity reached")
	}
	var used int64
	var findingCount int
	if err := tx.QueryRow(ctx, `SELECT bytes,findings FROM community.publication_usage($1)`, l.ID).Scan(&used, &findingCount); err != nil {
		return nil, err
	}
	if used+int64(len(doc)+len(raw(p.Evidence))) > s.limits().Bytes {
		return nil, fail(429, "community storage limit reached")
	}
	if p.ReplacementID != "" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND f.id::text=$2 AND NOT f.deleted AND r.status='active')`, l.ID, p.ReplacementID).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, fail(409, "replacement must be an active finding in this community")
		}
	}
	var d kb.Document
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, err
	}
	// Validate supplied evidence with the same portability gate as publications.
	d.Evidence = p.Evidence
	d.Markdown += "\n" + p.Reason
	for _, check := range kb.Validate(d) {
		if check.Blocking {
			return nil, fail(422, check.Message)
		}
	}
	seq, err := nextSequence(ctx, tx, l.ID)
	if err != nil {
		return nil, err
	}
	rid := id()
	_, err = tx.Exec(ctx, `INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,author_id,content_digest,text_digest,claim_digest,status,reason,replacement_id,assessment_evidence) SELECT $1,community_id,finding_id,$2,document,$3,content_digest,text_digest,claim_digest,$4,$5,$6,$7 FROM community.revisions WHERE id=$8`, rid, seq, user, p.Status, p.Reason, p.ReplacementID, raw(p.Evidence), current)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE community.findings SET revision=$1 WHERE community_id=$2 AND id=$3`, rid, l.ID, p.ID); err != nil {
		return nil, err
	}
	if err = audit(ctx, tx, l.ID, user, "finding.assess", p); err != nil {
		return nil, err
	}
	return map[string]any{"finding_id": p.ID, "revision": rid, "sequence": seq, "status": p.Status}, nil
}

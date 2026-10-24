package server

import (
	"context"
	"encoding/json"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
	"strings"
)

type publicationIdentity struct {
	FindingID  string `json:"finding_id,omitempty"`
	Revision   string `json:"revision,omitempty"`
	State      string `json:"state"`
	Candidates any    `json:"candidates,omitempty"`
}

func (s *Server) resolvePublication(ctx context.Context, tx pgx.Tx, user string, l kb.Library, d kb.Document) (publicationIdentity, error) {
	out := publicationIdentity{State: "new"}
	if len(d.SourceNamespace) > 200 || strings.ContainsAny(d.SourceNamespace, "\r\n") {
		return out, fail(400, "invalid source namespace")
	}
	if d.FindingID != "" {
		var status string
		err := tx.QueryRow(ctx, `SELECT f.id::text,f.revision::text,r.status FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND f.id=$2 AND NOT f.deleted`, l.ID, d.FindingID).Scan(&out.FindingID, &out.Revision, &status)
		if err != nil {
			return out, err
		}
		out.State = "revision"
		var permitted bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.members WHERE community_id=$1 AND user_id=$2 AND role IN ('owner','maintainer')) OR EXISTS(SELECT 1 FROM community.revisions WHERE community_id=$1 AND finding_id::text=$3 AND author_id=$2 AND submission_id IS NOT NULL)`, l.ID, user, d.FindingID).Scan(&permitted); err != nil {
			return out, err
		}
		if !permitted {
			out.State = "identity_review"
		}
		if out.Revision != d.BaseRevision {
			out.State = "conflict"
		}
		if status != "active" {
			out.State = "assessment_review"
		}
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT f.id::text,f.revision::text FROM community.publication_sources p JOIN community.findings f ON f.community_id=p.community_id AND f.id=p.finding_id WHERE p.community_id=$1 AND p.author_id=$2 AND p.namespace=$3 AND p.source_path=$4 AND NOT f.deleted ORDER BY f.id::text LIMIT 2`, l.ID, user, d.SourceNamespace, d.SourcePath)
	if err != nil {
		return out, err
	}
	matches := []publicationIdentity{}
	for rows.Next() {
		v := publicationIdentity{}
		if err = rows.Scan(&v.FindingID, &v.Revision); err != nil {
			rows.Close()
			return out, err
		}
		matches = append(matches, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(matches) > 1 {
		out.State = "ambiguous"
		out.Candidates = matches
		return out, nil
	}
	if len(matches) == 1 {
		out = matches[0]
		out.State = "identity_review"
		var digest, claimDigest, status string
		if err = tx.QueryRow(ctx, `SELECT content_digest,claim_digest,status FROM community.revisions WHERE id=$1`, out.Revision).Scan(&digest, &claimDigest, &status); err != nil {
			return out, err
		}
		if status == "active" && (digest == kb.ContentDigest(d) || (semantic.KnownScope(d.Build) && claimDigest == kb.ClaimDigest(d))) {
			out.State = "exact_copy"
		}
		return out, nil
	}
	// A renamed exact copy, including an independent publisher's identical payload.
	err = tx.QueryRow(ctx, `SELECT f.id::text,f.revision::text FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND NOT f.deleted AND r.status='active' AND (r.content_digest=$2 OR ($4 AND r.claim_digest=$3)) ORDER BY r.sequence LIMIT 1`, l.ID, kb.ContentDigest(d), kb.ClaimDigest(d), semantic.KnownScope(d.Build)).Scan(&out.FindingID, &out.Revision)
	if err == nil {
		out.State = "exact_copy"
		if !semantic.KnownScope(d.Build) {
			out.State = "identity_review"
		}
		return out, nil
	}
	if err != pgx.ErrNoRows {
		return out, err
	}
	out.Candidates, err = s.candidates(ctx, tx, l, raw(map[string]any{"document": d}))
	if err != nil {
		return out, err
	}
	// Candidate matches are not a verdict. Every overlap remains reviewable.
	if len(raw(out.Candidates.(map[string]any)["candidates"])) > 2 {
		out.State = "similarity_review"
	}
	return out, nil
}

func (s *Server) publicationResolve(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer", "publisher", "contributor"); err != nil {
		return nil, err
	}
	var p struct {
		Document kb.Document `json:"document"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if len(p.Document.Markdown) > 262144 {
		return nil, fail(400, "document too large")
	}
	return s.resolvePublication(ctx, tx, user, l, p.Document)
}

func (s *Server) publicationReceipts(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer", "publisher", "contributor"); err != nil {
		return nil, err
	}
	var p struct {
		Keys []string `json:"keys"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if len(p.Keys) > 200 {
		return nil, fail(400, "select at most 200 publication keys")
	}
	for _, key := range p.Keys {
		if err := validUUID(key); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT idempotency_key::text,id::text FROM community.submissions WHERE community_id=$1 AND author_id=$2 AND idempotency_key::text=ANY($3) UNION ALL SELECT idempotency_key::text,submission_id::text FROM community.submission_aliases WHERE community_id=$1 AND author_id=$2 AND idempotency_key::text=ANY($3)`, l.ID, user, p.Keys)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for rows.Next() {
		var key, sid string
		if err = rows.Scan(&key, &sid); err != nil {
			rows.Close()
			return nil, err
		}
		ids[key] = sid
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]kb.Submission{}
	for key, sid := range ids {
		v, e := readSubmission(ctx, tx, l.ID, sid)
		if e != nil {
			return nil, e
		}
		v.Document = kb.Document{}
		out[key] = v
	}
	return out, nil
}

// Resolve a queued contribution explicitly without mutating its submitted payload
// or retry digest. The next review certifies the target and its captured revision.
func (s *Server) resolveSubmission(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer"); err != nil {
		return nil, err
	}
	var p struct {
		ID           string `json:"id"`
		Mode         string `json:"mode"`
		FindingID    string `json:"finding_id"`
		BaseRevision string `json:"base_revision"`
		Rationale    string `json:"rationale"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Rationale) == "" || len(p.Rationale) > 8000 {
		return nil, fail(400, "explain the identity decision")
	}
	if _, err := tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return nil, err
	}
	v, err := readSubmission(ctx, tx, l.ID, p.ID)
	if err != nil {
		return nil, err
	}
	if v.State == "accepted" || v.State == "rejected" {
		return nil, fail(409, "submission finalized")
	}
	if p.FindingID != "" {
		var current string
		if err = tx.QueryRow(ctx, `SELECT revision::text FROM community.findings WHERE community_id=$1 AND id=$2 AND NOT deleted`, l.ID, p.FindingID).Scan(&current); err != nil {
			return nil, err
		}
		if current != p.BaseRevision {
			return nil, fail(409, "finding changed; compare current revision")
		}
	} else if p.BaseRevision != "" {
		return nil, fail(400, "base revision requires finding")
	}
	decision := p
	contribution, contributionRevision := "", ""
	if p.Mode != "" && p.Mode != "create" && p.Mode != "revision" && p.Mode != "contribution" {
		return nil, fail(400, "mode must be create, revision, or contribution")
	}
	if p.Mode == "create" && p.FindingID != "" {
		return nil, fail(400, "create has no target finding")
	}
	if (p.Mode == "revision" || p.Mode == "contribution") && p.FindingID == "" {
		return nil, fail(400, "select the target finding")
	}
	if p.Mode == "contribution" {
		contribution, contributionRevision = p.FindingID, p.BaseRevision
		p.FindingID, p.BaseRevision = "", ""
	}
	_, err = tx.Exec(ctx, `UPDATE community.submissions SET finding_id=$1,base_revision=$2,identity_review=false,state='needs_review',checks=checks || $3::jsonb,contribution_target=$5,contribution_revision=$6,resolution_version=resolution_version+1 WHERE id=$4`, p.FindingID, p.BaseRevision, raw([]kb.Check{{Code: "identity_decision", Message: p.Rationale}}), v.ID, contribution, contributionRevision)
	if err != nil {
		return nil, err
	}
	if err = audit(ctx, tx, l.ID, user, "submission.resolve", decision); err != nil {
		return nil, err
	}
	// Revalidate the compared identities, so unrelated community activity does
	// not ask the reviewer to repeat an unchanged decision.
	snapshot, err := s.resolvePublication(ctx, tx, v.Author, l, v.Document)
	if err != nil {
		return nil, err
	}
	mode := p.Mode
	if mode == "" && p.FindingID != "" {
		mode = "revision"
	}
	if _, err = tx.Exec(ctx, `UPDATE community.submissions SET identity_generation=(SELECT inventory_generation FROM community.libraries WHERE id=$1),identity_kind=$3,identity_snapshot=$4 WHERE id=$2`, l.ID, v.ID, mode, semantic.Hash(snapshot)); err != nil {
		return nil, err
	}
	return readSubmission(ctx, tx, l.ID, v.ID)
}

func (s *Server) matchFindings(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	var p struct {
		Digests         []string `json:"digests"`
		SourcePaths     []string `json:"source_paths"`
		SourceNamespace string   `json:"source_namespace"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if len(p.Digests) > 50 || len(p.SourcePaths) > 50 || len(p.SourceNamespace) > 200 {
		return nil, fail(400, "match at most 50 hashes")
	}
	for _, h := range p.Digests {
		if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
			return nil, fail(400, "invalid text digest")
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (r.text_digest,f.id) r.text_digest,f.id::text,f.revision::text,r.id::text,head.status,head.reason,head.replacement_id,head.document->>'build',r.id=f.revision FROM community.revisions r JOIN community.findings f ON f.community_id=r.community_id AND f.id=r.finding_id JOIN community.revisions head ON head.id=f.revision WHERE r.community_id=$1 AND NOT r.deleted AND NOT f.deleted AND r.text_digest=ANY($2) ORDER BY r.text_digest,f.id,r.sequence DESC LIMIT 501`, l.ID, p.Digests)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []kb.FindingMatch{}
	for rows.Next() {
		var v kb.FindingMatch
		if err = rows.Scan(&v.TextDigest, &v.FindingID, &v.Revision, &v.MatchedRevision, &v.Status, &v.Reason, &v.ReplacementID, &v.Build, &v.Current); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if len(out) > 500 {
		return nil, fail(409, "too many matching histories; narrow the hash batch")
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(p.SourcePaths) > 0 {
		sources, e := tx.Query(ctx, `SELECT DISTINCT p.source_path,f.id::text,f.revision::text,head.text_digest,head.status,head.reason,head.replacement_id,head.document->>'build' FROM community.publication_sources p JOIN community.findings f ON f.community_id=p.community_id AND f.id=p.finding_id JOIN community.revisions head ON head.id=f.revision WHERE p.community_id=$1 AND p.namespace=$2 AND p.source_path=ANY($3) AND p.source_path<>'' AND NOT f.deleted AND (p.author_id::text=$4 OR NOT EXISTS(SELECT 1 FROM community.publication_sources own WHERE own.community_id=p.community_id AND own.namespace=p.namespace AND own.source_path=p.source_path AND own.author_id::text=$4)) LIMIT 501`, l.ID, p.SourceNamespace, p.SourcePaths, user)
		if e != nil {
			return nil, e
		}
		count := 0
		for sources.Next() {
			var v kb.FindingMatch
			if e = sources.Scan(&v.SourcePath, &v.FindingID, &v.Revision, &v.TextDigest, &v.Status, &v.Reason, &v.ReplacementID, &v.Build); e != nil {
				sources.Close()
				return nil, e
			}
			v.MatchKind = "source"
			v.Current = true
			out = append(out, v)
			count++
		}
		sources.Close()
		if e = sources.Err(); e != nil {
			return nil, e
		}
		if count > 500 {
			return nil, fail(409, "too many source mappings; narrow the path batch")
		}
	}
	for i := range out {
		out[i].Warnings, err = integrityAlerts(ctx, tx, l.ID, out[i].FindingID, out[i].Revision)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

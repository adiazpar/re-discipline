package server

import (
	"context"
	"encoding/json"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
)

func (s *Server) submit(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer", "publisher", "contributor"); err != nil {
		return nil, err
	}
	var p struct {
		ImportGrant string      `json:"import_grant"`
		Key         string      `json:"idempotency_key"`
		Document    kb.Document `json:"document"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if err := validUUID(p.Key); err != nil {
		return nil, err
	}
	if p.Document.FindingID != "" {
		if err := validUUID(p.Document.FindingID); err != nil {
			return nil, err
		}
		if err := validUUID(p.Document.BaseRevision); err != nil {
			return nil, err
		}
	} else if p.Document.BaseRevision != "" {
		return nil, fail(400, "base revision requires a finding ID")
	}
	if p.Document.Supersedes != "" {
		if err := validUUID(p.Document.Supersedes); err != nil {
			return nil, err
		}
	}
	digest := kb.Digest(p.Document)
	var total int
	var totalBytes int64
	if err := tx.QueryRow(ctx, `SELECT libraries,bytes FROM community.service_capacity()`).Scan(&total, &totalBytes); err != nil {
		return nil, err
	}
	// This lock also protects quota checks and policy snapshots from concurrent changes.
	if _, err := tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return nil, err
	}
	var deleted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.publication_deletions WHERE community_id=$1 AND author_id=$2 AND idempotency_key=$3)`, l.ID, user, p.Key).Scan(&deleted); err != nil {
		return nil, err
	}
	if deleted {
		return nil, fail(410, "this publication was deleted; discard its queued retry")
	}
	var existing, oldDigest string
	err := tx.QueryRow(ctx, `SELECT id::text,digest FROM community.submissions WHERE community_id=$1 AND author_id=$2 AND idempotency_key=$3 UNION ALL SELECT s.id::text,s.digest FROM community.submission_aliases a JOIN community.submissions s ON s.id=a.submission_id WHERE a.community_id=$1 AND a.author_id=$2 AND a.idempotency_key=$3 LIMIT 1`, l.ID, user, p.Key).Scan(&existing, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return nil, fail(409, "idempotency key was already used for different content")
		}
		return readSubmission(ctx, tx, l.ID, existing)
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	// Re-preparing identical content under a new key is still the same author's
	// work. Independent authors remain separate contributions for reconciliation.
	err = tx.QueryRow(ctx, `SELECT id::text FROM community.submissions WHERE community_id=$1 AND author_id=$2 AND digest=$3 AND state IN ('queued','needs_review','accepted') ORDER BY created_at DESC LIMIT 1`, l.ID, user, digest).Scan(&existing)
	if err == nil {
		if _, err = tx.Exec(ctx, `INSERT INTO community.submission_aliases(community_id,author_id,idempotency_key,submission_id) VALUES($1,$2,$3,$4)`, l.ID, user, p.Key, existing); err != nil {
			return nil, err
		}
		return readSubmission(ctx, tx, l.ID, existing)
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	if totalBytes+int64(len(raw(p.Document)))*2 > int64(setting("MAX_TOTAL_CONTENT_BYTES", 200*1024*1024)) {
		return nil, fail(429, "service content capacity reached")
	}
	checks := validate(p.Document)
	for _, c := range checks {
		if c.Blocking {
			return nil, fail(422, c.Code+": "+c.Message)
		}
	}
	identity, err := s.resolvePublication(ctx, tx, user, l, p.Document)
	if err != nil {
		return nil, err
	}
	if p.ImportGrant != "" {
		if err = validUUID(p.ImportGrant); err != nil {
			return nil, err
		}
		var consumed bool
		if err = tx.QueryRow(ctx, `SELECT community.consume_import($1,$2,$3)`, p.ImportGrant, l.ID, int64(len(raw(p.Document)))*2).Scan(&consumed); err != nil {
			return nil, err
		}
		if !consumed {
			return nil, fail(429, "import allowance expired, exhausted, revoked, or unavailable to this publisher")
		}
	} else if err = s.consume(ctx, tx, user, "submission", s.limits().Submissions); err != nil {
		return nil, err
	}
	var used int64
	var count int
	if err = tx.QueryRow(ctx, `SELECT bytes,findings FROM community.publication_usage($1)`, l.ID).Scan(&used, &count); err != nil {
		return nil, err
	}
	if used+int64(len(raw(p.Document)))*2 > s.limits().Bytes {
		return nil, fail(429, "community storage limit reached")
	}
	if identity.State == "new" && count >= s.limits().Findings {
		return nil, fail(429, "community finding limit reached")
	}
	l, err = library(ctx, tx, l.ID)
	if err != nil {
		return nil, err
	}
	identityReview := identity.State != "new" && identity.State != "revision" && identity.State != "exact_copy"
	if identityReview {
		checks = append(checks, kb.Check{Code: "identity_review", Message: "Server identity resolution: " + identity.State + ". Compare publication.resolve candidates and resolve before acceptance."})
	}
	sid := id()
	base := identity.Revision
	if p.Document.FindingID != "" {
		base = p.Document.BaseRevision
	}
	_, err = tx.Exec(ctx, `INSERT INTO community.submissions(id,community_id,author_id,idempotency_key,digest,document,policy_version,checks,finding_id,base_revision,identity_review,claim_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, sid, l.ID, user, p.Key, digest, raw(p.Document), l.PolicyVersion, raw(checks), identity.FindingID, base, identityReview, kb.ClaimDigest(p.Document))
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO community.jobs(id,community_id,submission_id) VALUES($1,$2,$3)`, id(), l.ID, sid)
	if err != nil {
		return nil, err
	}
	if err = audit(ctx, tx, l.ID, user, "submission.create", map[string]string{"submission_id": sid, "digest": digest}); err != nil {
		return nil, err
	}
	return readSubmission(ctx, tx, l.ID, sid)
}
func validate(d kb.Document) []kb.Check {
	return kb.Validate(d)
}
func (s *Server) review(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer"); err != nil {
		return nil, err
	}
	var p struct {
		ID                string          `json:"id"`
		ResolutionVersion int             `json:"resolution_version"`
		Digest            string          `json:"digest"`
		PolicyVersion     int             `json:"policy_version"`
		Decision          string          `json:"decision"`
		Rationale         string          `json:"rationale"`
		PacketToken       string          `json:"packet_token"`
		Plan              *resolutionPlan `json:"plan,omitempty"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if p.Decision != "accept" && p.Decision != "reject" && p.Decision != "request_changes" {
		return nil, fail(400, "decision must be accept, reject, or request_changes")
	}
	if p.Decision == "accept" && p.Rationale == "" {
		p.Rationale = "Accepted the displayed claim, applicability and publication effect."
	}
	if strings.TrimSpace(p.Rationale) == "" || len(p.Rationale) > 8000 {
		return nil, fail(400, "provide a rationale of 1 to 8000 characters")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM community.libraries WHERE id=$1 FOR UPDATE`, l.ID); err != nil {
		return nil, err
	}
	l, err := library(ctx, tx, l.ID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM community.submissions WHERE community_id=$1 AND id=$2 FOR UPDATE`, l.ID, p.ID); err != nil {
		return nil, err
	}
	v, err := readSubmission(ctx, tx, l.ID, p.ID)
	if err != nil {
		return nil, err
	}
	if v.State == "accepted" || v.State == "rejected" {
		return nil, fail(409, "submission is already finalized")
	}
	if p.Digest != v.Digest || p.PolicyVersion != l.PolicyVersion || p.ResolutionVersion != v.ResolutionVersion {
		return nil, fail(409, "content or community policy changed; review the current revision")
	}
	// In maintainer mode a second person must accept a contributor's work; owners can explicitly use trusted mode for solo publishing.
	if p.Decision == "accept" && v.Author == user && l.Policy.Mode == "maintainer" {
		return nil, fail(403, "maintainer policy requires a different reviewer")
	}
	if p.Decision == "accept" && p.Plan != nil {
		packet, e := s.packet(ctx, tx, l, v)
		if e != nil {
			return nil, e
		}
		if p.PacketToken == "" || p.PacketToken != packet.Token {
			return nil, fail(409, "the comparison changed; review the current packet")
		}
		// A reviewer may select a displayed accepted comparison or explicitly keep
		// a distinct claim. No model result authorizes this operation.
		allowed := p.Plan.Mode == "create" && p.Plan.FindingID == "" && p.Plan.BaseRevision == ""
		if p.Plan.Mode == "revision" || p.Plan.Mode == "contribution" {
			allowed = p.Plan.FindingID == packet.Plan.FindingID && p.Plan.BaseRevision == packet.Plan.BaseRevision
			for _, candidate := range packet.Comparisons {
				if candidate.Reference.Kind == "finding" && candidate.Reference.ID == p.Plan.FindingID && candidate.Reference.Version == p.Plan.BaseRevision {
					allowed = true
				}
			}
		}
		if !allowed {
			return nil, fail(409, "select a current comparison from this review")
		}
		{
			_, e = s.resolveSubmission(ctx, tx, user, l, raw(map[string]any{"id": v.ID, "mode": p.Plan.Mode, "finding_id": p.Plan.FindingID, "base_revision": p.Plan.BaseRevision, "rationale": p.Rationale}))
			if e != nil {
				return nil, e
			}
			v, e = readSubmission(ctx, tx, l.ID, v.ID)
			if e != nil {
				return nil, e
			}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO community.reviews(id,community_id,submission_id,reviewer_id,digest,policy_version,decision,rationale) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id(), l.ID, v.ID, user, v.Digest, l.PolicyVersion, p.Decision, p.Rationale)
	if err != nil {
		return nil, err
	}
	if p.Decision == "accept" {
		err = s.accept(ctx, tx, l, v, user)
	} else {
		state := "rejected"
		if p.Decision == "request_changes" {
			state = "changes_requested"
		}
		_, err = tx.Exec(ctx, `UPDATE community.submissions SET state=$1 WHERE id=$2`, state, v.ID)
	}
	if err != nil {
		return nil, err
	}
	if err = audit(ctx, tx, l.ID, user, "submission.review", p); err != nil {
		return nil, err
	}
	return readSubmission(ctx, tx, l.ID, v.ID)
}
func nextSequence(ctx context.Context, tx pgx.Tx, c string) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, `UPDATE community.libraries SET sequence=sequence+1 WHERE id=$1 RETURNING sequence`, c).Scan(&seq)
	return seq, err
}
func (s *Server) accept(ctx context.Context, tx pgx.Tx, l kb.Library, v kb.Submission, reviewer string) error {
	for _, c := range validate(v.Document) {
		if c.Blocking {
			return fail(422, c.Message)
		}
	}
	var fid, base, contribution, contributionRevision, identityKind string
	var needsIdentity bool
	if err := tx.QueryRow(ctx, `SELECT finding_id,base_revision,identity_review,contribution_target,contribution_revision,identity_kind FROM community.submissions WHERE id=$1`, v.ID).Scan(&fid, &base, &needsIdentity, &contribution, &contributionRevision, &identityKind); err != nil {
		return err
	}
	if needsIdentity {
		return fail(409, "resolve the submission identity and similar findings before acceptance")
	}
	if contribution != "" {
		var revision, build, status string
		if err := tx.QueryRow(ctx, `SELECT f.revision::text,r.document->>'build',r.status FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND f.id::text=$2`, l.ID, contribution).Scan(&revision, &build, &status); err != nil {
			return err
		}
		if revision != contributionRevision || status != "active" {
			return fail(409, "contribution target changed; compare it again")
		}
		if !semantic.KnownScope(build) || !semantic.KnownScope(v.Document.Build) || strings.TrimSpace(build) != strings.TrimSpace(v.Document.Build) {
			return fail(409, "different applicability cannot be consolidated")
		}
	}
	// Re-resolve under the lock: another queued document may have been accepted
	// since this one was submitted. A stale 'new' decision cannot mint a duplicate.
	if fid == "" {
		identity, err := s.resolvePublication(ctx, tx, v.Author, l, v.Document)
		if err != nil {
			return err
		}
		var decided bool
		if err = tx.QueryRow(ctx, `SELECT identity_snapshot<>'' AND identity_snapshot=$2 FROM community.submissions WHERE id=$1`, v.ID, semantic.Hash(identity)).Scan(&decided); err != nil {
			return err
		}
		if (identityKind == "create" || identityKind == "contribution") && !decided {
			return fail(409, "the compared identities changed; review the current publication effect")
		}
		if identity.State == "exact_copy" && identityKind != "create" && identityKind != "contribution" {
			fid, base = identity.FindingID, identity.Revision
		} else if identity.State != "new" {
			// A maintainer may explicitly certify a distinct contribution.
			if !decided {
				return fail(409, "new overlap detected; resolve submission identity before acceptance")
			}
		}
	}
	newFinding := fid == ""
	if fid == "" {
		fid = id()
	} else {
		var current, digest, claimDigest, status string
		err := tx.QueryRow(ctx, `SELECT f.revision::text,r.content_digest,r.claim_digest,r.status FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND f.id=$2`, l.ID, fid).Scan(&current, &digest, &claimDigest, &status)
		if err != nil || current != base {
			_, e := tx.Exec(ctx, `UPDATE community.submissions SET state='conflict' WHERE id=$1`, v.ID)
			return e
		}
		if status != "active" && (reviewer == "" || digest == kb.ContentDigest(v.Document)) {
			return fail(409, "finding has a validity assessment; review and restore explicitly before changing its content")
		}
		copyIntent := v.Document.FindingID == "" && identityKind != "revision"
		if digest == kb.ContentDigest(v.Document) || (copyIntent && status == "active" && semantic.KnownScope(v.Document.Build) && claimDigest == kb.ClaimDigest(v.Document)) {
			if _, err = tx.Exec(ctx, `UPDATE community.submissions SET state='accepted',finding_id=$1,accepted_revision=$2 WHERE id=$3`, fid, current, v.ID); err != nil {
				return err
			}
			return bindSource(ctx, tx, l.ID, v.Author, v.Document, fid)
		}
	}
	if newFinding {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM community.findings WHERE community_id=$1 AND NOT deleted`, l.ID).Scan(&count); err != nil {
			return err
		}
		if count >= s.limits().Findings {
			return fail(429, "community finding limit reached")
		}
	}
	if v.Document.Supersedes != "" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.findings WHERE community_id=$1 AND id=$2)`, l.ID, v.Document.Supersedes).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fail(422, "superseded finding must belong to this community")
		}
	}
	seq, err := nextSequence(ctx, tx, l.ID)
	if err != nil {
		return err
	}
	rid := id()
	_, err = tx.Exec(ctx, `INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,submission_id,author_id,content_digest,text_digest,claim_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, rid, l.ID, fid, seq, raw(v.Document), v.ID, v.Author, kb.ContentDigest(v.Document), kb.TextDigest(v.Document.Markdown), kb.ClaimDigest(v.Document))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO community.findings(community_id,id,revision) VALUES($1,$2,$3) ON CONFLICT(community_id,id) DO UPDATE SET revision=excluded.revision,deleted=false`, l.ID, fid, rid)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE community.submissions SET state='accepted',finding_id=$2,accepted_revision=$3 WHERE id=$1`, v.ID, fid, rid)
	if err != nil {
		return err
	}
	if err = bindSource(ctx, tx, l.ID, v.Author, v.Document, fid); err != nil {
		return err
	}
	if contribution != "" && contribution != fid {
		_, err = s.relations(ctx, tx, reviewer, l, "finding.relate", raw(map[string]string{"source_id": fid, "source_revision": rid, "target_id": contribution, "target_revision": contributionRevision, "kind": "equivalent", "rationale": "Accepted as an independently attributed contribution after comparison of the claims and applicability."}))
	}
	return err
}

func bindSource(ctx context.Context, tx pgx.Tx, c, user string, d kb.Document, fid string) error {
	// An accepted identity decision becomes the durable alias for future imports.
	if _, err := tx.Exec(ctx, `DELETE FROM community.publication_sources WHERE community_id=$1 AND author_id=$2 AND namespace=$3 AND source_path=$4 AND finding_id::text<>$5`, c, user, d.SourceNamespace, d.SourcePath, fid); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO community.publication_sources(community_id,author_id,namespace,source_path,finding_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, c, user, d.SourceNamespace, d.SourcePath, fid)
	return err
}

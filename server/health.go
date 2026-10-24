package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
)

type claimRef struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version"`
}
type healthClaim struct {
	Ref      claimRef    `json:"reference"`
	Document kb.Document `json:"document"`
	Status   string      `json:"status"`
}
type healthIssue struct {
	ID            string             `json:"id"`
	Subject       claimRef           `json:"subject"`
	Other         claimRef           `json:"other"`
	Kind          string             `json:"kind"`
	Reason        string             `json:"reason"`
	Judgment      *semantic.Judgment `json:"judgment,omitempty"`
	PolicyVersion int                `json:"policy_version"`
	State         string             `json:"state"`
}

func loadHealthClaim(ctx context.Context, tx pgx.Tx, c string, ref claimRef) (healthClaim, error) {
	out := healthClaim{Ref: ref}
	var b []byte
	var err error
	if ref.Kind == "finding" {
		err = tx.QueryRow(ctx, `SELECT f.revision::text,r.document,r.status FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND f.id=$2 AND NOT f.deleted`, c, ref.ID).Scan(&out.Ref.Version, &b, &out.Status)
	} else if ref.Kind == "submission" {
		err = tx.QueryRow(ctx, `SELECT digest||'/'||resolution_version::text,document,state FROM community.submissions WHERE community_id=$1 AND id=$2 AND state NOT IN ('accepted','rejected')`, c, ref.ID).Scan(&out.Ref.Version, &b, &out.Status)
	} else {
		return out, fail(400, "invalid claim reference")
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out.Document)
	return out, err
}

// Accepted and permitted pending claims share one indexed candidate path. SQL RLS
// limits pending records for request callers; the worker can compare the inventory.
func healthCandidates(ctx context.Context, tx pgx.Tx, c string, source healthClaim) ([]healthClaim, bool, error) {
	parsed := engine.Parse(source.Document.SourcePath, source.Document.Markdown)
	terms := append(append([]string{}, parsed.Idents...), strings.Fields(parsed.Title)...)
	if len(terms) > 32 {
		terms = terms[:32]
	}
	query := strings.Join(terms, " OR ")
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT 'finding' AS kind,f.id::text AS id,f.revision::text AS version,r.document,r.status,
 CASE WHEN r.claim_digest=$4 THEN 100 ELSE ts_rank(r.search_terms,websearch_to_tsquery('english',$3))*10 END AS score
 FROM community.findings f JOIN community.revisions r ON r.id=f.revision
 JOIN (
  SELECT id FROM community.revisions WHERE community_id=$1 AND NOT deleted AND claim_digest=$4
  UNION SELECT id FROM community.revisions WHERE community_id=$1 AND NOT deleted AND search_terms @@ websearch_to_tsquery('english',$3)
  UNION SELECT id FROM community.revisions WHERE community_id=$1 AND NOT deleted AND $6 OPERATOR(public.<%) (document->>'markdown')
 ) matches ON matches.id=r.id
 WHERE f.community_id=$1 AND NOT f.deleted AND NOT r.deleted AND r.status IN ('active','disputed') AND NOT ($5='finding' AND f.id::text=$2)
 UNION ALL
 SELECT 'submission',s.id::text,s.digest||'/'||s.resolution_version::text,s.document,s.state,
 CASE WHEN s.claim_digest=$4 THEN 100 ELSE ts_rank(s.search_terms,websearch_to_tsquery('english',$3))*10 END
 FROM community.submissions s
 JOIN (
  SELECT id FROM community.submissions WHERE community_id=$1 AND state IN ('queued','needs_review','changes_requested','conflict') AND claim_digest=$4
  UNION SELECT id FROM community.submissions WHERE community_id=$1 AND state IN ('queued','needs_review','changes_requested','conflict') AND search_terms @@ websearch_to_tsquery('english',$3)
  UNION SELECT id FROM community.submissions WHERE community_id=$1 AND state IN ('queued','needs_review','changes_requested','conflict') AND $6 OPERATOR(public.<%) (document->>'markdown')
 ) matches ON matches.id=s.id
 WHERE s.community_id=$1 AND s.state IN ('queued','needs_review','changes_requested','conflict') AND NOT ($5='submission' AND s.id::text=$2)
 ) SELECT kind,id,version,document,status FROM candidates ORDER BY score DESC,kind,id LIMIT 65`, c, source.Ref.ID, query, kb.ClaimDigest(source.Document), source.Ref.Kind, parsed.Title)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []healthClaim{}
	for rows.Next() {
		var v healthClaim
		var b []byte
		if err = rows.Scan(&v.Ref.Kind, &v.Ref.ID, &v.Ref.Version, &b, &v.Status); err != nil {
			return nil, false, err
		}
		if err = json.Unmarshal(b, &v.Document); err != nil {
			return nil, false, err
		}
		out = append(out, v)
	}
	more := len(out) > 64
	if more {
		out = out[:64]
	}
	return out, more, rows.Err()
}

func newHealthIssue(c string, policy int, left, right claimRef, kind, reason string, j *semantic.Judgment) healthIssue {
	if right.ID != "" && left.Kind+left.ID > right.Kind+right.ID {
		left, right = right, left
	}
	return healthIssue{ID: semantic.Hash([]any{c, policy, semantic.Rubric, left, right, kind}), Subject: left, Other: right, Kind: kind, Reason: reason, Judgment: j, PolicyVersion: policy, State: "open"}
}

// RunHealthJob leases one bounded comparison chunk. It never publishes, merges,
// changes a truth grade or changes a claim's validity. Every issue is advisory.
func (s *Server) RunHealthJob(ctx context.Context) (bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// A background query must not monopolize the database if its plan regresses.
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='2s'`); err != nil {
		return false, err
	}
	var c, version, signature, incomplete string
	var ref claimRef
	var generation int64
	var offset int
	lease := id()
	err = tx.QueryRow(ctx, `SELECT community_id::text,subject_kind,subject_id::text,version,generation,candidate_offset,candidate_signature,scan_error FROM community.health_work WHERE community_id IN (SELECT id FROM community.libraries WHERE deletion_id IS NULL) AND available_at<=now() AND (locked_until IS NULL OR locked_until<now()) ORDER BY COALESCE(last_analyzed,'epoch'::timestamptz),available_at,community_id,subject_id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&c, &ref.Kind, &ref.ID, &version, &generation, &offset, &signature, &incomplete)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ref.Version = version
	ctx, done := s.trackCommunityWork(ctx, c)
	defer done()
	if _, err = tx.Exec(ctx, `UPDATE community.health_work SET lease_id=$1,locked_until=now()+interval '2 minutes',attempts=attempts+1 WHERE community_id=$2 AND subject_kind=$3 AND subject_id=$4`, lease, c, ref.Kind, ref.ID); err != nil {
		return true, err
	}
	l, err := library(ctx, tx, c)
	if err != nil {
		return true, err
	}
	source, err := loadHealthClaim(ctx, tx, c, ref)
	if err != nil {
		return true, err
	}
	var candidates []healthClaim
	var limited bool
	knownScope := semantic.KnownScope(source.Document.Build)
	if knownScope && (source.Ref.Kind != "finding" || source.Status == "active" || source.Status == "disputed") {
		candidates, limited, err = healthCandidates(ctx, tx, c, source)
		if err != nil {
			return true, err
		}
	}
	refs := []claimRef{}
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	currentSignature := semantic.Hash(refs)
	if signature != currentSignature || offset > len(candidates) {
		offset = 0
		incomplete = ""
	}
	if source.Ref.Kind == "finding" && source.Status != "active" && source.Status != "disputed" {
		candidates = nil
		offset = 0
	}
	issues := []healthIssue{}
	if !knownScope {
		incomplete = "Comparison needs complete applicability; candidate search and provider requests were skipped."
		issues = append(issues, newHealthIssue(c, l.PolicyVersion, source.Ref, claimRef{}, "applicability", "State the applicable build and conditions; unknown scope cannot establish equivalence.", nil))
	}
	if source.Ref.Kind == "finding" {
		var stale bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.finding_relations r JOIN community.findings a ON a.community_id=r.community_id AND a.id=r.source_id JOIN community.findings b ON b.community_id=r.community_id AND b.id=r.target_id WHERE r.community_id=$1 AND (r.source_id=$2 OR r.target_id=$2) AND (a.revision<>r.source_revision OR b.revision<>r.target_revision))`, c, source.Ref.ID).Scan(&stale)
		if err != nil {
			return true, err
		}
		if stale {
			issues = append(issues, newHealthIssue(c, l.PolicyVersion, source.Ref, claimRef{}, "stale_relationship", "A claim changed after its relationship was reviewed. Compare the current revisions.", nil))
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}

	end := min(offset+8, len(candidates))
	callCtx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	left := kb.ProjectClaim(source.Document, source.Status)
	for _, other := range candidates[offset:end] {
		kind, reason := "", ""
		var judgment *semantic.Judgment
		if kb.ClaimDigest(source.Document) == kb.ClaimDigest(other.Document) && semantic.KnownScope(source.Document.Build) {
			kind, reason = "duplicate", "The same claim, identifiers and applicability already occur here; retain attribution in one reader entry."
		} else if l.Policy.Assistance && s.Semantic != nil {
			right := kb.ProjectClaim(other.Document, other.Status)
			if left.Truncated || right.Truncated || !semantic.KnownScope(left.Build) || !semantic.KnownScope(right.Build) {
				incomplete = "Comparison needs complete applicability and claim text; no provider request was made."
				continue
			}
			j, e := s.Semantic.Evaluate(callCtx, c, map[string]any{"left": left, "right": right, "policy_version": l.PolicyVersion, "projection": semantic.ProjectionVersion, "left_revision": source.Ref.Version, "right_revision": other.Ref.Version}, semantic.CompareQuestions(), false)
			if e != nil {
				incomplete = "Optional comparison unavailable; ordinary review and exact checks remain available."
				continue
			}
			judgment = &j
			switch semantic.Relation(j, left, right) {
			case "equivalent":
				kind, reason = "duplicate", "These claims appear equivalent under the same conditions. Review before combining their reader entries."
			case "partial":
				kind, reason = "partial_overlap", "Some information overlaps; preserve new facts and qualifications while removing repetition."
			case "conflicting":
				kind, reason = "conflict", "These claims may disagree under overlapping conditions. A maintainer must resolve the wording or applicability."
			}
		}
		if kind != "" {
			issues = append(issues, newHealthIssue(c, l.PolicyVersion, source.Ref, other.Ref, kind, reason, judgment))
		}
	}
	finish, err := s.DB.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer finish.Rollback(ctx)
	if _, err = finish.Exec(ctx, `SELECT id FROM community.libraries WHERE id=$1 FOR UPDATE`, c); err != nil {
		return true, err
	}
	var valid bool
	err = finish.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.health_work w JOIN community.libraries l ON l.id=w.community_id WHERE w.community_id=$1 AND subject_kind=$2 AND subject_id=$3 AND lease_id=$4 AND locked_until>now() AND generation=$5 AND version=$6 AND l.policy_version=$7)`, c, ref.Kind, ref.ID, lease, generation, version, l.PolicyVersion).Scan(&valid)
	if err != nil {
		return true, err
	}
	if !valid {
		_, err = finish.Exec(ctx, `UPDATE community.health_work SET locked_until=NULL,available_at=now() WHERE community_id=$1 AND subject_kind=$2 AND subject_id=$3 AND lease_id=$4`, c, ref.Kind, ref.ID, lease)
		if err == nil {
			err = finish.Commit(ctx)
		}
		return true, err
	}
	for _, issue := range issues {
		current, e := loadHealthClaim(ctx, finish, c, issue.Subject)
		if e == pgx.ErrNoRows {
			continue
		}
		if e != nil {
			return true, e
		}
		if current.Ref.Version != issue.Subject.Version {
			continue
		}
		if issue.Other.ID != "" {
			other, e := loadHealthClaim(ctx, finish, c, issue.Other)
			if e == pgx.ErrNoRows {
				continue
			}
			if e != nil {
				return true, e
			}
			if other.Ref.Version != issue.Other.Version {
				continue
			}
			if issue.Kind == "duplicate" && issue.Subject.Kind == "finding" && issue.Other.Kind == "finding" {
				a, e := canonicalRef(ctx, finish, c, issue.Subject)
				if e != nil {
					return true, e
				}
				b, e := canonicalRef(ctx, finish, c, issue.Other)
				if e != nil {
					return true, e
				}
				if a.ID == b.ID {
					continue
				}
			}
		}
		_, err = finish.Exec(ctx, `INSERT INTO community.health_issues(id,community_id,subject_kind,subject_id,subject_version,other_kind,other_id,other_version,kind,reason,judgment,policy_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO NOTHING`, issue.ID, c, issue.Subject.Kind, issue.Subject.ID, issue.Subject.Version, issue.Other.Kind, issue.Other.ID, issue.Other.Version, issue.Kind, issue.Reason, raw(issue.Judgment), l.PolicyVersion)
		if err != nil {
			return true, err
		}
	}
	if limited && incomplete == "" {
		incomplete = "Candidate shortlist reached its bound; visited records are not a proof of semantic uniqueness."
	}
	if end < len(candidates) {
		_, err = finish.Exec(ctx, `UPDATE community.health_work SET candidate_offset=$1,candidate_signature=$2,locked_until=NULL,available_at=now()+interval '1 second',last_error=$3,scan_error=$3 WHERE community_id=$4 AND subject_kind=$5 AND subject_id=$6 AND lease_id=$7`, end, currentSignature, incomplete, c, ref.Kind, ref.ID, lease)
	} else {
		_, err = finish.Exec(ctx, `UPDATE community.health_work SET candidate_offset=0,candidate_signature='',scan_error='',locked_until=NULL,available_at=now()+interval '1 day',last_analyzed=now(),analyzed_version=version,analyzed_policy=$1,analyzed_generation=generation,last_error=$2 WHERE community_id=$3 AND subject_kind=$4 AND subject_id=$5 AND lease_id=$6`, l.PolicyVersion, incomplete, c, ref.Kind, ref.ID, lease)
	}
	if err != nil {
		return true, err
	}
	return true, finish.Commit(ctx)
}

func (s *Server) health(ctx context.Context, tx pgx.Tx, user string, l kb.Library, action string, b json.RawMessage) (any, error) {
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer"); err != nil {
		return nil, err
	}
	if action == "health.review" {
		return s.reviewHealth(ctx, tx, user, l, b)
	}
	var p struct {
		After string `json:"after"`
		Limit int    `json:"limit"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 50
	}
	rows, err := tx.Query(ctx, `SELECT id,subject_kind,subject_id::text,subject_version,other_kind,other_id,other_version,kind,reason,judgment,policy_version,state FROM community.health_issues WHERE community_id=$1 AND policy_version=$2 AND state='open' AND id>$3 ORDER BY id LIMIT $4`, l.ID, l.PolicyVersion, p.After, p.Limit+1)
	if err != nil {
		return nil, err
	}
	items := []healthIssue{}
	for rows.Next() {
		var v healthIssue
		var j []byte
		if err = rows.Scan(&v.ID, &v.Subject.Kind, &v.Subject.ID, &v.Subject.Version, &v.Other.Kind, &v.Other.ID, &v.Other.Version, &v.Kind, &v.Reason, &j, &v.PolicyVersion, &v.State); err != nil {
			rows.Close()
			return nil, err
		}
		json.Unmarshal(j, &v.Judgment)
		items = append(items, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	more := len(items) > p.Limit
	if more {
		items = items[:p.Limit]
	}
	next := ""
	if more {
		next = items[len(items)-1].ID
	}
	var total, checked, incomplete, open int
	var last *time.Time
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE analyzed_version=version AND analyzed_policy=$2 AND analyzed_generation=generation),count(*) FILTER(WHERE last_error<>''),max(last_analyzed) FROM community.health_work WHERE community_id=$1`, l.ID, l.PolicyVersion).Scan(&total, &checked, &incomplete, &last)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM community.health_issues WHERE community_id=$1 AND policy_version=$2 AND state='open'`, l.ID, l.PolicyVersion).Scan(&open); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "next": next, "progress": map[string]any{"total": total, "checked": checked, "pending": total - checked, "incomplete": incomplete, "open": open, "last_checked": last, "assisted": l.Policy.Assistance && s.Semantic != nil}}, nil
}

func (s *Server) reviewHealth(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	var p struct {
		ID        string `json:"id"`
		Decision  string `json:"decision"`
		Rationale string `json:"rationale"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Rationale) == "" || len(p.Rationale) > 4000 {
		return nil, fail(400, "explain the review decision")
	}
	if p.Decision != "dismiss" && p.Decision != "combine" {
		return nil, fail(400, "decision must be dismiss or combine")
	}
	if _, err := tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return nil, err
	}
	l, err := library(ctx, tx, l.ID)
	if err != nil {
		return nil, err
	}
	var issue healthIssue
	err = tx.QueryRow(ctx, `SELECT subject_kind,subject_id::text,subject_version,other_kind,other_id,other_version,kind,policy_version,state FROM community.health_issues WHERE community_id=$1 AND id=$2 FOR UPDATE`, l.ID, p.ID).Scan(&issue.Subject.Kind, &issue.Subject.ID, &issue.Subject.Version, &issue.Other.Kind, &issue.Other.ID, &issue.Other.Version, &issue.Kind, &issue.PolicyVersion, &issue.State)
	if err != nil {
		return nil, err
	}
	if issue.State != "open" || issue.PolicyVersion != l.PolicyVersion {
		return nil, fail(409, "review the current issue")
	}
	for _, ref := range []claimRef{issue.Subject, issue.Other} {
		if ref.ID == "" {
			continue
		}
		current, e := loadHealthClaim(ctx, tx, l.ID, ref)
		if e != nil {
			return nil, e
		}
		if current.Ref.Version != ref.Version {
			return nil, fail(409, "a claim changed; review the current revisions")
		}
	}
	state := "dismissed"
	if p.Decision == "combine" {
		if issue.Kind != "duplicate" || issue.Subject.Kind != "finding" || issue.Other.Kind != "finding" {
			return nil, fail(409, "pending contributions use ordinary submission review; partial overlaps require editing the claim")
		}
		err = s.combineGroups(ctx, tx, user, l, issue.Subject, issue.Other, p.Rationale)
		if err != nil {
			return nil, err
		}
		state = "resolved"
	}
	if _, err = tx.Exec(ctx, `UPDATE community.health_issues SET state=$1,reviewed_by=$2,rationale=$3 WHERE community_id=$4 AND id=$5`, state, user, p.Rationale, l.ID, p.ID); err != nil {
		return nil, err
	}
	return map[string]string{"state": state}, audit(ctx, tx, l.ID, user, "health.review", p)
}

func (s *Server) reviewPacket(ctx context.Context, tx pgx.Tx, user string, l kb.Library, b json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	v, err := readSubmission(ctx, tx, l.ID, p.ID)
	if err != nil {
		return nil, err
	}
	return s.packet(ctx, tx, l, v)
}

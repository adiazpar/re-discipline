package server

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
)

func (s *Server) candidates(ctx context.Context, tx pgx.Tx, l kb.Library, b json.RawMessage) (any, error) {
	var p struct {
		Document kb.Document `json:"document"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	if len(p.Document.Markdown) > 256*1024 {
		return nil, fail(400, "candidate document is too large")
	}
	doc := engine.Parse(p.Document.SourcePath, p.Document.Markdown)
	terms := append(append([]string{}, doc.Idents...), strings.Fields(doc.Title)...)
	if len(terms) > 32 {
		terms = terms[:32]
	}
	query := strings.Join(terms, " OR ")
	rows, err := tx.Query(ctx, `SELECT f.id::text,f.revision::text,r.author_id::text,r.document FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND NOT f.deleted AND NOT r.deleted AND f.id::text<>$2 AND r.search_terms @@ websearch_to_tsquery('english',$3) ORDER BY ts_rank(r.search_terms,websearch_to_tsquery('english',$3)) DESC LIMIT 30`, l.ID, p.Document.FindingID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		FindingID string `json:"finding_id"`
		Revision  string `json:"revision"`
		Author    string `json:"author"`
		Title     string `json:"title"`
		Build     string `json:"build"`
		Reason    string `json:"reason"`
		Score     int    `json:"score"`
	}
	out := []candidate{}
	for rows.Next() {
		var v candidate
		var raw []byte
		if err = rows.Scan(&v.FindingID, &v.Revision, &v.Author, &raw); err != nil {
			return nil, err
		}
		var d kb.Document
		if err = json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		parsed := engine.Parse(d.SourcePath, d.Markdown)
		v.Title = parsed.Title
		v.Build = d.Build
		v.Reason = "Overlapping topic; compare the claims and evidence."
		v.Score = 1
		if strings.EqualFold(strings.Join(strings.Fields(doc.Title), " "), strings.Join(strings.Fields(parsed.Title), " ")) {
			v.Score += 3
		}
		for _, a := range doc.Idents {
			for _, b := range parsed.Idents {
				if a == b {
					v.Score += 5
				}
			}
		}
		if strings.Join(strings.Fields(doc.Body), " ") == strings.Join(strings.Fields(parsed.Body), " ") && p.Document.Build == d.Build {
			v.Score += 20
			v.Reason = "Same claim text and build; evidence and authorship may differ."
		}
		if p.Document.Build != d.Build {
			v.Reason = "Overlapping topic with different applicability; do not assume equivalence."
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 10 {
		out = out[:10]
	}
	return map[string]any{"candidates": out, "automatic_merge": false}, rows.Err()
}

func (s *Server) relations(ctx context.Context, tx pgx.Tx, user string, l kb.Library, action string, b json.RawMessage) (any, error) {
	if action == "finding.relations" {
		var p struct {
			ID string `json:"id"`
		}
		if err := data(b, &p); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT r.source_id::text,r.target_id::text,r.source_revision::text,r.target_revision::text,r.kind,r.rationale,r.reviewer_id::text,(s.revision=r.source_revision AND t.revision=r.target_revision) FROM community.finding_relations r JOIN community.findings s ON s.community_id=r.community_id AND s.id=r.source_id JOIN community.findings t ON t.community_id=r.community_id AND t.id=r.target_id WHERE r.community_id=$1 AND ($2='' OR r.source_id::text=$2 OR r.target_id::text=$2) ORDER BY r.source_id,r.target_id`, l.ID, p.ID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []kb.Relation{}
		for rows.Next() {
			var v kb.Relation
			if err = rows.Scan(&v.SourceID, &v.TargetID, &v.SourceRevision, &v.TargetRevision, &v.Kind, &v.Rationale, &v.Reviewer, &v.Current); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	if err := role(ctx, tx, l.ID, user, "owner", "maintainer"); err != nil {
		return nil, err
	}
	var p struct {
		SourceID       string `json:"source_id"`
		TargetID       string `json:"target_id"`
		SourceRevision string `json:"source_revision"`
		TargetRevision string `json:"target_revision"`
		Kind           string `json:"kind"`
		Rationale      string `json:"rationale"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	for _, v := range []string{p.SourceID, p.TargetID, p.SourceRevision, p.TargetRevision} {
		if err := validUUID(v); err != nil {
			return nil, err
		}
	}
	if p.SourceID == p.TargetID || strings.TrimSpace(p.Rationale) == "" || len(p.Rationale) > 4000 {
		return nil, fail(400, "select distinct findings and explain the relationship")
	}
	if p.Kind != "equivalent" && p.Kind != "related" && p.Kind != "conflicting" && p.Kind != "separate" {
		return nil, fail(400, "relationship must be equivalent, related, conflicting, or separate")
	}
	if _, err := tx.Exec(ctx, `SELECT community.lock_library($1)`, l.ID); err != nil {
		return nil, err
	}
	builds := []string{}
	for i, f := range []string{p.SourceID, p.TargetID} {
		var revision, build, status string
		if err := tx.QueryRow(ctx, `SELECT f.revision::text,r.document->>'build',r.status FROM community.findings f JOIN community.revisions r ON r.id=f.revision WHERE f.community_id=$1 AND f.id=$2 AND NOT f.deleted`, l.ID, f).Scan(&revision, &build, &status); err != nil {
			return nil, err
		}
		want := p.SourceRevision
		if i == 1 {
			want = p.TargetRevision
		}
		if revision != want {
			return nil, fail(409, "a finding changed; compare the current revisions")
		}
		if p.Kind == "equivalent" && status != "active" {
			return nil, fail(409, "resolve the claim's validity before consolidating")
		}
		builds = append(builds, build)
	}
	if p.Kind == "equivalent" {
		if !semantic.KnownScope(builds[0]) || !semantic.KnownScope(builds[1]) {
			return nil, fail(409, "clarify applicability before consolidating")
		}
		if strings.Join(strings.Fields(builds[0]), " ") != strings.Join(strings.Fields(builds[1]), " ") {
			return nil, fail(409, "different applicability cannot be consolidated; use related or conflicting")
		}
		var chain bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.finding_relations r JOIN community.findings s ON s.community_id=r.community_id AND s.id=r.source_id JOIN community.findings t ON t.community_id=r.community_id AND t.id=r.target_id WHERE r.community_id=$1 AND r.kind='equivalent' AND (r.source_id=$2 OR r.target_id=$3) AND s.revision=r.source_revision AND t.revision=r.target_revision)`, l.ID, p.TargetID, p.SourceID).Scan(&chain); err != nil {
			return nil, err
		}
		if chain {
			return nil, fail(409, "choose a canonical finding directly; equivalence chains are not allowed")
		}
		// Retire obsolete outgoing equivalence before choosing a new canonical
		// target; the audit retains the old decision and its revisions.
		if _, err := tx.Exec(ctx, `DELETE FROM community.finding_relations r USING community.findings s,community.findings t WHERE r.community_id=$1 AND r.source_id=$2 AND r.kind='equivalent' AND s.community_id=r.community_id AND s.id=r.source_id AND t.community_id=r.community_id AND t.id=r.target_id AND (s.revision<>r.source_revision OR t.revision<>r.target_revision)`, l.ID, p.SourceID); err != nil {
			return nil, err
		}
	}
	if p.Kind == "separate" {
		if _, err := tx.Exec(ctx, `DELETE FROM community.finding_relations WHERE community_id=$1 AND source_id=$2 AND target_id=$3`, l.ID, p.SourceID, p.TargetID); err != nil {
			return nil, err
		}
	} else {
		_, err := tx.Exec(ctx, `INSERT INTO community.finding_relations(community_id,source_id,target_id,source_revision,target_revision,kind,rationale,reviewer_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(community_id,source_id,target_id) DO UPDATE SET source_revision=excluded.source_revision,target_revision=excluded.target_revision,kind=excluded.kind,rationale=excluded.rationale,reviewer_id=excluded.reviewer_id,created_at=now()`, l.ID, p.SourceID, p.TargetID, p.SourceRevision, p.TargetRevision, p.Kind, p.Rationale, user)
		if err != nil {
			return nil, err
		}
	}
	if p.Kind == "equivalent" {
		if err := retireGroupedIssues(ctx, tx, l.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"kind": p.Kind, "source_id": p.SourceID, "target_id": p.TargetID}, audit(ctx, tx, l.ID, user, "finding.relate", p)
}

// Only current, active, directly reviewed equivalence defines a reader group.
func canonicalRef(ctx context.Context, tx pgx.Tx, c string, ref claimRef) (claimRef, error) {
	var target claimRef
	target.Kind = "finding"
	err := tx.QueryRow(ctx, `SELECT r.target_id::text,r.target_revision::text FROM community.finding_relations r JOIN community.findings a ON a.community_id=r.community_id AND a.id=r.source_id JOIN community.findings b ON b.community_id=r.community_id AND b.id=r.target_id JOIN community.revisions ar ON ar.id=a.revision JOIN community.revisions br ON br.id=b.revision WHERE r.community_id=$1 AND r.source_id::text=$2 AND r.kind='equivalent' AND NOT a.deleted AND NOT b.deleted AND a.revision=r.source_revision AND b.revision=r.target_revision AND ar.status='active' AND br.status='active'`, c, ref.ID).Scan(&target.ID, &target.Version)
	if err == pgx.ErrNoRows {
		return ref, nil
	}
	return target, err
}

func retireGroupedIssues(ctx context.Context, tx pgx.Tx, c string) error {
	_, err := tx.Exec(ctx, `WITH heads AS (
 SELECT f.id,f.revision,COALESCE(r.target_id,f.id) AS canonical FROM community.findings f
 JOIN community.revisions h ON h.id=f.revision
 LEFT JOIN community.finding_relations r ON r.community_id=f.community_id AND r.source_id=f.id AND r.kind='equivalent' AND r.source_revision=f.revision
 AND EXISTS(SELECT 1 FROM community.findings t JOIN community.revisions th ON th.id=t.revision WHERE t.community_id=r.community_id AND t.id=r.target_id AND t.revision=r.target_revision AND NOT t.deleted AND th.status='active')
 WHERE f.community_id=$1 AND NOT f.deleted AND h.status='active')
 UPDATE community.health_issues i SET state='resolved',rationale='These current claims share a reviewed reader entry.' FROM heads a,heads b
 WHERE i.community_id=$1 AND i.state='open' AND i.kind='duplicate' AND i.subject_kind='finding' AND i.other_kind='finding'
 AND i.subject_id=a.id AND i.subject_version=a.revision::text AND i.other_id=b.id::text AND i.other_version=b.revision::text AND a.canonical=b.canonical`, c)
	return err
}

func (s *Server) combineGroups(ctx context.Context, tx pgx.Tx, user string, l kb.Library, a, b claimRef, reason string) error {
	left, err := canonicalRef(ctx, tx, l.ID, a)
	if err != nil {
		return err
	}
	right, err := canonicalRef(ctx, tx, l.ID, b)
	if err != nil {
		return err
	}
	if left.ID == right.ID {
		return retireGroupedIssues(ctx, tx, l.ID)
	}
	// Retain an existing canonical entry when adding a new contribution.
	if right.ID != b.ID && left.ID == a.ID {
		left, right = right, left
	}
	rows, err := tx.Query(ctx, `SELECT r.source_id::text,r.source_revision::text FROM community.finding_relations r JOIN community.findings f ON f.community_id=r.community_id AND f.id=r.source_id WHERE r.community_id=$1 AND r.target_id::text=$2 AND r.target_revision::text=$3 AND r.kind='equivalent' AND f.revision=r.source_revision AND NOT f.deleted ORDER BY r.source_id`, l.ID, right.ID, right.Version)
	if err != nil {
		return err
	}
	members := []claimRef{}
	for rows.Next() {
		var v claimRef
		v.Kind = "finding"
		if err = rows.Scan(&v.ID, &v.Version); err != nil {
			rows.Close()
			return err
		}
		members = append(members, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	members = append(members, right)
	for _, v := range members {
		_, err = s.relations(ctx, tx, user, l, "finding.relate", raw(map[string]string{"source_id": v.ID, "source_revision": v.Version, "target_id": left.ID, "target_revision": left.Version, "kind": "equivalent", "rationale": reason}))
		if err != nil {
			return err
		}
	}
	return nil
}

package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

func (s *Server) dispatch(ctx context.Context, tx pgx.Tx, user string, req kb.Request) (any, error) {
	if req.Action == "community.create" {
		if err := authenticated(user); err != nil {
			return nil, err
		}
		var l kb.Library
		if err := data(req.Data, &l); err != nil {
			return nil, err
		}
		if l.Visibility == "" {
			l.Visibility = "private"
		}
		if l.Visibility != "private" {
			return nil, fail(400, "new communities must be private; the owner can make them public afterward")
		}
		if s.Config.LocalOnly {
			if l.Policy.Mode == "" {
				l.Policy.Mode = "trusted"
			}
			if err := localPolicy(l); err != nil {
				return nil, err
			}
		}
		if err := kb.ValidateLibrary(l); err != nil {
			return nil, fail(400, err.Error())
		}
		if l.Policy.Mode == "automated" && s.Config.ReviewURL == "" && !(l.Policy.Assistance && s.Semantic != nil) {
			return nil, fail(409, "automated review is not configured on this service")
		}
		var total int
		var totalBytes int64
		if err := tx.QueryRow(ctx, `SELECT libraries,bytes FROM community.service_capacity()`).Scan(&total, &totalBytes); err != nil {
			return nil, err
		}
		if total >= setting("MAX_TOTAL_COMMUNITIES", 50) {
			return nil, fail(429, "service community capacity reached")
		}
		// Serialize owner creation quotas across requests and replicas.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, user); err != nil {
			return nil, err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM community.libraries WHERE owner_id=$1`, user).Scan(&count); err != nil {
			return nil, err
		}
		if count >= s.limits().Communities {
			return nil, fail(429, "owned community limit reached")
		}
		l.ID = id()
		l.PolicyVersion = 1
		l.Sequence = 0
		l.Role = "owner"
		if _, err := tx.Exec(ctx, `INSERT INTO community.libraries(id,owner_id,name,slug,visibility,policy) VALUES($1,$2,$3,$4,$5,$6)`, l.ID, user, l.Name, l.Slug, l.Visibility, raw(l.Policy)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO community.members(community_id,user_id,role) VALUES($1,$2,'owner')`, l.ID, user); err != nil {
			return nil, err
		}
		return l, audit(ctx, tx, l.ID, user, req.Action, map[string]any{"slug": l.Slug})
	}
	if req.Action == "community.list" {
		var p struct {
			Discover bool `json:"discover"`
		}
		if err := data(req.Data, &p); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM community.libraries WHERE (visibility='public' AND $1) OR community.member_role(id) IS NOT NULL ORDER BY created_at DESC LIMIT 100`, p.Discover)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for rows.Next() {
			var v string
			if err = rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
		out := []kb.Library{}
		for _, v := range ids {
			l, e := library(ctx, tx, v)
			if e != nil {
				return nil, e
			}
			out = append(out, l)
		}
		return out, nil
	}
	l, err := library(ctx, tx, req.Community)
	if err != nil {
		return nil, err
	}
	c := l.ID
	switch req.Action {
	case "health.list", "health.review":
		return s.health(ctx, tx, user, l, req.Action, req.Data)
	case "submission.packet":
		return s.reviewPacket(ctx, tx, user, l, req.Data)
	case "finding.contributions":
		var p struct {
			ID    string `json:"id"`
			After string `json:"after"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		var result json.RawMessage
		err = tx.QueryRow(ctx, `SELECT community.finding_contributions($1,$2,$3)`, l.ID, p.ID, p.After).Scan(&result)
		return result, err
	case "finding.consolidate":
		return s.consolidate(ctx, tx, user, l, req.Data)
	case "publication.resolve":
		return s.publicationResolve(ctx, tx, user, l, req.Data)
	case "publication.receipts":
		return s.publicationReceipts(ctx, tx, user, l, req.Data)
	case "submission.resolve":
		return s.resolveSubmission(ctx, tx, user, l, req.Data)
	case "finding.match":
		return s.matchFindings(ctx, tx, user, l, req.Data)
	case "finding.assess":
		return s.assessFinding(ctx, tx, user, l, req.Data)
	case "finding.candidates":
		return s.candidates(ctx, tx, l, req.Data)
	case "finding.relations", "finding.relate":
		return s.relations(ctx, tx, user, l, req.Action, req.Data)
	case "import.create", "import.list", "import.revoke":
		return s.importGrant(ctx, tx, user, l, req.Action, req.Data)
	case "community.get":
		return l, nil
	case "community.update":
		if err = role(ctx, tx, c, user, "owner"); err != nil {
			return nil, err
		}
		var p struct {
			Name            string    `json:"name"`
			Visibility      string    `json:"visibility"`
			Policy          kb.Policy `json:"policy"`
			ExpectedVersion int       `json:"expected_version"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		next := l
		next.Name = p.Name
		next.Visibility = p.Visibility
		next.Policy = p.Policy
		if s.Config.LocalOnly {
			if err := localPolicy(next); err != nil {
				return nil, err
			}
		}
		if err = kb.ValidateLibrary(next); err != nil {
			return nil, fail(400, err.Error())
		}
		if p.Policy.Mode == "automated" && s.Config.ReviewURL == "" && !(p.Policy.Assistance && s.Semantic != nil) {
			return nil, fail(409, "automated review is not configured")
		}
		tag, e := tx.Exec(ctx, `UPDATE community.libraries SET name=$1,visibility=$2,policy=$3,policy_version=policy_version+1 WHERE id=$4 AND policy_version=$5`, p.Name, p.Visibility, raw(p.Policy), c, p.ExpectedVersion)
		if e != nil {
			return nil, e
		}
		if tag.RowsAffected() != 1 {
			return nil, fail(409, "community changed; refresh before updating")
		}
		if err = audit(ctx, tx, c, user, req.Action, p); err != nil {
			return nil, err
		}
		return library(ctx, tx, c)
	case "member.list":
		if err = role(ctx, tx, c, user, "owner", "maintainer", "publisher", "contributor", "reader"); err != nil {
			return nil, err
		}
		rows, e := tx.Query(ctx, `SELECT user_id::text,role FROM community.members WHERE community_id=$1 ORDER BY role,user_id`, c)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var uid, r string
			if err = rows.Scan(&uid, &r); err != nil {
				return nil, err
			}
			out = append(out, map[string]string{"user_id": uid, "role": r})
		}
		return out, rows.Err()
	case "member.set", "member.remove":
		if err = role(ctx, tx, c, user, "owner"); err != nil {
			return nil, err
		}
		var p struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		if p.UserID == user {
			return nil, fail(400, "use ownership transfer to change the owner")
		}
		if req.Action == "member.set" && !memberRole(p.Role) {
			return nil, fail(400, "invalid member role")
		}
		var tagErr error
		if req.Action == "member.remove" {
			_, tagErr = tx.Exec(ctx, `DELETE FROM community.members WHERE community_id=$1 AND user_id=$2 AND role<>'owner'`, c, p.UserID)
		} else {
			_, tagErr = tx.Exec(ctx, `UPDATE community.members SET role=$1 WHERE community_id=$2 AND user_id=$3 AND role<>'owner'`, p.Role, c, p.UserID)
		}
		if tagErr != nil {
			return nil, tagErr
		}
		return map[string]bool{"updated": true}, audit(ctx, tx, c, user, req.Action, p)
	case "invite.create":
		if err = role(ctx, tx, c, user, "owner"); err != nil {
			return nil, err
		}
		var p struct {
			Role  string `json:"role"`
			Hours int    `json:"hours"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		if p.Role == "" {
			p.Role = "contributor"
		}
		if !memberRole(p.Role) {
			return nil, fail(400, "invalid invitation role")
		}
		if p.Hours == 0 {
			p.Hours = 72
		}
		if p.Hours < 1 || p.Hours > 168 {
			return nil, fail(400, "invitation lifetime must be 1 to 168 hours")
		}
		if err = s.consume(ctx, tx, user, "invite", 20); err != nil {
			return nil, err
		}
		secret := "ri_" + randomToken()
		iid := id()
		exp := time.Now().UTC().Add(time.Duration(p.Hours) * time.Hour)
		_, err = tx.Exec(ctx, `INSERT INTO community.invites(id,community_id,secret_hash,role,expires_at) VALUES($1,$2,$3,$4,$5)`, iid, c, hash(secret), p.Role, exp)
		if err != nil {
			return nil, err
		}
		if err = audit(ctx, tx, c, user, req.Action, map[string]any{"invite_id": iid, "role": p.Role}); err != nil {
			return nil, err
		}
		return map[string]any{"id": iid, "token": secret, "expires_at": exp, "url": strings.TrimRight(s.Config.PublicURL, "/") + "/join#" + secret}, nil
	case "invite.list":
		if err = role(ctx, tx, c, user, "owner"); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `SELECT community.erase_invites($1,NULL)`, c); err != nil {
			return nil, err
		}
		rows, e := tx.Query(ctx, `SELECT id::text,role,expires_at,revoked,used_by::text FROM community.invites WHERE community_id=$1 ORDER BY expires_at DESC LIMIT 100`, c)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var iid, r string
			var exp time.Time
			var rev bool
			var used *string
			if err = rows.Scan(&iid, &r, &exp, &rev, &used); err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"id": iid, "role": r, "expires_at": exp, "revoked": rev, "used_by": used})
		}
		return out, rows.Err()
	case "invite.revoke":
		if err = role(ctx, tx, c, user, "owner"); err != nil {
			return nil, err
		}
		var p struct {
			ID string `json:"id"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `SELECT community.erase_invites($1,$2)`, c, p.ID)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"revoked": true}, nil
	case "submission.create":
		return s.submit(ctx, tx, user, l, req.Data)
	case "submission.list":
		if err = role(ctx, tx, c, user, "owner", "maintainer", "publisher", "contributor"); err != nil {
			return nil, err
		}
		var p struct {
			State string `json:"state"`
			Group string `json:"group"`
			After string `json:"after"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		if p.Group != "" && p.Group != "attention" && p.Group != "completed" {
			return nil, fail(400, "unknown review group")
		}
		if p.After != "" {
			if err = validUUID(p.After); err != nil {
				return nil, err
			}
		}
		rows, e := tx.Query(ctx, `SELECT id::text FROM community.submissions WHERE community_id=$1 AND ($2='' OR state=$2) AND ($3='' OR ($3='attention' AND state NOT IN ('accepted','rejected')) OR ($3='completed' AND state IN ('accepted','rejected'))) AND ($4='' OR id::text>$4) ORDER BY id LIMIT 101`, c, p.State, p.Group, p.After)
		if e != nil {
			return nil, e
		}
		ids := []string{}
		for rows.Next() {
			var sid string
			if err = rows.Scan(&sid); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, sid)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
		out := []kb.Submission{}
		next := ""
		if len(ids) > 100 {
			ids = ids[:100]
			next = ids[99]
		}
		for _, sid := range ids {
			v, e := readSubmission(ctx, tx, c, sid)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		if p.Group != "" {
			return map[string]any{"items": out, "next": next}, nil
		}
		return out, nil
	case "submission.get":
		var p struct {
			ID string `json:"id"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		return readSubmission(ctx, tx, c, p.ID)
	case "submission.review":
		return s.review(ctx, tx, user, l, req.Data)
	case "finding.get", "finding.history":
		var p struct {
			ID     string `json:"id"`
			Before int64  `json:"before"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		rows, e := tx.Query(ctx, `SELECT r.sequence,r.finding_id::text,r.id::text,r.deleted,r.document,COALESCE(r.author_id::text,''),r.status,r.reason,r.replacement_id,r.assessment_evidence FROM community.revisions r JOIN community.findings f ON f.community_id=r.community_id AND f.id=r.finding_id WHERE r.community_id=$1 AND r.finding_id=$2 AND ($3 OR r.id=f.revision) AND ($4=0 OR r.sequence<$4) ORDER BY r.sequence DESC LIMIT 100`, c, p.ID, req.Action == "finding.history", p.Before)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []kb.Change{}
		for rows.Next() {
			var v kb.Change
			var d, evidence []byte
			if err = rows.Scan(&v.Sequence, &v.FindingID, &v.Revision, &v.Deleted, &d, &v.Author, &v.Status, &v.Reason, &v.ReplacementID, &evidence); err != nil {
				return nil, err
			}
			json.Unmarshal(d, &v.Document)
			json.Unmarshal(evidence, &v.AssessmentEvidence)
			out = append(out, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
		for i := range out {
			out[i].Warnings, err = integrityAlerts(ctx, tx, l.ID, out[i].FindingID, out[i].Revision)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case "finding.withdraw":
		if err = role(ctx, tx, c, user, "owner", "maintainer"); err != nil {
			return nil, err
		}
		var p struct {
			ID       string `json:"id"`
			Revision string `json:"revision"`
			Reason   string `json:"reason"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Reason) == "" {
			return nil, fail(400, "withdrawal requires a reason")
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM community.libraries WHERE id=$1 FOR UPDATE`, c); err != nil {
			return nil, err
		}
		var current string
		if err = tx.QueryRow(ctx, `SELECT revision::text FROM community.findings WHERE community_id=$1 AND id=$2`, c, p.ID).Scan(&current); err != nil {
			return nil, err
		}
		if current != p.Revision {
			return nil, fail(409, "finding changed; refresh before withdrawing")
		}
		seq, e := nextSequence(ctx, tx, c)
		if e != nil {
			return nil, e
		}
		rid := id()
		_, err = tx.Exec(ctx, `INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,deleted,author_id) VALUES($1,$2,$3,$4,'{}',true,$5)`, rid, c, p.ID, seq, user)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `SELECT community.erase_finding($1,$2)`, c, p.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"revision": rid, "sequence": seq}, nil
	case "changes", "export":
		var p struct {
			Since            int64 `json:"since"`
			IntegrityVersion int   `json:"integrity_version"`
			Through          int64 `json:"through"`
			Limit            int   `json:"limit"`
		}
		if err = data(req.Data, &p); err != nil {
			return nil, err
		}
		if p.Since < 0 || p.Through < 0 {
			return nil, fail(400, "invalid cursor")
		}
		if p.Limit <= 0 || p.Limit > 500 {
			p.Limit = 200
		}
		if p.Through == 0 {
			p.Through = l.Sequence
		}
		if p.Through > l.Sequence || p.Since > p.Through {
			return nil, fail(409, "cursor is ahead of this community")
		}
		rows, e := tx.Query(ctx, `SELECT sequence,finding_id::text,id::text,deleted,document,status,reason,replacement_id,assessment_evidence FROM community.revisions WHERE community_id=$1 AND sequence>$2 AND sequence<=$3 ORDER BY sequence LIMIT $4`, c, p.Since, p.Through, p.Limit)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := kb.Changes{Changes: []kb.Change{}, Through: p.Through, Library: l}
		cursor := p.Since
		for rows.Next() {
			var v kb.Change
			var d, evidence []byte
			if err = rows.Scan(&v.Sequence, &v.FindingID, &v.Revision, &v.Deleted, &d, &v.Status, &v.Reason, &v.ReplacementID, &evidence); err != nil {
				return nil, err
			}
			json.Unmarshal(d, &v.Document)
			json.Unmarshal(evidence, &v.AssessmentEvidence)
			// Legacy offline clients do not understand assessments. They must
			// remove refuted/superseded documents instead of indexing them.
			if p.IntegrityVersion < 1 && (v.Status == "refuted" || v.Status == "superseded") {
				v.Deleted = true
				v.Document = kb.Document{}
			}
			out.Changes = append(out.Changes, v)
			cursor = v.Sequence
		}
		out.More = cursor < p.Through
		return out, rows.Err()
	case "query":
		return s.query(ctx, tx, l, req.Data)
	case "audit.list":
		if err = role(ctx, tx, c, user, "owner", "maintainer"); err != nil {
			return nil, err
		}
		rows, e := tx.Query(ctx, `SELECT id,actor_id::text,action,detail,created_at FROM community.audit WHERE community_id=$1 ORDER BY id DESC LIMIT 100`, c)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var aid int64
			var actor *string
			var action string
			var detail json.RawMessage
			var at time.Time
			if err = rows.Scan(&aid, &actor, &action, &detail, &at); err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"id": aid, "actor": actor, "action": action, "detail": detail, "created_at": at})
		}
		return out, rows.Err()
	case "usage":
		return s.usage(ctx, tx, c)
	default:
		return nil, fail(400, "unknown operation")
	}
}
func memberRole(r string) bool {
	return r == "maintainer" || r == "publisher" || r == "contributor" || r == "reader"
}
func (s *Server) redeem(ctx context.Context, user string, b json.RawMessage) (any, error) {
	if err := authenticated(user); err != nil {
		return nil, err
	}
	var p struct {
		Token string `json:"token"`
	}
	if err := data(b, &p); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var c, r, iid string
	err = tx.QueryRow(ctx, `SELECT id::text,community_id::text,role FROM community.invites WHERE secret_hash=$1 AND expires_at>now() AND NOT revoked AND used_by IS NULL FOR UPDATE`, hash(p.Token)).Scan(&iid, &c, &r)
	if err != nil {
		return nil, fail(404, "invitation expired, revoked, or already used")
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM community.libraries WHERE id=$1 FOR UPDATE`, c); err != nil {
		return nil, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM community.members WHERE community_id=$1`, c).Scan(&count); err != nil {
		return nil, err
	}
	if count >= s.limits().Members {
		return nil, fail(429, "community member limit reached")
	}
	_, err = tx.Exec(ctx, `INSERT INTO community.members(community_id,user_id,role) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c, user, r)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM community.invites WHERE id=$1`, iid)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM community.audit WHERE community_id=$1 AND action LIKE 'invite.%' AND (detail->>'invite_id'=$2 OR detail->>'id'=$2)`, c, iid); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]string{"community_id": c}, nil
}

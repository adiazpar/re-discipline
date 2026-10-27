package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/connectionauth"
	"github.com/jackc/pgx/v5"
)

const connectionLease = 180 * 24 * time.Hour

func (s *Server) readerIdentity(r *http.Request, req kb.Request) (principal, error) {
	var p principal
	h := r.Header.Get("Authorization")
	if h != "" {
		if r.Header.Get("Signature") != "" {
			return p, fail(401, "use one authentication method")
		}
		if subtle.ConstantTimeCompare([]byte(h), []byte("Bearer "+s.Config.OperatorToken)) == 1 {
			return principal{operator: true, userID: s.Config.OperatorID}, nil
		}
		return p, fail(401, "reader bearer credentials retired; reconnect using a project key")
	}
	if r.Header.Get("Signature") == "" {
		return p, nil
	}
	proof, err := connectionauth.Verify(r, s.Config.PublicURL, time.Now())
	if err != nil {
		return p, fail(401, "invalid request signature; check the device clock")
	}
	p.proof = proof
	var revoked, expired, visible, frozen bool
	columns := `c.id::text,c.community_id::text,c.revoked_at IS NOT NULL,c.expires_at<=now(),(NOT c.public_connection OR l.visibility='public'),l.deletion_id IS NOT NULL`
	values := []any{&p.tokenID, &p.communityID, &revoked, &expired, &visible, &frozen}
	if req.Action == "community.stats" {
		// Counters come from the same row/snapshot already used to authorize
		// this key. Statistics never scan knowledge or open a search index.
		columns += `,l.knowledge_count,l.knowledge_bytes`
		values = append(values, &p.stats.Documents, &p.stats.KnowledgeBytes)
	}
	err = s.DB.QueryRow(r.Context(), `SELECT `+columns+` FROM community.reader_connections c JOIN community.libraries l ON l.id=c.community_id WHERE c.public_key=$1`, proof.PublicKey).Scan(values...)
	if err == pgx.ErrNoRows {
		return p, nil
	}
	if err != nil {
		return principal{}, err
	}
	if req.Action == "community.connect" || req.Action == "community.leave" {
		return p, nil
	}
	if req.Community != p.communityID {
		return p, fail(404, "resource unavailable")
	}
	if frozen {
		return p, errCommunityLocked
	}
	if revoked || !visible {
		return p, fail(403, "connection revoked; request a new invitation")
	}
	if expired {
		p.tokenID = ""
		return p, nil
	}
	// Renew at most daily, not once per query. No background client heartbeat is
	// needed: using the connection keeps its lease alive.
	_, err = s.DB.Exec(r.Context(), `UPDATE community.reader_connections SET renewed_at=now(),expires_at=now()+interval '180 days' WHERE id=$1 AND renewed_at<now()-interval '1 day' AND revoked_at IS NULL AND expires_at>now()`, p.tokenID)
	return p, err
}
func (s *Server) consumeProof(ctx context.Context, p connectionauth.Proof) error {
	// Retain from database time for the entire maximum proof window. A DB/app
	// clock offset must not make pruning delete a still-usable proof early.
	result, err := s.DB.Exec(ctx, `INSERT INTO community.request_nonces(public_key,nonce,expires_at) VALUES($1,$2,now()+($3 * interval '1 second')) ON CONFLICT DO NOTHING`, p.PublicKey, p.Nonce, int((connectionauth.Lifetime+2*connectionauth.ClockSkew)/time.Second))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fail(409, "request signature already used; retry with a fresh signature")
	}
	return nil
}
func (s *Server) connectReader(ctx context.Context, p principal, req kb.Request) (any, error) {
	if req.Community == "" {
		return nil, fail(400, "select a community")
	}
	if !p.operator && p.proof.PublicKey == "" {
		return nil, fail(401, "a signed project key is required")
	}
	var input struct {
		Invitation string `json:"invitation"`
	}
	if len(req.Data) > 0 {
		if err := data(req.Data, &input); err != nil {
			return nil, err
		}
	}
	if len(input.Invitation) > 256 {
		return nil, fail(400, "invalid invitation")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var cid, visibility string
	err = tx.QueryRow(ctx, `SELECT id::text,visibility FROM community.libraries WHERE id::text=$1 OR slug=$1 FOR UPDATE`, req.Community).Scan(&cid, &visibility)
	if err != nil {
		return nil, err
	}
	if p.communityID != "" && p.communityID != cid {
		return nil, fail(404, "resource unavailable")
	}
	l, err := library(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if p.operator {
		l.Role = "owner"
		return map[string]any{"community": l}, tx.Commit(ctx)
	}
	var existing string
	var revoked, expired, public bool
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,revoked_at IS NOT NULL,expires_at<=now(),public_connection,expires_at FROM community.reader_connections WHERE public_key=$1 FOR UPDATE`, p.proof.PublicKey).Scan(&existing, &revoked, &expired, &public, &expires)
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	l.Role = "reader"
	if existing != "" && !revoked && !expired && (!public || visibility == "public") && (!public || input.Invitation == "") {
		// An invitation response lost in transit can safely be retried with the same
		// key. It never consumes a second invitation or allocates another identity.
		return map[string]any{"community": l, "connection_id": existing, "expires_at": expires}, tx.Commit(ctx)
	}
	label := "Public project"
	invited := false
	if input.Invitation != "" {
		var invitationID string
		err = tx.QueryRow(ctx, `DELETE FROM community.reader_invitations WHERE secret_hash=$1 AND community_id=$2 AND expires_at>now() RETURNING id::text,label`, hash(input.Invitation), cid).Scan(&invitationID, &label)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, fail(403, "invitation invalid, expired or already used")
			}
			return nil, err
		}
		invited = true
	}
	if !invited && revoked {
		return nil, fail(403, "connection revoked; request a new invitation")
	}
	if !invited && visibility != "public" {
		return nil, fail(404, "resource unavailable; private access requires an invitation")
	}
	if existing != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM community.reader_connections WHERE id=$1`, existing); err != nil {
			return nil, err
		}
	}
	if err = s.connectionCapacity(ctx, tx, cid); err != nil {
		return nil, err
	}
	connectionID := id()
	expires = time.Now().UTC().Add(connectionLease)
	_, err = tx.Exec(ctx, `INSERT INTO community.reader_connections(id,community_id,public_key,label,public_connection,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, connectionID, cid, p.proof.PublicKey, label, !invited, expires)
	if err != nil {
		return nil, err
	}
	return map[string]any{"community": l, "connection_id": connectionID, "expires_at": expires}, tx.Commit(ctx)
}
func (s *Server) connectionCapacity(ctx context.Context, tx pgx.Tx, cid string) error {
	// The library lock serializes connection/invitation creation for this community.
	// Count tombstones too; abandoned keys cannot bypass the retained-record bound.
	var count int
	err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM community.reader_connections WHERE community_id=$1)+(SELECT count(*) FROM community.reader_invitations WHERE community_id=$1)`, cid).Scan(&count)
	if err != nil {
		return err
	}
	if count >= setting("MAX_COMMUNITY_CONNECTIONS", 10000) {
		return fail(429, "community connection capacity reached; contact the operator")
	}
	return nil
}
func (s *Server) leaveReader(ctx context.Context, p principal, req kb.Request) (any, error) {
	if p.operator || p.proof.PublicKey == "" {
		return nil, fail(401, "disconnect requires the project's signed key")
	}
	if p.communityID != "" && p.communityID != req.Community {
		return nil, fail(404, "resource unavailable")
	}
	// A revoked key cannot erase its own revocation tombstone then rejoin publicly.
	_, err := s.DB.Exec(ctx, `DELETE FROM community.reader_connections WHERE public_key=$1 AND community_id::text=$2 AND revoked_at IS NULL`, p.proof.PublicKey, req.Community)
	return map[string]bool{"disconnected": true}, err
}
func (s *Server) manageAccess(ctx context.Context, req kb.Request) (any, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var cid string
	err = tx.QueryRow(ctx, `SELECT id::text FROM community.libraries WHERE (id::text=$1 OR slug=$1) AND owner_id=$2 FOR UPDATE`, req.Community, s.Config.OperatorID).Scan(&cid)
	if err != nil {
		return nil, err
	}
	var p struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Hours int    `json:"hours"`
	}
	if err = data(req.Data, &p); err != nil {
		return nil, err
	}
	var result any
	switch req.Action {
	case "access.create":
		if p.Label == "" {
			p.Label = "Invited project"
		}
		if len(p.Label) > 120 {
			return nil, fail(400, "label must be at most 120 characters")
		}
		if p.Hours == 0 {
			p.Hours = 168
		}
		if p.Hours < 1 || p.Hours > 720 {
			return nil, fail(400, "invitation hours must be between 1 and 720")
		}
		if err = s.connectionCapacity(ctx, tx, cid); err != nil {
			return nil, err
		}
		token, invitationID, expires := "ri_"+randomToken(), id(), time.Now().UTC().Add(time.Duration(p.Hours)*time.Hour)
		_, err = tx.Exec(ctx, `INSERT INTO community.reader_invitations(id,community_id,secret_hash,label,expires_at) VALUES($1,$2,$3,$4,$5)`, invitationID, cid, hash(token), p.Label, expires)
		if err != nil {
			return nil, err
		}
		result = map[string]any{"community_id": cid, "id": invitationID, "invitation": token, "expires_at": expires}
	case "access.list":
		rows, err := tx.Query(ctx, `SELECT id::text,label,public_connection,expires_at,public_key,CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at<=now() THEN 'expired' ELSE 'active' END AS status FROM community.reader_connections WHERE community_id=$1 UNION ALL SELECT id::text,label,false,expires_at,'','invitation' FROM community.reader_invitations WHERE community_id=$1 ORDER BY expires_at DESC LIMIT 10000`, cid)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var connectionID, label, status, publicKey string
			var public bool
			var expires time.Time
			if err = rows.Scan(&connectionID, &label, &public, &expires, &publicKey, &status); err != nil {
				return nil, err
			}
			items = append(items, map[string]any{"id": connectionID, "label": label, "public": public, "status": status, "expires_at": expires, "public_key": publicKey})
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
		result = items
	case "access.revoke":
		if validUUID(p.ID) != nil {
			return nil, fail(400, "select a connection or invitation ID")
		}
		_, err = tx.Exec(ctx, `UPDATE community.reader_connections SET revoked_at=now(),expires_at=now()+interval '180 days' WHERE id=$1 AND community_id=$2 AND revoked_at IS NULL`, p.ID, cid)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM community.reader_invitations WHERE id=$1 AND community_id=$2`, p.ID, cid); err != nil {
			return nil, err
		}
		result = map[string]bool{"revoked": true}
	}
	return result, tx.Commit(ctx)
}

// Prune in indexed, bounded batches even when nobody is connecting. PostgreSQL
// autovacuum reclaims the dead tuples for reuse; no per-request history is kept.
func (s *Server) pruneConnections(ctx context.Context) error {
	for _, table := range []string{"reader_invitations", "reader_connections", "request_nonces"} {
		key := "id"
		if table == "request_nonces" {
			key = "(public_key,nonce)"
		}
		for {
			result, err := s.DB.Exec(ctx, `DELETE FROM community.`+table+` WHERE `+key+` IN (SELECT `+strings.Trim(key, "()")+` FROM community.`+table+` WHERE expires_at<=now() ORDER BY expires_at LIMIT 10000 FOR UPDATE SKIP LOCKED)`)
			if err != nil {
				return err
			}
			if result.RowsAffected() < 10000 {
				break
			}
		}
	}
	return nil
}
func (s *Server) runConnectionMaintenance(ctx context.Context) {
	for ctx.Err() == nil {
		job, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.pruneConnections(job)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Error("connection cleanup", "error", err)
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

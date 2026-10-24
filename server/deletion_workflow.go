package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errCommunityLocked = fail(423, "community pending deletion; the operator can cancel to reopen it")

type deletionInput struct {
	Step    string `json:"step"`
	Ticket  string `json:"ticket"`
	Archive *bool  `json:"archive"`
	Confirm bool   `json:"confirm"`
	Chain   string `json:"chain"`
	Records int64  `json:"records"`
}

func ticketHash(ticket string) string {
	h := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(h[:])
}

func exactCommunity(c string) error {
	id, e := uuid.Parse(c)
	if e != nil || id.String() != c {
		return fail(400, "community.delete requires the exact community UUID; resolve its name first")
	}
	return nil
}

// Every step authenticates the server operator at the gateway and the actual
// owner here. No step implies consent to the next one; the skill must ask.
func (s *Server) deletionOperation(ctx context.Context, owner string, req kb.Request) (any, error) {
	if e := exactCommunity(req.Community); e != nil {
		return nil, e
	}
	var p deletionInput
	if len(req.Data) > 0 {
		if e := json.Unmarshal(req.Data, &p); e != nil {
			return nil, fail(400, "invalid deletion request")
		}
	}
	if p.Step == "" {
		p.Step = "prepare"
	}
	if p.Step == "confirm" {
		if !p.Confirm || len(p.Ticket) < 32 {
			return nil, fail(409, "explicit final confirmation and deletion ticket required")
		}
		return s.deleteCommunity(ctx, owner, req.Community, p.Ticket)
	}
	tx, e := s.begin(ctx, owner)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var name, actualOwner string
	var frozen *string
	e = tx.QueryRow(ctx, `SELECT name,owner_id::text,deletion_id::text FROM community.libraries WHERE id=$1 FOR UPDATE`, req.Community).Scan(&name, &actualOwner, &frozen)
	if e != nil {
		return nil, e
	}
	if actualOwner != owner {
		return nil, fail(403, "server operator ownership required")
	}
	var sid, phase, hash, chain string
	var archive bool
	var expires time.Time
	var records int64
	e = tx.QueryRow(ctx, `SELECT id::text,phase,ticket_hash,archive,expires_at,chain,records FROM community.deletion_sessions WHERE community_id=$1 FOR UPDATE`, req.Community).Scan(&sid, &phase, &hash, &archive, &expires, &chain, &records)
	if e != nil && e != pgx.ErrNoRows {
		return nil, e
	}
	if sid != "" && time.Now().After(expires) {
		if e = unlockDeletion(ctx, tx, req.Community); e != nil {
			return nil, e
		}
		sid, phase = "", ""
		frozen = nil
	}
	out := map[string]any{"community_id": req.Community, "name": name, "confirmation_required": true, "deleted": false}
	switch p.Step {
	case "prepare":
		if frozen != nil {
			return nil, fail(409, "community already pending deletion; inspect status, resume the archive, or cancel")
		}
		ticket := randomToken()
		sid = id()
		phase = "preview"
		archive = false
		e = tx.QueryRow(ctx, `INSERT INTO community.deletion_sessions(id,community_id,owner_id,ticket_hash,phase) VALUES($1,$2,$3,$4,'preview') ON CONFLICT(community_id) DO UPDATE SET id=excluded.id,ticket_hash=excluded.ticket_hash,phase='preview',expires_at=now()+interval '1 hour' RETURNING expires_at`, sid, req.Community, owner, ticketHash(ticket)).Scan(&expires)
		out["ticket"] = ticket
		out["choices"] = []string{"archive then delete", "delete without archive", "cancel"}
	case "status":
		if sid == "" {
			phase = "active"
		}
	case "cancel":
		e = unlockDeletion(ctx, tx, req.Community)
		phase = "active"
		out["confirmation_required"] = false
	case "lock":
		if sid == "" || hash != ticketHash(p.Ticket) || p.Archive == nil {
			return nil, fail(409, "valid ticket and explicit archive choice required")
		}
		if phase != "preview" {
			if archive != *p.Archive {
				return nil, fail(409, "cancel before changing the archive choice")
			}
			break // Lost-response retries do not reset the stream or invalidate verification.
		}
		archive = *p.Archive
		phase = "locked"
		e = tx.QueryRow(ctx, `UPDATE community.deletion_sessions SET archive=$2,phase='locked',expires_at=now()+interval '1 hour' WHERE id=$1 RETURNING expires_at`, sid, archive).Scan(&expires)
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE community.libraries SET deletion_id=$2 WHERE id=$1`, req.Community, sid)
	case "verify":
		if sid == "" || hash != ticketHash(p.Ticket) || !archive || (phase != "downloaded" && phase != "verified") || chain != p.Chain || records != p.Records {
			return nil, fail(409, "archive download and local verification must finish before deletion")
		}
		phase = "verified"
		e = tx.QueryRow(ctx, `UPDATE community.deletion_sessions SET phase='verified',expires_at=now()+interval '1 hour' WHERE id=$1 RETURNING expires_at`, sid).Scan(&expires)
	default:
		return nil, fail(400, "deletion step must be prepare, lock, status, cancel, verify, or confirm")
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	if p.Step == "lock" {
		s.cancelCommunityWorkWith(req.Community, errCommunityLocked)
		s.scheduler.dropCommunityWith(req.Community, errCommunityLocked)
	}
	out["session_id"], out["phase"], out["archive"], out["expires_at"] = sid, phase, archive, expires
	return out, nil
}

func unlockDeletion(ctx context.Context, tx pgx.Tx, cid string) error {
	if _, e := tx.Exec(ctx, `SELECT set_config('app.deletion_control',$1,true)`, cid); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE community.libraries SET deletion_id=NULL WHERE id=$1`, cid); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `DELETE FROM community.deletion_sessions WHERE community_id=$1`, cid)
	return e
}

func (s *Server) expireDeletionSessions(ctx context.Context) error {
	rows, e := s.DB.Query(ctx, `SELECT community_id::text FROM community.deletion_sessions WHERE expires_at<=now() LIMIT 100`)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var c string
		if e = rows.Scan(&c); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, c)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, c := range ids {
		tx, e := s.DB.Begin(ctx)
		if e != nil {
			return e
		}
		// Parent first, matching all workflow operations; recheck after acquiring it.
		_, e = tx.Exec(ctx, `SELECT id FROM community.libraries WHERE id=$1 FOR UPDATE`, c)
		var expired bool
		if e == nil {
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.deletion_sessions WHERE community_id=$1 AND expires_at<=now())`, c).Scan(&expired)
		}
		if e == nil && expired {
			e = unlockDeletion(ctx, tx, c)
		}
		if e == nil {
			e = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if e != nil {
			return e
		}
	}
	return nil
}

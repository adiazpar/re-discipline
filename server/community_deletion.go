package server

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/google/uuid"
)

var errCommunityDeleted = fail(404, "community deleted")

type deletionResult struct {
	CommunityID    string `json:"community_id"`
	Deleted        bool   `json:"deleted"`
	AlreadyAbsent  bool   `json:"already_absent"`
	CleanupPending bool   `json:"cleanup_pending"`
}

// The gateway permits only the operator. The SQL function independently checks
// ownership. A UUID-only target makes retries safe even after a slug is reused.
func (s *Server) deleteCommunity(ctx context.Context, owner, cid, ticket string) (deletionResult, error) {
	out := deletionResult{CommunityID: cid}
	parsed, parseErr := uuid.Parse(cid)
	if parseErr != nil || parsed.String() != cid {
		return out, fail(400, "community.delete requires the exact community UUID; resolve its name first")
	}
	release, err := s.lockIndex(ctx, cid, true)
	if err != nil {
		return out, err
	}
	defer release()
	tx, err := s.begin(ctx, owner)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('app.deletion_ticket_hash',$1,true)`, ticketHash(ticket)); err != nil {
		return out, err
	}
	var existed bool
	if err = tx.QueryRow(ctx, `SELECT community.erase_community($1)`, cid).Scan(&existed); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out.Deleted, out.AlreadyAbsent = true, !existed
	// Access and all canonical content are already gone. An interrupted cleanup
	// cannot roll back that deletion; report it honestly and let the worker retry.
	if err = s.finishCommunityDeletion(ctx, cid); err != nil {
		out.CleanupPending = true
		slog.Warn("community deletion cleanup pending", "community", cid, "error", err)
	}
	return out, nil
}

// Caller holds the community index write lock. No snapshot can be rebuilt after
// the parent row is removed: snapshot rechecks that row under the same lock.
func (s *Server) finishCommunityDeletion(ctx context.Context, cid string) error {
	s.cancelCommunityWork(cid)
	s.scheduler.dropCommunity(cid)
	root := filepath.Join(s.Config.CacheDir, cid)
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	advice := s.Semantic
	if advice == nil {
		// Disabling optional assistance must not strand advice from an earlier run.
		path := filepath.Join(s.Config.CacheDir, "semantic")
		if _, err := os.Stat(path); err == nil {
			advice = semantic.New(semantic.Config{}, path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if advice != nil {
		// The existing shared advice cache stores hashed namespaces. Conservatively
		// invalidate all derived advice, including in-flight writes; keep budgets.
		// Other communities' canonical data and native search indexes are untouched.
		if err := advice.Purge(ctx); err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM community.deletion_cleanup WHERE community_id=$1`, cid)
	return err
}

func (s *Server) cleanDeletedCommunities(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT community_id::text FROM community.deletion_cleanup ORDER BY created_at LIMIT 20`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var cid string
		if err = rows.Scan(&cid); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, cid)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, cid := range ids {
		release, e := s.lockIndex(ctx, cid, true)
		if e != nil {
			return e
		}
		e = s.finishCommunityDeletion(ctx, cid)
		release()
		if e != nil {
			return e
		}
	}
	return nil
}

func (s *Server) runDeletionCleanup(ctx context.Context) {
	for {
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.expireDeletionSessions(work)
		if err == nil {
			err = s.cleanDeletedCommunities(work)
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("community deletion cleanup", "error", err)
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

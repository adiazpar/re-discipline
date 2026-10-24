package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/semaphore"
)

const indexWriterWeight int64 = 1 << 30

type searchState struct {
	index *semaphore.Weighted
	refs  int
	work  map[*communityWork]struct{}
}

type communityWork struct{ cancel context.CancelCauseFunc }

func (s *Server) searchState(cid string) *searchState {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	state := s.searchStates[cid]
	if state == nil {
		state = &searchState{index: semaphore.NewWeighted(indexWriterWeight), work: map[*communityWork]struct{}{}}
		s.searchStates[cid] = state
	}
	state.refs++
	return state
}

func (s *Server) releaseSearchState(cid string, state *searchState) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	state.refs--
	if state.refs == 0 {
		delete(s.searchStates, cid)
	}
}

// Keep cancellable requests and background work registered across SQLite/DB
// reads and optional model calls. State disappears after its final user exits.
func (s *Server) trackCommunityWork(ctx context.Context, cid string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	state := s.searchState(cid)
	w := &communityWork{cancel}
	s.searchMu.Lock()
	state.work[w] = struct{}{}
	s.searchMu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel(nil)
			s.searchMu.Lock()
			delete(state.work, w)
			s.searchMu.Unlock()
			s.releaseSearchState(cid, state)
		})
	}
}

func (s *Server) cancelCommunityWork(cid string) { s.cancelCommunityWorkWith(cid, errCommunityDeleted) }
func (s *Server) cancelCommunityWorkWith(cid string, cause error) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if state := s.searchStates[cid]; state != nil {
		for w := range state.work {
			w.cancel(cause)
		}
	}
}
func (s *Server) lockIndex(ctx context.Context, cid string, write bool) (func(), error) {
	weight := int64(1)
	if write {
		weight = indexWriterWeight
	}
	state := s.searchState(cid)
	lock := state.index
	if err := lock.Acquire(ctx, weight); err != nil {
		s.releaseSearchState(cid, state)
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { lock.Release(weight); s.releaseSearchState(cid, state) }) }, nil
}
func (s *Server) snapshot(ctx context.Context, tx pgx.Tx, l kb.Library, force bool) (kb.Library, string, map[string]string, func(), error) {
	repair := force
	for {
		release, err := s.lockIndex(ctx, l.ID, force)
		if err != nil {
			return l, "", nil, nil, err
		}
		l, err = library(ctx, tx, l.ID)
		if err != nil {
			release()
			return l, "", nil, nil, err
		}
		libraryRoot := filepath.Join(s.Config.CacheDir, l.ID)
		root := filepath.Join(libraryRoot, strconv.FormatInt(l.Sequence, 10))
		marker := filepath.Join(root, "revisions.json")
		meta, readErr := s.metadata.read(marker)
		_, indexErr := os.Stat(filepath.Join(root, ".re-discipline", "index.db"))
		if !repair && readErr == nil && indexErr == nil {
			if force {
				release()
				force = false
				continue
			} // another waiter finished the build
			return l, root, meta, release, nil
		}
		if !force {
			release()
			force = true
			continue
		}
		err = s.buildSnapshot(ctx, tx, l, root)
		if err == nil {
			kb.PruneIndexes(libraryRoot, strconv.FormatInt(l.Sequence, 10))
		}
		release()
		if err != nil {
			return l, "", nil, nil, err
		}
		force = false
		repair = false
	}
}
func (s *Server) buildSnapshot(ctx context.Context, tx pgx.Tx, l kb.Library, root string) error {
	// Bound simultaneous cold builds across communities independently of queries.
	select {
	case s.buildSlots <- struct{}{}:
		defer func() { <-s.buildSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// Called with the community write lock. Never rewrite an index being searched.
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	docs := filepath.Join(root, ".re-discipline", "docs")
	if err := os.MkdirAll(docs, 0700); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (finding_id) finding_id::text,id::text,deleted,document,status FROM community.revisions WHERE community_id=$1 AND sequence<=$2 ORDER BY finding_id,sequence DESC`, l.ID, l.Sequence)
	if err != nil {
		return err
	}
	meta := map[string]string{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			rows.Close()
			return err
		}
		var fid, rid, status string
		var deleted bool
		var body []byte
		if err = rows.Scan(&fid, &rid, &deleted, &body, &status); err != nil {
			rows.Close()
			return err
		}
		if deleted || status == "refuted" || status == "superseded" {
			continue
		}
		var d kb.Document
		if err = json.Unmarshal(body, &d); err != nil {
			rows.Close()
			return err
		}
		if err = os.WriteFile(filepath.Join(docs, fid+".md"), []byte(d.Markdown), 0600); err != nil {
			rows.Close()
			return err
		}
		meta[fid] = rid
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, _, err = engine.BuildContext(ctx, root, filepath.Join(root, ".re-discipline", "index.db")); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "revisions.json"), raw(meta), 0600)
}

// Prepare newly published generations in the background. Only one rebuild runs
// here at a time; first reads can still repair a missing or damaged snapshot.
func (s *Server) runIndexWorker(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := s.warmOneIndex(jobCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("index preparation", "error", err)
			}
		}
	}
}
func (s *Server) warmOneIndex(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT id::text,sequence FROM community.libraries WHERE deletion_id IS NULL ORDER BY created_at LIMIT 1000`)
	if err != nil {
		return err
	}
	var selected string
	for rows.Next() {
		var cid string
		var sequence int64
		if err = rows.Scan(&cid, &sequence); err != nil {
			rows.Close()
			return err
		}
		root := filepath.Join(s.Config.CacheDir, cid, strconv.FormatInt(sequence, 10))
		if _, err = os.Stat(filepath.Join(root, "revisions.json")); os.IsNotExist(err) {
			selected = cid
			break
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if selected == "" {
		return nil
	}
	ctx, done := s.trackCommunityWork(ctx, selected)
	defer done()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	l, err := library(ctx, tx, selected)
	if err != nil {
		return err
	}
	_, _, _, release, err := s.snapshot(ctx, tx, l, false)
	if err != nil {
		return err
	}
	release()
	return tx.Commit(ctx)
}

package server

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func (s *Server) indexRoot(cid string) string {
	return filepath.Join(s.Config.CacheDir, cid, "current")
}

func progress(ctx context.Context, db *sql.DB, key string) string {
	var v string
	_ = db.QueryRowContext(ctx, `SELECT value FROM remote_progress WHERE key=?`, key).Scan(&v)
	return v
}

// One bounded batch per turn. The same engine indexes local and remote data;
// ingestion never rebuilds a whole corpus or occupies a reader scheduler slot.
func (s *Server) stepIndex(ctx context.Context, cid string) (bool, error) {
	select {
	case s.buildSlots <- struct{}{}:
		defer func() { <-s.buildSlots }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	ctx, done := s.trackCommunityWork(ctx, cid)
	defer done()
	var target, reset int64
	if err := s.DB.QueryRow(ctx, `SELECT sequence,reset_sequence FROM community.libraries WHERE id=$1 AND deletion_id IS NULL`, cid).Scan(&target, &reset); err != nil {
		return false, err
	}
	release, err := s.lockIndex(ctx, cid, true)
	if err != nil {
		return false, err
	}
	defer release()
	root := s.indexRoot(cid)
	if _, e := os.Stat(filepath.Join(root, "rebuild.request")); e == nil {
		p := filepath.Join(root, ".re-discipline", "index.db")
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if e = os.Remove(p + suffix); e != nil && !os.IsNotExist(e) {
				return false, e
			}
		}
		if e = os.Remove(filepath.Join(root, "rebuild.request")); e != nil {
			return false, e
		}
	}
	db, err := engine.OpenRemoteIndex(ctx, root)
	if err != nil {
		return false, err
	}
	defer db.Close()
	seq, _ := strconv.ParseInt(progress(ctx, db, "sequence"), 10, 64)
	if seq == target && progress(ctx, db, "sequence") != "" {
		return false, nil
	}
	if reset > seq && progress(ctx, db, "reset") != strconv.FormatInt(reset, 10) {
		if _, err = db.ExecContext(ctx, `DELETE FROM docs; DELETE FROM docmeta; DELETE FROM remote_records; DELETE FROM remote_progress; INSERT INTO remote_progress(key,value) VALUES('reset',?)`, reset); err != nil {
			return false, err
		}
		seq = 0
	}
	cursorGen, _ := strconv.ParseInt(progress(ctx, db, "cursor_generation"), 10, 64)
	cursorID := progress(ctx, db, "cursor_id")
	if cursorGen < seq {
		cursorGen = seq
		cursorID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	}
	if cursorID == "" {
		cursorID = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text,source_path,markdown,fingerprint,generation FROM community.documents WHERE community_id=$1 AND (generation,id)>($2,$3::uuid) AND generation<=$4 ORDER BY generation,id LIMIT 32`, cid, cursorGen, cursorID, target)
	if err != nil {
		return false, err
	}
	batch := []engine.RemoteRecord{}
	for rows.Next() {
		var r engine.RemoteRecord
		if err = rows.Scan(&r.ID, &r.Path, &r.Markdown, &r.Fingerprint, &cursorGen); err != nil {
			rows.Close()
			return false, err
		}
		r.Generation = cursorGen
		batch = append(batch, r)
		cursorID = r.ID
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, err
	}
	complete := len(batch) < 32
	committed := int64(-1)
	if complete {
		committed = target
	}
	if err = engine.ApplyRemoteRecords(ctx, db, batch, committed); err != nil {
		return false, err
	}
	if complete {
		_, err = db.ExecContext(ctx, `DELETE FROM remote_progress WHERE key IN ('cursor_generation','cursor_id')`)
	} else {
		_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO remote_progress(key,value) VALUES('cursor_generation',?),('cursor_id',?)`, cursorGen, cursorID)
	}
	return len(batch) > 0, err
}

func (s *Server) warmOneIndex(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT id::text FROM community.libraries WHERE deletion_id IS NULL ORDER BY id`)
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
	// Rotate service between communities; one busy import cannot monopolize indexing.
	for offset := 0; offset < len(ids); offset++ {
		i := (s.indexTurn + offset) % len(ids)
		worked, e := s.stepIndex(ctx, ids[i])
		if e != nil {
			return e
		}
		if worked {
			s.indexTurn = (i + 1) % len(ids)
			return nil
		}
	}
	return nil
}

func (s *Server) runIndexWorker(ctx context.Context) {
	for {
		started := time.Now()
		job, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.warmOneIndex(job)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("knowledge indexing", "error", err)
		}
		delay := 100 * time.Millisecond
		if s.scheduler.stats()["queued"].(int) > 0 {
			delay = max(delay, time.Since(started)*3)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

package search

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/adiazpar/re-discipline/retrieval/internal/indexdb"
)

// RemoteRecord stores the exact current text alongside its search entry. The
// caller holds the per-community reader/writer lock across queries and updates.
type RemoteRecord struct {
	ID, Path, Markdown, Fingerprint string
	Generation                      int64
}

func OpenRemoteIndex(ctx context.Context, root string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Join(root, ".re-discipline"), 0700); err != nil {
		return nil, err
	}
	p := IndexPath(root)
	if !schemaCurrent(p) {
		// Caller holds the community write lock. Derived data is disposable.
		os.Remove(p)
		os.Remove(p + "-wal")
		os.Remove(p + "-shm")
		if _, _, err := BuildIndexFileContext(ctx, root, p); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA cache_size=-4096; CREATE TABLE IF NOT EXISTS remote_records(id TEXT PRIMARY KEY,path TEXT,markdown TEXT,fingerprint TEXT,search_rowid INTEGER,generation INTEGER); CREATE TABLE IF NOT EXISTS remote_progress(key TEXT PRIMARY KEY,value TEXT)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// ApplyRemoteRecords shares the local engine's tokenizer and index insertion.
// Each bounded batch updates text and search entries in one SQLite transaction.
func ApplyRemoteRecords(ctx context.Context, db *sql.DB, records []RemoteRecord, sequence int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range records {
		p := "docs/" + r.ID + ".md"
		if _, err = tx.ExecContext(ctx, `DELETE FROM docs WHERE rowid=(SELECT search_rowid FROM remote_records WHERE id=?)`, r.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM docmeta WHERE path=?`, p); err != nil {
			return err
		}
		d := ParseDoc(p, r.Markdown)
		// The server validated this fingerprint on upload; preserve it with the
		// indexed text rather than recomputing it for every update or query.
		d.FindingID, d.Fingerprint = r.ID, r.Fingerprint
		rowID, e := insertDocument(ctx, tx, d)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO remote_records(id,path,markdown,fingerprint,search_rowid,generation) VALUES(?,?,?,?,?,?)`, r.ID, r.Path, r.Markdown, r.Fingerprint, rowID, r.Generation); err != nil {
			return err
		}
	}
	if sequence >= 0 {
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO remote_progress(key,value) VALUES('sequence',?)`, sequence); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ReadRemoteRecords(ctx context.Context, db *sql.DB, ids []string) (map[string]RemoteRecord, error) {
	out := map[string]RemoteRecord{}
	for _, id := range ids {
		var r RemoteRecord
		r.ID = id
		if err := indexdb.ReadRow(ctx, db, `SELECT path,markdown,fingerprint,generation FROM remote_records WHERE id=?`, id).Scan(&r.Path, &r.Markdown, &r.Fingerprint, &r.Generation); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, nil
}

func ReplaceFile(src, dst string) error { return replaceFile(src, dst) }

func ReadRemoteIndex(root string) (*sql.DB, error) {
	return indexdb.OpenRead(IndexPath(root))
}

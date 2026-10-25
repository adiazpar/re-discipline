package search

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
	_ "modernc.org/sqlite"
)

// SwapIndex publishes tmpPath over dstPath while preserving open readers
// where the platform supports it. Retry transient sharing conflicts; on
// persistent failure remove the temp file and report — callers treat this as
// non-fatal (the existing index keeps serving).
func SwapIndex(tmpPath, dstPath string) error {
	// Upload acceleration belongs to the same disposable index, but is not a
	// search table. Preserve it when a local search rebuild replaces the file.
	if db, e := sql.Open("sqlite", tmpPath); e == nil {
		db.SetMaxOpenConns(1)
		if _, e = os.Stat(dstPath); e == nil {
			if _, e = db.Exec(`ATTACH DATABASE ? AS previous`, dstPath); e == nil {
				for table, schema := range knowledge.CacheTables {
					var exists int
					if db.QueryRow(`SELECT count(*) FROM previous.sqlite_master WHERE name=?`, table).Scan(&exists) == nil && exists == 1 {
						if _, e = db.Exec(schema + `; INSERT OR REPLACE INTO ` + table + ` SELECT * FROM previous.` + table); e != nil {
							db.Close()
							return fmt.Errorf("preserve local bookkeeping: %w", e)
						}
					}
				}
				db.Exec(`DETACH DATABASE previous`)
			}
		}
		db.Close()
	}

	var err error
	for i := 0; i < 10; i++ {
		if err = replaceFile(tmpPath, dstPath); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Remove(tmpPath)
	return fmt.Errorf("could not swap index into place: %w", err)
}

// schemaCurrent reports whether an index database was built by a binary
// with the current index format. An index built by an older binary
// (missing tables, or older indexing content: no stripping, different
// tokenization) would otherwise serve wrong or failing queries until
// the corpus happened to change.
func schemaCurrent(dbPath string) bool {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM indexmeta WHERE key = 'format'`).Scan(&v); err != nil {
		return false // table missing (pre-versioning index) or unreadable
	}
	return v == indexFormatVersion
}

// EnsureFresh implements the binding auto-reindex rule: a query never
// fails or blocks because a reindex could not complete. It returns
// warnings only.
func EnsureFresh(root string) []string {
	var warnings []string
	dbPath := IndexPath(root)

	current, err := ScanDocs(root)
	if err != nil {
		return append(warnings, fmt.Sprintf("corpus scan failed: %v", err))
	}
	// The optional symbol corpus is manifest-tracked exactly like docs:
	// editing or deleting .re-discipline/symbols.jsonl must rebuild.
	if sm := ScanSymbols(root); sm != nil {
		current = append(current, *sm)
	}

	stale := false
	if stored, err := ReadManifest(dbPath); err != nil {
		// An unreadable manifest requests a rebuild, not deletion. Only the
		// lock holder may publish, and it replaces the old index atomically
		// after the new one is complete. A read error is not proof that the
		// existing index cannot serve other readers.
		stale = true
	} else if ManifestDiffers(stored, current) || !schemaCurrent(dbPath) {
		stale = true
	}
	if !stale {
		return warnings
	}

	release, ok := TryLock(root)
	if !ok {
		return append(warnings, "another process is rebuilding the index; serving existing index")
	}
	defer release()

	// Holding the lock, sweep temp debris from rebuilders that were
	// killed mid-build (only the owner ever removed its own temp).
	if leftovers, _ := filepath.Glob(filepath.Join(root, ".re-discipline", "index.tmp-*.db")); leftovers != nil {
		for _, l := range leftovers {
			os.Remove(l)
		}
	}
	os.Remove(filepath.Join(root, ".re-discipline", "index.db.build"))

	tmp := filepath.Join(root, ".re-discipline", fmt.Sprintf("index.tmp-%d.db", os.Getpid()))
	_, buildWarnings, err := BuildIndexFile(root, tmp)
	warnings = append(warnings, buildWarnings...)
	if err != nil {
		os.Remove(tmp)
		return append(warnings, fmt.Sprintf("index rebuild failed: %v; serving existing index if present", err))
	}
	if err := SwapIndex(tmp, dbPath); err != nil {
		return append(warnings, err.Error()+"; serving existing index")
	}
	return warnings
}

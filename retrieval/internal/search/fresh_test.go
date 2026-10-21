package search

import (
	"bytes"
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestTryLockExcludesSecondHolder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root+"/.re-discipline", 0o755); err != nil {
		t.Fatal(err)
	}
	release, ok := TryLock(root)
	if !ok {
		t.Fatal("first lock must succeed")
	}
	if _, ok2 := TryLock(root); ok2 {
		t.Fatal("second lock must fail while first is held")
	}
	release()
	release2, ok3 := TryLock(root)
	if !ok3 {
		t.Fatal("lock must be reacquirable after release")
	}
	release2()
}

func TestEnsureFreshBuildsAndHealsCorruption(t *testing.T) {
	root := buildTestCorpus(t)

	warnings := EnsureFresh(root)
	if _, err := os.Stat(IndexPath(root)); err != nil {
		t.Fatalf("index must exist after EnsureFresh: %v (warnings %v)", err, warnings)
	}

	// Corrupt the index; EnsureFresh must replace it with a complete rebuild.
	if err := os.WriteFile(IndexPath(root), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	EnsureFresh(root)
	if _, err := ReadManifest(IndexPath(root)); err != nil {
		t.Fatalf("index must be rebuilt after corruption: %v", err)
	}

	// Fresh index + unchanged corpus: EnsureFresh must be a no-op.
	before, _ := os.Stat(IndexPath(root))
	EnsureFresh(root)
	after, _ := os.Stat(IndexPath(root))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged corpus must not trigger rebuild")
	}
}

// An index built by an older binary (no indexmeta table, or an older
// format version) must be treated as stale even though the manifest
// still matches — otherwise stale term data serves forever.
func TestEnsureFreshRebuildsOutdatedSchema(t *testing.T) {
	root := buildTestCorpus(t)
	EnsureFresh(root)

	// Case 1: pre-versioning index (no indexmeta table at all).
	db, err := sql.Open("sqlite", IndexPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE indexmeta`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if schemaCurrent(IndexPath(root)) {
		t.Fatal("missing indexmeta must read as outdated")
	}
	EnsureFresh(root)
	if !schemaCurrent(IndexPath(root)) {
		t.Fatal("outdated schema must trigger a rebuild")
	}

	// Case 2: older format version.
	db, err = sql.Open("sqlite", IndexPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE indexmeta SET value = '0' WHERE key = 'format'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if schemaCurrent(IndexPath(root)) {
		t.Fatal("older format version must read as outdated")
	}
	EnsureFresh(root)
	if !schemaCurrent(IndexPath(root)) {
		t.Fatal("version bump must trigger a rebuild")
	}
}

func TestEnsureFreshLockBusyServesExisting(t *testing.T) {
	root := buildTestCorpus(t)
	EnsureFresh(root)
	// Make corpus stale, then hold the lock as "another process".
	writeDoc(t, root, "docs/engine/new.md", "# New fact\nbody")
	release, ok := TryLock(root)
	if !ok {
		t.Fatal("setup lock failed")
	}
	defer release()
	warnings := EnsureFresh(root) // must return promptly, not block or panic
	_ = warnings
	if _, err := ReadManifest(IndexPath(root)); err != nil {
		t.Fatalf("existing index must remain usable: %v", err)
	}
}

func TestEnsureFreshUnreadableManifestDoesNotDeleteWithoutLock(t *testing.T) {
	for _, cause := range []string{"corrupt", "missing manifest"} {
		t.Run(cause, func(t *testing.T) {
			root := buildTestCorpus(t)
			EnsureFresh(root)
			if cause == "corrupt" {
				if err := os.WriteFile(IndexPath(root), []byte("not a database"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := sql.Open("sqlite", IndexPath(root))
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`DROP TABLE manifest`)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(IndexPath(root))
			if err != nil {
				t.Fatal(err)
			}
			release, ok := TryLock(root)
			if !ok {
				t.Fatal("setup lock failed")
			}
			defer release()
			warnings := EnsureFresh(root)
			if !strings.Contains(strings.Join(warnings, "\n"), "another process is rebuilding") {
				t.Fatalf("expected busy warning, got %v", warnings)
			}
			after, err := os.ReadFile(IndexPath(root))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("reader without rebuild lock changed published index: %v", err)
			}
		})
	}
}

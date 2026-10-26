package search

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func holdExclusiveIndex(t *testing.T, root string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", IndexPath(root))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`BEGIN EXCLUSIVE`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return func() { db.Exec(`ROLLBACK`); db.Close() }
}

func TestLocalQueryWaitsForBriefBookkeepingWrite(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/joint.md", "# Joint binding\nBind entities to demon joints.\n")
	want, warnings, err := Query(root, "joint binding", 8)
	if err != nil || len(warnings) != 0 || len(want) == 0 {
		t.Fatal(want, warnings, err)
	}
	releaseProject, ok := TryLock(root)
	if !ok {
		t.Fatal("project lock")
	}
	defer releaseProject()
	release := holdExclusiveIndex(t, root)
	defer func() {
		if release != nil {
			release()
		}
	}()
	type result struct {
		hits     []Hit
		warnings []string
		err      error
	}
	done := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		hits, warnings, err := Query(root, "joint binding", 8)
		done <- result{hits, warnings, err}
	}()
	<-started
	select {
	case got := <-done:
		t.Fatalf("query failed before the short write finished: %+v", got)
	case <-time.After(250 * time.Millisecond):
	}
	release()
	release = nil
	select {
	case got := <-done:
		if got.err != nil || len(got.warnings) != 0 || !reflect.DeepEqual(got.hits, want) {
			t.Fatalf("query changed during bookkeeping: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("query did not resume")
	}
}

func TestIndexReadCancellationDuringLockWait(t *testing.T) {
	for _, kind := range []string{"query", "copies"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeDoc(t, root, "docs/joint.md", "# Joint binding\nBind entities to demon joints.\n")
			EnsureFresh(root)
			release := holdExclusiveIndex(t, root)
			defer func() {
				if release != nil {
					release()
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if kind == "query" {
					_, _, err := QuerySnapshotContext(ctx, root, "joint binding", QueryOptions{Limit: 8})
					done <- err
				} else {
					_, err := LookupCopies(ctx, root, []string{"00000000-0000-4000-8000-000000000001"})
					done <- err
				}
			}()
			time.Sleep(25 * time.Millisecond)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("wanted cancellation, got %v", err)
				}
			case <-time.After(time.Second):
				release()
				release = nil
				<-done
				t.Fatal("lock wait did not respond to cancellation within one second")
			}
		})
	}
}

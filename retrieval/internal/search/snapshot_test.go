package search

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSnapshotQueriesMatchMutableRankingWithoutRescanningDocuments(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, ".re-discipline", "docs")
	if err := os.MkdirAll(docs, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(docs, "claim.md")
	if err := os.WriteFile(p, []byte("---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Clock uses timescale\nThe timescale controls engine time."), 0600); err != nil {
		t.Fatal(err)
	}
	opts := QueryOptions{Limit: 8}
	if _, _, err := QuerySnapshot(root, "timescale", opts); err == nil {
		t.Fatal("missing snapshot must fail explicitly")
	}
	regular, _, err := QueryOpts(root, "timescale", opts)
	if err != nil {
		t.Fatal(err)
	}
	frozen, _, err := QuerySnapshot(root, "timescale", opts)
	if err != nil || len(frozen) != 1 || !reflect.DeepEqual(regular, frozen) {
		t.Fatalf("ranking differs: %v", err)
	}
	if err = os.Remove(p); err != nil {
		t.Fatal(err)
	}
	frozen, _, err = QuerySnapshot(root, "timescale", opts)
	if err != nil || len(frozen) != 1 {
		t.Fatal("snapshot queried the mutable document tree")
	}
	regular, _, err = QueryOpts(root, "timescale", opts)
	if err != nil || len(regular) != 0 {
		t.Fatal("ordinary queries lost freshness checks")
	}
	if err = os.WriteFile(IndexPath(root), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = QuerySnapshot(root, "timescale", opts); err == nil {
		t.Fatal("corrupt snapshot must fail explicitly")
	}
}

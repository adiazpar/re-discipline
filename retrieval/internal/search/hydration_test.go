package search

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/internal/indexdb"
)

// A WAL writer can replace a row after ranking but before snippet generation.
// The selected title, identity, fingerprint and body must all stay in the old
// read snapshot; the following query must see the new complete document.
func TestCandidateHydrationPinsReadSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writer, err := OpenRemoteIndex(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fid := "00000000-0000-4000-8000-000000000001"
	old := RemoteRecord{ID: fid, Path: "docs/" + fid + ".md", Markdown: "# Needle old title\nneedle old body", Fingerprint: strings.Repeat("a", 64), Generation: 1}
	if err := ApplyRemoteRecords(ctx, writer, []RemoteRecord{old}, 1); err != nil {
		t.Fatal(err)
	}
	want, warnings, err := QuerySnapshotContext(ctx, root, "needle", QueryOptions{Limit: 1})
	if err != nil || len(warnings) != 0 || len(want) != 1 {
		t.Fatal(want, warnings, err)
	}
	reader, err := indexdb.OpenRead(IndexPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	match := BuildMatch("needle")
	selected, err := rankCandidates(ctx, tx, "needle", match, QueryOptions{Limit: 1})
	if err != nil || len(selected) != 1 {
		t.Fatal(selected, err)
	}
	updated := old
	updated.Markdown = "# Needle new title\nneedle new body"
	updated.Fingerprint = strings.Repeat("b", 64)
	updated.Generation = 2
	if err := ApplyRemoteRecords(ctx, writer, []RemoteRecord{updated}, 2); err != nil {
		t.Fatal(err)
	}
	if err := hydrateCandidates(ctx, tx, match, selected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected[0].Hit, want[0]) {
		t.Fatalf("payload crossed snapshots: got %+v, want %+v", selected[0].Hit, want[0])
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := QuerySnapshotContext(ctx, root, "needle", QueryOptions{Limit: 1})
	if err != nil || len(warnings) != 0 || len(got) != 1 || got[0].Fingerprint != updated.Fingerprint || !strings.Contains(got[0].Title, "new title") || !strings.Contains(got[0].Snippet, "new body") {
		t.Fatalf("next query missed committed replacement: %+v, %v, %v", got, warnings, err)
	}
}

func TestHydrationPagesAndExplainAcrossBatches(t *testing.T) {
	root := t.TempDir()
	// More than two hydration batches, with unique bodies and metadata to
	// detect row-order mistakes when SQL returns results in a different order.
	for i := 0; i < 530; i++ {
		writeDoc(t, root, fmt.Sprintf("docs/%04d.md", i), fmt.Sprintf("---\nkind: fact\ngrade: direct\nbuild: build-%d\nevidence: [reports/%d.md]\n---\n# Needle %d\nneedle body %d\n", i, i, 530-i, i))
	}
	explained, warnings, err := Explain(root, "needle", 530)
	if err != nil || len(warnings) != 0 || len(explained.Rows) != 530 {
		t.Fatal(len(explained.Rows), warnings, err)
	}
	for _, limit := range []int{0, 1, 8, 32, 255, 256, 257, 513, 530, 600} {
		hits, warnings, err := Query(root, "needle", limit)
		wantLimit := limit
		if wantLimit <= 0 {
			wantLimit = 8
		}
		if err != nil || len(warnings) != 0 || len(hits) != min(wantLimit, len(explained.Rows)) {
			t.Fatalf("limit %d: %d results, %v, %v", limit, len(hits), warnings, err)
		}
		for i, hit := range hits {
			if !reflect.DeepEqual(hit, explained.Rows[i].Hit) || hit.Build == "" || len(hit.Evidence) != 1 || hit.TextDigest == "" {
				t.Fatalf("limit %d, result %d: incomplete/different payload %+v", limit, i, hit)
			}
		}
	}
	// Explain must still expose candidates below its normal page boundary.
	small, warnings, err := Explain(root, "needle", 8)
	if err != nil || len(warnings) != 0 || len(small.Rows) != 160 || small.Rows[159].Snippet == "" {
		t.Fatal("explanation truncated", len(small.Rows), warnings, err)
	}
	empty, warnings, err := Query(root, "absentword", 8)
	if err != nil || len(warnings) != 0 || len(empty) != 0 {
		t.Fatal("empty query", empty, warnings, err)
	}
}

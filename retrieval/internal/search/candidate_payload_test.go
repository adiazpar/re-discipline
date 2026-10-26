package search

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
)

// Wide equal-score documents exercise the candidate cutoff, while reverse IDs
// make insertion order observably different from path order. Metadata and
// snippets must still belong to each selected document after a replacement.
func TestCandidatePayloadKeepsFilteredTiesAndReplacements(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := OpenRemoteIndex(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	makeRecord := func(number int, kind, grade, body, updated string) RemoteRecord {
		fid := fmt.Sprintf("00000000-0000-4000-8000-%012d", number)
		markdown := fmt.Sprintf("---\nid: %s\nstatus: promoted\nkind: %s\ngrade: %s\ncreated_at: 2026-01-01T00:00:00Z\nknowledge_updated_at: %s\n---\n# Needle\n\n## Knowledge\n%s\n", fid, kind, grade, updated, body)
		return RemoteRecord{ID: fid, Path: "docs/" + fid + ".md", Markdown: markdown,
			Fingerprint: knowledge.KnowledgeFingerprint(knowledge.ProjectKnowledge(markdown).Markdown), Generation: 1}
	}
	var records []RemoteRecord
	// These short matches could exhaust the candidate page if the kind/grade
	// constraints were applied after candidate selection.
	for n := 0; n < 120; n++ {
		records = append(records, makeRecord(n+1, "reference", "inferred", "needle", "2026-01-01T00:00:00Z"))
	}
	body := "needle " + strings.Repeat("Supporting details remain attached to this finding. ", 400)
	var expected []RemoteRecord
	for n := 0; n < 140; n++ {
		record := makeRecord(900000-n, "fact", "direct", body, "2026-01-01T00:00:00Z")
		expected = append(expected, record)
		records = append(records, record)
	}
	if err = ApplyRemoteRecords(ctx, db, records, 1); err != nil {
		t.Fatal(err)
	}
	check := func(want []RemoteRecord, limit int) {
		t.Helper()
		hits, warnings, err := QuerySnapshotContext(ctx, root, "needle", QueryOptions{Kind: "fact", Grade: "direct", Limit: limit})
		if err != nil || len(warnings) != 0 || len(hits) != len(want) {
			t.Fatalf("filtered snapshot: %v, %v, %d results; want %d", err, warnings, len(hits), len(want))
		}
		seen := map[string]bool{}
		for i, hit := range hits {
			if hit.FindingID != want[i].ID || hit.Path != want[i].Path || hit.Fingerprint != want[i].Fingerprint || hit.Kind != "fact" || hit.Grade != "direct" || hit.Status != "promoted" || seen[hit.FindingID] {
				t.Fatalf("candidate %d lost ordering, identity or metadata: %+v", i, hit)
			}
			seen[hit.FindingID] = true
			if !strings.Contains(hit.Snippet, "«needle»") || !strings.Contains(hit.Snippet, "Supporting details") {
				t.Fatalf("candidate %d lost its highlighted body: %q", i, hit.Snippet)
			}
			parsed := ParseDoc(want[i].Path, want[i].Markdown)
			if hit.CreatedAt != parsed.CreatedAt || hit.KnowledgeUpdatedAt != parsed.KnowledgeUpdatedAt {
				t.Fatalf("candidate %d has stale dates: %+v", i, hit)
			}
		}
	}
	check(expected[:3], 3)
	replacement := makeRecord(900000, "fact", "direct", body, "2026-01-02T00:00:00Z")
	replacement.Generation = 2
	if err = ApplyRemoteRecords(ctx, db, []RemoteRecord{replacement}, 2); err != nil {
		t.Fatal(err)
	}
	// Replacing an equal-score row gives it a later rowid. The old entry must
	// disappear, and the new metadata must travel with the new entry.
	expected = append(expected[1:], replacement)
	check(expected[:3], 3)
	check(expected, 140)
}

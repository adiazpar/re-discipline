package community

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

const copyID = "00000000-0000-4000-8000-000000000001"

func copyMarkdown(id, body, evidence string) string {
	return fmt.Sprintf("---\nid: %s\nstatus: promoted\nkind: fact\ngrade: direct\nbuild: Doom 2016\n---\n# Clock behavior\n\n## Knowledge\n%s\n\n## Evidence\n%s\n", id, body, evidence)
}

func TestCopyGroupingPreservesIdentityContentAndLocations(t *testing.T) {
	local := Result{Source: "local", Path: "docs/clock.md", FindingID: copyID, Fingerprint: strings.Repeat("a", 64), Title: "Clock", Snippet: "Local knowledge"}
	remote := local
	remote.Source, remote.Path, remote.Service, remote.CommunityID = "doom", "", "https://example.test", "doom"
	for _, order := range [][]Result{{local, remote}, {remote, local}, {remote, remote, local, local}} {
		got, err := collapseCopies("unused", order, 8)
		if err != nil || len(got) != 1 || got[0].Source != "local" || len(got[0].Locations) != 2 || got[0].ContentDiffers {
			t.Fatalf("copies failed to collapse: %+v %v", got, err)
		}
	}
	changed := remote
	changed.Fingerprint = strings.Repeat("b", 64)
	got, _ := collapseCopies("", []Result{local, changed}, 8)
	if len(got) != 2 || !got[0].ContentDiffers || !got[1].ContentDiffers {
		t.Fatalf("different content was hidden: %+v", got)
	}
	other := remote
	other.FindingID = "00000000-0000-4000-8000-000000000002"
	got, _ = collapseCopies("", []Result{local, other}, 8)
	if len(got) != 2 || got[0].ContentDiffers || got[1].ContentDiffers {
		t.Fatal("distinct identities collapsed because their text matches", got)
	}
	// A future additional authorized location must not add a ranking vote.
	other = remote
	other.Service, other.CommunityID, other.Source = "https://another.test", "elsewhere", "another"
	got, _ = collapseCopies("", []Result{remote, other, local}, 8)
	if len(got) != 1 || len(got[0].Locations) != 3 || got[0].Source != "local" {
		t.Fatal("copy locations lost", got)
	}
	for _, broken := range []Result{{}, {FindingID: copyID}, {Fingerprint: local.Fingerprint}, {FindingID: "not-a-uuid", Fingerprint: local.Fingerprint}} {
		a, b := broken, broken
		a.Source, a.Path = "local", "docs/a.md"
		b.Source, b.Service = "doom", remote.Service
		got, _ = collapseCopies("", []Result{a, b}, 8)
		if len(got) != 2 {
			t.Fatal("unproven equivalence hid a result", got)
		}
	}
}

func TestBothGroupsEvidenceFreeCopiesBeforeLimitWithoutFetchingDocuments(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".re-discipline", "docs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	remote := []Result{}
	for n := 1; n <= 12; n++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		raw := copyMarkdown(id, fmt.Sprintf("Clock knowledge %d", n), "Local evidence never uploaded")
		path := fmt.Sprintf("docs/clock-%d.md", n)
		if err := os.WriteFile(filepath.Join(root, ".re-discipline", path), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		d, err := PrepareKnowledge(path, raw)
		if err != nil {
			t.Fatal(err)
		}
		remote = append(remote, Result{FindingID: d.ID, Fingerprint: d.Fingerprint, Title: "Clock behavior", Kind: "fact", Grade: "direct"})
	}
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Action != "query" {
			t.Errorf("dedup downloaded a document: %s", req.Action)
		}
		calls.Add(1)
		json.NewEncoder(w).Encode(QueryResult{Hits: remote})
	}))
	defer h.Close()
	if err := SaveSettings(root, Settings{Mode: "both", Connections: []Connection{{Alias: "doom", Service: h.URL, CommunityID: "doom"}}}); err != nil {
		t.Fatal(err)
	}
	query := func(mode string, limit int) QueryResult {
		t.Helper()
		got, err := Query(context.Background(), root, "Clock", mode, false, engine.Options{Limit: limit, Assistance: "off"})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, mode := range []string{"both", "local", "external"} {
		got := query(mode, 8)
		seen := map[string]bool{}
		if len(got.Hits) != 8 {
			t.Fatal("duplicates underfilled final page", got)
		}
		for _, h := range got.Hits {
			if seen[h.FindingID] {
				t.Fatal("copy consumed another result slot", got)
			}
			seen[h.FindingID] = true
			if mode == "both" && (h.Source != "local" || len(h.Locations) != 2 || h.ContentDiffers) {
				t.Fatal("local copy or provenance missing", h)
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatal("extra requests for deduplication", calls.Load())
	}
	// Evidence-only edits and renames keep the group; real edits select one copy.
	path := filepath.Join(dir, "clock-1.md")
	for _, edit := range []struct {
		body string
		want int
	}{{"Clock knowledge 1", 12}, {"Clock behavior changed to 120 Hz", 12}} {
		if err := os.WriteFile(path, []byte(copyMarkdown(copyID, edit.body, "Changed evidence")), 0600); err != nil {
			t.Fatal(err)
		}
		got := query("both", 32)
		if len(got.Hits) != edit.want {
			t.Fatalf("edit grouping: %d want %d", len(got.Hits), edit.want)
		}
		for _, h := range got.Hits {
			if h.FindingID == copyID && h.ContentDiffers != (strings.Contains(edit.body, "120 Hz")) {
				t.Fatal("incorrect differing-copy state", h)
			}
		}
	}
	if err := os.WriteFile(path, []byte(copyMarkdown(copyID, "Clock knowledge 1", "Evidence")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(dir, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	if got := query("both", 32); len(got.Hits) != 12 {
		t.Fatal("rename lost identity", got)
	}
	if err := os.Remove(filepath.Join(dir, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	got := query("both", 32)
	for _, h := range got.Hits {
		if h.FindingID == copyID && (h.Source != "doom" || len(h.Locations) != 1) {
			t.Fatal("deleted local copy shadowed remote", h)
		}
	}
}

func TestCopyIdentityBelongsToIndexedText(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".re-discipline", "docs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "clock.md")
	before := copyMarkdown(copyID, "Clock runs at 60 Hz", "Evidence")
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	first, _, err := engine.Query(root, "Clock", engine.Options{})
	if err != nil || len(first) != 1 {
		t.Fatalf("initial: %+v %v", first, err)
	}
	after := copyMarkdown(copyID, "Clock runs at 120 Hz", "Evidence changed")
	if err := os.WriteFile(path, []byte(after), 0600); err != nil {
		t.Fatal(err)
	}
	stale, _, err := engine.QuerySnapshot(root, "Clock", engine.Options{})
	if err != nil || len(stale) != 1 || stale[0].Fingerprint != first[0].Fingerprint {
		t.Fatalf("identity detached from indexed text: %+v %v", stale, err)
	}
	fresh, _, err := engine.Query(root, "Clock", engine.Options{})
	if err != nil || len(fresh) != 1 || fresh[0].Fingerprint == first[0].Fingerprint || fresh[0].FindingID != copyID {
		t.Fatalf("changed identity not refreshed: %+v %v", fresh, err)
	}
}

func BenchmarkCollapseCopies256(b *testing.B) {
	hits := make([]Result, 0, 256)
	for i := 1; i <= 128; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		fp := fmt.Sprintf("%064x", i)
		hits = append(hits, Result{Source: "local", Path: fmt.Sprintf("docs/%d.md", i), FindingID: id, Fingerprint: fp}, Result{Source: "remote", Service: "https://example.test", CommunityID: "doom", FindingID: id, Fingerprint: fp})
	}
	b.ReportAllocs()
	for b.Loop() {
		got, err := collapseCopies("", hits, 128)
		if err != nil || len(got) != 128 {
			b.Fatal("bad grouping", err)
		}
	}
}

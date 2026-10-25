package community

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func benchmarkCorpus(b *testing.B, count int) (string, []Result) {
	b.Helper()
	root := b.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".re-discipline", "docs"), 0700); err != nil {
		b.Fatal(err)
	}
	remote := []Result{}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		term := fmt.Sprintf("symbol_%d", i)
		if i < 32 {
			term = "clockprobe"
		}
		path := fmt.Sprintf("docs/finding-%05d.md", i)
		raw := fmt.Sprintf("---\nid: %s\nstatus: promoted\nkind: fact\ngrade: direct\nidents: [%s]\n---\n# %s\n\n## Knowledge\n%s\n\n## Evidence\nLocal investigation notes.\n", id, term, term, strings.Repeat("Clock knowledge for this build. ", 16))
		if err := os.WriteFile(filepath.Join(root, ".re-discipline", path), []byte(raw), 0600); err != nil {
			b.Fatal(err)
		}
		if i < 32 {
			d, err := PrepareKnowledge(path, raw)
			if err != nil {
				b.Fatal(err)
			}
			remote = append(remote, Result{FindingID: d.ID, Fingerprint: d.Fingerprint, Sequence: 1, Title: term, Snippet: "Clock knowledge for this build.", Kind: "fact", Grade: "direct"})
		}
	}
	return root, remote
}

func BenchmarkRetrievalPaths10000(b *testing.B) {
	root, hits := benchmarkCorpus(b, 10000)
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		var p struct {
			CompareIDs []string `json:"compare_ids"`
		}
		json.Unmarshal(req.Data, &p)
		wanted := map[string]bool{}
		for _, id := range p.CompareIDs {
			wanted[id] = true
		}
		compared := []KnowledgeEntry{}
		for _, h := range hits {
			if wanted[h.FindingID] {
				compared = append(compared, KnowledgeEntry{ID: h.FindingID, Fingerprint: h.Fingerprint, Sequence: 1})
			}
		}
		json.NewEncoder(w).Encode(QueryResult{Hits: hits, Compared: compared, ComparisonComplete: true})
	}))
	defer h.Close()
	if err := SaveSettings(root, Settings{Mode: "both", Connections: []Connection{{Alias: "remote", Service: h.URL, CommunityID: "00000000-0000-4000-8000-000000100001"}}}); err != nil {
		b.Fatal(err)
	}
	if _, _, err := engine.Query(root, "clockprobe", engine.Options{Limit: 8}); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"local", "external", "both"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r, err := Query(context.Background(), root, "clockprobe", mode, false, engine.Options{Limit: 8, Assistance: "off"})
				if err != nil || len(r.Hits) != 8 {
					b.Fatalf("query failed: %v %+v", err, r)
				}
			}
		})
	}
	for _, count := range []int{4, 16} {
		links := []Connection{}
		for n := 0; n < count; n++ {
			links = append(links, Connection{Alias: fmt.Sprintf("community-%d", n), Service: h.URL, CommunityID: fmt.Sprintf("00000000-0000-4000-8000-%012d", n+100001)})
		}
		if err := SaveSettings(root, Settings{Mode: "both", Connections: links}); err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("both_%d_communities", count), func(b *testing.B) {
			if _, err := Query(context.Background(), root, "clockprobe", "both", false, engine.Options{Limit: 8, Assistance: "off"}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				out, err := Query(context.Background(), root, "clockprobe", "both", false, engine.Options{Limit: 8, Assistance: "off"})
				if err != nil || len(out.Hits) != 8 || len(out.Hits[0].Locations) != count+1 {
					b.Fatal("duplicate copies affected results", out, err)
				}
			}
		})
	}

}

func BenchmarkBuildIndex10000(b *testing.B) {
	root, _ := benchmarkCorpus(b, 10000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p := filepath.Join(root, ".re-discipline", "benchmark.db")
		if _, _, err := engine.Build(root, p); err != nil {
			b.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseFinding20KiB(b *testing.B) {
	raw := "---\nid: 00000000-0000-4000-8000-000000000001\nstatus: promoted\nkind: fact\n---\n# Clock\n\n## Knowledge\n" + strings.Repeat("Known behavior. ", 700) + "\n## Evidence\n" + strings.Repeat("Local evidence. ", 700)
	b.ReportAllocs()
	for b.Loop() {
		engine.Parse("docs/clock.md", raw)
	}
}

func BenchmarkUploadScan10000(b *testing.B) {
	root, _ := benchmarkCorpus(b, 10000)
	entries := make([]KnowledgeEntry, 0, 10000)
	paths, err := filepath.Glob(filepath.Join(root, ".re-discipline", "docs", "*.md"))
	if err != nil {
		b.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			b.Fatal(err)
		}
		d, err := PrepareKnowledge("docs/"+filepath.Base(p), string(raw))
		if err != nil {
			b.Fatal(err)
		}
		entries = append(entries, KnowledgeEntry{ID: d.ID, Path: d.Path, Fingerprint: d.Fingerprint})
		if err := os.Chtimes(p, old, old); err != nil {
			b.Fatal(err)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	cid := "00000000-0000-4000-8000-000000100001"
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			b.Error(err)
			return
		}
		switch req.Action {
		case "community.get":
			json.NewEncoder(w).Encode(Library{ID: cid, Role: "owner"})
		case "knowledge.inventory":
			var p struct{ After string }
			if err := json.Unmarshal(req.Data, &p); err != nil {
				b.Error(err)
				return
			}
			start := sort.Search(len(entries), func(i int) bool { return entries[i].ID > p.After })
			end := min(start+500, len(entries))
			page := KnowledgePage{Items: entries[start:end], Sequence: 1}
			if end < len(entries) {
				page.Next = entries[end-1].ID
			}
			json.NewEncoder(w).Encode(page)
		default:
			b.Errorf("unchanged scan attempted %s", req.Action)
			w.WriteHeader(400)
		}
	}))
	defer h.Close()
	b.Setenv("RE_DISCIPLINE_SERVICE", h.URL)
	b.Setenv("RE_DISCIPLINE_TOKEN", "benchmark-owner")
	conn := Connection{Alias: "doom", Service: h.URL, CommunityID: cid}
	if r, err := UploadKnowledge(context.Background(), root, conn, UploadOptions{}, true); err != nil || !r.Complete {
		b.Fatalf("seed cache: %+v %v", r, err)
	}
	// Stamping is a one-time source change; stabilize stat hints before measuring.
	for _, p := range paths {
		if err := os.Chtimes(p, old, old); err != nil {
			b.Fatal(err)
		}
	}
	if r, err := UploadKnowledge(context.Background(), root, conn, UploadOptions{}, true); err != nil || !r.Complete {
		b.Fatal(r, err)
	}
	// A search rebuild must preserve the upload cache when the index format changes.
	if _, _, err := engine.Query(root, "clockprobe", engine.Options{}); err != nil {
		b.Fatal(err)
	}
	for _, verify := range []bool{false, true} {
		b.Run(fmt.Sprintf("verify=%v", verify), func(b *testing.B) {
			b.ReportAllocs()
			var read int
			for b.Loop() {
				r, err := UploadKnowledge(context.Background(), root, conn, UploadOptions{Verify: verify}, true)
				if err != nil || !r.Complete || r.Uploaded != 0 || r.Unchanged != 10000 {
					b.Fatalf("scan: %+v %v", r, err)
				}
				read = r.Read
				if (!verify && read != 0) || (verify && read != 10000) {
					b.Fatalf("unexpected rereads: %d", read)
				}
			}
			b.ReportMetric(float64(read), "files_read/op")
		})
	}
}

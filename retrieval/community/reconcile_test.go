package community

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func TestBothResolvesCopiesBeforeRanking(t *testing.T) {
	cases := []struct {
		name, local, remote, wantSource, reason string
		baseline                                bool
		ack                                     int64
		sequence                                int64
	}{
		{"identical", "quartzprobe original", "quartzprobe original", "local", "", true, 0, 1},
		{"local edit", "quartzprobe corrected", "quartzprobe original", "local", "local_changed", true, 0, 1},
		{"remote edit", "quartzprobe original", "quartzprobe corrected", "doom", "remote_changed", true, 0, 2},
		{"both edited", "quartzprobe local correction", "quartzprobe remote correction", "local", "local_preferred_diverged", true, 0, 2},
		{"unknown baseline", "quartzprobe local", "quartzprobe remote", "local", "local_preferred_no_baseline", false, 0, 2},
		{"obsolete remote keyword", "replaced terminology", "quartzprobe original", "", "", true, 0, 1},
		{"obsolete local keyword", "quartzprobe original", "replaced terminology", "", "", true, 0, 2},
		{"upload ahead of index", "quartzprobe uploaded", "quartzprobe original", "local", "remote_index_behind", true, 3, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, ".re-discipline", "docs", "clock.md")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			write := func(body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(copyMarkdown(copyID, body, "Evidence")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			remoteBody := "quartzprobe original"
			sequence := int64(1)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				var req Request
				json.NewDecoder(r.Body).Decode(&req)
				if req.Action != "query" {
					t.Errorf("unexpected body fetch: %s", req.Action)
					return
				}
				var p struct {
					Query      string
					CompareIDs []string `json:"compare_ids"`
				}
				json.Unmarshal(req.Data, &p)
				d, _ := PrepareKnowledge("docs/clock.md", copyMarkdown(copyID, remoteBody, ""))
				result := QueryResult{ComparisonComplete: true}
				for _, id := range p.CompareIDs {
					if id == copyID {
						result.Compared = append(result.Compared, KnowledgeEntry{ID: id, Fingerprint: d.Fingerprint, Sequence: sequence})
					}
				}
				if strings.Contains(remoteBody, p.Query) {
					result.Hits = []Result{{FindingID: copyID, Fingerprint: d.Fingerprint, Sequence: sequence, Title: "Clock", Snippet: remoteBody, Kind: "fact", KnowledgeUpdatedAt: "2099-01-01T00:00:00Z"}}
				}
				json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			conn := Connection{Alias: "doom", Service: server.URL, CommunityID: "00000000-0000-4000-8000-000000100001"}
			if err := SaveSettings(root, Settings{Mode: "both", Connections: []Connection{conn}}); err != nil {
				t.Fatal(err)
			}
			query := func() QueryResult {
				t.Helper()
				r, e := Query(ctx, root, "quartzprobe", "both", false, engine.Options{Assistance: "off"})
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			if tc.baseline {
				write("quartzprobe original")
				if r := query(); len(r.Hits) != 1 {
					t.Fatal(r)
				}
			}
			write(tc.local)
			mu.Lock()
			remoteBody, sequence = tc.remote, tc.sequence
			mu.Unlock()
			if tc.ack > 0 {
				d, _ := PrepareKnowledge("docs/clock.md", copyMarkdown(copyID, tc.local, ""))
				if e := rememberCopies(ctx, root, conn, []KnowledgeEntry{{ID: copyID, Fingerprint: d.Fingerprint, Sequence: tc.ack}}); e != nil {
					t.Fatal(e)
				}
			}
			r := query()
			if tc.wantSource == "" {
				if len(r.Hits) != 0 {
					t.Fatalf("obsolete text survived: %+v", r.Hits)
				}
			} else {
				if len(r.Hits) != 1 || r.Hits[0].Source != tc.wantSource || r.Hits[0].Resolution != tc.reason || r.Hits[0].ContentDiffers != (tc.reason != "") {
					t.Fatalf("wrong winner: %+v", r.Hits)
				}
				if tc.reason != "" && len(r.Hits[0].OtherCopies) != 1 {
					t.Fatal("missing alternate metadata", r.Hits)
				}
			}
			mu.Lock()
			gotCalls := calls
			mu.Unlock()
			want := 1
			if tc.baseline {
				want++
			}
			if gotCalls != want {
				t.Fatal("extra network requests", gotCalls)
			}
			if tc.ack > 0 {
				bases, e := readCopyBases(ctx, root, conn, []string{copyID})
				if e != nil || bases[copyID].Sequence != tc.ack {
					t.Fatal("upload baseline rolled back", bases, e)
				}
			}
		})
	}
}

func TestMatchingBaselineSurvivesRebuildAndScopesConnection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, ".re-discipline", "docs", "clock.md")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte(copyMarkdown(copyID, "clock", "")), 0600)
	if _, _, e := engine.Query(root, "clock", engine.Options{}); e != nil {
		t.Fatal(e)
	}
	conn := Connection{Service: "https://example.test", CommunityID: "doom"}
	a := KnowledgeEntry{ID: copyID, Fingerprint: strings.Repeat("a", 64), Sequence: 3}
	if e := rememberCopies(ctx, root, conn, []KnowledgeEntry{a}); e != nil {
		t.Fatal(e)
	}
	// A late response with a lower sequence (even with identical current content)
	// must not erase the newer upload acknowledgement.
	b := a
	b.Sequence = 2
	b.Fingerprint = strings.Repeat("b", 64)
	if e := rememberCopies(ctx, root, conn, []KnowledgeEntry{b}); e != nil {
		t.Fatal(e)
	}
	db, _ := sql.Open("sqlite", filepath.Join(root, ".re-discipline", "index.db"))
	_, e := db.Exec(`UPDATE indexmeta SET value='0' WHERE key='format'`)
	db.Close()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e := engine.Query(root, "clock", engine.Options{}); e != nil {
		t.Fatal(e)
	}
	bases, e := readCopyBases(ctx, root, conn, []string{copyID})
	if e != nil || bases[copyID].Fingerprint != a.Fingerprint {
		t.Fatal(bases, e)
	}
	for _, other := range []Connection{{Service: conn.Service, CommunityID: "godzilla"}, {Service: "https://other.test", CommunityID: conn.CommunityID}} {
		bases, e := readCopyBases(ctx, root, other, []string{copyID})
		if e != nil || len(bases) != 0 {
			t.Fatal("cross-connection baseline", bases, e)
		}
	}
	os.Remove(filepath.Join(root, ".re-discipline", "index.db"))
	if _, _, e := engine.Query(root, "clock", engine.Options{}); e != nil {
		t.Fatal(e)
	}
	bases, e = readCopyBases(ctx, root, conn, []string{copyID})
	if e != nil || len(bases) != 0 {
		t.Fatal("cache loss did not reset safely", bases, e)
	}
	if source, _ := copyChoice("local", "remote", 4, bases[copyID]); source != "local" {
		t.Fatal("unsafe fallback")
	}
}

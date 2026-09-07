package community

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/google/uuid"
)

func portable() Document {
	return Document{SourcePath: "docs/time.md", Kind: "fact", Grade: "direct", Build: "DOOM 2016", Markdown: "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Engine clock uses timescale\nThe engine clock responds to timescale.", Evidence: []Evidence{{Label: "Clock observation", Excerpt: "The clock changed at the observed rate."}}}
}
func TestOfflineSyncWithdrawAndAccessRevocation(t *testing.T) {
	root := t.TempDir()
	cid, fid, rid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	state := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Action == "finding.relations" {
			json.NewEncoder(w).Encode([]Relation{})
			return
		}
		if req.Action != "changes" {
			t.Error(req.Action)
		}
		if state == 2 {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]string{"error": "access revoked"})
			return
		}
		var p struct {
			Since int64 `json:"since"`
		}
		json.Unmarshal(req.Data, &p)
		page := Changes{Library: Library{ID: cid}, Through: 1, Changes: []Change{}}
		if state == 0 && p.Since == 0 {
			page.Changes = append(page.Changes, Change{Sequence: 1, FindingID: fid, Revision: rid, Document: portable()})
		}
		if state == 1 {
			page.Through = 2
			if p.Since < 2 {
				page.Changes = append(page.Changes, Change{Sequence: 2, FindingID: fid, Revision: uuid.NewString(), Deleted: true})
			}
		}
		json.NewEncoder(w).Encode(page)
	}))
	defer h.Close()
	c, e := NewClient(h.URL)
	if e != nil {
		t.Fatal(e)
	}
	conn := Connection{Alias: "doom", Service: h.URL, CommunityID: cid}
	if _, e = c.Sync(context.Background(), root, conn); e != nil {
		t.Fatal(e)
	}
	hits, _, e := CachedQuery(root, conn, "timescale", engine.Options{})
	if e != nil || len(hits) != 1 {
		t.Fatalf("cache query: %v %v", hits, e)
	}
	if e = SaveSettings(root, Settings{Mode: "both", Connections: []Connection{conn}}); e != nil {
		t.Fatal(e)
	}
	r, e := Query(context.Background(), root, "timescale", "external", true, engine.Options{})
	if e != nil || len(r.Hits) != 1 {
		t.Fatalf("offline retrieval: %+v %v", r, e)
	}
	state = 1
	if _, e = c.Sync(context.Background(), root, conn); e != nil {
		t.Fatal(e)
	}
	hits, _, e = CachedQuery(root, conn, "timescale", engine.Options{})
	if e != nil || len(hits) != 0 {
		t.Fatalf("withdrawn finding remained: %v %v", hits, e)
	}
	state = 2
	if _, e = c.Sync(context.Background(), root, conn); e == nil {
		t.Fatal("revocation ignored")
	}
	if _, _, e = CachedQuery(root, conn, "timescale", engine.Options{}); e == nil {
		t.Fatal("revoked cache still accessible")
	}
}
func TestPreparationExcludesOpsAndTraversal(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(filepath.Join(base, "ops"), 0700)
	os.WriteFile(filepath.Join(base, "ops", "daemon.md"), []byte(portable().Markdown), 0600)
	os.WriteFile(filepath.Join(base, "time.md"), []byte(portable().Markdown), 0600)
	os.WriteFile(filepath.Join(root, "outside.md"), []byte(portable().Markdown), 0600)
	for _, p := range []string{"docs/ops/daemon.md", "../outside.md"} {
		if _, _, e := Prepare(root, Connection{}, p, "build"); e == nil {
			t.Errorf("allowed %s", p)
		}
	}
	d, _, e := Prepare(root, Connection{Alias: "doom"}, "docs/time.md", "DOOM 2016")
	if e != nil {
		t.Fatal(e)
	}
	if d.State != "draft" {
		t.Fatal(d.State)
	}
	if _, e = Queue(root, d.ID); e == nil {
		t.Fatal("missing evidence should block queue")
	}
	d.Document.Evidence = portable().Evidence
	atomicJSON(draftPath(root, d.ID), d)
	if _, e = Queue(root, d.ID); e != nil {
		t.Fatal(e)
	}
}
func TestOfflineQueueIdempotency(t *testing.T) {
	root := t.TempDir()
	calls := 0
	sid := uuid.NewString()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Action != "submission.create" {
			t.Error(req.Action)
		}
		json.NewEncoder(w).Encode(Submission{ID: sid, State: "queued"})
	}))
	defer h.Close()
	d := Draft{ID: uuid.NewString(), Connection: Connection{Service: h.URL, CommunityID: uuid.NewString()}, Document: portable(), State: "draft"}
	atomicJSON(draftPath(root, d.ID), d)
	if _, e := Queue(root, d.ID); e != nil {
		t.Fatal(e)
	}
	out, e := Flush(context.Background(), root)
	if e != nil || len(out) != 1 || out[0].SubmissionID != sid {
		t.Fatalf("flush: %+v %v", out, e)
	}
	if _, e = Flush(context.Background(), root); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("submitted draft sent again")
	}
}
func TestLocalModeNeverConnects(t *testing.T) {
	root := t.TempDir()
	calls := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer h.Close()
	os.MkdirAll(filepath.Join(root, ".re-discipline", "docs"), 0700)
	SaveSettings(root, Settings{Mode: "local", Connections: []Connection{{Alias: "remote", Service: h.URL, CommunityID: uuid.NewString()}}})
	if _, e := Query(context.Background(), root, "clock", "", false, engine.Options{}); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("local retrieval made external requests")
	}
}
func TestURLAndCredentialBoundaries(t *testing.T) {
	for _, u := range []string{"http://example.com", "https://user:password@example.com", "https://example.com?token=x", "https://example.com/private"} {
		if _, e := NewClient(u); e == nil {
			t.Errorf("accepted unsafe origin %s", u)
		}
	}
}

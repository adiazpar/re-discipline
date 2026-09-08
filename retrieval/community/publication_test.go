package community

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPortabilityUsesRealText(t *testing.T) {
	d := portable()
	d.Markdown += "\nNotes:\nThe byte escape is `\\n`. See https://example.org/report."
	d.Evidence = []Evidence{{Label: "Published source", URL: "https://example.org/evidence"}}
	if c := Validate(d); len(c) != 0 {
		t.Fatalf("portable text rejected: %+v", c)
	}
	for _, text := range []string{`C:\Users\name\trace.txt`, `c:/work/trace.txt`, `\\server\share\trace.txt`, `http://localhost:8080`, `/home/name/work`} {
		v := d
		v.Markdown += "\n" + text
		found := false
		for _, c := range Validate(v) {
			found = found || c.Code == "local_environment"
		}
		if !found {
			t.Errorf("local path admitted: %s", text)
		}
	}
}

func TestPreparationSurvivesLostCacheWithoutAssertingIdentity(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(dir, 0700)
	body := []byte(portable().Markdown)
	os.WriteFile(filepath.Join(dir, "time.md"), body, 0600)
	c := Connection{Service: "https://example.test", CommunityID: uuid.NewString(), Alias: "test"}
	d, _, err := Prepare(root, c, "docs/time.md", "build one")
	if err != nil {
		t.Fatal(err)
	}
	d.Document.Evidence = portable().Evidence
	atomicJSON(draftPath(root, d.ID), d)
	again, _, err := Prepare(root, c, "docs/time.md", "build one")
	if err != nil || again.ID != d.ID || again.Document.Evidence[0].Excerpt == "" {
		t.Fatal("preparation lost cached edits")
	}
	os.RemoveAll(filepath.Join(root, ".re-discipline", "community"))
	fresh, _, err := Prepare(root, c, "docs/time.md", "build one")
	if err != nil || fresh.ID != d.ID {
		t.Fatal("retry identity depends on local state")
	}
	os.WriteFile(filepath.Join(dir, "time.md"), append(body, []byte("\nChanged claim.")...), 0600)
	edited, _, err := Prepare(root, c, "docs/time.md", "build one")
	if err != nil || edited.ID == d.ID || edited.Document.FindingID != "" || edited.LocalID != "" {
		t.Fatal("client asserted publication identity")
	}
	if _, err = os.Stat(filepath.Join(root, ".re-discipline", "community", "publications.db")); !os.IsNotExist(err) {
		t.Fatal("client created an authoritative ledger")
	}
}

func TestBatchPartialFailureAndResume(t *testing.T) {
	root := t.TempDir()
	calls := 0
	seen := map[string]int{}
	cid := uuid.NewString()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Action != "submission.batch" {
			t.Error(req.Action)
		}
		var p struct {
			Items []BatchItem `json:"items"`
		}
		json.Unmarshal(req.Data, &p)
		calls++
		out := []BatchOutcome{}
		for i, item := range p.Items {
			seen[item.Key]++
			v := BatchOutcome{Key: item.Key}
			if calls == 1 && i == 1 {
				v.Status = 422
				v.Error = "needs evidence"
			} else {
				v.Submission = &Submission{ID: uuid.NewString(), CommunityID: cid, Digest: Digest(item.Document), State: "queued"}
			}
			out = append(out, v)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": out})
	}))
	defer h.Close()
	c := Connection{Service: h.URL, CommunityID: cid, Alias: "test"}
	SaveSettings(root, Settings{Mode: "both", Connections: []Connection{c}})
	ids := []string{}
	for i := 0; i < 3; i++ {
		d := Draft{ID: uuid.NewString(), Connection: c, Document: portable(), State: "queued"}
		d.Digest = Digest(d.Document)
		atomicJSON(draftPath(root, d.ID), d)
		ids = append(ids, d.ID)
	}
	if _, err := FlushBatch(context.Background(), root, "test", ""); err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, id := range ids {
		d, _ := ReadDraft(root, id)
		if d.State == "queued" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("pending=%d", pending)
	}
	if _, err := FlushBatch(context.Background(), root, "test", ""); err != nil {
		t.Fatal(err)
	}
	requests := 0
	for _, n := range seen {
		requests += n
	}
	if requests != 4 {
		t.Fatalf("successful documents resent: %d", requests)
	}
}

func TestSelectedEvidenceBoundary(t *testing.T) {
	root := t.TempDir()
	d := Draft{ID: uuid.NewString(), State: "draft", Document: portable()}
	atomicJSON(draftPath(root, d.ID), d)
	os.WriteFile(filepath.Join(root, "trace.txt"), []byte("unselected\nselected evidence\nnot selected"), 0600)
	got, err := AttachEvidence(root, d.ID, "trace.txt", "Selected trace", 2, 2)
	if err != nil || got.Document.Evidence[1].Excerpt != "selected evidence" {
		t.Fatalf("excerpt: %+v %v", got, err)
	}
	os.MkdirAll(filepath.Join(root, "ops"), 0700)
	os.WriteFile(filepath.Join(root, "ops", "private.txt"), []byte("private"), 0600)
	if _, err = AttachEvidence(root, d.ID, "ops/private.txt", "private", 1, 1); err == nil {
		t.Fatal("ops evidence admitted")
	}
	if _, err = AttachEvidence(root, d.ID, "../outside.txt", "outside", 1, 1); err == nil {
		t.Fatal("outside evidence admitted")
	}
	os.WriteFile(filepath.Join(root, "trace.txt"), []byte(strings.Repeat("x", 64*1024+1)), 0600)
	if _, err = AttachEvidence(root, d.ID, "trace.txt", "too long", 1, 1); err == nil {
		t.Fatal("oversized excerpt admitted")
	}
}

func TestPortablePromotionAndDraftRevision(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(base, 0700)
	body := strings.Replace(portable().Markdown, "status: promoted", "status: promoted\nbuild: Exact build and modifications", 1) + "\n## Supporting evidence\nSelected engine observation.\n## Limitations\nOne investigated build."
	os.WriteFile(filepath.Join(base, "finding.md"), []byte(body), 0600)
	c := Connection{Service: "https://example.test", CommunityID: uuid.NewString(), Alias: "test"}
	d, checks, err := Prepare(root, c, "docs/finding.md", "")
	if err != nil || len(checks) != 0 || d.Document.Build != "Exact build and modifications" || d.Document.Evidence[0].Excerpt != "Selected engine observation." {
		t.Fatalf("portable promotion: %+v %+v %v", d, checks, err)
	}
	d.State = "queued"
	d.LastStatus = 422
	atomicJSON(draftPath(root, d.ID), d)
	revision, err := Revise(context.Background(), root, d.ID)
	if err != nil || revision.ID == d.ID || revision.LocalID != d.LocalID {
		t.Fatalf("revision lost identity: %+v %v", revision, err)
	}
	old, _ := ReadDraft(root, d.ID)
	if old.State != "replaced" || old.ReplacedBy != revision.ID {
		t.Fatal("failed draft remains queued")
	}
	again, _, err := Prepare(root, c, "docs/finding.md", "")
	if err != nil || again.ID != revision.ID {
		t.Fatal("preparation did not follow the new draft")
	}
	revision.Document.Markdown += "\naudience: local"
	atomicJSON(draftPath(root, revision.ID), revision)
	if _, err = Queue(root, revision.ID); err == nil {
		t.Fatal("local marker added during editing bypassed queue checks")
	}
}

func TestSourceBlendingDoesNotStarveCommunity(t *testing.T) {
	hits := []Result{{Source: "local", Path: "first"}, {Source: "local", Path: "second"}, {Source: "doom", Service: "https://example.test", CommunityID: "community", FindingID: "external", Revision: "one"}}
	got, err := collapseCopies(t.TempDir(), interleaveSources(hits), 2)
	if err != nil || len(got) != 2 || got[1].Source != "doom" {
		t.Fatalf("community-only result starved: %+v %v", got, err)
	}
}

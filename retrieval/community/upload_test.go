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
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/google/uuid"
)

func TestUploadIncrementalIdentityAndEvidence(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(docs, 0700)
	cid := uuid.NewString()
	stored := map[string]KnowledgeDocument{}
	requests := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner" {
			t.Error("missing owner credential")
		}
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Community != cid {
			t.Error("wrong community")
		}
		switch req.Action {
		case "community.get":
			json.NewEncoder(w).Encode(Library{ID: cid, Role: "owner"})
		case "knowledge.inventory":
			page := KnowledgePage{Items: []KnowledgeEntry{}}
			for _, d := range stored {
				page.Items = append(page.Items, KnowledgeEntry{ID: d.ID, Path: d.Path, Fingerprint: d.Fingerprint})
			}
			json.NewEncoder(w).Encode(page)
		case "knowledge.put":
			var batch KnowledgeBatch
			json.Unmarshal(req.Data, &batch)
			requests++
			if len(req.Data) > MaxUploadBytes || len(batch.Documents) > MaxUploadDocuments {
				t.Error("unbounded batch")
			}
			receipt := KnowledgeReceipt{CommunityID: cid, Key: batch.Key}
			for _, d := range batch.Documents {
				if e := ValidateKnowledge(d); e != nil {
					t.Error(e)
				}
				if strings.Contains(d.Markdown, "private-research") {
					t.Error("evidence uploaded")
				}
				stored[d.ID] = d
				receipt.Items = append(receipt.Items, KnowledgeEntry{ID: d.ID, Path: d.Path, Fingerprint: d.Fingerprint})
				receipt.Updated++
			}
			json.NewEncoder(w).Encode(receipt)
		default:
			t.Error("retired action", req.Action)
			w.WriteHeader(400)
		}
	}))
	defer h.Close()
	t.Setenv("RE_DISCIPLINE_SERVICE", h.URL)
	t.Setenv("RE_DISCIPLINE_TOKEN", "owner")
	conn := Connection{Alias: "doom", Service: h.URL, CommunityID: cid}
	source := "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Clock\n\n## Knowledge\nThe timescale halves the clock.\n\n## Evidence\nprivate-research\n"
	p := filepath.Join(docs, "clock.md")
	os.WriteFile(p, []byte(source), 0600)
	upload := func(verify bool) UploadReport {
		t.Helper()
		r, e := UploadKnowledge(context.Background(), root, conn, UploadOptions{Verify: verify}, true)
		if e != nil || !r.Complete {
			t.Fatalf("upload: %+v %v", r, e)
		}
		return r
	}
	status, e := UploadKnowledge(context.Background(), root, conn, UploadOptions{}, false)
	if e != nil || status.Complete {
		t.Fatal("missing identity status", status, e)
	}
	first := upload(false)
	if first.Uploaded != 1 || first.Identified != 1 {
		t.Fatal(first)
	}
	body, _ := os.ReadFile(p)
	fid, e := FindingID(string(body))
	if e != nil || fid == "" || !strings.Contains(string(body), "private-research") {
		t.Fatal("local original or identity damaged")
	}
	// Cached runs do not reopen stable files, including after local search rebuild.
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)
	upload(false)
	if _, _, e = engine.Query(root, "timescale", engine.Options{}); e != nil {
		t.Fatal(e)
	}
	cached := upload(false)
	if cached.Read != 0 || cached.Uploaded != 0 {
		t.Fatal("cache lost after search rebuild", cached)
	}
	os.WriteFile(p, []byte(strings.ReplaceAll(string(body), "private-research", "private-research updated")), 0600)
	if r := upload(false); r.Uploaded != 0 {
		t.Fatal("evidence-only edit uploaded", r)
	}
	renamed := filepath.Join(docs, "renamed.md")
	os.Rename(p, renamed)
	edited := strings.ReplaceAll(string(body), "halves", "doubles")
	os.WriteFile(renamed, []byte(edited), 0600)
	if r := upload(false); r.Uploaded != 1 || len(stored) != 1 || stored[fid].Path != "docs/renamed.md" || !strings.Contains(stored[fid].Markdown, "doubles") {
		t.Fatal("rename/edit lost identity", r)
	}
	delete(stored, fid)
	if r := upload(false); r.Uploaded != 1 {
		t.Fatal("remote removal not restored", r)
	}
	os.WriteFile(filepath.Join(docs, "duplicate.md"), []byte(edited), 0600)
	before := requests
	r, e := UploadKnowledge(context.Background(), root, conn, UploadOptions{}, true)
	if e == nil || r.Complete || requests != before {
		t.Fatal("duplicate ID was not stopped", r, e)
	}
	os.Remove(filepath.Join(docs, "duplicate.md"))
	os.Remove(renamed)
	if r := upload(true); r.RemoteOnly != 1 || len(stored) != 1 {
		t.Fatal("missing local file deleted remote", r)
	}
	// Migration adopts only a unique exact path+fingerprint match, never a filename alone.
	migrated, _ := WithFindingID(source, fid)
	entry, _ := PrepareKnowledge("docs/clock.md", migrated)
	stored[fid] = entry
	os.WriteFile(p, []byte(source), 0600)
	if r := upload(true); r.Uploaded != 0 || r.Identified != 1 || len(stored) != 1 {
		t.Fatal("legacy exact copy duplicated", r)
	}
}

func TestBulkUploadBoundsAndReaderCannotUpload(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(docs, 0700)
	for n := 0; n < 130; n++ {
		fid := uuid.NewString()
		body := "---\nid: " + fid + "\nstatus: promoted\nkind: fact\n---\n# Bulk\n## Knowledge\n" + strings.Repeat("Claim ", 8000) + "\n## Evidence\nprivate\n"
		os.WriteFile(filepath.Join(docs, fid+".md"), []byte(body), 0600)
	}
	cid := uuid.NewString()
	batches, total := 0, 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Action {
		case "community.get":
			json.NewEncoder(w).Encode(Library{ID: cid, Role: "owner"})
		case "knowledge.inventory":
			json.NewEncoder(w).Encode(KnowledgePage{Items: []KnowledgeEntry{}})
		case "knowledge.put":
			var b KnowledgeBatch
			json.Unmarshal(req.Data, &b)
			batches++
			total += len(b.Documents)
			if len(req.Data) > MaxUploadBytes || len(b.Documents) > 50 {
				t.Error("batch exceeds bound")
			}
			out := KnowledgeReceipt{CommunityID: cid, Key: b.Key, Updated: len(b.Documents)}
			for _, d := range b.Documents {
				out.Items = append(out.Items, KnowledgeEntry{ID: d.ID, Fingerprint: d.Fingerprint})
			}
			json.NewEncoder(w).Encode(out)
		}
	}))
	defer h.Close()
	t.Setenv("RE_DISCIPLINE_SERVICE", h.URL)
	t.Setenv("RE_DISCIPLINE_TOKEN", "owner")
	conn := Connection{Service: h.URL, CommunityID: cid}
	out, e := UploadKnowledge(context.Background(), root, conn, UploadOptions{}, true)
	if e != nil || !out.Complete || total != 130 || batches < 4 {
		t.Fatal(out, e, total, batches)
	}
	t.Setenv("RE_DISCIPLINE_TOKEN", "")
	if _, e = UploadKnowledge(context.Background(), root, conn, UploadOptions{}, true); e == nil {
		t.Fatal("reader uploaded")
	}
	for _, action := range []string{"publish.prepare", "submission.create", "finding.history", "health.review"} {
		if _, e := Execute(context.Background(), root, Command{Action: action, Service: h.URL, Community: cid}); e == nil {
			t.Fatal("retired command accepted", action)
		}
	}
}

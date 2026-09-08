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
)

func TestServerCopyMatchesAndCorrectionsWithoutLedger(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(dir, 0700)
	markdown := portable().Markdown
	os.WriteFile(filepath.Join(dir, "time.md"), []byte(markdown), 0600)
	status := "active"
	current := true
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Action != "finding.match" || strings.Contains(string(req.Data), "timescale") {
			t.Error("local content leaked instead of hash")
		}
		json.NewEncoder(w).Encode([]FindingMatch{{TextDigest: TextDigest(markdown), FindingID: "finding", Revision: "revision", MatchedRevision: "revision", Status: status, Current: current, Reason: "A later measurement contradicts this claim."}})
	}))
	defer h.Close()
	c := Connection{Service: h.URL, CommunityID: "community", Alias: "doom"}
	initial := func() []Result {
		return []Result{{Source: "local", Path: "docs/time.md"}, {Source: "doom", Service: h.URL, CommunityID: c.CommunityID, FindingID: "finding", Revision: "revision"}}
	}
	hits, err := matchLocal(context.Background(), root, initial(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := collapseCopies(root, hits, 8)
	if err != nil || len(out) != 1 {
		t.Fatalf("copies: %+v %v", out, err)
	}
	withContribution := initial()
	withContribution[1].Contributions = []Result{{FindingID: "independent", Revision: "other"}}
	withContribution, _ = matchLocal(context.Background(), root, withContribution, c, false)
	grouped, _ := collapseCopies(root, withContribution, 8)
	if len(grouped) != 1 || len(grouped[0].Contributions) != 1 {
		t.Fatal("local copy hid independent community evidence")
	}
	for _, s := range []string{"refuted", "superseded", "disputed"} {
		status = s
		hits, err = matchLocal(context.Background(), root, initial(), c, false)
		if err != nil || len(hits[0].Warnings) == 0 {
			t.Fatalf("missing %s warning", s)
		}
	}
	status = "active"
	current = false
	hits, err = matchLocal(context.Background(), root, initial(), c, false)
	out, _ = collapseCopies(root, hits, 8)
	if err != nil || len(out) != 2 || len(out[0].Warnings) == 0 {
		t.Fatal("old local revision hid correction")
	}
	os.WriteFile(filepath.Join(dir, "time.md"), []byte(markdown+"\nLocal divergence."), 0600)
	hits, _ = matchLocal(context.Background(), root, initial(), c, false)
	out, _ = collapseCopies(root, hits, 8)
	if len(out) != 2 {
		t.Fatal("different local text hidden")
	}
	if _, err = os.Stat(filepath.Join(root, ".re-discipline", "community")); !os.IsNotExist(err) {
		t.Fatal("matching created local publication state")
	}
}

func TestOfflineRefutationAndEquivalentWarnings(t *testing.T) {
	root := t.TempDir()
	c := Connection{Service: "https://example.test", CommunityID: "community", Alias: "doom"}
	db, err := cacheDB(root, c)
	if err != nil {
		t.Fatal(err)
	}
	d := portable()
	db.Exec(`INSERT INTO meta VALUES('cursor','2')`)
	active := Change{FindingID: "corroboration", Revision: "one", Status: "active", Document: d}
	refuted := Change{FindingID: "canonical", Revision: "two", Status: "refuted", Reason: "Original assumption disproven", Document: d}
	for _, v := range []Change{active, refuted} {
		db.Exec(`INSERT INTO assessments VALUES(?,?)`, v.FindingID, string(mustJSON(v)))
		db.Exec(`INSERT INTO fingerprints VALUES(?,?,?)`, TextDigest(d.Markdown), v.FindingID, v.Revision)
	}
	relations := []Relation{{SourceID: active.FindingID, SourceRevision: active.Revision, TargetID: refuted.FindingID, TargetRevision: "old", Kind: "equivalent", Current: false}}
	db.Exec(`INSERT INTO meta VALUES('relations',?)`, string(mustJSON(relations)))
	db.Close()
	dir := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "claim.md"), []byte(d.Markdown), 0600)
	hits, err := matchLocal(context.Background(), root, []Result{{Source: "local", Path: "docs/claim.md"}}, c, true)
	if err != nil || len(hits[0].Warnings) < 2 {
		t.Fatalf("cached validity/linked warning lost: %+v %v", hits, err)
	}
}

func TestFingerprintPreservesClaimDifferences(t *testing.T) {
	d := portable()
	copy := d
	copy.SourcePath = "docs/renamed.md"
	copy.FindingID = "finding"
	copy.BaseRevision = "revision"
	if ContentDigest(copy) != ContentDigest(d) {
		t.Fatal("path or wire identity affects content fingerprint")
	}
	for _, edit := range []func(*Document){func(v *Document) { v.Build = "different build" }, func(v *Document) { v.Markdown += " not" }, func(v *Document) { v.Grade = "inferred" }, func(v *Document) { v.Evidence = []Evidence{{Label: "different", Excerpt: "different measurement"}} }} {
		v := d
		edit(&v)
		if ContentDigest(v) == ContentDigest(d) {
			t.Fatal("different claim consolidated")
		}
	}
}

func TestServerSourceVariantsKeepPublishedEvidence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(dir, 0700)
	local := portable().Markdown
	published := local + "\n## Publication provenance\nEvidence packaged separately."
	os.WriteFile(filepath.Join(dir, "time.md"), []byte(local), 0600)
	status := "active"
	ambiguous := false
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		var p struct {
			SourcePaths []string `json:"source_paths"`
		}
		json.Unmarshal(req.Data, &p)
		if len(p.SourcePaths) != 1 || p.SourcePaths[0] != "docs/time.md" || strings.Contains(string(req.Data), local) {
			t.Error("expected relative source path and hashes only")
		}
		rows := []FindingMatch{{SourcePath: "docs/time.md", MatchKind: "source", TextDigest: TextDigest(published), FindingID: "finding", Revision: "revision", Status: status, Current: true}}
		if ambiguous {
			other := rows[0]
			other.FindingID = "different"
			rows = append(rows, other)
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer h.Close()
	c := Connection{Service: h.URL, CommunityID: "community", Alias: "doom"}
	initial := func() []Result {
		return []Result{{Source: "local", Path: "docs/time.md"}, {Source: "doom", Service: h.URL, CommunityID: c.CommunityID, FindingID: "finding", Revision: "revision", TextDigest: TextDigest(published), Snippet: "Published evidence summary"}}
	}
	hits, err := matchLocal(context.Background(), root, initial(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	grouped, _ := collapseCopies(root, hits, 8)
	if len(grouped) != 1 || len(grouped[0].Warnings) == 0 || len(grouped[0].Versions) != 1 || grouped[0].Versions[0].Snippet != "Published evidence summary" {
		t.Fatalf("source variant erased or mislabeled: %+v", grouped)
	}
	status = "refuted"
	hits, _ = matchLocal(context.Background(), root, initial(), c, false)
	if len(hits[0].Warnings) == 0 || !strings.Contains(hits[0].Warnings[0], "refuted") {
		t.Fatal("portable local variant lost refutation warning")
	}
	status = "active"
	ambiguous = true
	hits, _ = matchLocal(context.Background(), root, initial(), c, false)
	grouped, _ = collapseCopies(root, hits, 8)
	if len(grouped) != 2 {
		t.Fatal("ambiguous source path silently grouped")
	}
}

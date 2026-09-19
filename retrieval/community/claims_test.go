package community

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

func TestProjectionKeepsConditionsAndShowsRemovedResearch(t *testing.T) {
	raw := "---\nstatus: promoted\nevidence:\n- archive/private.md\nrenderer: Vulkan\n---\n# The feature works\n## Evidence\nVulkan only; fails on OpenGL.\n### Supporting evidence\nprivate trace\n### Observations\nmore private trace\n## Conditions\nRequires module X.\n"
	p := ProjectPublication(raw)
	if strings.Contains(p.Markdown, "private") || strings.Contains(p.Markdown, "fails on OpenGL") {
		t.Fatal("research section leaked")
	}
	if !strings.Contains(p.Markdown, "Requires module X") || !strings.Contains(p.Markdown, "renderer: Vulkan") {
		t.Fatal("retained conditions lost")
	}
	if !p.NeedsReview || !strings.Contains(p.RemovedMaterial, "Vulkan only; fails on OpenGL.") {
		t.Fatal("qualification loss was hidden from local review")
	}
	for _, heading := range []string{"  ## Evidence ##", "Evidence\n--------", "## Research log"} {
		p = ProjectPublication("# Claim\nTrue under conditions.\n" + heading + "\nprivate material\n## Conditions\nVulkan only.")
		if strings.Contains(p.Markdown, "private material") || !p.NeedsReview || !strings.Contains(p.Markdown, "Vulkan only") {
			t.Fatalf("heading %q: %+v", heading, p)
		}
	}
	p = ProjectPublication("# Claim\n````\n```\n## Evidence\nThis is code, not a section.\n````\n## Evidence\nprivate log")
	if !strings.Contains(p.Markdown, "This is code") || strings.Contains(p.Markdown, "private log") {
		t.Fatal("fence length confused section boundaries")
	}
}

func TestClaimIdentityRetainsSubjectAndUnknownApplicabilityFields(t *testing.T) {
	a := portable()
	a.Markdown = "---\nstatus: promoted\ngrade: direct\nidents: [g_showHud]\nrenderer: Vulkan\nconditions: enabled\n---\n# Toggle\n0 disables and 1 enables."
	for _, replacement := range []struct{ old, next string }{{"Vulkan", "OpenGL"}, {"g_showHud", "g_showPlayer"}, {"enabled", "disabled"}, {"0 disables", "0 enables"}} {
		b := a
		b.Markdown = strings.Replace(b.Markdown, replacement.old, replacement.next, 1)
		if ClaimDigest(a) == ClaimDigest(b) {
			t.Fatalf("identity erased %s", replacement.old)
		}
		if semantic.Hash(ProjectClaim(a, "active")) == semantic.Hash(ProjectClaim(b, "active")) {
			t.Fatalf("model projection erased %s", replacement.old)
		}
	}
	b := a
	b.Evidence = []Evidence{{Label: "Different local research", Unavailable: true}}
	b.SourcePath = "docs/renamed.md"
	if ClaimDigest(a) != ClaimDigest(b) {
		t.Fatal("provenance or path multiplied the claim")
	}
	a.Markdown = "A field has size four."
	b = a
	a.SourcePath = "docs/field-a.md"
	b.SourcePath = "docs/field-b.md"
	if ClaimDigest(a) == ClaimDigest(b) {
		t.Fatal("headingless subject identity erased")
	}
}

func TestLocalProjectionRejectsEditAfterSearch(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(docs, 0700)
	d := portable()
	os.WriteFile(filepath.Join(docs, "claim.md"), []byte(d.Markdown), 0600)
	hit := Result{Source: "local", Path: "docs/claim.md", TextDigest: TextDigest(d.Markdown), Status: "promoted"}
	os.WriteFile(filepath.Join(docs, "claim.md"), []byte(d.Markdown+"\nCorrection."), 0600)
	if _, err := queryClaim(context.Background(), root, hit, Settings{}, false); err == nil {
		t.Fatal("new text scored against stale search metadata")
	}
}

func TestPlainWorkflowSurvivesMissingOptionalConfiguration(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(docs, 0700)
	d := portable()
	os.WriteFile(filepath.Join(docs, "claim.md"), []byte(d.Markdown), 0600)
	os.WriteFile(semantic.ConfigPath(root), []byte("{invalid"), 0600)
	out, err := Query(context.Background(), root, "timescale", "local", true, engine.Options{Limit: 1})
	if err != nil || len(out.Hits) != 1 || len(out.Warnings) == 0 {
		t.Fatalf("optional config broke search: %+v %v", out, err)
	}
	if _, _, err = Prepare(root, Connection{}, "docs/claim.md", "DOOM build 1"); err != nil {
		t.Fatalf("optional config broke preparation: %v", err)
	}
}

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
	if !strings.Contains(p.RemovedMaterial, "Vulkan only; fails on OpenGL.") {
		t.Fatal("removed evidence was not retained in the local projection result")
	}
	for _, heading := range []string{"  ## Evidence ##", "Evidence\n--------", "## Research log"} {
		p = ProjectPublication("# Claim\nTrue under conditions.\n" + heading + "\nprivate material\n## Conditions\nVulkan only.")
		if strings.Contains(p.Markdown, "private material") || !strings.Contains(p.Markdown, "Vulkan only") {
			t.Fatalf("heading %q: %+v", heading, p)
		}
	}
	p = ProjectPublication("# Claim\n````\n```\n## Evidence\nThis is code, not a section.\n````\n## Evidence\nprivate log")
	if !strings.Contains(p.Markdown, "This is code") || strings.Contains(p.Markdown, "private log") {
		t.Fatal("fence length confused section boundaries")
	}
}

func TestCuratedProjectionPreservesKnowledge(t *testing.T) {
	knowledge := "---\nstatus: promoted\nkind: fact\ngrade: direct\nbuild: Example build 123\nidents: [LoadTexture]\ntags: [textures]\n---\n# LoadTexture rejects compressed textures\n\n## Knowledge\n\nCompressed inputs return ERR_UNSUPPORTED.\n\n### Applicability\nDesktop rendering only; other paths are untested.\n\n### Example\n```text\n## Evidence\nThis heading is part of the example, not a section.\n```\n"
	evidence := "\n## Evidence\n\nReport: archive/textures/reports/R-001.md#loader\n### Script\narchive/textures/work/test.py\n~~~text\n## Knowledge\nprivate trace\n~~~\n"
	for _, raw := range []string{knowledge + evidence, "\ufeff" + strings.ReplaceAll(knowledge+evidence, "\n", "\r\n")} {
		p := ProjectPublication(raw)
		if p.Markdown != knowledge {
			t.Fatalf("curated knowledge changed: %+v", p)
		}
		if len(p.RemovedSections) != 1 || p.RemovedSections[0] != "evidence" || !strings.Contains(p.RemovedMaterial, "private trace") {
			t.Fatalf("local evidence preview lost: %+v", p)
		}
	}
}

func TestLegacyLayoutsStripEvidence(t *testing.T) {
	for name, raw := range map[string]string{
		"missing knowledge":     "# Claim\nDetails.\n## Evidence\nprivate trace",
		"empty knowledge":       "# Claim\n## Knowledge\n## Evidence\nprivate trace",
		"heading only":          "# Claim\n## Knowledge\n### Conditions\n## Evidence\nprivate trace",
		"evidence first":        "# Claim\n## Evidence\nprivate trace\n## Knowledge\nDetails.",
		"duplicate evidence":    "# Claim\n## Knowledge\nDetails.\n## Evidence\nprivate trace\n## Evidence\nmore traces",
		"section after":         "# Claim\n## Knowledge\nDetails.\n## Evidence\nprivate trace\n## Conditions\nDesktop only.",
		"legacy metadata":       "---\nevidence: [archive/report.md]\n---\n# Claim\n## Knowledge\nDetails.\n## Evidence\nprivate trace",
		"legacy heading":        "# Claim\n## Knowledge\nDetails.\n## Supporting evidence\nprivate trace",
		"nested research":       "# Claim\n## Knowledge\nDetails.\n### Research log\nprivate trace\n## Evidence\nReport.",
		"unclosed fence":        "# Claim\n## Knowledge\nDetails.\n## Evidence\nprivate trace\n```\nunclosed",
		"loose text":            "# Claim\nUnstructured details.\n## Knowledge\nDetails.\n## Evidence\nprivate trace",
		"multiple titles":       "# Claim\n# Another claim\n## Knowledge\nDetails.\n## Evidence\nprivate trace",
		"fenced fake knowledge": "# Claim\n```\n## Knowledge\nDetails.\n```\n## Evidence\nprivate trace",
	} {
		t.Run(name, func(t *testing.T) {
			p := ProjectPublication(raw)
			if strings.Contains(p.Markdown, "private trace") {
				t.Fatalf("legacy boundary leaked evidence: %+v", p)
			}
		})
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
}

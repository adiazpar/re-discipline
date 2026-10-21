package search

import (
	"strings"
	"testing"
)

const goodDoc = `---
status: promoted
kind: fact
grade: direct
tags: [entities, animation]
idents: [idAnimatedEntity::AttachJoint, ai_bindFlags]
evidence: [archive/demo/reports/R-007.md, archive/demo/reports/R-008.md]
---
# Entity binding goes through idAnimatedEntity::AttachJoint

Body text here.
`

func TestParseDocFrontmatter(t *testing.T) {
	d := ParseDoc("docs/engine/joint-binding.md", goodDoc)
	if d.Status != "promoted" || d.Kind != "fact" || d.Grade != "direct" {
		t.Fatalf("fields: %+v", d)
	}
	if len(d.Tags) != 2 || d.Tags[0] != "entities" {
		t.Fatalf("tags: %v", d.Tags)
	}
	if len(d.Idents) != 2 || d.Idents[0] != "idAnimatedEntity::AttachJoint" || d.Idents[1] != "ai_bindFlags" {
		t.Fatalf("idents: %v", d.Idents)
	}
	if len(d.Evidence) != 2 || d.Evidence[0] != "archive/demo/reports/R-007.md" || d.Evidence[1] != "archive/demo/reports/R-008.md" {
		t.Fatalf("evidence: %v", d.Evidence)
	}
	if d.Title != "Entity binding goes through idAnimatedEntity::AttachJoint" {
		t.Fatalf("title: %q", d.Title)
	}
	if d.Warning != "" {
		t.Fatalf("unexpected warning: %q", d.Warning)
	}
}

func TestParseDocSupersededMetadataWithWindowsLineEndings(t *testing.T) {
	const raw = "---\nstatus: superseded\nkind: fact\ngrade: direct\nbuild: pinned image\nevidence: [reports/correction.md]\n---\n# Withdrawn route\nSee the replacement.\n"
	for _, ending := range []string{"\n", "\r\n", "\r\r\n"} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(ending, "\r", "CR"), "\n", "LF"), func(t *testing.T) {
			d := ParseDoc("docs/withdrawn.md", strings.ReplaceAll(raw, "\n", ending))
			if d.Status != "superseded" || d.Kind != "fact" || d.Grade != "direct" || d.Build != "pinned image" {
				t.Fatalf("withdrawn finding lost its applicability metadata: %+v", d)
			}
			if len(d.Evidence) != 1 || d.Evidence[0] != "reports/correction.md" || d.Title != "Withdrawn route" {
				t.Fatalf("withdrawn finding lost its evidence or title: %+v", d)
			}
			if strings.Contains(d.Body, "status:") || d.Warning != "" {
				t.Fatalf("frontmatter was indexed as prose: %+v", d)
			}
		})
	}
}

func TestParseDocAliases(t *testing.T) {
	d := ParseDoc("docs/reference/cvars/timescale.md", `---
kind: reference
idents: [timescale]
aliases: [slow motion, game speed]
---
# Cvar `+"`"+`timescale`+"`"+` scales the time

scales the time
`)
	if len(d.Aliases) != 2 || d.Aliases[0] != "slow motion" || d.Aliases[1] != "game speed" {
		t.Fatalf("aliases: %v", d.Aliases)
	}
	if len(d.Idents) != 1 || d.Idents[0] != "timescale" {
		t.Fatalf("aliases must not disturb idents: %v", d.Idents)
	}
}

func TestParseDocMalformedIsLenient(t *testing.T) {
	d := ParseDoc("docs/broken.md", "---\nstatus: promoted\nno closing fence\n# A heading\nbody")
	if d.Warning == "" {
		t.Fatal("want warning for unclosed frontmatter")
	}
	if d.Body == "" || d.Status != "" {
		t.Fatalf("malformed doc must fall back to plain text: %+v", d)
	}
}

func TestParseDocCRLFAndNoFrontmatter(t *testing.T) {
	d := ParseDoc("docs/x.md", "# Title line\r\nbody\r\n")
	if d.Title != "Title line" || d.Warning != "" {
		t.Fatalf("%+v", d)
	}
	d2 := ParseDoc("docs/spawn-limit.md", "just prose, no heading")
	if d2.Title != "spawn-limit" {
		t.Fatalf("filename-stem title fallback, got %q", d2.Title)
	}
}

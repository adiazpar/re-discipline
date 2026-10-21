package search

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func buildTestCorpus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeDoc(t, root, "docs/engine/joint-binding.md", `---
status: promoted
kind: fact
grade: direct
tags: [entities]
---
# Entity binding goes through idAnimatedEntity::AttachJoint

Bind entities to demon joints with AttachJoint.
`)
	writeDoc(t, root, "docs/engine/spawn-limit.md", `---
status: superseded
kind: fact
grade: inferred
---
# Snapmap spawn limit is 12 concurrent AI

Old superseded claim.
`)
	writeDoc(t, root, "docs/ops/ghidra.md", `---
kind: ops
---
# Ghidra project lives at G:\\snaphak\\doom.gpr

Open with Ghidra 11.
`)
	writeDoc(t, root, "docs/engine/force-idle.md", `---
status: promoted
kind: fact
grade: direct
idents: [ai_forceIdle, idAI::SetIdleState]
evidence: [archive/demo/reports/R-042.md, archive/demo/reports/R-043.md]
---
# A behavior-gating cvar exists for demons

Prose only; the cvar is declared in frontmatter, not in this text.
`)
	writeDoc(t, root, "docs/engine/cyber-idle.md", `---
status: promoted
kind: fact
grade: direct
---
# A demon stands idle when its emotion band never crosses the attack threshold

The cyberdemon stood still and never attacked because its emotion band
asymmetry kept it stuck idle.
`)
	writeDoc(t, root, "docs/reference/events/ssaction-idle.md", `---
status: promoted
kind: reference
grade: inferred
idents: [ssaction_Idle]
---
# Event `+"`ssaction_Idle`"+` — a scripted action that makes the actor stand idle

A scripted action that makes the actor stand idle for a set duration.

## Signature

`+"`ssaction_Idle`"+` takes 2 arguments, verbatim from the engine's event dump.
`)
	writeDoc(t, root, "docs/broken.md", "---\nstatus: promoted\nunclosed")
	return root
}

func TestBuildIndexFileAndManifest(t *testing.T) {
	root := buildTestCorpus(t)
	dbPath := filepath.Join(root, ".re-discipline", "index.db")
	docs, warnings, err := BuildIndexFile(root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 7 {
		t.Fatalf("want 7 docs, got %d", len(docs))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "docs/broken.md") {
		t.Fatalf("want one lenient warning for broken doc, got %v", warnings)
	}
	stored, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := ScanDocs(root)
	if ManifestDiffers(stored, current) {
		t.Fatal("fresh index must match current scan")
	}
}

func TestStripRelationLinks(t *testing.T) {
	body := `# The claim title

Substantive claim text mentioning DeserializeFromJson.

## Evidence
Archived at git show abc123.

## Relations
- Depends on: [Some other doc's full title with RepairAndMigrate](other-doc.md)
- Split from: docs/truth/old.md (git show abc123)
Every other finding depends on this one; unique prose stays.

## Aftermath
- Depends on: outside Relations, this bullet is content and stays.
`
	got := StripRelationLinks(body)
	if strings.Contains(got, "Some other doc's full title") || strings.Contains(got, "Split from") {
		t.Fatalf("link bullets must be stripped:\n%s", got)
	}
	for _, keep := range []string{
		"Substantive claim text mentioning DeserializeFromJson.",
		"## Relations",
		"Every other finding depends on this one; unique prose stays.",
		"outside Relations, this bullet is content and stays.",
		"Archived at git show abc123.",
	} {
		if !strings.Contains(got, keep) {
			t.Fatalf("must keep %q:\n%s", keep, got)
		}
	}
	// A body with no Relations section is returned unchanged.
	if plain := "# T\n\n- Depends on: kept, not in a Relations section\n"; StripRelationLinks(plain) != plain {
		t.Fatal("bodies without Relations must be untouched")
	}
}

// The indexed body (and idents derived from it) must exclude Relations
// link bullets, so a doc no longer competes for its dependencies' topics.
func TestBuildIndexStripsRelationLinks(t *testing.T) {
	root := buildTestCorpus(t)
	writeDoc(t, root, "docs/engine/with-relations.md", `---
status: promoted
kind: fact
grade: direct
---
# A claim about the render pipeline

Own content about renderprogs.

## Relations
- Depends on: [Entity binding goes through idAnimatedEntity::AttachJoint](joint-binding.md)
`)
	dbPath := filepath.Join(root, ".re-discipline", "index.db")
	if _, _, err := BuildIndexFile(root, dbPath); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body, idents string
	if err := db.QueryRow(`SELECT body, idents FROM docs WHERE path = 'docs/engine/with-relations.md'`).Scan(&body, &idents); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "AttachJoint") || strings.Contains(idents, "attachjoint") {
		t.Fatalf("relation link leaked into index:\nbody: %s\nidents: %s", body, idents)
	}
	if !strings.Contains(body, "Own content about renderprogs.") {
		t.Fatalf("own content missing from indexed body: %s", body)
	}
}

// Declared idents: frontmatter must be searchable both verbatim and
// through ExpandIdentifiers, without appearing in the doc body.
func TestBuildIndexDeclaredIdents(t *testing.T) {
	root := buildTestCorpus(t)
	dbPath := filepath.Join(root, ".re-discipline", "index.db")
	if _, _, err := BuildIndexFile(root, dbPath); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var idents string
	if err := db.QueryRow(`SELECT idents FROM docs WHERE path = 'docs/engine/force-idle.md'`).Scan(&idents); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ai_forceidle", "forceidle", "force", "idle", "idai::setidlestate", "setidlestate"} {
		if !slices.Contains(strings.Fields(idents), want) {
			t.Fatalf("idents column missing %q: %s", want, idents)
		}
	}
	var evidence string
	if err := db.QueryRow(`SELECT evidence FROM docmeta WHERE path = 'docs/engine/force-idle.md'`).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != "archive/demo/reports/R-042.md\narchive/demo/reports/R-043.md" {
		t.Fatalf("docmeta evidence: %q", evidence)
	}
	// docmeta.idents stores declared identifiers verbatim-lowercased plus
	// their collapsed forms, and nothing expanded from title or body.
	var declared string
	if err := db.QueryRow(`SELECT idents FROM docmeta WHERE path = 'docs/engine/force-idle.md'`).Scan(&declared); err != nil {
		t.Fatal(err)
	}
	if declared != "ai_forceidle aiforceidle idai::setidlestate idaisetidlestate" {
		t.Fatalf("docmeta declared idents: %q", declared)
	}
}

func TestWriteIndexMD(t *testing.T) {
	root := buildTestCorpus(t)
	metas, _ := ScanDocs(root)
	docs, _ := LoadDocs(root, metas)
	if err := WriteIndexMD(root, docs); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".re-discipline", "docs", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "engine/joint-binding.md") || !strings.Contains(s, "superseded") {
		t.Fatalf("INDEX.md content:\n%s", s)
	}
}

func TestWriteIndexMDUnchangedKeepsFile(t *testing.T) {
	root := buildTestCorpus(t)
	metas, _ := ScanDocs(root)
	docs, _ := LoadDocs(root, metas)
	if err := WriteIndexMD(root, docs); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".re-discipline", "docs", "INDEX.md")
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(p, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteIndexMD(root, docs); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged content replaced or rewrote the index")
	}
}

// An alias is a phrasing hint, not a claim of authority. It must make the
// doc findable by words absent from its title and body, and it must NOT be
// written into docmeta.idents, because that column is what waives the
// reference penalty — an aliased reference doc would then outrank curated
// facts on every ordinary question containing the alias word.
func TestAliasesAreIndexedButGrantNoIdentAuthority(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/reference/cvars/timescale.md", `---
status: promoted
kind: reference
grade: direct
idents: [timescale]
aliases: [slow motion, bullet time]
---
# Cvar `+"`"+`timescale`+"`"+` scales the time

scales the time
`)
	dbPath := filepath.Join(root, "index.db")
	if _, _, err := BuildIndexFile(root, dbPath); err != nil {
		t.Fatal(err)
	}

	hits, _, err := Query(root, "how do I get slow motion", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "docs/reference/cvars/timescale.md" {
		t.Fatalf("alias must make the doc findable by its alias words, got %+v", hits)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var declared string
	if err := db.QueryRow(`SELECT idents FROM docmeta WHERE path = ?`,
		"docs/reference/cvars/timescale.md").Scan(&declared); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(declared, "slow") || strings.Contains(declared, "bullet") {
		t.Fatalf("alias words leaked into declared idents: %q", declared)
	}
	if !strings.Contains(declared, "timescale") {
		t.Fatalf("declared ident lost: %q", declared)
	}
}

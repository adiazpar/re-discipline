package search

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestExactIdentifierRanksAuthorityBeforeMentionPool(t *testing.T) {
	root := t.TempDir()
	// More short incidental matches than the normal candidate fetch can hold.
	for i := 0; i < rerankFetchMin+20; i++ {
		writeDoc(t, root, fmt.Sprintf("docs/mentions/m%03d.md", i),
			"---\nstatus: promoted\nkind: fact\n---\n# sh_execution_context side effect\n\nsh_execution_context\n")
	}
	writeDoc(t, root, "docs/authority.md", "---\nstatus: promoted\nkind: fact\ngrade: direct\nidents: [sh_execution_context]\n---\n# Context ownership contract\n\n"+strings.Repeat("Complete ownership and lifetime evidence. ", 4000))
	writeDoc(t, root, "docs/alias.md", "---\nstatus: promoted\nkind: fact\naliases: [sh_execution_context]\n---\n# sh_execution_context\n")
	writeDoc(t, root, "docs/old.md", "---\nstatus: superseded\nkind: fact\nidents: [sh_execution_context]\n---\n# sh_execution_context\n")
	for _, q := range []string{"sh_execution_context", "SH_EXECUTION_CONTEXT", "shexecutioncontext", "`sh_execution_context`"} {
		hits, _, err := Query(root, q, 1)
		if err != nil || len(hits) != 1 || hits[0].Path != "docs/authority.md" {
			t.Fatalf("exact lookup %q lost its authority: %+v, %v", q, hits, err)
		}
	}
	ranked, _, _, err := rank(root, "sh_execution_context", QueryOptions{Limit: 1})
	if err != nil || len(ranked) == 0 || !ranked[0].ExactIdentifier {
		t.Fatalf("explanation must expose exact-name priority: %+v, %v", ranked, err)
	}
	// A qualified question still uses relevance; one incidental matching term
	// must not promote a declaration unrelated to the rest of the question.
	ranked, _, _, err = rank(root, "sh_execution_context side effect", QueryOptions{Limit: 1})
	if err != nil || len(ranked) == 0 || ranked[0].Path == "docs/authority.md" {
		t.Fatalf("qualified question should retain relevance ranking: %+v, %v", ranked, err)
	}
	for _, row := range ranked {
		if row.ExactIdentifier {
			t.Fatalf("qualified question incorrectly treated as an exact lookup: %+v", row)
		}
	}
}

func TestExactIdentifierKeepsFiltersAndSupersededLast(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/current.md", "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# current sh_execution_context observation\n")
	writeDoc(t, root, "docs/old.md", "---\nstatus: superseded\nkind: reference\ngrade: inferred\nidents: [sh_execution_context]\n---\n# old contract\n")
	hits, _, err := Query(root, "sh_execution_context", 2)
	if err != nil || len(hits) != 2 || hits[0].Path != "docs/current.md" || hits[1].Status != "superseded" {
		t.Fatalf("exact declarations must not revive superseded authority: %+v, %v", hits, err)
	}
	hits, _, err = QueryOpts(root, "sh_execution_context", QueryOptions{Kind: "reference", Grade: "inferred", Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].Path != "docs/old.md" {
		t.Fatalf("explicit filters must still select the requested evidence: %+v, %v", hits, err)
	}
}

func TestQueryRanksAndLabels(t *testing.T) {
	root := buildTestCorpus(t)
	hits, _, err := Query(root, "how do I bind entities to demon joints AttachJoint", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "docs/engine/joint-binding.md" {
		t.Fatalf("hits: %+v", hits)
	}
	if hits[0].Status != "promoted" || hits[0].Grade != "direct" {
		t.Fatalf("hit metadata: %+v", hits[0])
	}
}

func TestQuerySupersededDownranked(t *testing.T) {
	root := buildTestCorpus(t)
	hits, _, err := Query(root, "snapmap spawn limit", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("superseded docs must still be retrievable")
	}
	last := hits[len(hits)-1]
	if last.Status != "superseded" {
		t.Fatalf("superseded must sort last: %+v", hits)
	}
	out := FormatHits(hits)
	if !strings.Contains(out, "[SUPERSEDED]") {
		t.Fatalf("superseded label missing:\n%s", out)
	}
}

func TestQueryEmptyAndUnusable(t *testing.T) {
	root := buildTestCorpus(t)
	hits, _, err := Query(root, "???", 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("unusable query: %v %v", hits, err)
	}
}

// Pins the bm25() weight-argument behavior observed in this
// modernc.org/sqlite build, on the exact production schema:
//   - scores are negative, more negative = better, so ascending ORDER BY
//     ranks best first;
//   - weight arguments map positionally onto ALL columns, UNINDEXED ones
//     included; weights on UNINDEXED positions are ignored;
//   - missing trailing weights default to 1.0, so bm25(docs, wTitle,
//     wBody, wIdents) is a complete spec for this schema.
func TestBM25WeightBehavior(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE VIRTUAL TABLE docs USING fts5(title, body, idents, path UNINDEXED, status UNINDEXED, kind UNINDEXED, grade UNINDEXED)`,
		`INSERT INTO docs VALUES('needle in title', 'plain body words here', '', 'title-doc', '', '', '')`,
		`INSERT INTO docs VALUES('other words', 'needle inside the body text', '', 'body-doc', '', '', '')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	scores := func(sel string) map[string]float64 {
		t.Helper()
		rows, err := db.Query(`SELECT path, ` + sel + ` FROM docs WHERE docs MATCH 'needle'`)
		if err != nil {
			t.Fatalf("%s: %v", sel, err)
		}
		defer rows.Close()
		out := map[string]float64{}
		for rows.Next() {
			var p string
			var s float64
			if err := rows.Scan(&p, &s); err != nil {
				t.Fatal(err)
			}
			out[p] = s
		}
		return out
	}

	weighted := scores(`bm25(docs, 8.0, 1.0, 4.0)`)
	if weighted["title-doc"] >= 0 || weighted["body-doc"] >= 0 {
		t.Fatalf("bm25 scores must be negative: %v", weighted)
	}
	if weighted["title-doc"] >= weighted["body-doc"] {
		t.Fatalf("title weight 8.0 must rank title-doc better (more negative): %v", weighted)
	}

	// Weights on the four trailing UNINDEXED positions are ignored:
	// identical scores with and without them.
	padded := scores(`bm25(docs, 8.0, 1.0, 4.0, 100.0, 100.0, 100.0, 100.0)`)
	if padded["title-doc"] != weighted["title-doc"] || padded["body-doc"] != weighted["body-doc"] {
		t.Fatalf("UNINDEXED weights must be ignored: %v vs %v", padded, weighted)
	}

	// Missing trailing weights default to 1.0: no-arg bm25 == all-ones.
	if flat := scores(`bm25(docs)`); flat["title-doc"] != flat["body-doc"] {
		t.Fatalf("default weights must be uniform: %v", flat)
	}
}

func TestQueryKindGradeFilter(t *testing.T) {
	root := buildTestCorpus(t)
	// "ghidra project" matches the ops doc; "entities" the fact docs.
	hits, _, err := QueryOpts(root, "ghidra project entities", QueryOptions{Limit: 10, Kind: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "docs/ops/ghidra.md" {
		t.Fatalf("kind=ops filter: %+v", hits)
	}
	hits, _, err = QueryOpts(root, "ghidra project entities", QueryOptions{Limit: 10, Grade: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Grade != "direct" {
			t.Fatalf("grade=direct filter leaked %+v", h)
		}
	}
	if len(hits) == 0 {
		t.Fatal("grade=direct filter must still return the direct docs")
	}
	// Empty options remain unfiltered.
	unfiltered, _, err := QueryOpts(root, "ghidra project entities", QueryOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(unfiltered) <= len(hits) {
		t.Fatalf("unfiltered must return more: %d vs %d", len(unfiltered), len(hits))
	}
}

func TestQueryDeclaredIdents(t *testing.T) {
	root := buildTestCorpus(t)
	// The corpus doc declares idents: [ai_forceIdle, idAI::SetIdleState]
	// but never mentions them in title or body.
	for _, q := range []string{"ai_forceIdle", "forceIdle", "what does SetIdleState do"} {
		hits, _, err := Query(root, q, 5)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, h := range hits {
			if h.Path == "docs/engine/force-idle.md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("query %q must find the declaring doc: %+v", q, hits)
		}
	}
}

// Reference docs are bulk-generated and outnumber curated facts; on a
// concept query that names no declared identifier, a matching fact doc
// must outrank them even when the reference doc's term overlap is
// comparable (the query-scaled reference penalty).
func TestQueryReferencePenalizedOnConceptQuery(t *testing.T) {
	root := buildTestCorpus(t)
	hits, _, err := Query(root, "why does the demon stand idle instead of attacking", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "docs/engine/cyber-idle.md" {
		t.Fatalf("fact doc must outrank reference docs on a concept query: %+v", hits)
	}
	// The reference doc is demoted, not hidden.
	found := false
	for _, h := range hits {
		if h.Path == "docs/reference/events/ssaction-idle.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reference doc must still be retrievable: %+v", hits)
	}
}

// A query naming a declared identifier is exactly what reference docs
// exist for: the declaring doc is exempt from the reference penalty and must
// rank first, in both the verbatim and the collapsed spelling.
func TestQueryReferenceIdentLookupExempt(t *testing.T) {
	root := buildTestCorpus(t)
	for _, q := range []string{
		"what does ssaction_Idle do",
		"what arguments does ssactionIdle take",
	} {
		hits, _, err := Query(root, q, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 || hits[0].Path != "docs/reference/events/ssaction-idle.md" {
			t.Fatalf("query %q must rank the declaring reference doc first: %+v", q, hits)
		}
	}
}

func TestQueryEvidenceAndSnippetMarkers(t *testing.T) {
	root := buildTestCorpus(t)
	hits, _, err := Query(root, "ai_forceIdle", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	var hit *Hit
	for i := range hits {
		if hits[i].Path == "docs/engine/force-idle.md" {
			hit = &hits[i]
		}
	}
	if hit == nil {
		t.Fatalf("declaring doc missing: %+v", hits)
	}
	if len(hit.Evidence) != 2 || hit.Evidence[0] != "archive/demo/reports/R-042.md" || hit.Evidence[1] != "archive/demo/reports/R-043.md" {
		t.Fatalf("evidence: %v", hit.Evidence)
	}
	// Snippets carry «» markers around matched terms.
	joints, _, err := Query(root, "bind entities to demon joints", 5)
	if err != nil || len(joints) == 0 {
		t.Fatalf("%v %v", joints, err)
	}
	if !strings.Contains(joints[0].Snippet, "«") || !strings.Contains(joints[0].Snippet, "»") {
		t.Fatalf("snippet missing highlight markers: %q", joints[0].Snippet)
	}
	if out := FormatHits(joints); !strings.Contains(out, joints[0].Snippet) {
		t.Fatalf("FormatHits must carry the snippet:\n%s", out)
	}
}

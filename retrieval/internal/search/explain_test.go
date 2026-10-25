package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole value of Explain is that it reports what a search actually
// did. If it ever ranked separately from Query, it would be a plausible
// lie, so this pins the two to the same order.
func TestExplainAgreesWithQuery(t *testing.T) {
	root := buildTestCorpus(t)
	for _, q := range []string{
		"how do I stop a demon attacking",
		"ai_forceIdle",
		"binding entities to joints",
	} {
		hits, _, err := Query(root, q, 5)
		if err != nil {
			t.Fatal(err)
		}
		ex, _, err := Explain(root, q, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(ex.Rows) < len(hits) {
			t.Fatalf("%q: explain returned fewer rows (%d) than query hits (%d)",
				q, len(ex.Rows), len(hits))
		}
		for i, h := range hits {
			if ex.Rows[i].Path != h.Path {
				t.Fatalf("%q: rank %d is %s in query but %s in explain",
					q, i+1, h.Path, ex.Rows[i].Path)
			}
			if ex.Rows[i].FinalRank != i+1 {
				t.Fatalf("%q: row %d carries FinalRank %d", q, i+1, ex.Rows[i].FinalRank)
			}
		}
	}
}

// A reference doc that declares the identifier being searched for must
// report a waived penalty, and one that does not must report the charge.
// These two facts are what make the report worth reading.
func TestExplainReportsThePenaltyAndTheExemption(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/reference/cvars/ai-forceidle.md", `---
status: promoted
kind: reference
grade: direct
idents: [ai_forceIdle]
---
# Cvar `+"`"+`ai_forceIdle`+"`"+` holds demons in idle

holds demons in idle
`)
	writeDoc(t, root, "docs/reference/cvars/ai-forcewalk.md", `---
status: promoted
kind: reference
grade: direct
idents: [ai_forceWalk]
---
# Cvar `+"`"+`ai_forceWalk`+"`"+` holds demons to a walk

holds demons to a walk
`)
	if _, _, err := BuildIndexFile(root, filepath.Join(root, "index.db")); err != nil {
		t.Fatal(err)
	}
	ex, _, err := Explain(root, "ai_forceIdle", 5)
	if err != nil {
		t.Fatal(err)
	}
	var owner, other *ExplainRow
	for i := range ex.Rows {
		if strings.HasSuffix(ex.Rows[i].Path, "ai-forceidle.md") {
			owner = &ex.Rows[i]
		}
		if strings.HasSuffix(ex.Rows[i].Path, "ai-forcewalk.md") {
			other = &ex.Rows[i]
		}
	}
	if owner == nil || other == nil {
		t.Fatalf("expected both docs as candidates, got %+v", ex.Rows)
	}
	if !owner.Declares || owner.Penalty != 0 {
		t.Fatalf("the declaring doc must be exempt: declares=%v penalty=%v",
			owner.Declares, owner.Penalty)
	}
	if other.Declares || other.Penalty <= 0 {
		t.Fatalf("the non-declaring reference doc must be charged: declares=%v penalty=%v",
			other.Declares, other.Penalty)
	}
	if owner.Final != owner.Raw-owner.Boost || other.Final != other.Raw+other.Penalty-other.Boost {
		t.Fatalf("final must equal raw plus penalty minus boost: %+v %+v", owner, other)
	}
	if owner.FinalRank != 1 {
		t.Fatalf("the doc declaring the searched name must rank first, got %d", owner.FinalRank)
	}
}

// The dropped half of the split is reported, because it answers "why
// did my question not match".
func TestExplainReportsDroppedFunctionWords(t *testing.T) {
	root := buildTestCorpus(t)
	ex, _, err := Explain(root, "how does the spawn limit work", 5)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ex.Dropped, " ")
	for _, w := range []string{"how", "does", "the"} {
		if !strings.Contains(joined, w) {
			t.Fatalf("expected %q reported as dropped, got %v", w, ex.Dropped)
		}
	}
	if strings.Contains(strings.Join(ex.Terms, " "), "the") {
		t.Fatalf("function words must not be searched for: %v", ex.Terms)
	}
}

func TestStatsCountsTheCorpus(t *testing.T) {
	root := buildTestCorpus(t)
	if _, _, err := BuildIndexFile(root, filepath.Join(root, "index.db")); err != nil {
		t.Fatal(err)
	}
	st, _, err := Stats(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Documents == 0 {
		t.Fatal("expected a non-zero document count")
	}
	total := 0
	for _, n := range st.ByKind {
		total += n
	}
	if total != st.Documents {
		t.Fatalf("kind breakdown sums to %d but there are %d documents", total, st.Documents)
	}
	if st.ByStatus["superseded"] == 0 {
		t.Fatalf("the fixture has a superseded doc; got %v", st.ByStatus)
	}
	r := st.Ranking
	if r.TitleWeight != weightTitle || r.IdentsWeight != weightIdents ||
		r.BodyWeight != weightBody || r.ReferencePenaltyFraction != referencePenaltyFraction {
		t.Fatalf("reported ranking constants must match the ones in use: %+v", r)
	}
	if r.StopWords != len(stopTerms) {
		t.Fatalf("stop-word count %d does not match the table (%d)", r.StopWords, len(stopTerms))
	}
}

// Stats refreshes a stale or missing index the same way a query does, so
// a project that has never been indexed still reports a real census
// rather than zeroes or an error.
func TestStatsBuildsAMissingIndex(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/x.md", "# A claim about spawning\n\nbody\n")
	st, _, err := Stats(root)
	if err != nil {
		t.Fatalf("stats must not fail without an index: %v", err)
	}
	if st.Documents != 1 {
		t.Fatalf("expected the one document to be indexed and counted, got %d", st.Documents)
	}
	if st.IndexFormat != indexFormatVersion {
		t.Fatalf("expected the freshly built index to carry format %q, got %q",
			indexFormatVersion, st.IndexFormat)
	}
}

// An empty knowledge base is a legitimate state, not a failure: a project
// that just ran init has no documents yet and must still be describable.
func TestStatsOnAnEmptyKnowledgeBase(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".re-discipline", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _, err := Stats(root)
	if err != nil {
		t.Fatalf("stats must not fail on an empty corpus: %v", err)
	}
	if st.Documents != 0 || st.Symbols != 0 || st.GoldenQuestions != 0 {
		t.Fatalf("expected an all-zero census, got %+v", st)
	}
	if st.Ranking.ReferencePenaltyFraction != referencePenaltyFraction {
		t.Fatal("ranking constants must be reported even with no documents")
	}
}

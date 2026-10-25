package search

import (
	"reflect"
	"testing"
)

func TestFieldSignalsPreferTheQuestionOverOneRepeatedWord(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/overlay.md", `---
status: promoted
kind: reference
idents: [view_showOverlay]
aliases: [conceal the overlay]
---
# Overlay visibility
The view_showOverlay setting disables the on-screen overlay.
`)
	writeDoc(t, root, "docs/conceal.md", `---
status: promoted
kind: reference
idents: [conceal]
---
# Event conceal
conceal conceal conceal hides an entity.
`)
	query := "how do I conceal the overlay"
	hits, _, err := Query(root, query, 8)
	if err != nil || len(hits) != 2 || hits[0].Path != "docs/overlay.md" {
		t.Fatalf("the whole question must beat a repeated ordinary word: %v %+v", err, hits)
	}
	// Aliases help relevance without acquiring exact-name authority. A literal
	// event-name lookup must still return the document that declares it.
	exact, _, err := Query(root, "conceal", 8)
	if err != nil || exact[0].Path != "docs/conceal.md" {
		t.Fatalf("identifier authority changed: %v %+v", err, exact)
	}
	remote, _, err := QuerySnapshot(root, query, QueryOptions{Limit: 8})
	if err != nil || !reflect.DeepEqual(hits, remote) {
		t.Fatalf("local and community snapshot rankings diverged: %v", err)
	}
}

func TestFieldSignalsCountCoverageAndOrderWithoutRepetitionVotes(t *testing.T) {
	f := newFieldQuery("red access key")
	coverage, pairs := f.match("red access key")
	if coverage != 1 || pairs != 1 {
		t.Fatalf("whole phrase: %v %v", coverage, pairs)
	}
	repeatedCoverage, repeatedPairs := f.match("red access key red access key", "red access key")
	if repeatedCoverage != coverage || repeatedPairs != pairs {
		t.Fatal("repetition must not create extra coverage or pair votes")
	}
	coverage, pairs = f.match("key access red")
	if coverage != 1 || pairs != 0 {
		t.Fatalf("reversed words cover the topic but not the phrase: %v %v", coverage, pairs)
	}
	_, pairs = newFieldQuery("red key").match("red", "key")
	if pairs != 0 {
		t.Fatal("a phrase cannot cross field boundaries")
	}
	coverage, _ = newFieldQuery("not visible").match("visible")
	if coverage != 0.5 {
		t.Fatal("negation must stay in the query")
	}
}

func TestFieldSignalsKeepStemmedAndPartialMatches(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/key.md", "# Red key\nThis key opens a door.\n")
	for _, q := range []string{"keys", "red key with missing terminology"} {
		hits, _, err := Query(root, q, 8)
		if err != nil || len(hits) != 1 {
			t.Fatalf("field boosts must not become an AND filter for %q: %v %+v", q, err, hits)
		}
	}
}

func TestReferencePreferenceScalesWithQueryAndIgnoresSupersededLeader(t *testing.T) {
	rows := []ScoredHit{
		{Hit: Hit{Kind: "fact"}, Raw: -20, Coverage: 1},
		{Hit: Hit{Kind: "reference"}, Raw: -18, Coverage: 1},
		{Hit: Hit{Kind: "reference"}, Raw: -10, Declares: true},
		{Hit: Hit{Kind: "fact"}, Raw: -1000, Superseded: true},
	}
	applyFieldRanking(rows)
	if rows[1].Penalty != 6 || rows[0].Penalty != 0 || rows[2].Penalty != 0 {
		t.Fatalf("only non-declaring references pay the current-query preference: %+v", rows)
	}
	for i := range rows {
		rows[i].Raw *= 0.5
	}
	applyFieldRanking(rows)
	if rows[1].Penalty != 3 || rows[0].Boost != 5 {
		t.Fatal("adjustments must follow BM25's scale instead of a fixed score offset")
	}
}

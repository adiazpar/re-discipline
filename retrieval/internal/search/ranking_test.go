package search

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Independent reference for the original experiment's regexp/set semantics.
var referenceFieldRE = regexp.MustCompile(`[a-z0-9]+`)

func referenceFieldWords(s string) []string {
	var out []string
	for _, w := range referenceFieldRE.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 1 && !stopTerms[w] {
			out = append(out, w)
		}
	}
	return out
}

func referenceFieldMatch(f fieldQuery, fields ...string) (float64, float64) {
	words, pairs := map[string]bool{}, map[wordPair]bool{}
	for _, field := range fields {
		tokens := referenceFieldWords(field)
		for i, w := range tokens {
			if _, ok := f.terms[w]; ok {
				words[w] = true
			}
			if i > 0 {
				p := wordPair{tokens[i-1], w}
				if _, ok := f.pairs[p]; ok {
					pairs[p] = true
				}
			}
		}
	}
	a, b := 0.0, 0.0
	if len(f.terms) != 0 {
		a = float64(len(words)) / float64(len(f.terms))
	}
	if len(f.pairs) != 0 {
		b = float64(len(pairs)) / float64(len(f.pairs))
	}
	return a, b
}

func FuzzFieldSignalsPreserveRegexpSemantics(f *testing.F) {
	f.Add("red access key", "red access key red", "key access red")
	f.Add("not visible", "VISIBLE---view_showOverlay", "Key İ TEMS café")
	f.Add("x\x00 aa_12", "\xffé AA\t12", "")
	f.Fuzz(func(t *testing.T, query, title, idents string) {
		if len(query)+len(title)+len(idents) > 4096 {
			t.Skip()
		}
		for _, s := range []string{query, title, idents} {
			if !reflect.DeepEqual(fieldWords(s), referenceFieldWords(s)) {
				t.Fatalf("token mismatch for %q", s)
			}
		}
		q := newFieldQuery(query)
		a, b := q.match(title, idents)
		x, y := referenceFieldMatch(q, title, idents)
		if a != x || b != y {
			t.Fatalf("feature mismatch: %v/%v versus %v/%v", a, b, x, y)
		}
	})
}

func TestFieldSignalsLongQueryBitBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 65, 66, 129} {
		var words []string
		for i := 0; i < n; i++ {
			words = append(words, fmt.Sprintf("term%d", i))
		}
		text := strings.Join(words, " ")
		q := newFieldQuery(text)
		for _, fields := range [][]string{{text, text}, {strings.Join(words[:n/2], " "), strings.Join(words[n/2:], " ")}, {""}} {
			a, b := q.match(fields...)
			x, y := referenceFieldMatch(q, fields...)
			if a != x || b != y {
				t.Fatalf("%d terms: %v/%v versus %v/%v", n, a, b, x, y)
			}
		}
	}
}

func BenchmarkFieldSignals(b *testing.B) {
	q := newFieldQuery("which field controls the visibility of an editor overlay")
	fields := []string{"Editor overlay visibility control", "view_showOverlay view show overlay editor visibility overlay control control visibility"}
	for _, name := range []string{"regexp_sets", "scan_bits"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if name == "regexp_sets" {
					referenceFieldMatch(q, fields...)
				} else {
					q.match(fields...)
				}
			}
		})
	}
}

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

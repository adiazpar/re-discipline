package search

import (
	"regexp"
	"strings"
)

// Signals are fractions of the strongest current BM25 match. Unlike a fixed
// additive penalty, their strength follows the query's relevance scale. Exact
// identifier priority and superseded-last ordering remain separate sort keys.
const (
	referencePenaltyFraction = 0.3
	coverageFraction         = 0.5
	orderedPairFraction      = 0.25
)

var fieldWordRE = regexp.MustCompile(`[a-z0-9]+`)

// These conservative lexical features supplement FTS5's stemming, never filter
// its candidates. Do not stem again with a different tokenizer or invent synonyms.
func fieldWords(text string) []string {
	words := fieldWordRE.FindAllString(strings.ToLower(text), -1)
	out := words[:0]
	for _, w := range words {
		if len(w) > 1 && !stopTerms[w] {
			out = append(out, w)
		}
	}
	return out
}

type wordPair struct{ first, second string }
type fieldQuery struct {
	terms map[string]bool
	pairs map[wordPair]bool
}

func newFieldQuery(q string) fieldQuery {
	terms, _ := AnalyzeQuery(q)
	f := fieldQuery{terms: map[string]bool{}, pairs: map[wordPair]bool{}}
	for _, w := range fieldWords(strings.Join(terms, " ")) {
		f.terms[w] = true
	}
	words := fieldWords(q)
	for i := 1; i < len(words); i++ {
		f.pairs[wordPair{words[i-1], words[i]}] = true
	}
	return f
}

// Repetition contributes no extra votes. Pair matches never span the title and
// identifier fields. The existing identifier field includes aliases and expanded
// code names; this does not grant those aliases declared-identifier authority.
func (f fieldQuery) match(fields ...string) (coverage, pairs float64) {
	seenWords := map[string]bool{}
	seenPairs := map[wordPair]bool{}
	for _, text := range fields {
		words := fieldWords(text)
		for i, w := range words {
			if f.terms[w] {
				seenWords[w] = true
			}
			if i > 0 {
				p := wordPair{words[i-1], w}
				if f.pairs[p] {
					seenPairs[p] = true
				}
			}
		}
	}
	if len(f.terms) > 0 {
		coverage = float64(len(seenWords)) / float64(len(f.terms))
	}
	if len(f.pairs) > 0 {
		pairs = float64(len(seenPairs)) / float64(len(f.pairs))
	}
	return
}

func applyFieldRanking(rows []ScoredHit) {
	scale, fallback := 0.0, 0.0
	for _, r := range rows {
		fallback = max(fallback, -r.Raw)
		if !r.Superseded {
			scale = max(scale, -r.Raw)
		}
	}
	if scale == 0 {
		scale = fallback
	}
	for i := range rows {
		r := &rows[i]
		if r.Kind == "reference" && !r.Declares {
			r.Penalty = scale * referencePenaltyFraction
		}
		r.Boost = scale * (coverageFraction*r.Coverage + orderedPairFraction*r.OrderedPairs)
		r.Final = r.Raw + r.Penalty - r.Boost
	}
}

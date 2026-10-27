package search

import (
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

// visitFieldWords preserves the experiment's ASCII word definition after
// Unicode lowercasing. Scanning avoids a regexp result slice for every candidate.
// Features supplement FTS5 stemming; they do not filter candidates or add synonyms.
func visitFieldWords(text string, visit func(string)) {
	text = strings.ToLower(text)
	wordByte := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	for i := 0; i < len(text); {
		if !wordByte(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && wordByte(text[i]) {
			i++
		}
		word := text[start:i]
		if len(word) > 1 && !stopTerms[word] {
			visit(word)
		}
	}
}

func fieldWords(text string) []string {
	var words []string
	visitFieldWords(text, func(word string) { words = append(words, word) })
	return words
}

type wordPair struct{ first, second string }
type fieldQuery struct {
	terms map[string]int
	pairs map[wordPair]int
}

func newFieldQuery(q string) fieldQuery {
	terms, _ := AnalyzeQuery(q)
	f := fieldQuery{terms: map[string]int{}, pairs: map[wordPair]int{}}
	for _, w := range fieldWords(strings.Join(terms, " ")) {
		if _, exists := f.terms[w]; !exists {
			f.terms[w] = len(f.terms)
		}
	}
	words := fieldWords(q)
	for i := 1; i < len(words); i++ {
		p := wordPair{words[i-1], words[i]}
		if _, exists := f.pairs[p]; !exists {
			f.pairs[p] = len(f.pairs)
		}
	}
	return f
}

// Repetition contributes no extra votes. Pair matches never span the title and
// identifier fields. The existing identifier field includes aliases and expanded
// code names; this does not grant those aliases declared-identifier authority.
func (f fieldQuery) match(fields ...string) (coverage, pairs float64) {
	// Ordinary questions need two machine words, with no per-candidate maps or
	// token arrays. Long questions retain exact counting through a bounded fallback
	// sized only by the query, never the document or corpus.
	var seenWords, seenPairs uint64
	var longWords, longPairs []bool
	if len(f.terms) > 64 {
		longWords = make([]bool, len(f.terms))
	}
	if len(f.pairs) > 64 {
		longPairs = make([]bool, len(f.pairs))
	}
	wordCount, pairCount := 0, 0
	first := func(index int, mask *uint64, long []bool) bool {
		if long != nil {
			if long[index] {
				return false
			}
			long[index] = true
			return true
		}
		bit := uint64(1) << uint(index)
		if *mask&bit != 0 {
			return false
		}
		*mask |= bit
		return true
	}
	for _, text := range fields {
		previous := ""
		visitFieldWords(text, func(word string) {
			if i, ok := f.terms[word]; ok && first(i, &seenWords, longWords) {
				wordCount++
			}
			if previous != "" {
				if i, ok := f.pairs[wordPair{previous, word}]; ok && first(i, &seenPairs, longPairs) {
					pairCount++
				}
			}
			previous = word
		})
	}
	if len(f.terms) > 0 {
		coverage = float64(wordCount) / float64(len(f.terms))
	}
	if len(f.pairs) > 0 {
		pairs = float64(pairCount) / float64(len(f.pairs))
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

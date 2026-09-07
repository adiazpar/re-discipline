package search

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Hit is one ranked query result.
type Hit struct {
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Snippet  string   `json:"snippet"`
	Status   string   `json:"status,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Grade    string   `json:"grade,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// QueryOptions carries optional constraints for QueryOpts. The zero
// value means "current default behavior": limit 8, no filtering.
type QueryOptions struct {
	Sources string // local, external, or both; consumed by the community client
	Offline bool   // avoid remote requests; consumed by the community client
	Limit   int    // <= 0 means 8
	Kind    string // filter to this kind (fact|ops|reference); empty = no filter
	Grade   string // filter to this grade (direct|inferred|reported); empty = no filter
}

// BM25 column weights for the three indexed columns (title, body,
// idents). Weight arguments map positionally onto ALL table columns,
// UNINDEXED ones included, and missing trailing weights default to 1.0
// — the four UNINDEXED columns trail the indexed three, so passing just
// these three is exact (pinned in TestBM25WeightBehavior). Values chosen
// by sweeping combinations against golden.jsonl; identifier-bearing
// titles and the idents column must outrank incidental body mentions.
const (
	weightTitle  = 4.0
	weightBody   = 1.0
	weightIdents = 2.0
)

// referencePenalty is added to the bm25 score (negative = better, so
// adding worsens) of kind:reference rows whose DECLARED identifiers
// match no query term. Bulk-generated reference docs outnumber curated
// fact docs by an order of magnitude and would otherwise displace them
// on natural-language concept queries; a lookup that names a declared
// identifier ("what does spawnSingleAI do") is exempt, so identifier
// lookups reach their reference doc regardless of this value. Chosen by
// sweeping 0.25–50 against golden.jsonl's fact and reference halves
// separately: 7–15 maximizes both (fact 99/106, reference 41/42); 10 is
// the midpoint of the plateau.
const referencePenalty = 10.0

// rerankFetchMin is how many candidate rows QueryOpts fetches before
// applying referencePenalty in Go: enough that demoting reference rows
// cannot starve the final page of the fact rows ranked just below them.
const rerankFetchMin = 100

// Query refreshes the index if stale (never failing the query for it),
// then returns ranked hits. Superseded docs sort last, labeled by the
// caller via FormatHits.
func Query(root, q string, limit int) ([]Hit, []string, error) {
	return QueryOpts(root, q, QueryOptions{Limit: limit})
}

// QueryOpts is Query with optional kind/grade filtering.
func QueryOpts(root, q string, opts QueryOptions) ([]Hit, []string, error) {
	ranked, _, warnings, err := rank(root, q, opts)
	if err != nil {
		return nil, warnings, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 8
	}
	var hits []Hit
	for _, s := range ranked {
		if len(hits) >= limit {
			break
		}
		hits = append(hits, s.Hit)
	}
	return hits, warnings, nil
}

// ScoredHit is one candidate with the ranking arithmetic left visible:
// what bm25 said, what the reference penalty added, and what the two
// came to. Explain reports these so a surprising result can be read
// rather than guessed at.
type ScoredHit struct {
	Hit
	Raw     float64 `json:"raw"`     // bm25, negative, more negative is better
	Penalty float64 `json:"penalty"` // referencePenalty, or 0
	Final   float64 `json:"final"`   // Raw + Penalty; the sort key
	// Declares reports that this doc names one of the query's terms in
	// its own idents frontmatter, which is what waives the penalty.
	Declares   bool `json:"declares_query_term"`
	Superseded bool `json:"superseded"`
}

// rank runs the whole ranking pipeline and returns every candidate it
// considered, best first, alongside the FTS5 expression the question
// became. QueryOpts pages it; Explain reports it. Both go through here
// so the explanation can never drift from the answer.
func rank(root, q string, opts QueryOptions) ([]ScoredHit, string, []string, error) {
	warnings := EnsureFresh(root)
	match := BuildMatch(q)
	if match == "" {
		return nil, "", warnings, nil
	}
	dbPath := IndexPath(root)
	if _, err := os.Stat(dbPath); err != nil {
		return nil, match, append(warnings, "no index and rebuild unavailable; no results"), nil
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, match, warnings, err
	}
	defer db.Close()
	if opts.Limit <= 0 {
		opts.Limit = 8
	}
	// bm25() returns negative scores (more negative = better), so the
	// ascending ORDER BY is correct; pinned in TestBM25WeightBehavior.
	// More candidates than the page are fetched so the Go-side
	// reference-penalty re-rank below has the fact rows that belong on
	// the page after reference rows are demoted past them.
	q1 := fmt.Sprintf(`
		SELECT docs.path, docs.title, docs.status, docs.kind, docs.grade,
		       snippet(docs, 1, '«', '»', '…', 40),
		       COALESCE(docmeta.evidence, ''),
		       COALESCE(docmeta.idents, ''),
		       bm25(docs, %v, %v, %v) AS score
		FROM docs LEFT JOIN docmeta ON docmeta.path = docs.path
		WHERE docs MATCH ?`, weightTitle, weightBody, weightIdents)
	args := []any{match}
	if opts.Kind != "" {
		q1 += ` AND docs.kind = ?`
		args = append(args, opts.Kind)
	}
	if opts.Grade != "" {
		q1 += ` AND docs.grade = ?`
		args = append(args, opts.Grade)
	}
	q1 += ` ORDER BY (docs.status = 'superseded') ASC, score LIMIT ?`
	fetch := opts.Limit * 20
	if fetch < rerankFetchMin {
		fetch = rerankFetchMin
	}
	args = append(args, fetch)
	rows, err := db.Query(q1, args...)
	if err != nil {
		return nil, match, warnings, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()
	queryTerms := map[string]bool{}
	for _, t := range strings.Split(match, " OR ") {
		queryTerms[strings.Trim(t, `"`)] = true
	}
	var all []ScoredHit
	for rows.Next() {
		var h Hit
		var evidence, declared string
		var score float64
		if err := rows.Scan(&h.Path, &h.Title, &h.Status, &h.Kind, &h.Grade, &h.Snippet, &evidence, &declared, &score); err != nil {
			return nil, match, warnings, err
		}
		if evidence != "" {
			h.Evidence = strings.Split(evidence, "\n")
		}
		s := ScoredHit{Hit: h, Raw: score, Final: score,
			Declares: declaredIdentMatch(declared, queryTerms), Superseded: h.Status == "superseded"}
		if h.Kind == "reference" && !s.Declares {
			s.Penalty = referencePenalty
			s.Final = score + referencePenalty
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, match, warnings, err
	}
	// Superseded-last stays the outermost sort key, exactly as in the
	// SQL ORDER BY; the stable sort preserves bm25 order among ties.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Superseded != all[j].Superseded {
			return !all[i].Superseded
		}
		return all[i].Final < all[j].Final
	})
	return all, match, warnings, nil
}

// declaredIdentMatch reports whether any query term equals one of a
// doc's declared identifiers (both sides lowercased). A doc that
// declares the identifier the user typed is the canonical answer for
// it and must never be penalized for its kind.
func declaredIdentMatch(declared string, queryTerms map[string]bool) bool {
	for _, id := range strings.Fields(declared) {
		if queryTerms[id] {
			return true
		}
	}
	return false
}

// FormatHits renders hits as a numbered plain-text list for terminals
// and MCP text content.
func FormatHits(hits []Hit) string {
	if len(hits) == 0 {
		return "No results. Try rewording, or grep .re-discipline/docs/ directly.\n"
	}
	var b strings.Builder
	for i, h := range hits {
		label := strings.Join(compact(h.Status, h.Kind, h.Grade), "/")
		prefix := ""
		if h.Status == "superseded" {
			prefix = "[SUPERSEDED] "
		}
		fmt.Fprintf(&b, "%d. %s[%s] %s\n   %s\n   %s\n", i+1, prefix, label, h.Path, h.Title, h.Snippet)
	}
	return b.String()
}

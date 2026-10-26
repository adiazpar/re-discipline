package search

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/internal/indexdb"
)

// sortByRaw orders candidates as bm25 alone would have, superseded last,
// mirroring rank's own tie-breaking so the two orders are comparable.
func sortByRaw(rows []*ScoredHit) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Superseded != rows[j].Superseded {
			return !rows[i].Superseded
		}
		return rows[i].Raw < rows[j].Raw
	})
}

// Ranking reports the constants that decide an order. They are compiled
// in rather than configured, so a caller that wants to describe or draw
// the ranking has to be told them rather than read them from a file.
type Ranking struct {
	TitleWeight              float64 `json:"title_weight"`
	IdentsWeight             float64 `json:"idents_weight"`
	BodyWeight               float64 `json:"body_weight"`
	ReferencePenaltyFraction float64 `json:"reference_penalty_fraction"`
	CoverageFraction         float64 `json:"coverage_fraction"`
	OrderedPairFraction      float64 `json:"ordered_pair_fraction"`
	DefaultLimit             int     `json:"default_limit"`
	CandidateFetch           int     `json:"candidate_fetch_min"`
	Tokenizer                string  `json:"tokenizer"`
	StopWords                int     `json:"stop_words"`
}

// CurrentRanking returns the constants this build ranks with.
func CurrentRanking() Ranking {
	return Ranking{
		TitleWeight: weightTitle, IdentsWeight: weightIdents, BodyWeight: weightBody,
		ReferencePenaltyFraction: referencePenaltyFraction, CoverageFraction: coverageFraction,
		OrderedPairFraction: orderedPairFraction, DefaultLimit: 8,
		CandidateFetch: rerankFetchMin, Tokenizer: "porter unicode61",
		StopWords: len(stopTerms),
	}
}

// StatsResult is a census of one knowledge base and the machine that
// searches it.
type StatsResult struct {
	Root            string         `json:"root"`
	Documents       int            `json:"documents"`
	ByKind          map[string]int `json:"by_kind"`
	ByStatus        map[string]int `json:"by_status"`
	ByGrade         map[string]int `json:"by_grade"`
	Symbols         int            `json:"symbols"`
	GoldenQuestions int            `json:"golden_questions"`
	IndexFormat     string         `json:"index_format"`
	Ranking         Ranking        `json:"ranking"`
}

// Stats counts what is in the index, refreshing it first if stale. It is
// the shape of the corpus, not a health check: nothing here is a pass or
// a fail, and an empty knowledge base reports zeroes rather than erroring.
func Stats(root string) (StatsResult, []string, error) {
	warnings := EnsureFresh(root)
	out := StatsResult{
		Root: root, Ranking: CurrentRanking(),
		ByKind: map[string]int{}, ByStatus: map[string]int{}, ByGrade: map[string]int{},
	}
	out.GoldenQuestions = countGolden(root)

	dbPath := IndexPath(root)
	if _, err := os.Stat(dbPath); err != nil {
		return out, append(warnings, "no index yet; run: re-search index"), nil
	}
	db, err := indexdb.OpenRead(dbPath)
	if err != nil {
		return out, warnings, err
	}
	defer db.Close()

	if err := indexdb.ReadRow(context.Background(), db, `SELECT count(*) FROM docs`).Scan(&out.Documents); err != nil {
		return out, warnings, err
	}
	if err := indexdb.ReadRow(context.Background(), db, `SELECT count(*) FROM symbols`).Scan(&out.Symbols); err != nil {
		return out, warnings, err
	}
	// An index written by an older build may predate indexmeta; an
	// unknown format is reported, not treated as a failure.
	_ = indexdb.ReadRow(context.Background(), db, `SELECT value FROM indexmeta WHERE key = 'format'`).Scan(&out.IndexFormat)

	for col, into := range map[string]map[string]int{
		"kind": out.ByKind, "status": out.ByStatus, "grade": out.ByGrade,
	} {
		rows, err := indexdb.ReadRows(context.Background(), db, `SELECT `+col+`, count(*) FROM docs GROUP BY `+col)
		if err != nil {
			return out, warnings, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return out, warnings, err
			}
			if k == "" {
				k = "(unset)"
			}
			into[k] = n
		}
		rows.Close()
	}
	return out, warnings, nil
}

func countGolden(root string) int {
	f, err := os.Open(filepath.Join(root, ".re-discipline", "golden.jsonl"))
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

// ExplainRow is one candidate plus where it sat before and after the
// field signals and reference preference were applied.
type ExplainRow struct {
	ScoredHit
	RawRank   int `json:"raw_rank"`
	FinalRank int `json:"final_rank"`
}

// ExplainResult is everything behind one answer: what the question was
// reduced to, and every candidate considered with its arithmetic shown.
type ExplainResult struct {
	Question string       `json:"question"`
	Terms    []string     `json:"terms"`
	Dropped  []string     `json:"dropped"`
	Match    string       `json:"match"`
	Returned int          `json:"returned"`
	Ranking  Ranking      `json:"ranking"`
	Rows     []ExplainRow `json:"rows"`
}

// Explain answers a question and shows its working. It runs the same
// ranking pipeline as Query -- not a reimplementation of it -- so what
// it reports is what a search returns.
//
// limit is the page size being explained (the cut line), not how many
// rows come back: every candidate the ranker considered is returned, so
// a caller can see what sat just below the page.
func Explain(root, q string, limit int) (ExplainResult, []string, error) {
	if limit <= 0 {
		limit = 8
	}
	terms, dropped := AnalyzeQuery(q)
	out := ExplainResult{
		Question: q, Terms: terms, Dropped: dropped,
		Returned: limit, Ranking: CurrentRanking(),
	}
	ranked, match, warnings, err := rank(root, q, QueryOptions{Limit: limit})
	out.Match = match
	if err != nil {
		return out, warnings, err
	}

	// ranked is already in final order. Re-sorting a copy by raw score
	// recovers where each row sat before the adjustments, which is the whole
	// point of the report.
	byRaw := make([]*ScoredHit, len(ranked))
	for i := range ranked {
		byRaw[i] = &ranked[i]
	}
	sortByRaw(byRaw)
	rawRank := map[string]int{}
	for i, s := range byRaw {
		rawRank[s.Path] = i + 1
	}
	for i, s := range ranked {
		out.Rows = append(out.Rows, ExplainRow{ScoredHit: s, RawRank: rawRank[s.Path], FinalRank: i + 1})
	}
	return out, warnings, nil
}

// FormatStats renders a census as plain text for a terminal.
func FormatStats(s StatsResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d documents, %d symbols, %d golden questions\n",
		s.Documents, s.Symbols, s.GoldenQuestions)
	if s.IndexFormat != "" {
		fmt.Fprintf(&b, "index format %s\n", s.IndexFormat)
	}
	for _, g := range []struct {
		label string
		m     map[string]int
	}{{"kind", s.ByKind}, {"status", s.ByStatus}, {"grade", s.ByGrade}} {
		if len(g.m) == 0 {
			continue
		}
		keys := make([]string, 0, len(g.m))
		for k := range g.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "\nby %s\n", g.label)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-12s %6d\n", k, g.m[k])
		}
	}
	r := s.Ranking
	fmt.Fprintf(&b, "\nranking\n")
	fmt.Fprintf(&b, "  weights      title x%v, idents x%v, body x%v\n",
		r.TitleWeight, r.IdentsWeight, r.BodyWeight)
	fmt.Fprintf(&b, "  penalty      +%v x strongest current BM25 magnitude for reference docs declaring no query term\n", r.ReferencePenaltyFraction)
	fmt.Fprintf(&b, "  field boost  coverage x%v + ordered pairs x%v, on the same BM25 scale\n", r.CoverageFraction, r.OrderedPairFraction)
	b.WriteString("  exact names  declared identifiers first within current/superseded groups\n")
	fmt.Fprintf(&b, "  page size    %d (from %d candidates)\n", r.DefaultLimit, r.CandidateFetch)
	fmt.Fprintf(&b, "  tokenizer    %s, %d stop words\n", r.Tokenizer, r.StopWords)
	return b.String()
}

// FormatExplain renders one answer's working as plain text. Rows the
// penalty moved are marked, since those are the ones worth looking at.
func FormatExplain(e ExplainResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "question   %s\n", e.Question)
	fmt.Fprintf(&b, "searched   %s\n", strings.Join(e.Terms, " "))
	if e.Match != BuildMatch(e.Question) {
		fmt.Fprintf(&b, "expanded   %s\n", e.Match)
	}
	if len(e.Dropped) > 0 {
		fmt.Fprintf(&b, "dropped    %s\n", strings.Join(e.Dropped, " "))
	}
	if len(e.Rows) == 0 {
		b.WriteString("\nno candidates matched\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%d candidates considered, %d returned. Current documents precede superseded ones; exact declared names precede score ranking within each group. Scores are negative; lower is better.\n\n",
		len(e.Rows), e.Returned)
	fmt.Fprintf(&b, "  %-4s %-4s %9s %7s %7s %9s  %s\n", "was", "now", "bm25", "penalty", "boost", "final", "path")
	for _, r := range e.Rows {
		if r.FinalRank > e.Returned {
			break
		}
		note := ""
		if r.ExactIdentifier {
			note = "  (declares the exact lookup; ranked before score)"
		} else if r.Penalty > 0 {
			note = "  (reference, declares none of your terms)"
		} else if r.Declares {
			note = "  (declares your term; penalty waived)"
		}
		fmt.Fprintf(&b, "  %-4d %-4d %9.3f %7.3f %7.3f %9.3f  %s%s\n",
			r.RawRank, r.FinalRank, r.Raw, r.Penalty, r.Boost, r.Final, r.Path, note)
	}
	return b.String()
}

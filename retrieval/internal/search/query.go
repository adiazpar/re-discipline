package search

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/internal/indexdb"
)

// Hit is one ranked query result.
type Hit struct {
	CreatedAt          string   `json:"created_at,omitempty"`
	KnowledgeUpdatedAt string   `json:"knowledge_updated_at,omitempty"`
	FindingID          string   `json:"finding_id,omitempty"`
	Fingerprint        string   `json:"fingerprint,omitempty"`
	Build              string   `json:"build,omitempty"`
	TextDigest         string   `json:"text_digest,omitempty"`
	Path               string   `json:"path"`
	Title              string   `json:"title"`
	Snippet            string   `json:"snippet"`
	Status             string   `json:"status,omitempty"`
	Kind               string   `json:"kind,omitempty"`
	Grade              string   `json:"grade,omitempty"`
	Evidence           []string `json:"evidence,omitempty"`
}

// QueryOptions carries optional constraints for QueryOpts. The zero
// value means "current default behavior": limit 8, no filtering.
type QueryOptions struct {
	CompareIDs  []string // bounded local candidate identities, internal remote reconciliation
	Root        string
	Assistance  string   // configured (default) or off; provider is always optional
	Sources     string   // local, external, or both; consumed by the community client
	Communities []string // explicit connected aliases; required when more than one is connected
	Offline     bool     // avoid remote requests; consumed by the community client
	Limit       int      // <= 0 means 8
	Kind        string   // filter to this kind (fact|ops|reference); empty = no filter
	Grade       string   // filter to this grade (direct|inferred|reported); empty = no filter
}

// BM25 column weights for the three indexed columns (title, body,
// idents). Weight arguments map positionally onto ALL table columns,
// UNINDEXED ones included, and missing trailing weights default to 1.0
// — the four UNINDEXED columns trail the indexed three, so passing just
// these three is exact (pinned in TestBM25WeightBehavior). Values chosen
// by sweeping combinations against golden.jsonl; identifier-bearing
// titles and the idents column must outrank incidental body mentions.
const (
	weightTitle  = 2.0
	weightBody   = 1.0
	weightIdents = 4.0
)

// rerankFetchMin is how many candidate rows QueryOpts fetches before
// applying field coverage and reference preference in Go: enough that demoting reference rows
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
	return queryIndex(root, q, opts, true)
}

// QuerySnapshot reads an explicitly built immutable index. Its owner must select
// a new snapshot when content changes. Mutable project queries use QueryOpts.
func QuerySnapshot(root, q string, opts QueryOptions) ([]Hit, []string, error) {
	return QuerySnapshotContext(context.Background(), root, q, opts)
}
func QuerySnapshotContext(ctx context.Context, root, q string, opts QueryOptions) ([]Hit, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(IndexPath(root)); err != nil {
		return nil, nil, err
	}
	ranked, _, warnings, err := rankIndexContext(ctx, root, q, opts, false, false)
	if err != nil {
		return nil, warnings, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 8
	}
	hits := make([]Hit, 0, min(limit, len(ranked)))
	for _, h := range ranked[:min(limit, len(ranked))] {
		hits = append(hits, h.Hit)
	}
	return hits, warnings, nil
}

func queryIndex(root, q string, opts QueryOptions, refresh bool) ([]Hit, []string, error) {
	ranked, _, warnings, err := rankIndexContext(context.Background(), root, q, opts, refresh, false)
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

// ScoredHit exposes the arithmetic shared by query and immutable snapshots.
type ScoredHit struct {
	Hit
	rowID        int64   // SQLite identity within this read transaction; never exposed.
	Raw          float64 `json:"raw"`
	Penalty      float64 `json:"penalty"`
	Boost        float64 `json:"boost"`
	Final        float64 `json:"final"`
	Coverage     float64 `json:"coverage"`
	OrderedPairs float64 `json:"ordered_pairs"`
	// Declares reports that this doc names one of the query's terms in
	// its own idents frontmatter, which is what waives the penalty.
	Declares   bool `json:"declares_query_term"`
	Superseded bool `json:"superseded"`
	// ExactIdentifier is a whole-query lookup of a declared identifier, not
	// a body mention or an alias. It outranks BM25 within the same status.
	ExactIdentifier bool `json:"exact_identifier"`
}

// rank returns every candidate, including its payload, for Explain. Normal
// queries use the same candidate selection and scoring, then hydrate only their
// final page. Both stages share one read transaction, including for local indexes.
func rank(root, q string, opts QueryOptions) ([]ScoredHit, string, []string, error) {
	return rankIndexContext(context.Background(), root, q, opts, true, true)
}

func rankIndexContext(ctx context.Context, root, q string, opts QueryOptions, refresh, hydrateAll bool) ([]ScoredHit, string, []string, error) {
	var warnings []string
	if refresh {
		warnings = EnsureFresh(root)
	}
	match := BuildMatch(q)
	if match == "" {
		return nil, "", warnings, nil
	}
	dbPath := IndexPath(root)
	if _, err := os.Stat(dbPath); err != nil {
		return nil, match, append(warnings, "no index and rebuild unavailable; no results"), nil
	}
	db, err := indexdb.OpenRead(dbPath)
	if err != nil {
		return nil, match, warnings, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, match, warnings, err
	}
	defer tx.Rollback()
	if !refresh {
		var version string
		if err := indexdb.ReadRow(ctx, tx, `SELECT value FROM indexmeta WHERE key = 'format'`).Scan(&version); err != nil {
			return nil, match, warnings, err
		}
		if version != indexFormatVersion {
			return nil, match, warnings, fmt.Errorf("snapshot index has an unsupported format")
		}
	}
	if opts.Limit <= 0 {
		opts.Limit = 8
	}
	all, err := rankCandidates(ctx, tx, q, match, opts)
	if err != nil {
		return nil, match, warnings, err
	}
	if !hydrateAll {
		all = all[:min(opts.Limit, len(all))]
	}
	if err := hydrateCandidates(ctx, tx, match, all); err != nil {
		return nil, match, warnings, err
	}
	return all, match, warnings, nil
}

func rankCandidates(ctx context.Context, tx *sql.Tx, q, match string, opts QueryOptions) ([]ScoredHit, error) {
	// bm25() returns negative scores (more negative = better), so the
	// ascending ORDER BY is correct; pinned in TestBM25WeightBehavior.
	// More candidates than the page are fetched so the Go-side
	// reference-penalty re-rank below has the fact rows that belong on
	// the page after reference rows are demoted past them.
	exact := strings.ToLower(strings.Trim(strings.TrimSpace(q), "\"`"))
	if exact == "" || wordRe.FindString(exact) != exact {
		exact = ""
	}
	q1 := fmt.Sprintf(`WITH candidates AS MATERIALIZED (
		SELECT docs.rowid AS candidate_rowid,
		       (docs.status = 'superseded') AS superseded,
		       bm25(docs, %v, %v, %v) AS score,
		       (? <> '' AND instr(' ' || COALESCE(docmeta.idents, '') || ' ', ' ' || ? || ' ') > 0) AS exact_identifier
		FROM docs LEFT JOIN docmeta ON docmeta.path = docs.path
		WHERE docs MATCH ?`, weightTitle, weightBody, weightIdents)
	args := []any{exact, exact, match}
	if opts.Kind != "" {
		q1 += ` AND docs.kind = ?`
		args = append(args, opts.Kind)
	}
	if opts.Grade != "" {
		q1 += ` AND docs.grade = ?`
		args = append(args, opts.Grade)
	}
	// Select declared exact answers before the candidate limit too: a long
	// authoritative document must not be crowded out by short mentions.
	q1 += ` ORDER BY superseded ASC, exact_identifier DESC, score LIMIT ?`
	fetch := opts.Limit * 20
	if fetch < rerankFetchMin {
		fetch = rerankFetchMin
	}
	args = append(args, fetch)
	// Fetch only fields used by the ranker. Snippets and full metadata are
	// loaded after the final order and page are known, in this same snapshot.
	q1 += `)
		SELECT candidates.candidate_rowid, docs.title, docs.status, docs.kind,
		       COALESCE(docmeta.idents, ''), docs.idents,
		       candidates.score, candidates.exact_identifier
		FROM candidates JOIN docs ON docs.rowid = candidates.candidate_rowid
		LEFT JOIN docmeta ON docmeta.path = docs.path
		ORDER BY candidates.superseded ASC, candidates.exact_identifier DESC,
		         candidates.score, candidates.candidate_rowid`
	rows, err := indexdb.ReadRows(ctx, tx, q1, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()
	queryTerms := map[string]bool{}
	for _, t := range strings.Split(match, " OR ") {
		queryTerms[strings.Trim(t, `"`)] = true
	}
	var all []ScoredHit
	features := newFieldQuery(q)
	for rows.Next() {
		var s ScoredHit
		var declared, indexedIdents string
		if err := rows.Scan(&s.rowID, &s.Title, &s.Status, &s.Kind, &declared, &indexedIdents, &s.Raw, &s.ExactIdentifier); err != nil {
			return nil, err
		}
		s.Final = s.Raw
		s.Declares = declaredIdentMatch(declared, queryTerms)
		s.Superseded = s.Status == "superseded"
		s.Coverage, s.OrderedPairs = features.match(s.Title, indexedIdents)
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	applyFieldRanking(all)
	// Superseded-last stays the outermost sort key, exactly as in the
	// SQL ORDER BY; the stable sort preserves bm25 order among ties.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Superseded != all[j].Superseded {
			return !all[i].Superseded
		}
		if all[i].ExactIdentifier != all[j].ExactIdentifier {
			return all[i].ExactIdentifier
		}
		return all[i].Final < all[j].Final
	})
	return all, nil
}

// hydrateCandidates never changes ranking. Batching keeps Explain and large
// internal pages below SQLite's parameter limit. The transaction pins the rowids,
// metadata and highlighted body to the snapshot used for candidate selection.
func hydrateCandidates(ctx context.Context, tx *sql.Tx, match string, ranked []ScoredHit) error {
	const batchSize = 256
	for offset := 0; offset < len(ranked); offset += batchSize {
		batch := ranked[offset:min(offset+batchSize, len(ranked))]
		args := []any{match}
		byID := make(map[int64]*Hit, len(batch))
		for i := range batch {
			args = append(args, batch[i].rowID)
			byID[batch[i].rowID] = &batch[i].Hit
		}
		query := `SELECT docs.rowid, docs.path, docs.title, docs.status, docs.kind, docs.grade,
		       snippet(docs, 1, '«', '»', '…', 40),
		       COALESCE(docmeta.evidence, ''),
		       COALESCE(docmeta.build, ''), COALESCE(docmeta.text_digest, ''),
		       COALESCE(docmeta.finding_id, ''), COALESCE(docmeta.fingerprint, ''),
		       COALESCE(docmeta.created_at, ''), COALESCE(docmeta.knowledge_updated_at, '')
		FROM docs LEFT JOIN docmeta ON docmeta.path = docs.path
		WHERE docs MATCH ? AND docs.rowid IN (` + strings.TrimRight(strings.Repeat("?,", len(batch)), ",") + `)`
		rows, err := indexdb.ReadRows(ctx, tx, query, args...)
		if err != nil {
			return fmt.Errorf("candidate payload: %w", err)
		}
		// Close each batch before starting another on the single connection.
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var h Hit
				var id int64
				var evidence string
				if err := rows.Scan(&id, &h.Path, &h.Title, &h.Status, &h.Kind, &h.Grade, &h.Snippet, &evidence, &h.Build, &h.TextDigest, &h.FindingID, &h.Fingerprint, &h.CreatedAt, &h.KnowledgeUpdatedAt); err != nil {
					return err
				}
				dest, ok := byID[id]
				if !ok {
					return fmt.Errorf("unexpected candidate payload row %d", id)
				}
				if evidence != "" {
					h.Evidence = strings.Split(evidence, "\n")
				}
				*dest = h
				delete(byID, id)
			}
			return rows.Err()
		}()
		if err != nil {
			return err
		}
		if len(byID) != 0 {
			return fmt.Errorf("candidate payload missing from read snapshot")
		}
	}
	return nil
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

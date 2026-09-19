package search

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

// IndexPath returns the canonical index location for a project root.
func IndexPath(root string) string {
	return filepath.Join(root, ".re-discipline", "index.db")
}

// indexFormatVersion is stored in the indexmeta table and checked by
// schemaCurrent. Bump it whenever indexing CONTENT changes (stripping,
// tokenization, column composition), not just table shape — an index
// built by an older binary must be rebuilt even when the corpus is
// unchanged, or queries silently serve stale term data.
// 4: added the symbols table (exact-name lookup, outside FTS).
// 5: aliases frontmatter is indexed into the idents column.
// 6: porter stemming, so "wraps" matches "wrapping".
const indexFormatVersion = "7" // applicability and indexed text identity for revision-bound assistance

// relationLinkRe matches "- <Label>: ..." bullets inside a Relations
// section ("- Depends on:", "- Split from:", and future labels alike).
var relationLinkRe = regexp.MustCompile(`^\s*-\s+[A-Za-z][A-Za-z ]*:\s`)

// StripRelationLinks removes cross-reference link bullets from a doc's
// "## Relations" section for INDEXING only — the file is untouched.
// Those bullets quote other docs' titles verbatim (corpus survey: 1,516
// "- Depends on:" + 130 "- Split from:" lines across 243 docs), which
// makes every doc compete for every related doc's topic terms. Free
// prose inside the section is real content and is kept.
func StripRelationLinks(body string) string {
	if !strings.Contains(body, "## Relations") {
		return body
	}
	lines := strings.Split(body, "\n")
	out := lines[:0]
	inRelations := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			inRelations = trimmed == "## Relations"
		}
		if inRelations && relationLinkRe.MatchString(line) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// BuildIndexFile writes a complete FTS5 index + manifest to dbPath.
// Corpus problems degrade to warnings; only I/O or SQL failures error.
func BuildIndexFile(root, dbPath string) ([]Doc, []string, error) {
	metas, err := ScanDocs(root)
	if err != nil {
		return nil, nil, err
	}
	docs, warnings := LoadDocs(root, metas)
	symMeta := ScanSymbols(root)
	syms, symWarnings := LoadSymbols(root)
	warnings = append(warnings, symWarnings...)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, warnings, err
	}
	defer db.Close()

	stmts := []string{
		// Porter stemming folds word forms together, so a question asking
		// what "wraps" the executable reaches a doc that says "wrapping".
		// Measured twice on the same 12,314-doc corpus: worth -1 on its
		// own, worth +4 once aliases exist (215 -> 219 of 231). Aliases are
		// natural-language phrasings, which is exactly the text whose word
		// forms vary; stemming has little to fold in an identifier dump.
		`CREATE VIRTUAL TABLE docs USING fts5(title, body, idents, path UNINDEXED, status UNINDEXED, kind UNINDEXED, grade UNINDEXED, tokenize = 'porter unicode61')`,
		`CREATE TABLE manifest(path TEXT PRIMARY KEY, mtime INTEGER, size INTEGER)`,
		// Per-doc metadata that must stay out of the searchable text
		// (evidence holds file paths — indexing them would add noise).
		// idents holds the DECLARED identifiers verbatim-lowercased,
		// space-joined, for query-time declared-ident matching — distinct
		// from the docs.idents FTS column, which also carries identifiers
		// expanded out of the title and body.
		`CREATE TABLE docmeta(path TEXT PRIMARY KEY, evidence TEXT, idents TEXT, build TEXT, text_digest TEXT)`,
		`CREATE TABLE indexmeta(key TEXT PRIMARY KEY, value TEXT)`,
		// Symbols are exact-name records (struct layouts, constants) from
		// the optional .re-discipline/symbols.jsonl. They deliberately
		// stay OUT of the docs FTS table: tens of thousands of bulk
		// records would poison document frequencies for free-text search.
		`CREATE TABLE symbols(name TEXT NOT NULL, kind TEXT NOT NULL, render TEXT NOT NULL, source TEXT NOT NULL)`,
		`CREATE INDEX symbols_name ON symbols(name COLLATE NOCASE)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return nil, warnings, err
		}
	}
	if _, err := db.Exec(`INSERT INTO indexmeta(key, value) VALUES('format', ?)`, indexFormatVersion); err != nil {
		return nil, warnings, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, warnings, err
	}
	for _, d := range docs {
		indexBody := StripRelationLinks(d.Body)
		parts := ExpandIdentifiers(d.Title + " " + indexBody)
		parts = append(parts, d.Tags...)
		// Declared idents go in both verbatim-lowercased and expanded,
		// so a declared ai_forceIdle matches queries for ai_forceIdle,
		// forceIdle, force, and idle.
		for _, id := range d.Idents {
			parts = append(parts, strings.ToLower(id))
		}
		if len(d.Idents) > 0 {
			parts = append(parts, ExpandIdentifiers(strings.Join(d.Idents, " "))...)
		}
		// Aliases are indexed as ordinary searchable text in the idents
		// column. They deliberately do NOT enter docmeta.idents: an alias
		// is a phrasing hint, not a claim of authority over an identifier,
		// so it must not waive the reference penalty.
		for _, a := range d.Aliases {
			parts = append(parts, strings.Fields(strings.ToLower(a))...)
			parts = append(parts, ExpandIdentifiers(a)...)
		}
		idents := strings.Join(parts, " ")
		if _, err := tx.Exec(`INSERT INTO docs(title, body, idents, path, status, kind, grade) VALUES(?,?,?,?,?,?,?)`,
			d.Title, indexBody, idents, d.Path, d.Status, d.Kind, d.Grade); err != nil {
			tx.Rollback()
			return nil, warnings, err
		}
		{
			// Each declared ident is stored verbatim-lowercased plus
			// with :: and _ collapsed, mirroring the two whole-identifier
			// forms BuildMatch emits, so either spelling of a query
			// matches. Split segments are deliberately NOT stored: a doc
			// declaring spawnSingleAI is not the canonical answer for
			// every query containing "spawn".
			var declared []string
			for _, id := range d.Idents {
				lower := strings.ToLower(id)
				declared = append(declared, lower)
				collapsed := strings.ReplaceAll(strings.ReplaceAll(lower, "::", ""), "_", "")
				if collapsed != lower {
					declared = append(declared, collapsed)
				}
			}
			if _, err := tx.Exec(`INSERT INTO docmeta(path, evidence, idents, build, text_digest) VALUES(?,?,?,?,?)`,
				d.Path, strings.Join(d.Evidence, "\n"), strings.Join(declared, " "), d.Build, d.TextDigest); err != nil {
				tx.Rollback()
				return nil, warnings, err
			}
		}
	}
	for _, s := range syms {
		if _, err := tx.Exec(`INSERT INTO symbols(name, kind, render, source) VALUES(?,?,?,?)`,
			s.Name, s.Kind, s.Render, s.Source); err != nil {
			tx.Rollback()
			return nil, warnings, err
		}
	}
	if symMeta != nil {
		metas = append(metas, *symMeta)
	}
	for _, m := range metas {
		if _, err := tx.Exec(`INSERT INTO manifest(path, mtime, size) VALUES(?,?,?)`, m.Path, m.MTime, m.Size); err != nil {
			tx.Rollback()
			return nil, warnings, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, warnings, err
	}
	return docs, warnings, nil
}

// ReadManifest loads the stored file manifest from an index database.
func ReadManifest(dbPath string) (map[string]FileMeta, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT path, mtime, size FROM manifest`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FileMeta{}
	for rows.Next() {
		var m FileMeta
		if err := rows.Scan(&m.Path, &m.MTime, &m.Size); err != nil {
			return nil, err
		}
		out[m.Path] = m
	}
	return out, rows.Err()
}

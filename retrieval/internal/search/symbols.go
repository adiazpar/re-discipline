package search

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/internal/indexdb"
)

// symbolsRelPath is the optional symbol corpus, relative to
// .re-discipline/ — the same base the doc manifest uses.
const symbolsRelPath = "symbols.jsonl"

// Symbol is one exact-name record from .re-discipline/symbols.jsonl.
// Symbols are bulk machine-derived records (struct layouts, constants)
// that would poison FTS document frequencies if indexed as docs; they
// live in their own table and are queried by name, never by free text.
type Symbol struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`   // e.g. struct | constant | group
	Render string `json:"render"`           // human-readable, shown verbatim
	Source string `json:"source,omitempty"` // provenance path
}

// SymbolHits is the result of one symbol lookup.
type SymbolHits struct {
	Exact   bool     `json:"exact"` // Symbols matched the name exactly (case-insensitive)
	Total   int      `json:"total"` // matching rows before Limit truncation
	Symbols []Symbol `json:"symbols"`
}

// SymbolsPath returns the symbol corpus location for a project root.
func SymbolsPath(root string) string {
	return filepath.Join(root, ".re-discipline", symbolsRelPath)
}

// ScanSymbols stats the optional symbol corpus for staleness tracking.
// A missing (or unreadable) file returns nil: symbols are optional and
// their absence is never an error.
func ScanSymbols(root string) *FileMeta {
	info, err := os.Stat(SymbolsPath(root))
	if err != nil || info.IsDir() {
		return nil
	}
	return &FileMeta{Path: symbolsRelPath, MTime: info.ModTime().UnixNano(), Size: info.Size()}
}

// LoadSymbols parses the symbol corpus. Missing file yields no symbols
// and no warnings; malformed lines are skipped with one summary warning
// — corpus content never fails a build, mirroring LoadDocs.
func LoadSymbols(root string) ([]Symbol, []string) {
	f, err := os.Open(SymbolsPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("%s: %v (skipped)", symbolsRelPath, err)}
	}
	defer f.Close()

	var syms []Symbol
	bad := 0
	firstBad := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var s Symbol
		if err := json.Unmarshal([]byte(line), &s); err != nil || s.Name == "" || s.Render == "" {
			bad++
			if firstBad == 0 {
				firstBad = lineNo
			}
			continue
		}
		syms = append(syms, s)
	}
	var warnings []string
	if err := scanner.Err(); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: %v (partial read)", symbolsRelPath, err))
	}
	if bad > 0 {
		warnings = append(warnings, fmt.Sprintf("%s: skipped %d malformed line(s), first at line %d", symbolsRelPath, bad, firstBad))
	}
	return syms, warnings
}

// escapeLike makes s literal inside a LIKE pattern using ESCAPE '\'.
// Symbol names are full of underscores, which LIKE would otherwise
// treat as single-character wildcards.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// LookupSymbol resolves a symbol by exact name (case-insensitive).
// When nothing matches exactly it falls back to substring matching so
// a half-remembered fragment ("langEntry") still finds
// idLangDict_langEntry_t — engine identifiers bury the memorable part
// mid-name, which rules out prefix-only matching, and the table is
// small enough (~28k rows) that a LIKE scan is instant. The fallback
// only runs when exact matching found nothing, so it can never
// displace an exact hit. Limit <= 0 means 5, matching QueryOpts.
func LookupSymbol(root, name string, limit int) (SymbolHits, []string, error) {
	warnings := EnsureFresh(root)
	var res SymbolHits
	name = strings.TrimSpace(name)
	if name == "" {
		return res, warnings, nil
	}
	dbPath := IndexPath(root)
	if _, err := os.Stat(dbPath); err != nil {
		return res, append(warnings, "no index and rebuild unavailable; no results"), nil
	}
	db, err := indexdb.OpenRead(dbPath)
	if err != nil {
		return res, warnings, err
	}
	defer db.Close()
	if limit <= 0 {
		limit = 5
	}

	fetch := func(where string, args ...any) ([]Symbol, int, error) {
		var total int
		if err := indexdb.ReadRow(context.Background(), db, `SELECT COUNT(*) FROM symbols WHERE `+where, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
		if total == 0 {
			return nil, 0, nil
		}
		rows, err := indexdb.ReadRows(context.Background(), db, `SELECT name, kind, render, source FROM symbols WHERE `+where+
			` ORDER BY length(name), name, kind LIMIT ?`, append(args, limit)...)
		if err != nil {
			return nil, 0, err
		}
		defer rows.Close()
		var syms []Symbol
		for rows.Next() {
			var s Symbol
			if err := rows.Scan(&s.Name, &s.Kind, &s.Render, &s.Source); err != nil {
				return nil, 0, err
			}
			syms = append(syms, s)
		}
		return syms, total, rows.Err()
	}

	syms, total, err := fetch(`name = ? COLLATE NOCASE`, name)
	if err != nil {
		return res, warnings, fmt.Errorf("symbol lookup failed: %w", err)
	}
	if total > 0 {
		return SymbolHits{Exact: true, Total: total, Symbols: syms}, warnings, nil
	}
	syms, total, err = fetch(`name LIKE ? ESCAPE '\'`, "%"+escapeLike(name)+"%")
	if err != nil {
		return res, warnings, fmt.Errorf("symbol lookup failed: %w", err)
	}
	return SymbolHits{Exact: false, Total: total, Symbols: syms}, warnings, nil
}

// CountSymbols reports how many symbol rows an index database holds.
func CountSymbols(dbPath string) (int, error) {
	db, err := indexdb.OpenRead(dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = indexdb.ReadRow(context.Background(), db, `SELECT COUNT(*) FROM symbols`).Scan(&n)
	return n, err
}

// FormatSymbols renders a lookup as plain text for terminals and MCP.
// Exact hits print full renders; substring fallback prints a compact
// name list — it is a disambiguation aid, not a result page.
func FormatSymbols(name string, res SymbolHits) string {
	if res.Total == 0 {
		return fmt.Sprintf("No symbol matches %q. Symbols come from .re-discipline/symbols.jsonl; lookup is exact name first, then substring.\n", name)
	}
	var b strings.Builder
	if res.Exact {
		for i, s := range res.Symbols {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[%s] %s\n%s\n", compactLabel(s), s.Source, s.Render)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "No exact match for %q; %d symbol name(s) contain it:\n", name, res.Total)
	for _, s := range res.Symbols {
		fmt.Fprintf(&b, "  %s  (%s)\n", s.Name, compactLabel(s))
	}
	if res.Total > len(res.Symbols) {
		fmt.Fprintf(&b, "  ... and %d more; raise --limit or refine the name\n", res.Total-len(res.Symbols))
	}
	return b.String()
}

func compactLabel(s Symbol) string {
	if s.Kind == "" {
		return "symbol"
	}
	return s.Kind
}

package search

import (
	"path"
	"strings"
)

// Doc is one parsed markdown document from .re-discipline/docs/.
type Doc struct {
	Build    string // optional explicit applicability for portable publication
	Path     string // relative to .re-discipline/, forward slashes
	Title    string
	Body     string
	Status   string // promoted | superseded | candidate | ""
	Kind     string // fact | ops | ""
	Grade    string // direct | inferred | reported | ""
	Tags     []string
	Idents   []string // identifiers this doc declares itself authoritative for
	Aliases  []string // alternate phrasings a searcher might use; indexed as text, never grants the reference-penalty exemption
	Evidence []string // evidence paths; stored for callers, never indexed as text
	Warning  string   // non-empty when frontmatter was malformed
}

// ParseDoc parses raw markdown with optional frontmatter. It is lenient:
// malformed frontmatter degrades to plain-text indexing with a Warning,
// never an error.
func ParseDoc(relPath, raw string) Doc {
	d := Doc{Path: relPath}
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	body := text
	if strings.HasPrefix(text, "---\n") {
		rest := text[len("---\n"):]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			fm := rest[:end]
			after := rest[end+len("\n---"):]
			if nl := strings.Index(after, "\n"); nl >= 0 {
				body = after[nl+1:]
			} else {
				body = ""
			}
			parseFrontmatter(fm, &d)
		} else {
			d.Warning = "unclosed frontmatter; indexed as plain text"
		}
	}
	d.Body = body
	d.Title = titleOf(body, relPath)
	return d
}

func parseFrontmatter(fm string, d *Doc) {
	for _, line := range strings.Split(fm, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "build":
			d.Build = val
		case "status":
			d.Status = val
		case "kind":
			d.Kind = val
		case "grade":
			d.Grade = val
		case "tags":
			d.Tags = parseList(val)
		case "idents":
			d.Idents = parseList(val)
		case "aliases":
			d.Aliases = append(d.Aliases, parseList(val)...)
		case "evidence":
			d.Evidence = parseList(val)
		}
	}
}

func parseList(val string) []string {
	val = strings.Trim(val, "[]")
	var out []string
	for _, item := range strings.Split(val, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func titleOf(body, relPath string) string {
	for _, line := range strings.Split(body, "\n") {
		if t, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(t)
		}
	}
	base := path.Base(relPath)
	return strings.TrimSuffix(base, path.Ext(base))
}

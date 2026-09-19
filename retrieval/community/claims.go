package community

import (
	"encoding/json"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

// ClaimProjection is a reviewable publication copy, never a rewrite of local research.
type ClaimProjection struct {
	Markdown        string   `json:"markdown"`
	RemovedSections []string `json:"removed_sections"`
	RemovedMaterial string   `json:"removed_material,omitempty"` // local preview only
	NeedsReview     bool     `json:"needs_review"`
}

func ProjectPublication(raw string) ClaimProjection {
	text := strings.ReplaceAll(strings.TrimPrefix(raw, "\ufeff"), "\r\n", "\n")
	p := ClaimProjection{RemovedSections: []string{}}
	lines := strings.Split(text, "\n")
	out := []string{}
	frontmatter, evidenceField, skipLevel := false, false, 0
	fence := ""
	removed := []string{}
	setextUnderline := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if i == 0 && trim == "---" {
			frontmatter = true
			out = append(out, line)
			continue
		}
		if frontmatter {
			if trim == "---" {
				frontmatter, evidenceField = false, false
				out = append(out, line)
				continue
			}
			if strings.HasPrefix(line, "evidence:") {
				evidenceField = true
				p.RemovedSections = append(p.RemovedSections, "evidence metadata")
				continue
			}
			if evidenceField && (trim == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "- ")) {
				continue
			}
			evidenceField = false
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			length := len(trim) - len(strings.TrimLeft(trim, string(trim[0])))
			marker := trim[:length]
			if fence == "" {
				fence = marker
			} else if marker[0] == fence[0] && len(marker) >= len(fence) && strings.TrimSpace(trim[len(marker):]) == "" {
				fence = ""
			}
			if skipLevel == 0 {
				out = append(out, line)
			} else {
				removed = append(removed, line)
			}
			continue
		}
		if setextUnderline {
			setextUnderline = false
			if skipLevel == 0 {
				out = append(out, line)
			} else {
				removed = append(removed, line)
			}
			continue
		}
		if fence == "" {
			heading := strings.TrimLeft(line, " ")
			level := 0
			title := ""
			if len(line)-len(heading) <= 3 && strings.HasPrefix(heading, "#") {
				level = len(heading) - len(strings.TrimLeft(heading, "#"))
				if level <= 6 && len(heading) > level && heading[level] == ' ' {
					title = strings.TrimSpace(heading[level:])
					title = strings.TrimSpace(strings.TrimRight(title, "#"))
				} else {
					level = 0
				}
			} else if trim != "" && i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if len(next) > 0 && strings.Trim(next, "=") == "" {
					level = 1
					title = trim
					setextUnderline = true
				}
				if len(next) > 0 && strings.Trim(next, "-") == "" {
					level = 2
					title = trim
					setextUnderline = true
				}
			}
			if level > 0 {
				if skipLevel > 0 && level <= skipLevel {
					skipLevel = 0
				}
				title = strings.ToLower(title)
				if skipLevel == 0 && (title == "evidence" || title == "supporting evidence" || title == "research log" || title == "investigation log") {
					skipLevel = level
					p.RemovedSections = append(p.RemovedSections, title)
					p.NeedsReview = true
					removed = append(removed, line)
					continue
				}
			}
		}
		if skipLevel == 0 {
			out = append(out, line)
		} else {
			removed = append(removed, line)
		}
	}
	p.RemovedMaterial = strings.Join(removed, "\n")
	p.Markdown = strings.TrimSpace(strings.Join(out, "\n")) + "\n"
	return p
}

// ClaimDigest ignores transport/provenance metadata, never numbers, qualifiers or
// body sections. Legacy bodies are not automatically rewritten for equivalence.
func ClaimDigest(d Document) string {
	parsed := engine.Parse(d.SourcePath, d.Markdown)
	return semantic.Hash([]string{parsed.Title, strings.TrimSpace(strings.ReplaceAll(parsed.Body, "\r\n", "\n")), claimMetadata(d.Markdown), strings.TrimSpace(d.Build), d.Kind, d.Supersedes})
}

// Preserve unknown metadata conservatively: it may carry subject or applicability.
func claimMetadata(markdown string) string {
	text := strings.ReplaceAll(strings.TrimPrefix(markdown, "\ufeff"), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return text
	}
	lines := strings.Split(text[4:4+end], "\n")
	kept := []string{}
	skip := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if skip && (trim == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "- ")) {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		skip = ok && (key == "evidence" || key == "status" || key == "grade")
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func ProjectClaim(d Document, status string) semantic.Claim {
	parsed := engine.Parse(d.SourcePath, d.Markdown)
	return semantic.Project(semantic.Claim{Title: parsed.Title, Body: parsed.Body, Metadata: claimMetadata(d.Markdown), Build: d.Build, Status: status, Kind: d.Kind, Idents: parsed.Idents})
}

func rawObject(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

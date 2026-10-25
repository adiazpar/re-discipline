package community

import (
	"encoding/json"

	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

// ClaimProjection and ProjectPublication use the same projection as the search index.
type ClaimProjection = knowledge.ClaimProjection

func ProjectPublication(raw string) ClaimProjection { return knowledge.ProjectPublication(raw) }

// ClaimDigest ignores transport/provenance metadata, never numbers, qualifiers or
// body sections. Legacy bodies are not automatically rewritten for equivalence.
func ClaimDigest(d Document) string {
	parsed := engine.Parse(d.SourcePath, d.Markdown)
	return semantic.Hash([]string{parsed.Title, strings.TrimSpace(strings.ReplaceAll(parsed.Body, "\r\n", "\n")), claimMetadata(d.Markdown), strings.TrimSpace(d.Build), d.Kind})
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

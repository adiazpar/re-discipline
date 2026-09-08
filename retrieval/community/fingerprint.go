package community

import "strings"

// TextDigest identifies exact text, allowing only transport newline differences.
// It deliberately does not erase numbers, negation, code, or applicability.
func TextDigest(markdown string) string {
	return sourceDigest([]byte(strings.TrimSpace(strings.ReplaceAll(markdown, "\r\n", "\n"))))
}

// ContentDigest excludes transport identity and provenance paths, while retaining
// the claim, applicability, grade and evidence. It is not a semantic similarity score.
func ContentDigest(d Document) string {
	d.FindingID, d.BaseRevision, d.SourcePath, d.SourceNamespace = "", "", "", ""
	d.Markdown = strings.TrimSpace(strings.ReplaceAll(d.Markdown, "\r\n", "\n"))
	return Digest(d)
}

type FindingMatch struct {
	Warnings        []string `json:"warnings,omitempty"`
	TextDigest      string   `json:"text_digest"`
	FindingID       string   `json:"finding_id"`
	Revision        string   `json:"revision"`
	MatchedRevision string   `json:"matched_revision"`
	Status          string   `json:"status"`
	Reason          string   `json:"reason,omitempty"`
	ReplacementID   string   `json:"replacement_id,omitempty"`
	Build           string   `json:"build"`
	Current         bool     `json:"current"`
}

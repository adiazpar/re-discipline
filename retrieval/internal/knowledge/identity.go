// Package knowledge defines stable finding identity and the evidence-free content
// fingerprint shared by upload and retrieval. It has no dependency on either.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const KnowledgeFormat = "re-discipline-knowledge-2"

func normalizeKnowledge(raw string) string {
	return strings.ReplaceAll(strings.TrimPrefix(raw, "\ufeff"), "\r\n", "\n")
}

// FindingID reads exactly one canonical UUID in frontmatter. A filename is
// never identity, and a changed/invalid ID is never silently repaired.
func FindingID(raw string) (string, error) {
	text := normalizeKnowledge(raw)
	if !strings.HasPrefix(text, "---\n") {
		return "", fmt.Errorf("finding requires frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 && strings.HasSuffix(text, "\n---") {
		end = len(text) - 8
	}
	if end < 0 {
		return "", fmt.Errorf("unclosed finding frontmatter")
	}
	id := ""
	for _, line := range strings.Split(text[4:4+end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "id" {
			continue
		}
		if key != "id" {
			return "", fmt.Errorf("id must be a top-level frontmatter field")
		}
		value = strings.TrimSpace(value)
		parsed, err := uuid.Parse(value)
		if id != "" || err != nil || parsed == uuid.Nil || parsed.String() != value {
			return "", fmt.Errorf("finding must have one canonical UUID in its id field")
		}
		id = value
	}
	return id, nil
}

func WithFindingID(raw, id string) (string, error) {
	current, err := FindingID(raw)
	if err != nil {
		return "", err
	}
	if current != "" {
		if current != id {
			return "", fmt.Errorf("finding already has another identity")
		}
		return raw, nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return "", fmt.Errorf("invalid finding ID")
	}
	// Preserve the source's newline style and all existing text.
	start := strings.IndexByte(raw, '\n') + 1
	newline := "\n"
	if strings.HasSuffix(raw[:start], "\r\n") {
		newline = "\r\n"
	}
	return raw[:start] + "id: " + id + newline + raw[start:], nil
}

// KnowledgeFingerprint is versioned and excludes path and stable identity.
// Evidence has already been removed; conditions and all other metadata remain.
func KnowledgeFingerprint(markdown string) string {
	text := normalizeKnowledge(markdown)
	lines := strings.Split(text, "\n")
	frontmatter := false
	kept := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 && line == "---" {
			frontmatter = true
		} else if frontmatter && line == "---" {
			frontmatter = false
		} else if frontmatter && (strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "created_at:") || strings.HasPrefix(line, "knowledge_updated_at:")) {
			continue
		}
		kept = append(kept, line)
	}
	sum := sha256.Sum256([]byte(KnowledgeFormat + "\n" + strings.TrimSpace(strings.Join(kept, "\n")) + "\n"))
	return hex.EncodeToString(sum[:])
}

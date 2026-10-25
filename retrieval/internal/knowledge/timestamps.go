package knowledge

import (
	"fmt"
	"strings"
	"time"
)

// Dates describe curation; they never establish which copy is authoritative.
func TimestampFields(raw string) (string, string, error) {
	if _, err := FindingID(raw); err != nil {
		return "", "", err
	}
	text := normalizeKnowledge(raw)
	end := strings.Index(text[4:], "\n---")
	values := map[string]string{}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name := strings.TrimSpace(key)
		if name != "created_at" && name != "knowledge_updated_at" {
			continue
		}
		if key != name || values[name] != "" {
			return "", "", fmt.Errorf("%s must occur once at the top level", name)
		}
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		if err != nil {
			return "", "", fmt.Errorf("%s must be an RFC3339 timestamp", name)
		}
		values[name] = parsed.UTC().Format(time.RFC3339Nano)
	}
	return values["created_at"], values["knowledge_updated_at"], nil
}

func WithTimestamps(raw, created, updated string) (string, error) {
	if _, _, err := TimestampFields(raw); err != nil {
		return "", err
	}
	newline := "\n"
	if strings.Contains(raw, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(raw, newline)
	out := []string{lines[0]}
	front := true
	out = append(out, "created_at: "+created, "knowledge_updated_at: "+updated)
	for _, line := range lines[1:] {
		if front && line == "---" {
			front = false
		}
		if front && (strings.HasPrefix(line, "created_at:") || strings.HasPrefix(line, "knowledge_updated_at:")) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, newline), nil
}

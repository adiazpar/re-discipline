package community

import "strings"

// TextDigest identifies exact text, allowing only transport newline differences.
// It deliberately does not erase numbers, negation, code, or applicability.
func TextDigest(markdown string) string {
	return sourceDigest([]byte(strings.TrimSpace(strings.ReplaceAll(markdown, "\r\n", "\n"))))
}

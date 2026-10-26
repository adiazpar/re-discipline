package search

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

const (
	compoundProbeLimit     = 32
	compoundExpansionLimit = 4
)

var compoundWordRE = regexp.MustCompile(`^[A-Za-z]{6,24}$`)

type matchProber interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// expandCompounds bridges missing word boundaries without a vocabulary service
// or a corpus-specific synonym list. Only an unmatched natural-language word
// is eligible; the split must occur as a real phrase in a title or body. The
// generated identifier field is deliberately excluded: its adjacent tokens do
// not necessarily form phrases in the source. Original terms and identifier
// authority remain unchanged. Work and the number of extra phrases are bounded.
func expandCompounds(ctx context.Context, db matchProber, q, match string) (string, error) {
	probes := 0
	exists := func(expression string) (bool, error) {
		probes++
		var id int64
		err := db.QueryRowContext(ctx, `SELECT rowid FROM docs WHERE docs MATCH ? LIMIT 1`, expression).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}
	seenWords, seenPhrases := map[string]bool{}, map[string]bool{}
	var phrases []string
	for _, token := range wordRe.FindAllString(q, -1) {
		if !compoundWordRE.MatchString(token) || isIdentifier(token) {
			continue
		}
		word := strings.ToLower(token)
		if seenWords[word] || stopTerms[word] {
			continue
		}
		seenWords[word] = true
		if probes >= compoundProbeLimit {
			break
		}
		found, err := exists(`"` + word + `"`)
		if err != nil {
			return "", err
		}
		if found {
			continue
		}
		for split := 3; split <= len(word)-3; split++ {
			left, right := word[:split], word[split:]
			if stopTerms[left] || stopTerms[right] {
				continue
			}
			if probes >= compoundProbeLimit || len(phrases) >= compoundExpansionLimit {
				break
			}
			phrase := `"` + left + ` ` + right + `"`
			found, err = exists(`{title body} : ` + phrase)
			if err != nil {
				return "", err
			}
			if found && !seenPhrases[phrase] {
				phrases = append(phrases, phrase)
				seenPhrases[phrase] = true
			}
		}
		if len(phrases) >= compoundExpansionLimit {
			break
		}
	}
	if len(phrases) != 0 {
		match += " OR {title body} : " + strings.Join(phrases, " OR {title body} : ")
	}
	return match, nil
}

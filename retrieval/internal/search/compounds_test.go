package search

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCompoundBoundaryRetrievalAndSnapshotParity(t *testing.T) {
	for _, pair := range []struct{ joined, split string }{
		{"lookahead", "look ahead"}, {"prebaked", "pre baked"},
		{"readback", "read back"}, {"gameplay", "game play"},
		{"keypress", "key press"}, {"writeback", "write back"},
	} {
		t.Run(pair.joined, func(t *testing.T) {
			root := t.TempDir()
			writeDoc(t, root, "docs/answer.md", "# Boundary behavior\nThe "+pair.split+" control determines this behavior.\n")
			writeDoc(t, root, "docs/alias.md", "---\naliases: ["+pair.split+"]\n---\n# Another subject\nAn unrelated control.\n")
			hits, _, err := Query(root, pair.joined, 8)
			if err != nil || len(hits) != 1 || hits[0].Path != "docs/answer.md" {
				t.Fatalf("a real source phrase must be found without matching generated-field adjacency: %v %+v", err, hits)
			}
			remote, _, err := QuerySnapshot(root, pair.joined, QueryOptions{Limit: 8})
			if err != nil || !reflect.DeepEqual(hits, remote) {
				t.Fatalf("local/snapshot drift: %v %+v", err, remote)
			}
			ex, _, err := Explain(root, pair.joined, 8)
			if err != nil || !strings.Contains(ex.Match, `{title body} : "`+pair.split+`"`) || !strings.Contains(FormatExplain(ex), "expanded") {
				t.Fatalf("the actual expanded match must be inspectable: %v %+v", err, ex)
			}
		})
	}
}

func TestCompoundExpansionKeepsExistingWordsAndIdentifierAuthority(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/phrase.md", "# Predictive control\nConfigure the look ahead window.\n")
	writeDoc(t, root, "docs/owner.md", "---\nkind: reference\nidents: [lookahead]\n---\n# Declared lookup\nThe lookahead identifier has a distinct purpose.\n")
	ex, _, err := Explain(root, "lookahead", 8)
	if err != nil || ex.Match != BuildMatch("lookahead") || len(ex.Rows) == 0 || ex.Rows[0].Path != "docs/owner.md" || !ex.Rows[0].ExactIdentifier {
		t.Fatalf("an already indexed word must not be reinterpreted: %v %+v", err, ex)
	}
	for _, query := range []string{"LookAhead", "look_ahead", "look::ahead", "this that those"} {
		ex, _, err = Explain(root, query, 8)
		if err != nil || ex.Match != BuildMatch(query) {
			t.Fatalf("code-shaped queries and function words must retain the original analysis: %q %v %+v", query, err, ex)
		}
	}
}

func TestCompoundExpansionRequiresSourceAdjacency(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/apart.md", "# A look\nObserve the distant area far ahead.\n")
	writeDoc(t, root, "docs/alias.md", "---\naliases: [look ahead]\n---\n# Other\nNo source phrase.\n")
	ex, _, err := Explain(root, "lookahead", 8)
	if err != nil || ex.Match != BuildMatch("lookahead") || len(ex.Rows) != 0 {
		t.Fatalf("a split cannot be invented from separate fields or aliases: %v %+v", err, ex)
	}
}

type countedProber struct {
	db    *sql.DB
	count int
}

func (p *countedProber) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.count++
	return p.db.QueryRowContext(ctx, query, args...)
}

func TestCompoundProbeBudgetAndCancellation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE VIRTUAL TABLE docs USING fts5(title,body,idents)`); err != nil {
		t.Fatal(err)
	}
	p := &countedProber{db: db}
	q := "abcdefghijklmnopqrstuvwx zyxwvutsrqponmlkjihgfedc anotherunknowncompound"
	match, err := expandCompounds(context.Background(), p, q, BuildMatch(q))
	if err != nil || p.count > compoundProbeLimit || match != BuildMatch(q) {
		t.Fatalf("unrecognized terms must stay within a fixed probe budget: count=%d %v %q", p.count, err, match)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = expandCompounds(ctx, db, "lookahead", BuildMatch("lookahead")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must stop expansion: %v", err)
	}
}

func TestCompoundExpansionBudget(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/phrases.md", "# Boundaries\nabc defghi; abcd efghi; abcde fghi; abcdef ghi; jkl mnopqr.\n")
	ex, _, err := Explain(root, "abcdefghi jklmnopqr", 8)
	if err != nil || strings.Count(ex.Match, "{title body}") != compoundExpansionLimit || strings.Contains(ex.Match, `"jkl mnopqr"`) {
		t.Fatalf("ambiguous compounds must not expand beyond the phrase budget: %v %+v", err, ex)
	}
}

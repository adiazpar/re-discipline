package community

import (
	"context"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func portable() Document {
	return Document{SourcePath: "docs/time.md", Kind: "fact", Grade: "direct", Build: "DOOM 2016", Markdown: "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Engine clock uses timescale\nThe engine clock responds to timescale."}
}
func TestLocalModeNeverConnects(t *testing.T) {
	root := t.TempDir()
	calls := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer h.Close()
	os.MkdirAll(filepath.Join(root, ".re-discipline", "docs"), 0700)
	SaveSettings(root, Settings{Mode: "local", Connections: []Connection{{Alias: "remote", Service: h.URL, CommunityID: uuid.NewString()}}})
	if _, e := Query(context.Background(), root, "clock", "", false, engine.Options{}); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("local retrieval made external requests")
	}
}
func TestURLAndCredentialBoundaries(t *testing.T) {
	for _, u := range []string{"http://example.com", "https://user:password@example.com", "https://example.com?token=x", "https://example.com/private"} {
		if _, e := NewClient(u); e == nil {
			t.Errorf("accepted unsafe origin %s", u)
		}
	}
}

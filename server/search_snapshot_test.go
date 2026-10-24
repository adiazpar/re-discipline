package server

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func TestCommunityQueryHealsDamagedSnapshot(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "snapshot-recovery", "private", "trusted")
	submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	type response struct {
		Hits []kb.Result `json:"hits"`
	}
	query := func() response {
		return unpack[response](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "timescale", "assistance": false}, 200))
	}
	first := query()
	if len(first.Hits) != 1 {
		t.Fatal("missing initial finding")
	}
	l = unpack[kb.Library](t, op(t, h, owner, "community.get", l.ID, map[string]any{}, 200))
	path := filepath.Join(s.Config.CacheDir, l.ID, strconv.FormatInt(l.Sequence, 10), ".re-discipline", "index.db")
	if err := os.WriteFile(path, []byte("corrupt snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	second := query()
	if len(second.Hits) != 1 || second.Hits[0].FindingID != first.Hits[0].FindingID || second.Hits[0].Revision != first.Hits[0].Revision {
		t.Fatal("snapshot recovery changed accepted knowledge")
	}
}

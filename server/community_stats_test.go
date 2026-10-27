package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func TestCommunityStatisticsScopeCountersAndLifecycle(t *testing.T) {
	s, h, owner := searchFixture(t)
	a := create(t, h, owner, "stats-doom", "public", "")
	b := create(t, h, owner, "stats-godzilla", "private", "")
	ra := joinReader(t, h, owner, a, false)
	rb := joinReader(t, h, owner, b, true)
	doc := testKnowledge(t, "", "statsanchor", "Current Doom knowledge")
	op(t, h, owner, "knowledge.put", a.ID, testBatch(doc), 200)
	check := func(key, cid string, count, size int64) {
		t.Helper()
		payload := op(t, h, key, "community.stats", cid, nil, 200)
		got := unpack[kb.CommunityStats](t, payload)
		fields := unpack[map[string]any](t, payload)
		if got.CommunityID != cid || got.Documents != count || got.KnowledgeBytes != size || got.ObservedAt.IsZero() || len(fields) != 4 {
			t.Fatalf("incorrect or excessive statistics: %s", payload)
		}
	}
	check(ra, a.ID, 1, int64(len(doc.Markdown)))
	check(rb, b.ID, 0, 0)
	check(owner, a.ID, 1, int64(len(doc.Markdown)))
	if len(s.searchStates) != 0 || s.scheduler.stats()["completed"].(uint64) != 0 {
		t.Fatal("metadata invoked retrieval")
	}
	for _, cid := range []string{b.ID, a.Slug, "", id()} {
		op(t, h, ra, "community.stats", cid, nil, 404)
	}
	op(t, h, "", "community.stats", a.ID, nil, 401)
	op(t, h, newReaderKey(), "community.stats", a.ID, nil, 401)
	for _, action := range []string{"usage", "server.stats", "community.list", "access.list"} {
		op(t, h, ra, action, a.ID, nil, 403)
	}
	op(t, h, ra, "community.stats", a.ID, map[string]any{"all": true}, 400)
	changed := testKnowledge(t, doc.ID, "statsanchor", "Longer replacement with current Doom knowledge")
	op(t, h, owner, "knowledge.put", a.ID, testBatch(changed), 200)
	check(ra, a.ID, 1, int64(len(changed.Markdown)))
	op(t, h, owner, "knowledge.delete", a.ID, map[string]any{"ids": []string{doc.ID}}, 200)
	check(ra, a.ID, 0, 0)
	// Metadata obeys the same signature/replay and connection lifecycle as reads.
	body := raw(kb.Request{Action: "community.stats", Community: a.ID})
	req, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(body))
	authorizeTestRequest(req, ra)
	for _, want := range []int{200, 409} {
		copy := req.Clone(context.Background())
		copy.Body = io.NopCloser(bytes.NewReader(body))
		res, err := h.Client().Do(copy)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatal(res.StatusCode, want)
		}
	}
	if _, err := s.DB.Exec(context.Background(), `UPDATE community.reader_connections SET expires_at=now()-interval '1 second' WHERE id=$1`, connectionID(t, s, ra)); err != nil {
		t.Fatal(err)
	}
	op(t, h, ra, "community.stats", a.ID, nil, 401)
	op(t, h, owner, "access.revoke", b.ID, map[string]any{"id": connectionID(t, s, rb)}, 200)
	op(t, h, rb, "community.stats", b.ID, nil, 403)
	private := joinReader(t, h, owner, b, true)
	ticket := prepareDeletion(t, h, owner, b.ID, false)
	op(t, h, private, "community.stats", b.ID, nil, 423)
	confirmDeletion(t, h, owner, b.ID, ticket)
	op(t, h, private, "community.stats", b.ID, nil, 401)
}

func TestCommunityStatisticsDoNotOccupySearchSlots(t *testing.T) {
	t.Setenv("MAX_ACTIVE_SEARCHES", "1")
	s, h, owner := searchFixture(t)
	l := create(t, h, owner, "stats-resources", "public", "")
	reader := joinReader(t, h, owner, l, false)
	putTestKnowledge(t, s, h, owner, l.ID, "", "statsanchor", "correct result")
	hold, _, err := s.scheduler.acquire(context.Background(), l.ID, "held-search")
	if err != nil {
		t.Fatal(err)
	}
	op(t, h, reader, "community.stats", l.ID, nil, 200)
	hold()
	for i := 0; i < cap(s.statsSlots); i++ {
		s.statsSlots <- struct{}{}
	}
	defer func() {
		for len(s.statsSlots) > 0 {
			<-s.statsSlots
		}
	}()
	before := time.Now()
	op(t, h, reader, "community.stats", l.ID, nil, 503)
	if time.Since(before) > time.Second {
		t.Fatal("metadata wait was unbounded")
	}
	op(t, h, reader, "query", l.ID, map[string]any{"query": "statsanchor"}, 200)
	if len(s.statsWaiters) != 0 || len(s.requestSlots) != 0 {
		t.Fatal("admission slot leaked")
	}
}

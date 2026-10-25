package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	ka "github.com/adiazpar/re-discipline/retrieval/knowledgearchive"
	"github.com/klauspost/compress/zstd"
)

func prepareDeletion(t *testing.T, h *httptest.Server, operator, cid string, archive bool) string {
	t.Helper()
	p := unpack[map[string]any](t, op(t, h, operator, "community.delete", cid, nil, 200))
	ticket := p["ticket"].(string)
	op(t, h, operator, "community.delete", cid, map[string]any{"step": "lock", "ticket": ticket, "archive": archive}, 200)
	return ticket
}
func confirmDeletion(t *testing.T, h *httptest.Server, operator, cid, ticket string) json.RawMessage {
	t.Helper()
	return op(t, h, operator, "community.delete", cid, map[string]any{"step": "confirm", "ticket": ticket, "confirm": true}, 200)
}
func deleteFixture(t *testing.T, h *httptest.Server, operator, cid string) json.RawMessage {
	t.Helper()
	return confirmDeletion(t, h, operator, cid, prepareDeletion(t, h, operator, cid, false))
}
func archiveHTTP(t *testing.T, h *httptest.Server, operator, cid, ticket string, index int64, want int) ([]byte, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: "community.archive.chunk", Community: cid, Data: raw(archiveRequest{ticket, index})})))
	authorizeTestRequest(req, operator)
	res, e := h.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	if res.StatusCode != want {
		t.Fatalf("archive HTTP %d wanted %d: %s", res.StatusCode, want, b)
	}
	return b, res.Header
}

func TestKnowledgeArchiveWorkflow(t *testing.T) {
	s, h, operator := searchFixture(t)
	ctx := context.Background()
	a := create(t, h, operator, "archive-doom", "public", "trusted")
	b := create(t, h, operator, "archive-godzilla", "private", "trusted")
	for _, l := range []kb.Library{a, b} {
		putTestKnowledge(t, s, h, operator, l.ID, "", "timescale", "Engine time scale")
	}
	reader := joinReader(t, h, operator, a, false)
	inv := unpack[map[string]any](t, op(t, h, operator, "access.create", a.ID, nil, 200))
	preview := unpack[map[string]any](t, op(t, h, operator, "community.delete", a.ID, nil, 200))
	ticket := preview["ticket"].(string)
	if preview["deleted"] != false || preview["phase"] != "preview" {
		t.Fatal(preview)
	}
	op(t, h, reader, "query", a.ID, map[string]string{"query": "timescale"}, 200)
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "confirm", "ticket": ticket, "confirm": true}, 423)
	op(t, h, reader, "community.delete", a.ID, nil, 403)
	archiveHTTP(t, h, reader, a.ID, ticket, 0, 403)
	archiveHTTP(t, h, "", a.ID, ticket, 0, 401)
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "lock", "ticket": randomToken(), "archive": true}, 409)
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "lock", "ticket": ticket}, 409)
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "lock", "ticket": ticket, "archive": true}, 200)
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "lock", "ticket": ticket, "archive": false}, 409)
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "verify", "ticket": ticket, "chain": "fabricated", "records": 0}, 409)
	op(t, h, reader, "query", a.ID, map[string]string{"query": "timescale"}, 423)
	op(t, h, operator, "community.get", a.ID, nil, 423)
	op(t, h, newReaderKey(), "community.connect", a.ID, nil, 423)
	op(t, h, operator, "knowledge.put", a.ID, map[string]any{}, 423)
	if _, e := s.DB.Exec(ctx, `UPDATE community.documents SET markdown='conflicting edit' WHERE community_id=$1`, a.ID); e == nil {
		t.Fatal("SQL mutation bypassed freeze")
	}
	op(t, h, operator, "query", b.ID, map[string]string{"query": "timescale"}, 200)
	archiveHTTP(t, h, operator, b.ID, ticket, 0, 404)
	// Lost response: fetch a frame but do not persist it. The actual client must replay it.
	first, head := archiveHTTP(t, h, operator, a.ID, ticket, 0, 200)
	again, head2 := archiveHTTP(t, h, operator, a.ID, ticket, 0, 200)
	if !bytes.Equal(first, again) || head.Get("X-Archive-Chain") != head2.Get("X-Archive-Chain") {
		t.Fatal("retry changed archive frame")
	}
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "confirm", "ticket": ticket, "confirm": true}, 423)
	client := &kb.Client{URL: h.URL, Token: operator, HTTP: h.Client()}
	path := filepath.Join(t.TempDir(), "knowledge.zst")
	result, e := client.DownloadArchive(ctx, a.ID, ticket, path)
	if e != nil {
		t.Fatal(e)
	}
	if result.(map[string]any)["verified"] != true {
		t.Fatal(result)
	}
	summary, e := ka.Verify(path)
	if e != nil || summary.Counts["document"] != 1 || len(summary.Counts) != 1 {
		t.Fatal(summary, e)
	}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	decoder, e := zstd.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := io.ReadAll(decoder)
	decoder.Close()
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{s.Config.OperatorID, operator, testPublicKey(reader), inv["invitation"].(string), "author_id", "reviewer_id", "owner_id", "members", "public_key", "visibility"} {
		if bytes.Contains(plain, []byte(secret)) {
			t.Fatalf("archive leaked identity/access field %q", secret)
		}
	}
	if !bytes.Contains(plain, []byte("timescale")) {
		t.Fatal("knowledge missing")
	}
	// Prove that archived knowledge can be reconstructed for the shared engine.
	restored := t.TempDir()
	docs := filepath.Join(restored, ".re-discipline", "docs")
	if e = os.MkdirAll(docs, 0700); e != nil {
		t.Fatal(e)
	}
	for _, line := range bytes.Split(plain, []byte("\n")) {
		var rec ka.Record
		if json.Unmarshal(line, &rec) != nil || rec.Type != "document" {
			continue
		}
		var revision kb.KnowledgeDocument
		if e = json.Unmarshal(rec.Data, &revision); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(docs, revision.ID+".md"), []byte(revision.Markdown), 0600); e != nil {
			t.Fatal(e)
		}
	}
	hits, _, e := engine.Query(restored, "timescale", engine.Options{Limit: 5})
	if e != nil || len(hits) != 1 {
		t.Fatal("archived knowledge did not retrieve", hits, e)
	}

	// Verification never deletes. A separate explicit confirmation is mandatory.
	var count int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM community.libraries WHERE id=$1`, a.ID).Scan(&count)
	if count != 1 {
		t.Fatal("download deleted community")
	}
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "confirm", "ticket": ticket}, 409)
	confirmDeletion(t, h, operator, a.ID, ticket)
	confirmDeletion(t, h, operator, a.ID, ticket) // idempotent retry of exact UUID
	op(t, h, operator, "query", b.ID, map[string]string{"query": "timescale"}, 200)
	if _, e = os.Stat(path); e != nil {
		t.Fatal("deletion removed local archive", e)
	}
}

func TestArchiveLockCancelExpiryRestartAndCapacity(t *testing.T) {
	s, h, operator := searchFixture(t)
	ctx := context.Background()
	a := create(t, h, operator, "lock-a", "public", "trusted")
	b := create(t, h, operator, "lock-b", "public", "trusted")
	ta := prepareDeletion(t, h, operator, a.ID, true)
	pb := unpack[map[string]any](t, op(t, h, operator, "community.delete", b.ID, nil, 200))
	tb := pb["ticket"].(string)
	op(t, h, operator, "community.delete", b.ID, map[string]any{"step": "lock", "ticket": tb, "archive": true}, 409)
	op(t, h, operator, "community.get", b.ID, nil, 200)
	restarted := New(s.DB, s.Config)
	h.Config.Handler = restarted.Handler()
	op(t, h, operator, "community.get", a.ID, nil, 423)
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "cancel"}, 200)
	op(t, h, operator, "community.get", a.ID, nil, 200)
	archiveHTTP(t, h, operator, a.ID, ta, 0, 404)
	ta = prepareDeletion(t, h, operator, a.ID, true)
	if _, e := s.DB.Exec(ctx, `UPDATE community.deletion_sessions SET expires_at=now()-interval '1 second' WHERE community_id=$1`, a.ID); e != nil {
		t.Fatal(e)
	}
	op(t, h, operator, "community.delete", a.ID, map[string]any{"step": "confirm", "ticket": ta, "confirm": true}, 423)
	if e := restarted.expireDeletionSessions(ctx); e != nil {
		t.Fatal(e)
	}
	op(t, h, operator, "community.get", a.ID, nil, 200)
	archiveHTTP(t, h, operator, a.ID, ta, 0, 404)
	// Empty corpus still has a complete, verifiable knowledge archive.
	tb = prepareDeletion(t, h, operator, b.ID, true)
	client := &kb.Client{URL: h.URL, Token: operator, HTTP: h.Client()}
	path := filepath.Join(t.TempDir(), "empty.zst")
	if _, e := client.DownloadArchive(ctx, b.ID, tb, path); e != nil {
		t.Fatal(e)
	}
	summary, e := ka.Verify(path)
	if e != nil || summary.Records != 0 {
		t.Fatal(summary, e)
	}
	op(t, h, operator, "community.delete", b.ID, map[string]any{"step": "cancel"}, 200)
	op(t, h, operator, "community.get", b.ID, nil, 200)
}

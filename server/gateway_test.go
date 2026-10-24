package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func searchFixture(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	s, _ := fixture(t)
	s.Config.OperatorID, s.Config.OperatorToken = id(), randomToken()
	h := httptest.NewServer(s.Handler())
	s.Config.PublicURL = h.URL
	t.Cleanup(h.Close)
	return s, h, s.Config.OperatorToken
}
func TestOperatorAndScopedReaders(t *testing.T) {
	s, h, operator := searchFixture(t)
	doom := create(t, h, operator, "doom-readers", "public", "trusted")
	godzilla := create(t, h, operator, "godzilla-readers", "public", "trusted")
	sub := submit(t, h, operator, doom.ID, id(), document())
	runJob(t, s)
	sub = unpack[kb.Submission](t, op(t, h, operator, "submission.get", doom.ID, map[string]string{"id": sub.ID}, 200))
	reader := joinReader(t, h, operator, doom, false)
	op(t, h, "", "query", doom.ID, map[string]string{"query": "timescale"}, 401)
	for _, action := range []string{"community.create", "community.update", "submission.create", "submission.batch", "finding.withdraw", "access.create", "access.revoke", "community.list", "changes", "export", "member.set", "invite.create"} {
		op(t, h, reader, action, doom.ID, map[string]any{}, 403)
	}
	for _, action := range []string{"community.get", "query", "finding.get", "finding.match", "finding.relations"} {
		op(t, h, reader, action, godzilla.ID, map[string]any{}, 404)
	}
	result := unpack[kb.QueryResult](t, op(t, h, reader, "query", doom.ID, map[string]string{"query": "timescale"}, 200))
	if len(result.Hits) != 1 || result.Hits[0].CommunityID != doom.ID {
		t.Fatalf("query isolation: %+v", result)
	}
	op(t, h, reader, "finding.get", doom.ID, map[string]string{"id": sub.FindingID}, 200)
	// Scope is also enforced by PostgreSQL if an internal query omits its filter.
	var p principal
	if err := s.DB.QueryRow(context.Background(), `SELECT id::text,community_id::text FROM community.reader_connections WHERE public_key=$1`, testPublicKey(reader)).Scan(&p.tokenID, &p.communityID); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), principalKey{}, p)
	tx, err := s.begin(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM community.libraries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("RLS scope: %d %v", count, err)
	}
	tx.Rollback(ctx)
	privateReader := joinReader(t, h, operator, doom, true)
	// Making public content private invalidates all public connections permanently.
	op(t, h, operator, "community.update", doom.ID, map[string]any{"name": doom.Name, "visibility": "private", "policy": doom.Policy, "expected_version": doom.PolicyVersion}, 200)
	op(t, h, reader, "query", doom.ID, map[string]string{"query": "timescale"}, 403)
	op(t, h, newReaderKey(), "community.connect", doom.ID, nil, 404)
	op(t, h, privateReader, "community.connect", doom.ID, nil, 200)
	op(t, h, privateReader, "query", doom.ID, map[string]string{"query": "timescale"}, 200)
	grants := unpack[[]map[string]any](t, op(t, h, operator, "access.list", doom.ID, nil, 200))
	if len(grants) != 2 {
		t.Fatalf("expected active private connection and public revocation tombstone: %+v", grants)
	}
	op(t, h, operator, "access.revoke", doom.ID, map[string]any{"id": connectionID(t, s, privateReader)}, 200)
	op(t, h, privateReader, "query", doom.ID, map[string]string{"query": "timescale"}, 403)
	for _, path := range []string{"/v1/auth/device/start", "/v1/auth/github/start", "/"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("retired route %s: %d", path, w.Code)
		}
	}
}
func TestConcurrentSnapshotsAndAdmission(t *testing.T) {
	s, h, operator := searchFixture(t)
	a := create(t, h, operator, "parallel-doom", "private", "trusted")
	b := create(t, h, operator, "parallel-godzilla", "private", "trusted")
	ctx := context.Background()
	lock, err := s.lockIndex(ctx, a.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock()
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	other, err := s.lockIndex(deadline, a.ID, false)
	if err != nil {
		t.Fatal("readers serialize", err)
	}
	other()
	writer, err := s.lockIndex(deadline, b.ID, true)
	if err != nil {
		t.Fatal("communities serialize", err)
	}
	writer()
	if _, err = s.lockIndex(deadline, a.ID, true); err == nil {
		t.Fatal("writer bypassed active reader")
	}

}

func TestQueuedReaderRevocationAndOperatorControl(t *testing.T) {
	t.Setenv("MAX_ACTIVE_SEARCHES", "1")
	s, h, operator := searchFixture(t)
	l := create(t, h, operator, "queued-revocation", "private", "trusted")
	reader := joinReader(t, h, operator, l, true)
	hold, _, err := s.scheduler.acquire(context.Background(), l.ID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: "query", Community: l.ID, Data: raw(map[string]string{"query": "timescale"})})))
		authorizeTestRequest(req, reader)
		r, e := http.DefaultClient.Do(req)
		if e != nil {
			done <- 0
			return
		}
		defer r.Body.Close()
		io.Copy(io.Discard, r.Body)
		done <- r.StatusCode
	}()
	deadline := time.Now().Add(3 * time.Second)
	for s.scheduler.stats()["queued"].(int) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request did not queue")
		}
		time.Sleep(5 * time.Millisecond)
	}
	op(t, h, operator, "server.stats", "", nil, 200)
	op(t, h, reader, "server.stats", l.ID, nil, 403)
	grants := unpack[[]map[string]any](t, op(t, h, operator, "access.list", l.ID, nil, 200))
	op(t, h, operator, "access.revoke", l.ID, map[string]any{"id": grants[0]["id"]}, 200)
	hold()
	if code := <-done; code != 404 && code != 401 {
		t.Fatalf("queued revoked reader returned HTTP %d", code)
	}
}

func TestPublicRateBucketCannotLockOutOperator(t *testing.T) {
	t.Setenv("MAX_UNAUTHENTICATED_REQUESTS_PER_IP_MINUTE", "1")
	_, h, operator := searchFixture(t)
	op(t, h, "", "query", id(), map[string]string{"query": "test"}, 401)
	op(t, h, "", "query", id(), map[string]string{"query": "test"}, 429)
	op(t, h, operator, "server.stats", "", nil, 200)
}

func TestConcurrentReadersKeepCommunityScopeWithoutMinuteQuotas(t *testing.T) {
	t.Setenv("MAX_UNAUTHENTICATED_REQUESTS_PER_IP_MINUTE", "3")
	// Retired settings must not silently reinstate authenticated reader quotas.
	t.Setenv("MAX_REQUESTS_PER_CONNECTION_MINUTE", "1")
	t.Setenv("MAX_REQUESTS_PER_IP_MINUTE", "1")
	t.Setenv("MAX_ACTIVE_SEARCHES", "2")
	s, h, operator := searchFixture(t)
	communities := []kb.Library{create(t, h, operator, "collision-doom", "public", "trusted"), create(t, h, operator, "collision-godzilla", "public", "trusted")}
	fid := id() // Deliberately identical finding ID, distinct content and revisions.
	tokens := []string{}
	revisions := []string{}
	for i, l := range communities {
		doc := document()
		doc.Markdown += fmt.Sprintf("\ncommunitymarker%d\n", i)
		rid := id()
		revisions = append(revisions, rid)
		ctx := context.Background()
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO community.findings(community_id,id,revision) VALUES($1,$2,$3)`, l.ID, fid, rid); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,author_id,content_digest,text_digest) VALUES($1,$2,$3,1,$4,$5,$6,$7)`, rid, l.ID, fid, raw(doc), s.Config.OperatorID, kb.ContentDigest(doc), kb.TextDigest(doc.Markdown)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE community.libraries SET sequence=1 WHERE id=$1`, l.ID); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		reader := joinReader(t, h, operator, l, true)
		tokens = append(tokens, reader)
		op(t, h, reader, "query", l.ID, map[string]string{"query": "timescale"}, 200)
	}
	// Exhaust the shared-IP setup bucket. Existing readers must still work.
	op(t, h, "invalid", "query", communities[0].ID, nil, 401)
	op(t, h, "invalid", "query", communities[0].ID, nil, 429)
	errors := make(chan error, 400)
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for n := range jobs {
				i := n % 2
				l := communities[i]
				call := func(action, cid string, data any) (int, []byte, error) {
					r, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: action, Community: cid, Data: raw(data)})))
					authorizeTestRequest(r, tokens[i])
					res, e := h.Client().Do(r)
					if e != nil {
						return 0, nil, e
					}
					defer res.Body.Close()
					body, e := io.ReadAll(res.Body)
					return res.StatusCode, body, e
				}
				code, b, e := call("query", l.ID, map[string]string{"query": "timescale"})
				var result kb.QueryResult
				if e != nil || code != 200 || json.Unmarshal(b, &result) != nil || len(result.Hits) != 1 || result.Hits[0].CommunityID != l.ID || result.Hits[0].Revision != revisions[i] {
					errors <- fmt.Errorf("query lost scope or was throttled: status %d error %v", code, e)
					continue
				}
				code, b, e = call("finding.get", l.ID, map[string]string{"id": fid})
				var docs []kb.Change
				if e != nil || code != 200 || json.Unmarshal(b, &docs) != nil || len(docs) != 1 || docs[0].Revision != revisions[i] || !strings.Contains(docs[0].Document.Markdown, fmt.Sprintf("communitymarker%d", i)) {
					errors <- fmt.Errorf("document lost scope or was throttled: status %d error %v", code, e)
					continue
				}
				code, _, e = call("finding.get", communities[1-i].ID, map[string]string{"id": fid})
				if e != nil || code != 404 {
					errors <- fmt.Errorf("credential crossed communities: status %d error %v", code, e)
				}
			}
		}()
	}
	for i := range 400 {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	stats := s.scheduler.stats()
	if stats["running"].(int) != 0 || stats["queued"].(int) != 0 || stats["queued_bytes"].(int64) != 0 {
		t.Fatal("requests leaked capacity", stats)
	}
}

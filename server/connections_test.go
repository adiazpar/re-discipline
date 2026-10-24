package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func TestInvitationIsSingleUseAndKeyReconnectIsIdempotent(t *testing.T) {
	s, h, operator := searchFixture(t)
	l := create(t, h, operator, "invite-race", "private", "trusted")
	grant := unpack[map[string]any](t, op(t, h, operator, "access.create", l.ID, nil, 200))
	invite := grant["invitation"].(string)
	var workers sync.WaitGroup
	results := make(chan string, 32)
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			key := newReaderKey()
			req, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: "community.connect", Community: l.ID, Data: raw(map[string]string{"invitation": invite})})))
			authorizeTestRequest(req, key)
			res, err := h.Client().Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer res.Body.Close()
			io.Copy(io.Discard, res.Body)
			if res.StatusCode == 200 {
				results <- key
			} else if res.StatusCode != 403 {
				t.Errorf("invitation race status %d", res.StatusCode)
			}
		}()
	}
	workers.Wait()
	close(results)
	keys := []string{}
	for k := range results {
		keys = append(keys, k)
	}
	if len(keys) != 1 {
		t.Fatalf("single invitation granted %d keys", len(keys))
	}
	firstID := connectionID(t, s, keys[0])
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			op(t, h, keys[0], "community.connect", l.ID, map[string]string{"invitation": invite}, 200)
		}()
	}
	workers.Wait()
	if connectionID(t, s, keys[0]) != firstID {
		t.Fatal("retry changed connection")
	}
	var count int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.reader_connections`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate records", count, err)
	}
	op(t, h, keys[0], "community.leave", l.ID, nil, 200)
	op(t, h, keys[0], "community.connect", l.ID, map[string]string{"invitation": invite}, 403)
}

func TestReplayIsAtomicPersistentAndDoesNotConsumeForgedProof(t *testing.T) {
	s, h, operator := searchFixture(t)
	l := create(t, h, operator, "replay-proof", "public", "trusted")
	key := joinReader(t, h, operator, l, false)
	body := raw(kb.Request{Action: "community.get", Community: l.ID, Data: raw(nil)})
	original, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(body))
	authorizeTestRequest(original, key)
	// An invalid signature with the same nonce must not reserve the valid proof.
	forged := original.Clone(context.Background())
	forged.Body = io.NopCloser(bytes.NewReader(body))
	forged.Header.Set("Signature", "connection=:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==:")
	res, err := h.Client().Do(forged)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("forged signature accepted", res.StatusCode)
	}
	codes := make(chan int, 32)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r := original.Clone(context.Background())
			r.Body = io.NopCloser(bytes.NewReader(body))
			res, e := h.Client().Do(r)
			if e != nil {
				t.Error(e)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			codes <- res.StatusCode
		}()
	}
	workers.Wait()
	close(codes)
	success, replay := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code == 409 {
			replay++
		} else {
			t.Error("unexpected replay status", code)
		}
	}
	if success != 1 || replay != 31 {
		t.Fatal("replay race", success, replay)
	}
	// Another service instance sharing the database also rejects the proof.
	restarted := New(s.DB, s.Config)
	r := original.Clone(context.Background())
	r.Body = io.NopCloser(bytes.NewReader(body))
	w := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("restart forgot replay", w.Code, w.Body.String())
	}
}

func TestConnectionLifecycleCleanupAndRevocation(t *testing.T) {
	s, h, operator := searchFixture(t)
	ctx := context.Background()
	public := create(t, h, operator, "lease-public", "public", "trusted")
	private := create(t, h, operator, "lease-private", "private", "trusted")
	a, b := joinReader(t, h, operator, public, false), joinReader(t, h, operator, private, true)
	oldA := connectionID(t, s, a)
	oldB := connectionID(t, s, b)
	// Normal use extends an old but still active lease; it does not update on every read.
	if _, err := s.DB.Exec(ctx, `UPDATE community.reader_connections SET renewed_at=now()-interval '2 days',expires_at=now()+interval '178 days' WHERE id=$1`, oldA); err != nil {
		t.Fatal(err)
	}
	op(t, h, a, "community.get", public.ID, nil, 200)
	var first, second time.Time
	s.DB.QueryRow(ctx, `SELECT renewed_at FROM community.reader_connections WHERE id=$1`, oldA).Scan(&first)
	op(t, h, a, "community.get", public.ID, nil, 200)
	s.DB.QueryRow(ctx, `SELECT renewed_at FROM community.reader_connections WHERE id=$1`, oldA).Scan(&second)
	if time.Since(first) > time.Minute || !first.Equal(second) {
		t.Fatal("lease renewal is missing or writes every request")
	}
	// Silence (including a deleted local key) expires server access without client cleanup.
	if _, err := s.DB.Exec(ctx, `UPDATE community.reader_connections SET expires_at=now()-interval '1 second' WHERE id=$1 OR id=$2`, oldA, oldB); err != nil {
		t.Fatal(err)
	}
	invitation := unpack[map[string]any](t, op(t, h, operator, "access.create", private.ID, nil, 200))
	if _, err := s.DB.Exec(ctx, `UPDATE community.reader_invitations SET expires_at=now()-interval '1 second' WHERE id=$1`, invitation["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE community.request_nonces SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := s.pruneConnections(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"reader_connections", "reader_invitations", "request_nonces"} {
		var count int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.`+table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	op(t, h, a, "community.get", public.ID, nil, 401)
	op(t, h, a, "community.connect", public.ID, nil, 200)
	if connectionID(t, s, a) == oldA {
		t.Fatal("expired identity reused")
	}
	op(t, h, b, "community.connect", private.ID, nil, 404)
	op(t, h, b, "community.connect", private.ID, map[string]any{"invitation": invitation["invitation"]}, 403)
	op(t, h, operator, "access.revoke", public.ID, map[string]string{"id": connectionID(t, s, a)}, 200)
	op(t, h, a, "community.get", public.ID, nil, 403)
	op(t, h, a, "community.connect", public.ID, nil, 403)
	op(t, h, a, "community.leave", public.ID, nil, 200)
	op(t, h, a, "community.connect", public.ID, nil, 403) // Disconnect cannot erase a ban.
	// Tombstones are bounded too. After retention a public key can register again;
	// this system intentionally does not promise a permanent ban on a person.
	if _, err := s.DB.Exec(ctx, `UPDATE community.reader_connections SET expires_at=now()-interval '1 second' WHERE public_key=$1`, testPublicKey(a)); err != nil {
		t.Fatal(err)
	}
	if err := s.pruneConnections(ctx); err != nil {
		t.Fatal(err)
	}
	op(t, h, a, "community.connect", public.ID, nil, 200)
	op(t, h, a, "community.leave", public.ID, nil, 200)
	op(t, h, a, "community.leave", public.ID, nil, 200) // Lost disconnect response is safe.
	var communities int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM community.libraries`).Scan(&communities)
	if communities != 2 {
		t.Fatal("cleanup deleted corpora")
	}
}

func TestConnectionCapacityCountsInvitationsAndTombstones(t *testing.T) {
	t.Setenv("MAX_COMMUNITY_CONNECTIONS", "2")
	s, h, operator := searchFixture(t)
	l := create(t, h, operator, "capacity", "public", "trusted")
	key := joinReader(t, h, operator, l, false)
	op(t, h, operator, "access.revoke", l.ID, map[string]string{"id": connectionID(t, s, key)}, 200)
	invite := unpack[map[string]any](t, op(t, h, operator, "access.create", l.ID, nil, 200))
	op(t, h, newReaderKey(), "community.connect", l.ID, nil, 429)
	op(t, h, operator, "access.create", l.ID, nil, 429)
	// Redeeming an invitation replaces it with one connection, not an extra row.
	other := newReaderKey()
	op(t, h, other, "community.connect", l.ID, map[string]any{"invitation": invite["invitation"]}, 200)
	op(t, h, other, "community.get", l.ID, nil, 200)
}

func TestInvitationUpgradesExistingPublicConnectionBeforePrivacyChange(t *testing.T) {
	s, h, operator := searchFixture(t)
	l := create(t, h, operator, "invited-upgrade", "public", "trusted")
	key := joinReader(t, h, operator, l, false)
	old := connectionID(t, s, key)
	grant := unpack[map[string]any](t, op(t, h, operator, "access.create", l.ID, nil, 200))
	op(t, h, key, "community.connect", l.ID, map[string]any{"invitation": grant["invitation"]}, 200)
	if connectionID(t, s, key) == old {
		t.Fatal("invitation failed to replace public grant")
	}
	op(t, h, operator, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": "private", "policy": l.Policy, "expected_version": l.PolicyVersion}, 200)
	op(t, h, key, "community.get", l.ID, nil, 200)
}

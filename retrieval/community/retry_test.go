package community

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"crypto/ed25519"
	"github.com/adiazpar/re-discipline/retrieval/connectionauth"
	"github.com/adiazpar/re-discipline/retrieval/engine"
)

type retryTransport func(*http.Request) (*http.Response, error)

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBusyReadRetriesAreBoundedAndKeepIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, test := range []struct {
			action string
			status int
			after  string
			want   int
		}{
			{"query", 503, "1", 3}, {"knowledge.get", 429, "0", 3},
			{"query", 503, "60", 1}, {"query", 503, "garbled", 1},
			{"query", 401, "", 1}, {"query", 403, "", 1}, {"query", 404, "", 1},
			{"submission.create", 503, "1", 1}, {"community.connect", 503, "1", 1},
		} {
			calls := 0
			original := ""
			c := &Client{URL: "https://one.example", Token: "reader-doom"}
			c.HTTP = &http.Client{Transport: retryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if calls == 1 {
					original = string(body)
				}
				if r.URL.Host != "one.example" || r.Header.Get("Authorization") != "Bearer reader-doom" || string(body) != original {
					t.Fatal("retry changed identity or payload")
				}
				var req Request
				json.Unmarshal(body, &req)
				if req.Community != "doom" {
					t.Fatal("retry changed community")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Retry-After": {test.after}}, Body: io.NopCloser(strings.NewReader(`{"error":"busy"}`))}, nil
			})}
			var out any
			err := c.Operation(context.Background(), test.action, "doom", map[string]string{"query": "clock"}, &out)
			if err == nil || calls != test.want {
				t.Fatalf("%+v: calls=%d err=%v", test, calls, err)
			}
		}
	})
}

func TestBusyReadCancellationAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, cancelEarly := range []bool{false, true} {
			calls := 0
			c := &Client{URL: "https://one.example", Token: "reader"}
			c.HTTP = &http.Client{Transport: retryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				status, body := 503, `{"error":"busy"}`
				if calls == 2 {
					status, body = 200, `{"value":"doom-only"}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"1"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if cancelEarly {
				cancel()
			}
			var out map[string]string
			err := c.Operation(ctx, "knowledge.get", "doom", map[string]string{"id": "shared"}, &out)
			cancel()
			if cancelEarly {
				if !errors.Is(err, context.Canceled) || calls > 1 {
					t.Fatal("cancelled retry continued", err, calls)
				}
			} else if err != nil || calls != 2 || out["value"] != "doom-only" {
				t.Fatal("busy read did not recover", err, calls, out)
			}
		}
	})
}

func TestRetryAfterDatesRespectWaitBudget(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if _, ok := retryDelay(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat), 0, now); ok {
		t.Fatal("far-future retry date bypassed budget")
	}
	d, ok := retryDelay(now.Add(2*time.Second).Format(http.TimeFormat), 0, now)
	if !ok || d < 2*time.Second || d >= 2250*time.Millisecond {
		t.Fatal("Retry-After date not honored", d, ok)
	}
}

func TestPublicRenewalDoesNotResetBusyRetryBudget(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := &Client{URL: "https://one.example", PrivateKey: key}
		c.HTTP = &http.Client{Transport: retryTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			var req Request
			json.NewDecoder(r.Body).Decode(&req)
			status, body := 503, `{"error":"busy"}`
			if calls == 3 {
				status = 401
			}
			if req.Action == "community.connect" {
				status, body = 200, `{"community":{"id":"doom"},"access_token":"renewed"}`
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		var out any
		err := c.Operation(context.Background(), "query", "doom", map[string]string{"query": "clock"}, &out)
		var api *APIError
		if !errors.As(err, &api) || api.Status != 503 || calls != 5 {
			t.Fatal("renewal reset retry budget", calls, err)
		}
	})
}

func TestRemoteQueryRejectsContradictoryCommunityIdentity(t *testing.T) {
	for _, hit := range []Result{
		{CommunityID: "godzilla"},
		{CommunityID: "doom", Locations: []Location{{CommunityID: "godzilla"}}},
	} {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(QueryResult{Hits: []Result{hit}})
		}))
		out, err := RemoteQuery(context.Background(), Connection{Service: h.URL, CommunityID: "doom"}, "clock", engine.Options{})
		h.Close()
		if err == nil || len(out.Hits) != 0 {
			t.Fatal("contradictory response relabeled instead of rejected")
		}
	}
}

func TestSignedRetriesUseFreshProofsForSameProject(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, key, _ := ed25519.GenerateKey(nil)
		seen := map[string]bool{}
		identity := ""
		calls := 0
		c := &Client{URL: "https://kb.example", PrivateKey: key, KeyCommunity: "doom"}
		c.HTTP = &http.Client{Transport: retryTransport(func(r *http.Request) (*http.Response, error) {
			proof, err := connectionauth.Verify(r, c.URL, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if seen[proof.Nonce] {
				t.Fatal("retry reused proof")
			}
			seen[proof.Nonce] = true
			if identity == "" {
				identity = proof.PublicKey
			}
			if identity != proof.PublicKey {
				t.Fatal("retry changed project key")
			}
			var req Request
			json.NewDecoder(r.Body).Decode(&req)
			if req.Community != "doom" {
				t.Fatal("retry changed community")
			}
			calls++
			status, body := 503, `{"error":"busy"}`
			if calls == 3 {
				status, body = 200, `{}`
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"1"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		var result any
		if err := c.Operation(context.Background(), "query", "doom", map[string]string{"query": "clock"}, &result); err != nil || calls != 3 {
			t.Fatal(calls, err)
		}
	})
}

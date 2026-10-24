package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSupabaseIdentityRequiresVerifiedAccount(t *testing.T) {
	user := id()
	for _, tc := range []struct {
		name      string
		anonymous bool
		confirmed string
		status    int
	}{
		{"verified", false, "2026-09-07T00:00:00Z", 0},
		{"unconfirmed", false, "", 403},
		{"anonymous", true, "2026-09-07T00:00:00Z", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/auth/v1/user" || r.Header.Get("Authorization") != "Bearer test" {
					t.Error("incorrect identity request")
				}
				writeJSON(w, 200, map[string]any{"id": user, "is_anonymous": tc.anonymous, "email_confirmed_at": tc.confirmed})
			}))
			defer provider.Close()
			s := New(nil, Config{SupabaseURL: provider.URL})
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Authorization", "Bearer test")
			got, err := s.identity(r)
			if tc.status == 0 {
				if err != nil || got != user {
					t.Fatalf("identity: %s %v", got, err)
				}
				return
			}
			if ae, ok := err.(*apiError); !ok || ae.Status != tc.status {
				t.Fatalf("expected %d, got %v", tc.status, err)
			}
		})
	}
}

func TestDeviceExchangeAndRevocation(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	post := func(path string, p any, token string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", h.URL+path, bytes.NewReader(raw(p)))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, e := h.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode != want {
			t.Fatalf("%s: %d %+v", path, res.StatusCode, out)
		}
		return out
	}
	start := post("/v1/auth/device/start", map[string]any{}, "", 200)
	secret := start["device_secret"].(string)
	code := start["user_code"].(string)
	post("/v1/auth/device/poll", map[string]string{"device_secret": secret}, "", 202)
	post("/v1/auth/device/approve", map[string]string{"code": code}, owner, 200)
	post("/v1/auth/device/approve", map[string]string{"code": code}, owner, 404)
	result := post("/v1/auth/device/poll", map[string]string{"device_secret": secret}, "", 200)
	token := result["access_token"].(string)
	post("/v1/auth/device/poll", map[string]string{"device_secret": secret}, "", 401)
	s.Authenticate = nil
	req, _ := http.NewRequest("GET", h.URL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	user, e := s.identity(req)
	if e != nil || user != owner {
		t.Fatalf("device token: %s %v", user, e)
	}
	devices := unpack[[]struct {
		ID string `json:"id"`
	}](t, op(t, h, token, "token.list", "", map[string]any{}, 200))
	if len(devices) != 1 {
		t.Fatal("device missing", devices)
	}
	op(t, h, token, "token.revoke", "", map[string]string{"id": devices[0].ID}, 200)
	if _, e = s.identity(req); e == nil {
		t.Fatal("revoked token accepted")
	}
	var remaining int
	if e = s.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM community.tokens WHERE secret_hash=$1)+(SELECT count(*) FROM community.devices WHERE secret_hash=$2)`, hash(token), hash(secret)).Scan(&remaining); e != nil || remaining != 0 {
		t.Fatalf("deleted credentials retained: %d %v", remaining, e)
	}
}
func TestCredentialTablesAreNotAvailableToAppRole(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	for _, table := range []string{"tokens", "accounts", "browser_sessions", "oauth_states"} {
		tx, e := s.begin(ctx, id())
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(ctx, `SELECT * FROM community.`+table)
		tx.Rollback(ctx)
		if e == nil {
			t.Fatalf("credential table %s accessible to request role", table)
		}
	}
}

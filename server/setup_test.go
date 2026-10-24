package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupBlocksCommunityAndAuthenticationRoutes(t *testing.T) {
	// A nil DB makes any accidental dispatch to a community operation fail.
	s := New(nil, Config{SetupOnly: true, JevEnabled: true})
	if s.Semantic != nil {
		t.Fatal("setup enabled the optional paid reviewer")
	}
	for _, path := range []string{"/", "/v1/config", "/v1/operations", "/v1/auth/github/start", "/v1/auth/device/start", "/healthz/"} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			r := httptest.NewRequest(method, path, strings.NewReader(`{"action":"community.create"}`))
			r.Header.Set("Authorization", "Bearer irrelevant")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "setup only") {
				t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
			}
		}
	}
}

func TestSetupHealthChecksDatabaseAndReportsLimitedMode(t *testing.T) {
	s, h := fixture(t)
	s.Config.SetupOnly = true
	res, err := h.Client().Get(h.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var result struct {
		OK               bool   `json:"ok"`
		Mode             string `json:"mode"`
		CommunityEnabled bool   `json:"community_enabled"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !result.OK || result.Mode != "setup" || result.CommunityEnabled {
		t.Fatalf("unexpected setup health: %+v, HTTP %d", result, res.StatusCode)
	}
	s.DB.Close()
	res, err = h.Client().Get(h.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 503 {
		t.Fatalf("unavailable database reported healthy: %d", res.StatusCode)
	}
}

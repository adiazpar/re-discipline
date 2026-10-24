package community

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerPingNeedsNoProjectOrCredentials(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/healthz" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request: %s %s, authorization present: %v", r.Method, r.URL, r.Header.Get("Authorization") != "")
		}
		w.Write([]byte(`{"ok":true,"protocol":1,"mode":"setup","community_enabled":false}`))
	}))
	defer h.Close()
	t.Setenv("RE_DISCIPLINE_SERVICE", h.URL)
	t.Setenv("RE_DISCIPLINE_TOKEN", "must-not-be-sent")
	result, err := Execute(context.Background(), t.TempDir(), Command{Action: "server.ping", Service: h.URL})
	if err != nil {
		t.Fatal(err)
	}
	health := result.(ServerHealth)
	if !health.OK || health.Mode != "setup" || health.Service != h.URL || health.CommunityEnabled == nil || *health.CommunityEnabled {
		t.Fatalf("unexpected health: %+v", health)
	}
}

func TestServerPingRejectsInvalidOrUnavailableServices(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"database unavailable", `{"error":"database unavailable"}`, 503},
		{"wrong protocol", `{"ok":true,"protocol":999}`, 200},
		{"not healthy", `{"ok":false,"protocol":1}`, 200},
		{"not JSON", "<html>hello</html>", 200},
		{"oversized", strings.Repeat(" ", 8193), 200},
		{"redirect", "", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://127.0.0.1:1/never-follow")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer h.Close()
			if _, err := Ping(context.Background(), h.URL); err == nil {
				t.Fatal("accepted an invalid health response")
			}
		})
	}
	for _, url := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/path", "https://example.com?secret=yes"} {
		if _, err := Ping(context.Background(), url); err == nil {
			t.Fatalf("accepted invalid service origin %q", url)
		}
	}
}

func TestServerPingHonorsCancellation(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := Ping(ctx, h.URL); err == nil {
		t.Fatal("cancelled health check succeeded")
	}
}

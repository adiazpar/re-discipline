package community

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"crypto/ed25519"
	"crypto/rand"
)

func TestOnlyOperatorManagementTriggersDeploymentCheck(t *testing.T) {
	previous := OperatorUpdate
	defer func() { OperatorUpdate = previous }()
	checks := 0
	OperatorUpdate = func(context.Context, string, bool) (any, error) { checks++; return nil, nil }
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer host.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	for _, tc := range []struct {
		action string
		token  string
		key    ed25519.PrivateKey
		want   int
	}{
		{"server.stats", "operator", nil, 1},
		{"knowledge.upload", "operator", nil, 1},
		{"query", "operator", key, 0},
		{"knowledge.get", "operator", key, 0},
		{"community.connect", "operator", key, 0},
		{"community.leave", "operator", key, 0},
		{"knowledge.upload", "", key, 0},
	} {
		before := checks
		client := &Client{URL: host.URL, Token: tc.token, HTTP: host.Client(), PrivateKey: tc.key}
		var out map[string]any
		if err := client.Operation(context.Background(), tc.action, "", nil, &out); err != nil {
			t.Fatal(err)
		}
		if checks-before != tc.want {
			t.Fatalf("%s: checked %d times", tc.action, checks-before)
		}
	}
}

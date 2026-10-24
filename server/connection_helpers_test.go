package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/connectionauth"
)

func newReaderKey() string {
	_, k, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return "test-key:" + base64.RawURLEncoding.EncodeToString(k.Seed())
}
func testPrivateKey(key string) ed25519.PrivateKey {
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "test-key:"))
	if err != nil {
		panic(err)
	}
	return ed25519.NewKeyFromSeed(seed)
}
func testPublicKey(key string) string {
	return base64.RawURLEncoding.EncodeToString(testPrivateKey(key).Public().(ed25519.PublicKey))
}
func authorizeTestRequest(r *http.Request, key string) {
	if strings.HasPrefix(key, "test-key:") {
		if err := connectionauth.Sign(r, testPrivateKey(key), time.Now()); err != nil {
			panic(err)
		}
	} else if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
}
func joinReader(t *testing.T, h *httptest.Server, operator string, l kb.Library, private bool) string {
	t.Helper()
	key := newReaderKey()
	invitation := ""
	if private {
		grant := unpack[map[string]any](t, op(t, h, operator, "access.create", l.ID, nil, 200))
		invitation = grant["invitation"].(string)
	}
	op(t, h, key, "community.connect", l.ID, map[string]string{"invitation": invitation}, 200)
	return key
}
func connectionID(t *testing.T, s *Server, key string) string {
	t.Helper()
	var connectionID string
	if err := s.DB.QueryRow(context.Background(), `SELECT id::text FROM community.reader_connections WHERE public_key=$1`, testPublicKey(key)).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	return connectionID
}

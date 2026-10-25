package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/server/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "re_community_test_" + strings.ReplaceAll(id(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close()
	})
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatalf("migration must be idempotent: %v", err)
	}
	s := New(db, Config{CacheDir: t.TempDir(), PublicURL: "https://example.test"})
	s.Config.OperatorID, s.Config.OperatorToken = id(), randomToken()
	h := httptest.NewServer(s.Handler())
	s.Config.PublicURL = h.URL

	t.Cleanup(h.Close)
	return s, h
}
func op(t *testing.T, h *httptest.Server, user, action, c string, p any, want int) json.RawMessage {
	t.Helper()
	req, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: action, Community: c, Data: raw(p)})))
	authorizeTestRequest(req, user)
	res, err := h.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b json.RawMessage
	if err = json.NewDecoder(res.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s HTTP %d wanted %d: %s", action, res.StatusCode, want, b)
	}
	return b
}
func unpack[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func create(t *testing.T, h *httptest.Server, owner, slug, visibility, mode string) kb.Library {
	t.Helper()
	l := unpack[kb.Library](t, op(t, h, owner, "community.create", "", kb.Library{Name: "Engine research", Slug: slug, Policy: kb.Policy{Scope: "DOOM 2016 engine behavior, excluding local development infrastructure.", Exclusions: []string{"personal development services"}}}, 200))
	if visibility != "private" {
		l = unpack[kb.Library](t, op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200))
	}
	return l
}

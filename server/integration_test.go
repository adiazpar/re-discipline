package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
	s.Authenticate = func(ctx context.Context, token string) (string, error) {
		if err := validUUID(token); err != nil {
			return "", fail(401, "invalid test identity")
		}
		return token, nil
	}
	// Exercise the preserved ingestion core independently of the operator/reader
	// boundary. gateway_test.go covers the only public production handler.
	h := httptest.NewServer(s.legacyHandler())
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
	l := unpack[kb.Library](t, op(t, h, owner, "community.create", "", kb.Library{Name: "Engine research", Slug: slug, Policy: kb.Policy{Scope: "DOOM 2016 engine behavior, excluding local development infrastructure.", Mode: mode, Exclusions: []string{"personal development services"}}}, 200))
	if visibility != "private" {
		l = unpack[kb.Library](t, op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200))
	}
	return l
}
func document() kb.Document {
	return kb.Document{SourcePath: "docs/engine-clock.md", Markdown: "---\nstatus: promoted\nkind: fact\ngrade: direct\nidents: [timescale]\n---\n# The timescale variable scales engine time\nA timescale value of 0.5 halves the engine time step.", Kind: "fact", Grade: "direct", Build: "DOOM 2016 build 1", Evidence: []kb.Evidence{{Label: "Observed time step", Excerpt: "At timescale 0.5, the recorded time step was half of the baseline."}}}
}
func submit(t *testing.T, h *httptest.Server, user, c, key string, d kb.Document) kb.Submission {
	return unpack[kb.Submission](t, op(t, h, user, "submission.create", c, map[string]any{"idempotency_key": key, "document": d}, 200))
}
func runJob(t *testing.T, s *Server) {
	t.Helper()
	done, err := s.RunJob(context.Background())
	if err != nil || !done {
		t.Fatalf("job: done=%v error=%v", done, err)
	}
}

func TestCommunityLifecycleAndIsolation(t *testing.T) {
	s, h := fixture(t)
	a, b, contributor := id(), id(), id()
	l := create(t, h, a, "doom-private", "private", "trusted")
	for _, action := range []string{"community.get", "query", "changes", "member.list", "submission.list", "usage"} {
		op(t, h, b, action, l.ID, map[string]any{}, 404)
		op(t, h, "", action, l.ID, map[string]any{}, 404)
	}
	op(t, h, b, "community.update", l.ID, map[string]any{}, 404)
	inv := unpack[map[string]any](t, op(t, h, a, "invite.create", l.ID, map[string]any{"role": "contributor"}, 200))
	op(t, h, contributor, "invite.redeem", "", map[string]any{"token": inv["token"]}, 200)
	op(t, h, b, "invite.redeem", "", map[string]any{"token": inv["token"]}, 404)
	op(t, h, contributor, "member.set", l.ID, map[string]any{"user_id": contributor, "role": "maintainer"}, 403)
	key := id()
	d := document()
	v := submit(t, h, contributor, l.ID, key, d)
	replay := submit(t, h, contributor, l.ID, key, d)
	if replay.ID != v.ID {
		t.Fatal("idempotency failed")
	}
	d.Markdown += " altered"
	op(t, h, contributor, "submission.create", l.ID, map[string]any{"idempotency_key": key, "document": d}, 409)
	runJob(t, s)
	v = unpack[kb.Submission](t, op(t, h, contributor, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	if v.State != "needs_review" {
		t.Fatalf("contributor unexpectedly trusted: %s", v.State)
	}
	op(t, h, contributor, "submission.review", l.ID, map[string]any{}, 403)
	review := map[string]any{"id": v.ID, "digest": "wrong", "policy_version": l.PolicyVersion, "decision": "accept", "rationale": "Evidence supports the engine claim."}
	op(t, h, a, "submission.review", l.ID, review, 409)
	review["digest"] = v.Digest
	accepted := unpack[kb.Submission](t, op(t, h, a, "submission.review", l.ID, review, 200))
	if accepted.State != "accepted" {
		t.Fatal(accepted.State)
	}
	changes := unpack[kb.Changes](t, op(t, h, contributor, "changes", l.ID, map[string]any{}, 200))
	if len(changes.Changes) != 1 || changes.Through != 1 {
		t.Fatalf("changes: %+v", changes)
	}
	f := changes.Changes[0]
	results := unpack[map[string]json.RawMessage](t, op(t, h, contributor, "query", l.ID, map[string]any{"query": "timescale"}, 200))
	if len(unpack[[]any](t, results["hits"])) != 1 {
		t.Fatalf("query: %s", results["hits"])
	}
	other := create(t, h, b, "other-community", "public", "trusted")
	op(t, h, b, "finding.get", other.ID, map[string]string{"id": f.FindingID}, 200)
	if got := unpack[[]any](t, op(t, h, b, "finding.get", other.ID, map[string]string{"id": f.FindingID}, 200)); len(got) != 0 {
		t.Fatal("cross-community finding leaked")
	}
	op(t, h, a, "finding.withdraw", l.ID, map[string]any{"id": f.FindingID, "revision": f.Revision, "reason": "New evidence contradicts this observation."}, 200)
	after := unpack[kb.Changes](t, op(t, h, contributor, "changes", l.ID, map[string]any{"since": 1}, 200))
	if len(after.Changes) != 1 || !after.Changes[0].Deleted {
		t.Fatal("withdrawal tombstone missing")
	}
	op(t, h, a, "member.remove", l.ID, map[string]any{"user_id": contributor}, 200)
	op(t, h, contributor, "changes", l.ID, map[string]any{}, 404)
	tx, err := s.begin(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var n int
	if err = tx.QueryRow(context.Background(), `SELECT count(*) FROM community.revisions WHERE community_id=$1`, l.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("RLS leaked revisions: %d %v", n, err)
	}
	if _, err = tx.Exec(context.Background(), `UPDATE community.libraries SET visibility='public' WHERE id=$1`, l.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedPublicationAndRevisionConflict(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "trusted-engine", "public", "trusted")
	v := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	v = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	if v.State != "accepted" {
		t.Fatal(v.State)
	}
	page := unpack[kb.Changes](t, op(t, h, "", "changes", l.ID, map[string]any{}, 200))
	f := page.Changes[0]
	d := document()
	d.FindingID = f.FindingID
	d.BaseRevision = f.Revision
	d.Markdown += "\nVerified a second time."
	v1 := submit(t, h, owner, l.ID, id(), d)
	d.Markdown += " Another observation."
	v2 := submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	runJob(t, s)
	one := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v1.ID}, 200))
	two := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v2.ID}, 200))
	if one.State != "accepted" || two.State != "conflict" {
		t.Fatalf("lost update: %s %s", one.State, two.State)
	}
	snapshot := unpack[kb.Changes](t, op(t, h, "", "changes", l.ID, map[string]any{"through": 1}, 200))
	if len(snapshot.Changes) != 1 || snapshot.More {
		t.Fatal("snapshot boundary not respected")
	}
}

func TestQuotasAndPublicationExclusions(t *testing.T) {
	s, h := fixture(t)
	_ = s
	owner := id()
	t.Setenv("MAX_OWNED_COMMUNITIES", "1")
	t.Setenv("MAX_DAILY_SUBMISSIONS", "1")
	l := create(t, h, owner, "limited-library", "private", "trusted")
	op(t, h, owner, "community.create", "", kb.Library{Name: "Second library", Slug: "second-library", Visibility: "private", Policy: l.Policy}, 429)
	for _, mutate := range []func(*kb.Document){func(d *kb.Document) { d.SourcePath = "docs/ops/setup.md" }, func(d *kb.Document) { d.SourcePath = "docs/../../secret.md" }, func(d *kb.Document) { d.Markdown += "\nC:\\Users\\alexd\\daemon" }, func(d *kb.Document) { d.Kind = "ops" }} {
		d := document()
		mutate(&d)
		op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": id(), "document": d}, 422)
	}
	key := id()
	submit(t, h, owner, l.ID, key, document())
	submit(t, h, owner, l.ID, key, document())
	changed := document()
	changed.Markdown += "\nA different observation."
	op(t, h, owner, "submission.create", l.ID, map[string]any{"idempotency_key": id(), "document": changed}, 429)
}

func TestParallelOwnerQuota(t *testing.T) {
	_, h := fixture(t)
	owner := id()
	t.Setenv("MAX_OWNED_COMMUNITIES", "1")
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := kb.Library{Name: "Parallel community", Slug: fmt.Sprintf("parallel-%d", i), Visibility: "private", Policy: kb.Policy{Scope: "Engine behavior under investigation.", Mode: "trusted"}}
			req, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: "community.create", Data: raw(l)})))
			req.Header.Set("Authorization", "Bearer "+owner)
			res, e := h.Client().Do(req)
			if e != nil {
				statuses <- 0
				return
			}
			res.Body.Close()
			statuses <- res.StatusCode
		}(i)
	}
	wg.Wait()
	close(statuses)
	success, limited := 0, 0
	for code := range statuses {
		if code == 200 {
			success++
		}
		if code == 429 {
			limited++
		}
	}
	if success != 1 || limited != 1 {
		t.Fatalf("parallel quota failed: success=%d limited=%d", success, limited)
	}
}

func TestAutomatedReviewAndPolicyRace(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		if p["model"] != "test-reviewer" {
			t.Error("review model missing")
		}
		writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"decision":"accept","rationale":"Scope and provided evidence support the portable engine claim."}`}}}})
	}))
	defer model.Close()
	s.Config.ReviewURL = model.URL
	t.Setenv("REVIEW_MODEL", "test-reviewer")
	l := create(t, h, owner, "automated-library", "private", "automated")
	v := submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	got := unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	if got.State != "accepted" {
		t.Fatal(got.State)
	}
	t.Setenv("MAX_DAILY_MODEL_REVIEWS", "1")
	d := document()
	d.Markdown += "\nAdditional observation."
	v = submit(t, h, owner, l.ID, id(), d)
	runJob(t, s)
	got = unpack[kb.Submission](t, op(t, h, owner, "submission.get", l.ID, map[string]string{"id": v.ID}, 200))
	if got.State != "needs_review" {
		t.Fatal("review budget did not fail to manual review")
	}
}

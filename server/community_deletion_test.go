package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCommunityDeletionErasesOnlyTarget(t *testing.T) {
	s, h, operator := searchFixture(t)
	ctx := context.Background()
	admin := s.DB
	// Exercise the actual restricted runtime role, not the migration superuser.
	cfg := s.DB.Config()
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE re_community_worker`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s.DB = pool
	a := create(t, h, operator, "delete-target", "public", "trusted")
	b := create(t, h, operator, "delete-survivor", "private", "trusted")
	for _, l := range []kb.Library{a, b} {
		submit(t, h, operator, l.ID, id(), document())
		runJob(t, s)
		op(t, h, operator, "query", l.ID, map[string]string{"query": "timescale"}, 200)
	}
	reader := joinReader(t, h, operator, a, false)
	keepReader := joinReader(t, h, operator, b, true)
	op(t, h, reader, "query", a.ID, map[string]string{"query": "timescale"}, 200)
	op(t, h, keepReader, "query", b.ID, map[string]string{"query": "timescale"}, 200)
	inv := unpack[map[string]any](t, op(t, h, operator, "access.create", a.ID, nil, 200))
	op(t, h, operator, "access.create", b.ID, nil, 200)
	// Pending publication, jobs, health work, aliases, legacy invitations and
	// import grants must be erased along with accepted revisions and sources.
	d := document()
	d.Markdown += "\nAn additional pending claim."
	pending := submit(t, h, operator, a.ID, id(), d)
	if _, err = admin.Exec(ctx, `INSERT INTO community.submission_aliases(community_id,author_id,idempotency_key,submission_id) VALUES($1,$2,$3,$4)`, a.ID, s.Config.OperatorID, id(), pending.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO community.invites(id,community_id,secret_hash,role,expires_at) VALUES($1,$2,$3,'reader',now()+interval '1 day')`, id(), a.ID, randomToken()); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO community.import_grants(id,community_id,user_id,created_by,expires_at,remaining_count,remaining_bytes) VALUES($1,$2,$3,$3,now()+interval '1 day',1,100)`, id(), a.ID, s.Config.OperatorID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO community.health_issues(id,community_id,subject_kind,subject_id,subject_version,kind,reason,policy_version) VALUES($1,$2,'submission',$3,'v','fixture','delete this issue',1)`, id(), a.ID, pending.ID); err != nil {
		t.Fatal(err)
	}
	// Neither readers nor an actor owning a different community can erase it.
	op(t, h, reader, "community.delete", a.ID, nil, 403)
	op(t, h, keepReader, "community.delete", a.ID, nil, 404)
	op(t, h, "", "community.delete", a.ID, nil, 401)
	op(t, h, operator, "community.delete", a.Slug, nil, 400)
	op(t, h, operator, "community.delete", strings.ToUpper(a.ID), nil, 400)
	for _, actor := range []string{id(), s.Config.OperatorID} {
		tx, e := s.begin(ctx, actor)
		if e != nil {
			t.Fatal(e)
		}
		if actor == s.Config.OperatorID {
			_, e = tx.Exec(ctx, `SELECT set_config('app.read_token',$1,true)`, connectionID(t, s, reader))
			if e != nil {
				t.Fatal(e)
			}
		}
		_, e = tx.Exec(ctx, `SELECT community.erase_community($1)`, a.ID)
		tx.Rollback(ctx)
		if e == nil {
			t.Fatal("SQL erasure bypassed owner-only authorization")
		}
	}
	result := unpack[deletionResult](t, deleteFixture(t, h, operator, a.ID))
	if !result.Deleted || result.CleanupPending || result.AlreadyAbsent {
		t.Fatal(result)
	}
	// Discover all community-owned tables so a future schema addition cannot
	// silently leave data behind. Shared operator/nonce accounting is separate.
	rows, err := admin.Query(ctx, `SELECT table_name FROM information_schema.columns WHERE table_schema='community' AND column_name='community_id' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var n int
		if err = admin.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{"community", table}.Sanitize()+` WHERE community_id=$1`, a.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s retained rows: %d %v", table, n, err)
		}
	}
	var n int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM community.request_nonces WHERE public_key=$1`, testPublicKey(reader)).Scan(&n); err != nil || n != 0 {
		t.Fatal("reader nonces retained", n, err)
	}
	if _, err = os.Stat(filepath.Join(s.Config.CacheDir, a.ID)); !os.IsNotExist(err) {
		t.Fatal("deleted index remains", err)
	}
	if _, err = os.Stat(filepath.Join(s.Config.CacheDir, b.ID)); err != nil {
		t.Fatal("surviving index lost", err)
	}
	s.metadata.mu.Lock()
	for path := range s.metadata.entries {
		if strings.HasPrefix(path, filepath.Join(s.Config.CacheDir, a.ID)+string(os.PathSeparator)) {
			t.Error("deleted metadata retained")
		}
	}
	s.metadata.mu.Unlock()
	op(t, h, operator, "community.get", a.ID, nil, 404)
	op(t, h, operator, "submission.create", a.ID, map[string]any{"idempotency_key": id(), "document": document()}, 404)
	op(t, h, newReaderKey(), "community.connect", a.ID, map[string]any{"invitation": inv["invitation"]}, 404)
	keep := unpack[kb.QueryResult](t, op(t, h, keepReader, "query", b.ID, map[string]string{"query": "timescale"}, 200))
	if len(keep.Hits) != 1 || keep.Hits[0].CommunityID != b.ID {
		t.Fatal("other community changed", keep)
	}
	// The name is reusable, but a retry with the old UUID cannot touch its replacement.
	replacement := create(t, h, operator, a.Slug, "private", "trusted")
	if replacement.ID == a.ID {
		t.Fatal("UUID reused")
	}
	result = unpack[deletionResult](t, confirmDeletion(t, h, operator, a.ID, strings.Repeat("x", 43)))
	if !result.Deleted || !result.AlreadyAbsent || result.CleanupPending {
		t.Fatal(result)
	}
	op(t, h, operator, "community.get", replacement.ID, nil, 200)
}

func TestCommunityDeletionCancelsQueuedAndRunningWork(t *testing.T) {
	s, h, operator := searchFixture(t)
	s.scheduler = testScheduler(1, 32)
	a := create(t, h, operator, "delete-busy", "public", "trusted")
	b := create(t, h, operator, "keep-busy", "public", "trusted")
	reader := joinReader(t, h, operator, a, false)
	ctx, done := s.trackCommunityWork(context.Background(), a.ID)
	defer done()
	release, _, err := s.scheduler.acquire(ctx, a.ID, "running")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	finished := make(chan struct{})
	go func() { <-ctx.Done(); release(); close(finished) }()
	response := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: "query", Community: a.ID, Data: raw(map[string]string{"query": "timescale"})})))
		authorizeTestRequest(r, reader)
		res, e := h.Client().Do(r)
		if e != nil {
			response <- 0
			return
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		response <- res.StatusCode
	}()
	deadline := time.Now().Add(5 * time.Second)
	for s.scheduler.stats()["queued"].(int) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.scheduler.stats()["queued"].(int) != 1 {
		t.Fatal("request never queued")
	}
	out := unpack[deletionResult](t, deleteFixture(t, h, operator, a.ID))
	if !out.Deleted || out.CleanupPending {
		t.Fatal(out)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("running work not cancelled")
	}
	if context.Cause(ctx) != errCommunityLocked {
		t.Fatal(context.Cause(ctx))
	}
	select {
	case code := <-response:
		if code != 423 {
			t.Fatal("queued read not denied", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued read stuck")
	}
	op(t, h, operator, "query", b.ID, map[string]string{"query": "timescale"}, 200)
	done()
	s.searchMu.Lock()
	_, exists := s.searchStates[a.ID]
	s.searchMu.Unlock()
	if exists {
		t.Fatal("deleted community retained runtime state")
	}
	s.scheduler.mu.Lock()
	for key := range s.scheduler.history {
		if key.community == a.ID {
			t.Error("deleted community retained scheduling history")
		}
	}
	s.scheduler.mu.Unlock()
}

func TestCommunityDeletionCleanupSurvivesRestart(t *testing.T) {
	s, h, operator := searchFixture(t)
	a := create(t, h, operator, "delete-restart", "private", "trusted")
	submit(t, h, operator, a.ID, id(), document())
	runJob(t, s)
	op(t, h, operator, "query", a.ID, map[string]string{"query": "timescale"}, 200)
	blocked := filepath.Join(s.Config.CacheDir, "semantic")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Semantic = semantic.New(semantic.Config{Enabled: true}, blocked)
	result := unpack[deletionResult](t, deleteFixture(t, h, operator, a.ID))
	if !result.Deleted || !result.CleanupPending {
		t.Fatal(result)
	}
	op(t, h, operator, "community.get", a.ID, nil, 404)
	var n int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.deletion_cleanup WHERE community_id=$1`, a.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("missing durable cleanup", n, err)
	}
	// Simulate a process dying with files still present, then restarting.
	root := filepath.Join(s.Config.CacheDir, a.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old-document"), []byte("erase this"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.DB, s.Config)
	if err := restarted.cleanDeletedCommunities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("orphaned cache survived restart", err)
	}
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.deletion_cleanup WHERE community_id=$1`, a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("completed cleanup retained", n, err)
	}
}

func TestCommunityDeletionCancelsBackgroundReview(t *testing.T) {
	s, h, operator := searchFixture(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer provider.Close()
	s.Config.ReviewURL = provider.URL
	t.Setenv("REVIEW_MODEL", "deletion-fixture")
	l := create(t, h, operator, "delete-review", "private", "automated")
	submit(t, h, operator, l.ID, id(), document())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := s.RunJob(ctx); finished <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("background review never started")
	}
	out := unpack[deletionResult](t, deleteFixture(t, h, operator, l.ID))
	if !out.Deleted || out.CleanupPending {
		t.Fatal(out)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("deleted community's provider request continued")
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("background review remained active")
	}
	var n int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.jobs WHERE community_id=$1`, l.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("review job resurrected", n, err)
	}
}

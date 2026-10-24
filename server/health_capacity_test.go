package server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthWorkerLeavesCapacityAfterSlowWork(t *testing.T) {
	for _, elapsed := range []time.Duration{0, 100 * time.Millisecond, time.Second, 10 * time.Second, 75 * time.Second} {
		if delay := healthJobDelay(elapsed, true, nil); delay < time.Second || delay < elapsed*4 {
			t.Fatalf("background work can exceed its duty budget: work=%s delay=%s", elapsed, delay)
		}
	}
	if healthJobDelay(time.Millisecond, false, nil) < 5*time.Second {
		t.Fatal("empty queue is polled too often")
	}
	if healthJobDelay(time.Millisecond, true, errors.New("query timeout")) < 30*time.Second {
		t.Fatal("query failures must back off")
	}
}

type candidateQueryTrace struct{ count atomic.Int32 }

func (t *candidateQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "WITH candidates AS") {
		t.count.Add(1)
	}
	return ctx
}
func (*candidateQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestUnknownApplicabilityCompletesWithoutCandidateScan(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "unknown-applicability", "private", "maintainer")
	d := document()
	d.Build = "TBD"
	submit(t, h, owner, l.ID, id(), d)
	trace := &candidateQueryTrace{}
	cfg := s.DB.Config()
	cfg.ConnConfig.Tracer = trace
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE re_community_worker`)
		return err
	}
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	worker := New(db, Config{CacheDir: t.TempDir()})
	if worked, err := worker.RunHealthJob(context.Background()); err != nil || !worked {
		t.Fatalf("health job: %v %v", worked, err)
	}
	if trace.count.Load() != 0 {
		t.Fatal("unknown applicability performed an unusable candidate scan")
	}
	var complete, incomplete bool
	if err = db.QueryRow(context.Background(), `SELECT analyzed_version=version,last_error<>'' FROM community.health_work WHERE community_id=$1`, l.ID).Scan(&complete, &incomplete); err != nil || !complete || !incomplete {
		t.Fatalf("job must finish with an explicit exception: complete=%v incomplete=%v err=%v", complete, incomplete, err)
	}
}

func TestStoredSearchTermsFollowChangesAndErasure(t *testing.T) {
	s, h := fixture(t)
	owner := id()
	l := create(t, h, owner, "search-terms", "private", "trusted")
	submit(t, h, owner, l.ID, id(), document())
	runJob(t, s)
	ctx := context.Background()
	for _, table := range []string{"submissions", "revisions"} {
		var equal bool
		if err := s.DB.QueryRow(ctx, `SELECT bool_and(search_terms=to_tsvector('english',document->>'markdown')) FROM community.`+table+` WHERE community_id=$1`, l.ID).Scan(&equal); err != nil || !equal {
			t.Fatalf("%s initial terms differ: %v", table, err)
		}
		assertStoredDocumentSize(t, s, table, l.ID)
		if _, err := s.DB.Exec(ctx, `UPDATE community.`+table+` SET document=jsonb_set(document,'{markdown}','"quartzsignal"') WHERE community_id=$1`, l.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DB.QueryRow(ctx, `SELECT bool_and(search_terms @@ plainto_tsquery('english','quartzsignal')) FROM community.`+table+` WHERE community_id=$1`, l.ID).Scan(&equal); err != nil || !equal {
			t.Fatalf("%s did not update terms: %v", table, err)
		}
		assertStoredDocumentSize(t, s, table, l.ID)
		if _, err := s.DB.Exec(ctx, `UPDATE community.`+table+` SET document='{}' WHERE community_id=$1`, l.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DB.QueryRow(ctx, `SELECT bool_and(search_terms IS NULL) FROM community.`+table+` WHERE community_id=$1`, l.ID).Scan(&equal); err != nil || !equal {
			t.Fatalf("%s retained terms after erasure: %v", table, err)
		}
		assertStoredDocumentSize(t, s, table, l.ID)
	}
}

func assertStoredDocumentSize(t *testing.T, s *Server, table, community string) {
	t.Helper()
	var equal bool
	if err := s.DB.QueryRow(context.Background(), `SELECT bool_and(document_bytes=octet_length(document::text)) FROM community.`+table+` WHERE community_id=$1`, community).Scan(&equal); err != nil || !equal {
		t.Fatalf("%s stored quota size differs from the original accounting: %v", table, err)
	}
}

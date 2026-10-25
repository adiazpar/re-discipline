package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Force a concurrent CREATE ROLE past its existence check while another
// transaction owns the uncommitted role. Fresh parallel database installations
// must handle the catalog uniqueness race without aborting their migration.
func TestRoleBootstrapWaitsForConcurrentCreator(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, tc := range []struct{ file, role string }{{"001_initial.sql", "re_community_app"}, {"003_runtime_and_capacity.sql", "re_community_worker"}} {
		t.Run(tc.role, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			name := "re_role_race_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			defer db.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{name}.Sanitize())
			first, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(context.Background())
			if _, err = first.Exec(ctx, "CREATE ROLE "+pgx.Identifier{name}.Sanitize()+" NOLOGIN NOBYPASSRLS"); err != nil {
				t.Fatal(err)
			}
			second, err := db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Release()
			source, err := files.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			script := string(source)
			script = script[strings.Index(script, "DO $$") : strings.Index(script, "END $$;")+len("END $$;")]
			script = strings.ReplaceAll(script, tc.role, name)
			done := make(chan error, 1)
			go func() { _, e := second.Exec(ctx, script); done <- e }()
			waiting := false
			for !waiting && ctx.Err() == nil {
				if err = db.QueryRow(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, second.Conn().PgConn().PID()).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if !waiting {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if !waiting {
				t.Fatal("concurrent creator did not reach the lock")
			}
			if err = first.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal("concurrent role creation aborted bootstrap", err)
			}
			var login, bypass bool
			if err = db.QueryRow(ctx, `SELECT rolcanlogin,rolbypassrls FROM pg_roles WHERE rolname=$1`, name).Scan(&login, &bypass); err != nil || login || bypass {
				t.Fatal("role attributes changed", login, bypass, err)
			}
		})
	}
}

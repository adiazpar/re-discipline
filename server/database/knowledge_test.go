package database

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpgradeKeepsOnlyCurrentKnowledge(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	name := "re_knowledge_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.Database = name
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(ctx, `CREATE SCHEMA community; CREATE TABLE community.schema_migrations(name text PRIMARY KEY,applied_at timestamptz DEFAULT now())`)
	if e != nil {
		t.Fatal(e)
	}
	entries, _ := files.ReadDir(".")
	for _, entry := range entries {
		if entry.Name() >= "022" {
			break
		}
		b, _ := files.ReadFile(entry.Name())
		if _, e = db.Exec(ctx, string(b)); e != nil {
			t.Fatal(entry.Name(), e)
		}
		db.Exec(ctx, `INSERT INTO community.schema_migrations(name) VALUES($1)`, entry.Name())
	}
	cid, owner, fid, r1, r2 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, e = db.Exec(ctx, `INSERT INTO community.libraries(id,owner_id,name,slug,visibility,policy,sequence) VALUES($1,$2,'Migration','migration','private','{"mode":"trusted","scope":"Synthetic migration test"}',2)`, cid, owner)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO community.findings(community_id,id,revision) VALUES($1,$2,$3)`, cid, fid, r2)
	if e != nil {
		t.Fatal(e)
	}
	for n, rid := range []string{r1, r2} {
		text := "old discarded claim"
		if n == 1 {
			text = "current retained knowledge"
		}
		doc := kb.Document{SourcePath: "docs/claim.md", Markdown: "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Claim\n## Knowledge\n" + text + "\n## Evidence\nsecret local research\n", Kind: "fact", Grade: "direct"}
		b, _ := json.Marshal(doc)
		_, e = db.Exec(ctx, `INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,author_id) VALUES($1,$2,$3,$4,$5,$6)`, rid, cid, fid, n+1, b, owner)
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	var gotID, body, fp string
	if e = db.QueryRow(ctx, `SELECT id::text,markdown,fingerprint FROM community.documents WHERE community_id=$1`, cid).Scan(&gotID, &body, &fp); e != nil {
		t.Fatal(e)
	}
	if gotID != fid || !strings.Contains(body, "current retained knowledge") || strings.Contains(body, "old discarded") || strings.Contains(body, "secret local") || kb.KnowledgeFingerprint(body) != fp {
		t.Fatal("migration changed identity/current knowledge", gotID, body)
	}
	var count int
	var size int64
	if e = db.QueryRow(ctx, `SELECT knowledge_count,knowledge_bytes FROM community.libraries WHERE id=$1`, cid).Scan(&count, &size); e != nil || count != 1 || size != int64(len(body)) {
		t.Fatal(count, size, e)
	}
	var retired *string
	if e = db.QueryRow(ctx, `SELECT to_regclass('community.revisions')::text`).Scan(&retired); e != nil || retired != nil {
		t.Fatal("history retained", retired, e)
	}
	if e = Migrate(ctx, db); e != nil {
		t.Fatal("upgrade not idempotent", e)
	}
	// Upgrade an existing current-document store without rewriting content,
	// chronology or the per-document sequence used to detect index lag.
	var beforeDate string
	if e = db.QueryRow(ctx, `UPDATE community.documents SET fingerprint=$2 WHERE community_id=$1 RETURNING updated_at::text`, cid, strings.Repeat("a", 64)).Scan(&beforeDate); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `DELETE FROM community.schema_migrations WHERE name='024_knowledge_timestamps.sql'`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `UPDATE community.libraries SET deletion_id=$2 WHERE id=$1`, cid, uuid.NewString()); e != nil {
		t.Fatal(e)
	}
	if e = Migrate(ctx, db); e == nil {
		t.Fatal("rehash changed a frozen archive")
	}
	var frozenHash string
	if e = db.QueryRow(ctx, `SELECT fingerprint FROM community.documents WHERE community_id=$1`, cid).Scan(&frozenHash); e != nil || frozenHash != strings.Repeat("a", 64) {
		t.Fatal("failed migration was not atomic", frozenHash, e)
	}
	thaw, e := db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = thaw.Exec(ctx, `SELECT set_config('app.deletion_control',$1,true)`, cid); e != nil {
		t.Fatal(e)
	}
	if _, e = thaw.Exec(ctx, `UPDATE community.libraries SET deletion_id=NULL WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	if e = thaw.Commit(ctx); e != nil {
		t.Fatal(e)
	}

	if e = Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	var afterDate, afterBody, afterFP string
	var generation int64
	if e = db.QueryRow(ctx, `SELECT markdown,fingerprint,updated_at::text,generation FROM community.documents WHERE community_id=$1 AND id=$2`, cid, fid).Scan(&afterBody, &afterFP, &afterDate, &generation); e != nil {
		t.Fatal(e)
	}
	if afterBody != body || afterFP != kb.KnowledgeFingerprint(body) || afterDate != beforeDate || generation != 2 {
		t.Fatal("rehash changed knowledge or chronology", afterDate, generation)
	}
	if e = Migrate(ctx, db); e != nil {
		t.Fatal("rehash not idempotent", e)
	}

}

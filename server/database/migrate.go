package database

import (
	"context"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(713905221); CREATE SCHEMA IF NOT EXISTS community; CREATE TABLE IF NOT EXISTS community.schema_migrations(name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()); ALTER TABLE community.schema_migrations ENABLE ROW LEVEL SECURITY;`); err != nil {
		return err
	}
	entries, _ := files.ReadDir(".")
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM community.schema_migrations WHERE name=$1)`, f.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		b, _ := files.ReadFile(f.Name())
		if _, err = tx.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("migration %s: %w", f.Name(), err)
		}
		if f.Name() == "022_current_knowledge.sql" {
			if err = migrateKnowledge(ctx, tx); err != nil {
				return err
			}
		}
		if f.Name() == "024_knowledge_timestamps.sql" {
			if err = rehashKnowledge(ctx, tx); err != nil {
				return fmt.Errorf("migrate knowledge fingerprints (finish or cancel any active archive first): %w", err)
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO community.schema_migrations(name) VALUES($1)`, f.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

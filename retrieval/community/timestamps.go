package community

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
)

type stampStore interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func stampFinding(ctx context.Context, db stampStore, path, raw string, info os.FileInfo, now time.Time) (string, bool, error) {
	id, err := FindingID(raw)
	if err != nil {
		return raw, false, err
	}
	created, updated, err := knowledge.TimestampFields(raw)
	if err != nil {
		return raw, false, err
	}
	fp := KnowledgeFingerprint(ProjectKnowledge(raw).Markdown)
	var oldFP, oldCreated, oldUpdated, format string
	err = db.QueryRowContext(ctx, `SELECT fingerprint,created_at,updated_at,format FROM finding_stamps WHERE id=?`, id).Scan(&oldFP, &oldCreated, &oldUpdated, &format)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return raw, false, err
	}
	instant := now.UTC().Format(time.RFC3339Nano)
	if format == KnowledgeFormat {
		created, updated = oldCreated, oldUpdated
		if oldFP != fp {
			updated = instant
		}
	}
	if created == "" {
		created = instant
	}
	if updated == "" {
		updated = created
	}
	next, err := knowledge.WithTimestamps(raw, created, updated)
	if err != nil {
		return raw, false, err
	}
	currentCreated, currentUpdated, _ := knowledge.TimestampFields(raw)
	changed := currentCreated != created || currentUpdated != updated
	if changed {
		if _, err = replaceFinding(path, raw, next, info); err != nil {
			return raw, false, err
		}
		raw = next
	}
	if oldFP == fp && oldCreated == created && oldUpdated == updated && format == KnowledgeFormat {
		return raw, changed, nil
	}
	_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO finding_stamps(id,fingerprint,created_at,updated_at,format) VALUES(?,?,?,?,?)`, id, fp, created, updated, KnowledgeFormat)
	return raw, changed, err
}

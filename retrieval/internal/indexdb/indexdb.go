// Package indexdb opens shared index files without changing their journal mode.
// Brief SQLite waits keep cancellation responsive; read operations and explicitly
// rolled-back bookkeeping transactions may retry transient lock contention.
package indexdb

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func OpenRead(path string) (*sql.DB, error)  { return open(path, "ro") }
func OpenWrite(path string) (*sql.DB, error) { return open(path, "rwc") }

func open(path, mode string) (*sql.DB, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p = filepath.ToSlash(p)
	// SQLite drive-letter URIs need /C:/..., not a URI authority.
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=" + mode + "&_pragma=busy_timeout(100)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Retry is only for reads or transactions rolled back on every failed attempt.
// Never retry an operation with other side effects. The five-second contention
// budget starts after the first busy result; it does not cap query execution.
func Retry(ctx context.Context, attempt func() error) error {
	var until time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := attempt()
		if err == nil {
			return nil
		}
		if cancelled := ctx.Err(); cancelled != nil {
			return cancelled
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&255 != 5 {
			return err
		}
		if until.IsZero() {
			until = time.Now().Add(5 * time.Second)
		}
		remaining := time.Until(until)
		if remaining <= 0 {
			return err
		}
		timer := time.NewTimer(min(25*time.Millisecond, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type rowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type rowsReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type Row struct {
	ctx   context.Context
	db    rowReader
	query string
	args  []any
}

// ReadRow retries a single read statement. Call Scan once, as with sql.Row.
func ReadRow(ctx context.Context, db rowReader, query string, args ...any) Row {
	return Row{ctx: ctx, db: db, query: query, args: args}
}
func (r Row) Scan(dest ...any) error {
	return Retry(r.ctx, func() error { return r.db.QueryRowContext(r.ctx, r.query, r.args...).Scan(dest...) })
}

// ReadRows retries acquiring a read cursor. The caller must close it and check
// Rows.Err; no retry occurs once results are being consumed.
func ReadRows(ctx context.Context, db rowsReader, query string, args ...any) (*sql.Rows, error) {
	var rows *sql.Rows
	err := Retry(ctx, func() error {
		var err error
		rows, err = db.QueryContext(ctx, query, args...)
		if err != nil && rows != nil {
			rows.Close()
			rows = nil
		}
		return err
	})
	return rows, err
}

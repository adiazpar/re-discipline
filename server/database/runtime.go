package database

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"strings"
)

// ProvisionRuntime creates a dedicated service login without rotating existing credentials.
func ProvisionRuntime(ctx context.Context, db *pgxpool.Pool, migrationURL string) (string, error) {
	u, err := url.Parse(migrationURL)
	if err != nil || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("bootstrap requires a PostgreSQL connection URL")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='re_community_runtime')`).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("re_community_runtime already exists; reuse its saved DATABASE_URL")
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", err
	}
	password := base64.RawURLEncoding.EncodeToString(b)
	// Password is generated from the URL-safe base64 alphabet, with no SQL quotes.
	if _, err = tx.Exec(ctx, `CREATE ROLE re_community_runtime LOGIN NOINHERIT PASSWORD '`+password+`'`); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `GRANT re_community_app,re_community_worker TO re_community_runtime`); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	username := "re_community_runtime"
	if parts := strings.SplitN(u.User.Username(), ".", 2); len(parts) == 2 {
		username += "." + parts[1]
	}
	u.User = url.UserPassword(username, password)
	return u.String(), nil
}

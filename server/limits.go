package server

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"strconv"
)

type Limits struct {
	Communities int   `json:"communities_per_server"`
	Bytes       int64 `json:"bytes_per_community"`
	Findings    int   `json:"documents_per_community"`
}

func setting(name string, defaultValue int) int {
	v, e := strconv.Atoi(os.Getenv(name))
	if e != nil || v <= 0 {
		return defaultValue
	}
	return v
}
func (s *Server) limits() Limits {
	return Limits{setting("MAX_TOTAL_COMMUNITIES", 1000), int64(setting("MAX_COMMUNITY_BYTES", 100<<30)), setting("MAX_COMMUNITY_FINDINGS", 1000000)}
}
func (s *Server) consume(ctx context.Context, tx pgx.Tx, user, kind string, limit int) error {
	var used int
	err := tx.QueryRow(ctx, `INSERT INTO community.usage(user_id,day,kind,count) VALUES($1,current_date,$2,1) ON CONFLICT(user_id,day,kind) DO UPDATE SET count=community.usage.count+1 RETURNING count`, user, kind).Scan(&used)
	if err != nil {
		return err
	}
	if used > limit {
		return fail(429, "daily "+kind+" limit reached")
	}
	return nil
}
func (s *Server) usage(ctx context.Context, tx pgx.Tx, c string) (any, error) {
	var bytes int64
	var findings int
	err := tx.QueryRow(ctx, `SELECT knowledge_bytes,knowledge_count FROM community.libraries WHERE id=$1`, c).Scan(&bytes, &findings)
	return map[string]any{"knowledge_bytes": bytes, "documents": findings, "limits": s.limits()}, err
}

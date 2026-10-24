package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (s *Server) identity(r *http.Request) (string, error) {
	if p, ok := r.Context().Value(principalKey{}).(principal); ok {
		return p.userID, nil
	}
	if s.Config.LocalOnly {
		return s.localIdentity(r)
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		return s.cookieIdentity(r)
	}
	if !strings.HasPrefix(h, "Bearer ") {
		return "", fail(401, "invalid authorization")
	}
	token := strings.TrimPrefix(h, "Bearer ")
	if s.Authenticate != nil {
		return s.Authenticate(r.Context(), token)
	}
	if strings.HasPrefix(token, "rc_") {
		var user string
		err := s.DB.QueryRow(r.Context(), `SELECT user_id::text FROM community.tokens WHERE secret_hash=$1 AND NOT revoked AND expires_at>now()`, hash(token)).Scan(&user)
		if err != nil {
			return "", fail(401, "session expired or revoked; sign in again")
		}
		return user, nil
	}
	if s.Config.SupabaseURL == "" {
		return "", fail(401, "authentication is not configured")
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", strings.TrimRight(s.Config.SupabaseURL, "/")+"/auth/v1/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("apikey", s.Config.SupabaseKey)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return "", fail(503, "identity provider unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fail(401, "session expired or invalid")
	}
	var u struct {
		ID               string `json:"id"`
		Anonymous        bool   `json:"is_anonymous"`
		EmailConfirmedAt string `json:"email_confirmed_at"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 128*1024)).Decode(&u); err != nil || validUUID(u.ID) != nil {
		return "", fail(401, "invalid identity")
	}
	if u.Anonymous || u.EmailConfirmedAt == "" {
		return "", fail(403, "a verified email account is required")
	}
	return u.ID, nil
}
func (s *Server) deviceStart(w http.ResponseWriter, r *http.Request) {
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(713905223)`); err != nil {
		writeError(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM community.devices WHERE expires_at<=now()`); err != nil {
		writeError(w, err)
		return
	}
	var pending int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM community.devices`).Scan(&pending); err != nil {
		writeError(w, err)
		return
	}
	if pending >= setting("MAX_PENDING_DEVICES", 1000) {
		writeError(w, fail(429, "device sign-in capacity reached; retry later"))
		return
	}
	secret := randomToken()
	code := strings.ToUpper(hash(randomToken())[:12])
	_, err = tx.Exec(r.Context(), `INSERT INTO community.devices(secret_hash,code,expires_at) VALUES($1,$2,now()+interval '10 minutes')`, hash(secret), code)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"device_secret": secret, "user_code": code, "verification_url": strings.TrimRight(s.Config.PublicURL, "/") + "/device?code=" + code, "expires_in": 600, "interval": 5})
}
func (s *Server) deviceApprove(w http.ResponseWriter, r *http.Request) {
	user, err := s.identity(r)
	if err == nil {
		err = authenticated(user)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var v struct {
		Code string `json:"code"`
	}
	if err = decode(r.Body, &v); err != nil {
		writeError(w, err)
		return
	}
	tag, err := s.DB.Exec(r.Context(), `UPDATE community.devices SET user_id=$1 WHERE code=$2 AND expires_at>now() AND user_id IS NULL AND NOT consumed`, user, strings.ToUpper(strings.TrimSpace(v.Code)))
	if err != nil {
		writeError(w, err)
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, fail(404, "code expired, invalid, or already approved"))
		return
	}
	writeJSON(w, 200, map[string]any{"approved": true})
}
func (s *Server) devicePoll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var v struct {
		Secret string `json:"device_secret"`
	}
	if err := decode(r.Body, &v); err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var user *string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM community.devices WHERE secret_hash=$1 AND expires_at>now() AND NOT consumed FOR UPDATE`, hash(v.Secret)).Scan(&user)
	if err != nil {
		writeError(w, fail(401, "device request expired or consumed"))
		return
	}
	if user == nil {
		writeJSON(w, 202, map[string]any{"pending": true})
		return
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,713905224))`, *user); err != nil {
		writeError(w, err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM community.tokens WHERE user_id=$1 AND (revoked OR expires_at<=now())`, *user); err != nil {
		writeError(w, err)
		return
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM community.tokens WHERE user_id=$1`, *user).Scan(&active); err != nil {
		writeError(w, err)
		return
	}
	if active >= setting("MAX_ACCOUNT_DEVICES", 20) {
		writeError(w, fail(429, "device limit reached; revoke an existing device"))
		return
	}
	token := "rc_" + randomToken()
	expires := time.Now().UTC().Add(30 * 24 * time.Hour)
	_, err = tx.Exec(ctx, `INSERT INTO community.tokens(id,user_id,secret_hash,label,expires_at) VALUES($1,$2,$3,'Plugin device',$4)`, id(), *user, hash(token), expires)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM community.devices WHERE secret_hash=$1`, hash(v.Secret))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": token, "expires_at": expires, "user_id": *user})
}
func (s *Server) tokens(ctx context.Context, user string, req kb.Request) (any, error) {
	if err := authenticated(user); err != nil {
		return nil, err
	}
	if req.Action == "token.revoke" {
		var v struct {
			ID string `json:"id"`
		}
		if err := data(req.Data, &v); err != nil {
			return nil, err
		}
		tag, err := s.DB.Exec(ctx, `DELETE FROM community.tokens WHERE id=$1 AND user_id=$2`, v.ID, user)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, pgx.ErrNoRows
		}
		return map[string]bool{"revoked": true}, nil
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM community.tokens WHERE user_id=$1 AND (revoked OR expires_at<=now())`, user); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text,label,expires_at,revoked FROM community.tokens WHERE user_id=$1 ORDER BY created_at DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var sid, label string
		var exp time.Time
		var revoked bool
		if err = rows.Scan(&sid, &label, &exp, &revoked); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": sid, "label": label, "expires_at": exp, "revoked": revoked})
	}
	return out, rows.Err()
}

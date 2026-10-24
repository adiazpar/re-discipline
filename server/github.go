package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

func (s *Server) cookieName(kind string) string {
	if strings.HasPrefix(s.Config.PublicURL, "https://") {
		return "__Host-re_" + kind
	}
	return "re_" + kind
}
func (s *Server) setCookie(w http.ResponseWriter, kind, value string, seconds int) {
	c := &http.Cookie{Name: s.cookieName(kind), Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Config.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: seconds}
	if seconds < 0 {
		c.Expires = time.Unix(1, 0)
	} else {
		c.Expires = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	http.SetCookie(w, c)
}
func (s *Server) cookieIdentity(r *http.Request) (string, error) {
	if s.Config.GitHubClientID == "" {
		return "", nil
	}
	c, err := r.Cookie(s.cookieName("session"))
	if err != nil {
		return "", nil
	}
	var user string
	err = s.DB.QueryRow(r.Context(), `SELECT user_id::text FROM community.browser_sessions WHERE secret_hash=$1 AND expires_at>now()`, hash(c.Value)).Scan(&user)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return user, err
}
func (s *Server) oauthConfig() *oauth2.Config {
	return &oauth2.Config{ClientID: s.Config.GitHubClientID, ClientSecret: s.Config.GitHubClientSecret, Endpoint: github.Endpoint, RedirectURL: s.Config.PublicURL + "/v1/auth/github/callback", Scopes: []string{"user:email"}}
}
func safeReturnPath(path string) string {
	if len(path) > 2048 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(u.Path, "/v1/") || strings.ContainsAny(u.Path, "\\\r\n") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return u.RequestURI()
}
func (s *Server) githubStart(w http.ResponseWriter, r *http.Request) {
	state, verifier := randomToken(), oauth2.GenerateVerifier()
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(713905224)`); err != nil {
		writeError(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM community.oauth_states WHERE expires_at<=now()`); err != nil {
		writeError(w, err)
		return
	}
	var pending int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM community.oauth_states`).Scan(&pending); err != nil {
		writeError(w, err)
		return
	}
	if pending >= 1000 {
		writeError(w, fail(429, "sign-in capacity reached; retry later"))
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO community.oauth_states(state_hash,verifier,return_path,expires_at) VALUES($1,$2,$3,now()+interval '10 minutes')`, hash(state), verifier, safeReturnPath(r.URL.Query().Get("return")))
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, err)
		return
	}
	s.setCookie(w, "oauth", state, 600)
	http.Redirect(w, r, s.oauthConfig().AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}
func (s *Server) githubJSON(ctx context.Context, token, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return fail(503, "GitHub sign-in is unavailable; try again")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fail(401, "GitHub identity could not be verified")
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 128*1024)).Decode(v); err != nil {
		return fail(401, "invalid GitHub identity response")
	}
	return nil
}
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	c, err := r.Cookie(s.cookieName("oauth"))
	if err != nil || len(state) != 43 || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		writeError(w, fail(400, "sign-in state is invalid; start sign-in again"))
		return
	}
	s.setCookie(w, "oauth", "", -1)
	var verifier, returnPath string
	err = s.DB.QueryRow(r.Context(), `DELETE FROM community.oauth_states WHERE state_hash=$1 AND expires_at>now() RETURNING verifier,return_path`, hash(state)).Scan(&verifier, &returnPath)
	if err != nil {
		writeError(w, fail(400, "sign-in expired or was already used; start again"))
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 1024 {
		writeError(w, fail(400, "GitHub authorization was not completed"))
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, s.HTTP)
	token, err := s.oauthConfig().Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		writeError(w, fail(401, "GitHub sign-in failed; start again"))
		return
	}
	// GitHub tokens are used only to verify identity, never stored or sent to the browser.
	var gh struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err = s.githubJSON(ctx, token.AccessToken, "/user", &gh); err != nil {
		writeError(w, err)
		return
	}
	if gh.ID <= 0 || len(gh.Login) < 1 || len(gh.Login) > 100 {
		writeError(w, fail(401, "invalid GitHub account"))
		return
	}
	var emails []struct {
		Email    string `json:"email"`
		Verified bool   `json:"verified"`
		Primary  bool   `json:"primary"`
	}
	if err = s.githubJSON(ctx, token.AccessToken, "/user/emails?per_page=100", &emails); err != nil {
		writeError(w, err)
		return
	}
	email := ""
	for _, e := range emails {
		if e.Verified && len(e.Email) >= 3 && len(e.Email) <= 320 {
			email = e.Email
			if e.Primary {
				break
			}
		}
	}
	if email == "" {
		writeError(w, fail(403, "a verified GitHub email is required"))
		return
	}
	secret := randomToken()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var user string
	// The immutable provider ID is the only linking key. Never link accounts by email or login.
	err = tx.QueryRow(ctx, `INSERT INTO community.accounts(github_id,login,email) VALUES($1,$2,$3) ON CONFLICT(github_id) DO UPDATE SET login=excluded.login,email=excluded.email RETURNING id::text`, gh.ID, gh.Login, email).Scan(&user)
	if err != nil {
		writeError(w, err)
		return
	}
	// Upsert locks this account, serializing concurrent session issuance and enforcing its cap.
	if _, err = tx.Exec(ctx, `DELETE FROM community.browser_sessions WHERE expires_at<=now()`); err != nil {
		writeError(w, err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM community.browser_sessions WHERE secret_hash IN (SELECT secret_hash FROM community.browser_sessions WHERE user_id=$1 ORDER BY created_at DESC,secret_hash OFFSET 19)`, user); err != nil {
		writeError(w, err)
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO community.browser_sessions(secret_hash,user_id,expires_at) VALUES($1,$2,now()+interval '30 days')`, hash(secret), user); err != nil {
		writeError(w, err)
		return
	}
	if old, e := r.Cookie(s.cookieName("session")); e == nil {
		if _, err = tx.Exec(ctx, `DELETE FROM community.browser_sessions WHERE secret_hash=$1`, hash(old.Value)); err != nil {
			writeError(w, err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, err)
		return
	}
	s.setCookie(w, "session", secret, 30*24*60*60)
	http.Redirect(w, r, safeReturnPath(returnPath), http.StatusSeeOther)
}
func (s *Server) browserSession(w http.ResponseWriter, r *http.Request) {
	user, err := s.cookieIdentity(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if user == "" {
		writeJSON(w, 200, map[string]any{"session": nil})
		return
	}
	var email, login string
	if err = s.DB.QueryRow(r.Context(), `SELECT email,login FROM community.accounts WHERE id=$1`, user).Scan(&email, &login); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"session": map[string]any{"user": map[string]string{"id": user, "email": email, "login": login}}})
}
func (s *Server) browserLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName("session")); err == nil {
		if _, err = s.DB.Exec(r.Context(), `DELETE FROM community.browser_sessions WHERE secret_hash=$1`, hash(c.Value)); err != nil {
			writeError(w, err)
			return
		}
	}
	s.setCookie(w, "session", "", -1)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

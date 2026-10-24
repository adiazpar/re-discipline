package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSafeReturnPath(t *testing.T) {
	for _, bad := range []string{"https://evil.test", "//evil.test", "/\\evil.test", "/%2f/evil.test", "/%5cevil.test", "/v1/auth/logout", "/foo\r\nLocation: evil"} {
		if got := safeReturnPath(bad); got != "/" {
			t.Errorf("accepted %q as %q", bad, got)
		}
	}
	if got := safeReturnPath("/device?code=ABC"); got != "/device?code=ABC" {
		t.Fatal(got)
	}
}

func TestGitHubSessionSecurity(t *testing.T) {
	s, _ := fixture(t)
	s.Config.GitHubClientID = "test-client"
	s.Config.GitHubClientSecret = "test-secret"
	s.Authenticate = nil
	handler := s.legacyHandler()
	existing := id()
	_, err := s.DB.Exec(context.Background(), `INSERT INTO community.accounts(id,github_id,login,email) VALUES($1,1234,'old-name','old@example.test')`, existing)
	if err != nil {
		t.Fatal(err)
	}
	s.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Host + r.URL.Path {
		case "github.com/login/oauth/access_token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("code_verifier") == "" || r.Form.Get("code") != "valid-code" {
				t.Fatal("missing PKCE verifier/code")
			}
			body = `{"access_token":"private-github-token","token_type":"bearer"}`
		case "api.github.com/user":
			if r.Header.Get("Authorization") != "Bearer private-github-token" {
				t.Fatal("missing token")
			}
			body = `{"id":1234,"login":"new-name"}`
		case "api.github.com/user/emails":
			body = `[{"email":"verified@example.test","verified":true,"primary":true}]`
		default:
			t.Fatalf("unexpected identity request: %s", r.URL.Host+r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest("GET", "/v1/auth/github/start?return=%2Fdevice%3Fcode%3DABC", nil))
	if start.Code != 302 {
		t.Fatal(start.Code, start.Body.String())
	}
	redirect, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if redirect.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("missing PKCE")
	}
	oauthCookie := start.Result().Cookies()[0]
	if !oauthCookie.Secure || !oauthCookie.HttpOnly || oauthCookie.Domain != "" || oauthCookie.Path != "/" {
		t.Fatal("insecure OAuth cookie")
	}
	callbackURL := "/v1/auth/github/callback?code=valid-code&state=" + redirect.Query().Get("state")
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest("GET", callbackURL, nil))
	if missing.Code != 400 {
		t.Fatal("accepted missing state cookie")
	}
	request := httptest.NewRequest("GET", callbackURL, nil)
	request.AddCookie(oauthCookie)
	callback := httptest.NewRecorder()
	handler.ServeHTTP(callback, request)
	if callback.Code != 303 || callback.Header().Get("Location") != "/device?code=ABC" {
		t.Fatal(callback.Code, callback.Body.String())
	}
	var session *http.Cookie
	for _, c := range callback.Result().Cookies() {
		if c.Name == s.cookieName("session") {
			session = c
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("insecure session")
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, request)
	if replay.Code != 400 {
		t.Fatal("accepted OAuth replay")
	}
	read := httptest.NewRequest("GET", "/v1/auth/session", nil)
	read.AddCookie(session)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, read)
	var current struct {
		Session struct {
			User struct {
				ID    string `json:"id"`
				Login string `json:"login"`
			} `json:"user"`
		} `json:"session"`
	}
	if err = json.Unmarshal(result.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Session.User.ID != existing {
		t.Fatal("provider identity did not preserve original owner")
	}
	if current.Session.User.Login != "new-name" {
		t.Fatal("session did not return the current GitHub username")
	}
	if strings.Contains(result.Body.String(), session.Value) || strings.Contains(result.Body.String(), "private-github-token") {
		t.Fatal("session exposed secret")
	}
	for _, origin := range []string{"", "https://evil.test"} {
		req := httptest.NewRequest("POST", "/v1/auth/logout", nil)
		req.AddCookie(session)
		req.Header.Set("Origin", origin)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 403 {
			t.Fatal("accepted cross-origin session mutation")
		}
	}
	req := httptest.NewRequest("POST", "/v1/auth/logout", nil)
	req.AddCookie(session)
	req.Header.Set("Origin", s.Config.PublicURL)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, read)
	if result.Body.String() != "{\"session\":null}\n" {
		t.Fatal("logout did not revoke session", result.Body.String())
	}
}

func TestProxyAddressCannotBeSpoofed(t *testing.T) {
	t.Setenv("MAX_REQUESTS_PER_IP_MINUTE", "600")
	s := New(nil, Config{TrustedProxy: "127.0.0.1"})
	for i := 0; i < 601; i++ {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = "203.0.113.5:1"
		r.Header.Set("X-Real-IP", "192.0.2."+string(rune(i)))
		allowed := s.allow(r)
		if i == 600 && allowed {
			t.Fatal("untrusted proxy header bypassed rate limit")
		}
	}
}

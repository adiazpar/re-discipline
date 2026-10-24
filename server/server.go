package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	OperatorID         string
	OperatorToken      string
	SetupOnly          bool
	LocalOnly          bool
	LocalOwnerID       string
	LocalOwnerToken    string
	JevEnabled         bool
	PublicURL          string
	SupabaseURL        string
	SupabaseKey        string
	GitHubClientID     string
	GitHubClientSecret string
	TrustedProxy       string
	OpsStatusDir       string
	WebDir             string
	CacheDir           string
	ReviewURL          string
	ReviewKey          string
}

type Server struct {
	archiveSlot    chan struct{}
	archiveNext    time.Time
	buildSlots     chan struct{}
	Semantic       *semantic.Client
	DB             *pgxpool.Pool
	Config         Config
	HTTP           *http.Client
	Authenticate   func(context.Context, string) (string, error)
	rateMu         sync.Mutex
	rates          map[string]*rateWindow
	searchMu       sync.Mutex
	searchStates   map[string]*searchState
	scheduler      *searchScheduler
	metadata       *metadataCache
	budget         SearchBudget
	requestSlots   chan struct{}
	ingressWaiters chan struct{}
	operatorSlots  chan struct{}
}
type rateWindow struct {
	Start time.Time
	Count int
}
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string       { return e.Message }
func fail(status int, msg string) error { return &apiError{status, msg} }
func New(db *pgxpool.Pool, c Config) *Server {
	s := &Server{DB: db, Config: c, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, rates: map[string]*rateWindow{}}
	s.searchStates = make(map[string]*searchState)
	s.budget = CurrentSearchBudget()
	s.scheduler = newSearchScheduler(s.budget)
	s.metadata = newMetadataCache(s.budget.MetadataBytes)
	s.buildSlots = make(chan struct{}, 1)
	s.archiveSlot = make(chan struct{}, 1)
	s.requestSlots = make(chan struct{}, s.budget.Ingress)
	s.ingressWaiters = make(chan struct{}, s.budget.Queue)
	s.operatorSlots = make(chan struct{}, 4)
	if c.JevEnabled && !c.SetupOnly && !c.LocalOnly {
		s.Semantic = semantic.New(semantic.Config{Enabled: true, DailyRequests: setting("MAX_DAILY_JEV_REQUESTS", 200)}, filepath.Join(c.CacheDir, "semantic"))
		s.Semantic.Reserve = func(ctx context.Context) error {
			tx, err := s.DB.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			if err = s.consume(ctx, tx, "00000000-0000-0000-0000-000000000000", "jev_request", setting("MAX_DAILY_JEV_REQUESTS", 200)); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
	}
	return s
}

// Legacy routes are retained only for ingestion-core regression tests and the
// old loopback owner mode. The deployed search service uses Handler in gateway.go.
func (s *Server) legacyHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := checkRecoveryHealth(s.Config.OpsStatusDir, time.Now()); err != nil {
			writeError(w, fail(503, err.Error()))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.DB.Ping(ctx); err != nil {
			writeError(w, fail(503, "database unavailable"))
			return
		}
		mode := "ready"
		if s.Config.SetupOnly {
			mode = "setup"
		} else if s.Config.LocalOnly {
			mode = "local"
		}
		writeJSON(w, 200, map[string]any{"ok": true, "protocol": kb.ProtocolVersion, "mode": mode, "community_enabled": !s.Config.SetupOnly})
	})
	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"supabase_url": s.Config.SupabaseURL, "supabase_publishable_key": s.Config.SupabaseKey, "github_auth": s.Config.GitHubClientID != "", "protocol": kb.ProtocolVersion})
	})
	if s.Config.GitHubClientID != "" {
		mux.HandleFunc("GET /v1/auth/github/start", s.githubStart)
		mux.HandleFunc("GET /v1/auth/github/callback", s.githubCallback)
		mux.HandleFunc("GET /v1/auth/session", s.browserSession)
		mux.HandleFunc("POST /v1/auth/logout", s.browserLogout)
	}
	mux.HandleFunc("POST /v1/operations", s.operation)
	mux.HandleFunc("POST /v1/auth/device/start", s.deviceStart)
	mux.HandleFunc("POST /v1/auth/device/poll", s.devicePoll)
	mux.HandleFunc("POST /v1/auth/device/approve", s.deviceApprove)
	if s.Config.WebDir != "" {
		fs := http.FileServer(http.Dir(s.Config.WebDir))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			p := filepath.Join(s.Config.WebDir, filepath.Clean("/"+r.URL.Path))
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(s.Config.WebDir, "index.html"))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if s.Config.SetupOnly && r.URL.Path != "/healthz" {
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, fail(503, "server setup only: community access is not enabled"))
			return
		}
		if s.Config.LocalOnly && r.URL.Path != "/healthz" {
			w.Header().Set("Cache-Control", "no-store")
			if r.URL.Path != "/v1/operations" {
				writeError(w, fail(404, "local owner mode exposes community operations only"))
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != s.Config.PublicURL {
				writeError(w, fail(403, "browser request origin does not match this service"))
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && r.Header.Get("Authorization") == "" {
			if _, err := r.Cookie(s.cookieName("session")); err == nil && r.Header.Get("Origin") != s.Config.PublicURL {
				writeError(w, fail(403, "browser request origin does not match this service"))
				return
			}
		}
		if !s.allow(r) {
			writeError(w, fail(429, "request limit reached; retry in one minute"))
			return
		}
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("request panic", "error", rec)
				writeError(w, fail(500, "internal error"))
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) allow(r *http.Request) bool {
	limit := setting("MAX_REQUESTS_PER_IP_MINUTE", 6000)
	namespace := "legacy/"
	if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
		namespace += "auth/"
		limit = 60
	}
	return s.allowIP(r, namespace, limit)
}
func (s *Server) allowUntrusted(r *http.Request) bool {
	return s.allowIP(r, "untrusted/", setting("MAX_UNAUTHENTICATED_REQUESTS_PER_IP_MINUTE", 600))
}
func (s *Server) allowIP(r *http.Request, namespace string, limit int) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	// Only the configured proxy may supply this header; the proxy overwrites it.
	if host == s.Config.TrustedProxy && host != "" {
		if ip := net.ParseIP(r.Header.Get("X-Real-IP")); ip != nil {
			host = ip.String()
		}
	}
	host = namespace + host
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	now := time.Now()
	v := s.rates[host]
	if v == nil && len(s.rates) >= 10000 {
		for k, entry := range s.rates {
			if now.Sub(entry.Start) > time.Minute {
				delete(s.rates, k)
			}
		}
		if len(s.rates) >= 10000 {
			return false
		}
	}
	if v == nil || now.Sub(v.Start) > time.Minute {
		v = &rateWindow{Start: now}
		s.rates[host] = v
	}
	v.Count++
	return v.Count <= limit
}
func decode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fail(400, "invalid request: "+err.Error())
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fail(400, "request must contain one JSON object")
	}
	return nil
}
func data(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	return decode(strings.NewReader(string(raw)), v)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	status := 500
	msg := "request failed"
	var ae *apiError
	var pe *pgconn.PgError
	if errors.As(err, &ae) {
		status = ae.Status
		msg = ae.Message
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		status = 503
		msg = "search deadline reached; retry shortly"
		w.Header().Set("Retry-After", "1")
	} else if errors.Is(err, pgx.ErrNoRows) {
		status = 404
		msg = "resource unavailable"
	} else if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			status = 409
			msg = "resource already exists"
			if pe.ConstraintName == "one_active_archive" {
				msg = "another archive is active; finish or cancel it first"
			}
		case "22P02":
			status = 400
			msg = "invalid identifier"
		case "42501":
			status = 403
			msg = "permission denied"
		case "55006":
			status = 423
			msg = "community pending deletion or confirmation required"
		case "40001", "40P01":
			status = 409
			msg = "concurrent change; retry the operation"
		}
	}
	if status == 500 {
		slog.Error("request failed", "error", err)
	}
	writeJSON(w, status, map[string]any{"error": msg})
}
func (s *Server) begin(ctx context.Context, user string) (pgx.Tx, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE re_community_app`); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.user_id',$1,true)`, user); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if p, ok := ctx.Value(principalKey{}).(principal); ok && !p.operator {
		if _, err = tx.Exec(ctx, `SELECT set_config('app.read_token',$1,true)`, p.tokenID); err != nil {
			tx.Rollback(ctx)
			return nil, err
		}
	}
	return tx, nil
}
func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	var req kb.Request
	if err := decode(r.Body, &req); err != nil {
		writeError(w, err)
		return
	}
	user, err := s.identity(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if s.Config.LocalOnly && !localOperation(req.Action) {
		writeError(w, fail(403, "operation is not enabled in local owner mode"))
		return
	}
	if req.Community == "" {
		switch req.Action {
		case "community.create", "community.list", "invite.redeem", "token.list", "token.revoke":
		default:
			writeError(w, fail(400, "select a community UUID or slug for this operation"))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var result any
	// One public route accepts one or many prepared items. The legacy batch name
	// remains wire-compatible; chunking never becomes a publication mode.
	if req.Action == "submission.create" {
		var shape map[string]json.RawMessage
		if e := json.Unmarshal(req.Data, &shape); e == nil && shape["items"] != nil {
			req.Action = "submission.batch"
		}
	}
	switch req.Action {
	case "submission.batch":
		result, err = s.submitBatch(ctx, user, req.Community, req.Data)
	case "invite.redeem":
		result, err = s.redeem(ctx, user, req.Data)
	case "token.list", "token.revoke":
		result, err = s.tokens(ctx, user, req)
	default:
		tx, e := s.begin(ctx, user)
		if e != nil {
			writeError(w, e)
			return
		}
		defer tx.Rollback(ctx)
		// Cache invalidation must use the resolved ID even when the request used
		// a slug. Resolve under the same permissions as the mutation.
		if req.Action == "finding.withdraw" {
			l, e := library(ctx, tx, req.Community)
			if e != nil {
				writeError(w, e)
				return
			}
			req.Community = l.ID
			release, lockErr := s.lockIndex(ctx, l.ID, true)
			if lockErr != nil {
				writeError(w, lockErr)
				return
			}
			defer release()
		}
		result, err = s.dispatch(ctx, tx, user, req)
		if err == nil {
			err = tx.Commit(ctx)
		}
	}
	if err != nil {
		if cause := context.Cause(ctx); cause == errCommunityDeleted || cause == errCommunityLocked {
			err = cause
		}
		writeError(w, err)
		return
	}
	if req.Action == "member.list" {
		if err = s.addMemberLogins(ctx, result.([]map[string]string)); err != nil {
			writeError(w, err)
			return
		}
	}

	if req.Action == "finding.withdraw" {
		if err = validUUID(req.Community); err == nil {
			err = os.RemoveAll(filepath.Join(s.Config.CacheDir, req.Community))
		}
		if err == nil && s.Semantic != nil {
			err = s.Semantic.Purge(ctx)
		}
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if cause := context.Cause(ctx); cause == errCommunityDeleted || cause == errCommunityLocked {
		writeError(w, cause)
		return
	}
	writeJSON(w, 200, result)
}
func authenticated(user string) error {
	if user == "" {
		return fail(401, "sign in to continue")
	}
	return nil
}
func role(ctx context.Context, tx pgx.Tx, c, user string, allowed ...string) error {
	if err := authenticated(user); err != nil {
		return err
	}
	var got string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(community.member_role($1),'')`, c).Scan(&got); err != nil {
		return err
	}
	for _, want := range allowed {
		if got == want {
			return nil
		}
	}
	return fail(403, "community role does not permit this operation")
}
func audit(ctx context.Context, tx pgx.Tx, c, user, action string, detail any) error {
	b, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, `INSERT INTO community.audit(community_id,actor_id,action,detail) VALUES($1,$2,$3,$4)`, c, user, action, b)
	return err
}
func id() string       { return uuid.NewString() }
func raw(v any) []byte { b, _ := json.Marshal(v); return b }
func library(ctx context.Context, tx pgx.Tx, c string) (kb.Library, error) {
	var l kb.Library
	var p []byte
	var frozen *string
	err := tx.QueryRow(ctx, `SELECT id::text,name,slug,visibility,policy,policy_version,sequence,COALESCE(community.member_role(id),''),deletion_id::text FROM community.libraries WHERE id::text=$1 OR slug=$1`, c).Scan(&l.ID, &l.Name, &l.Slug, &l.Visibility, &p, &l.PolicyVersion, &l.Sequence, &l.Role, &frozen)
	if err == nil && frozen != nil {
		return l, errCommunityLocked
	}
	if err == nil {
		err = json.Unmarshal(p, &l.Policy)
		if reader, ok := ctx.Value(principalKey{}).(principal); ok && !reader.operator {
			l.Role = "reader"
		}
	}
	return l, err
}
func readSubmission(ctx context.Context, tx pgx.Tx, c, sid string) (kb.Submission, error) {
	var v kb.Submission
	var d, ch []byte
	var created time.Time
	err := tx.QueryRow(ctx, `SELECT id::text,community_id::text,author_id::text,state,digest,document,checks,policy_version,created_at,finding_id,accepted_revision,base_revision,identity_review,resolution_version,contribution_target,contribution_revision FROM community.submissions WHERE community_id=$1 AND id=$2`, c, sid).Scan(&v.ID, &v.CommunityID, &v.Author, &v.State, &v.Digest, &d, &ch, &v.PolicyVersion, &created, &v.FindingID, &v.Revision, &v.BaseRevision, &v.IdentityReview, &v.ResolutionVersion, &v.ContributionTarget, &v.ContributionRevision)
	if err == nil {
		err = json.Unmarshal(d, &v.Document)
		json.Unmarshal(ch, &v.Checks)
		v.CreatedAt = created.UTC().Format(time.RFC3339)
	}
	return v, err
}
func validUUID(v string) error {
	if _, err := uuid.Parse(v); err != nil {
		return fail(400, fmt.Sprintf("invalid UUID: %s", v))
	}
	return nil
}

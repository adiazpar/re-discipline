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
	OperatorID      string
	OperatorToken   string
	SetupOnly       bool
	LocalOnly       bool
	LocalOwnerID    string
	LocalOwnerToken string
	JevEnabled      bool
	PublicURL       string
	TrustedProxy    string
	OpsStatusDir    string
	CacheDir        string
}

type Server struct {
	indexTurn       int
	uploadScheduler *searchScheduler
	archiveSlot     chan struct{}
	archiveNext     time.Time
	buildSlots      chan struct{}
	Semantic        *semantic.Client
	DB              *pgxpool.Pool
	Config          Config
	HTTP            *http.Client
	rateMu          sync.Mutex
	rates           map[string]*rateWindow
	searchMu        sync.Mutex
	searchStates    map[string]*searchState
	scheduler       *searchScheduler
	budget          SearchBudget
	requestSlots    chan struct{}
	ingressWaiters  chan struct{}
	operatorSlots   chan struct{}
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
	if c.LocalOnly {
		c.OperatorID, c.OperatorToken = c.LocalOwnerID, c.LocalOwnerToken
	}
	s := &Server{DB: db, Config: c, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, rates: map[string]*rateWindow{}}
	s.searchStates = make(map[string]*searchState)
	s.budget = CurrentSearchBudget()
	s.scheduler = newSearchScheduler(s.budget)
	writeBudget := s.budget
	writeBudget.Workers = max(1, min(s.budget.CPUs/2, int(s.budget.Memory/(128*MiB)), 4))
	writeBudget.Queue = 16
	writeBudget.QueueBytes = max(8*MiB, s.budget.Memory/16)
	s.uploadScheduler = newSearchScheduler(writeBudget)
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
	var req kb.Request
	if err := decode(r.Body, &req); err != nil {
		writeError(w, err)
		return
	}
	p, ok := r.Context().Value(principalKey{}).(principal)
	if !ok {
		writeError(w, fail(401, "verified operator or reader required"))
		return
	}
	ctx := r.Context()
	tx, err := s.begin(ctx, p.userID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='3s'`); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.dispatch(ctx, tx, p.userID, req)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, err)
		return
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
func validUUID(v string) error {
	if _, err := uuid.Parse(v); err != nil {
		return fail(400, fmt.Sprintf("invalid UUID: %s", v))
	}
	return nil
}

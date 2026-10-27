package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/buildinfo"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/connectionauth"
)

type principalKey struct{}
type principal struct {
	operator                     bool
	userID, tokenID, communityID string
	proof                        connectionauth.Proof
	stats                        kb.CommunityStats
}

func ValidateOperator(c Config) error {
	if validUUID(c.OperatorID) != nil || len(c.OperatorToken) < 43 {
		return errors.New("SERVER_OPERATOR_ID and a random SERVER_OPERATOR_TOKEN are required")
	}
	return nil
}

// Handler is the operator-owned, remote-only search surface. Legacy account,
// membership, browser and synchronization routes are deliberately not mounted.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == "GET" && r.URL.Path == "/healthz" {
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
			if s.Config.SetupOnly {
				writeJSON(w, 200, map[string]any{"ok": true, "protocol": kb.ProtocolVersion, "mode": "setup", "community_enabled": false, "software": buildinfo.Current()})
				return
			}
			if err := ValidateOperator(s.Config); err != nil {
				writeError(w, fail(503, "operator is not configured"))
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "protocol": kb.ProtocolVersion, "mode": "ready", "community_enabled": true, "remote_only": true, "software": buildinfo.Current()})
			return
		}
		if s.Config.SetupOnly {
			writeError(w, fail(503, "server setup only"))
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/operations" {
			writeError(w, fail(404, "route unavailable"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.Config.PublicURL {
			writeError(w, fail(403, "request origin does not match this service"))
			return
		}
		if ValidateOperator(s.Config) != nil {
			writeError(w, fail(503, "operator is not configured"))
			return
		}
		operatorRequest := subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Config.OperatorToken)) == 1
		slots := s.requestSlots
		if operatorRequest {
			slots = s.operatorSlots
		}
		var ingressOnce sync.Once
		releaseIngress := func() { ingressOnce.Do(func() { <-slots }) }
		select {
		case slots <- struct{}{}:
		default:
			select {
			case s.ingressWaiters <- struct{}{}:
			default:
				w.Header().Set("Retry-After", "1")
				writeError(w, fail(503, "server admission queue full; retry shortly"))
				return
			}
			timer := time.NewTimer(time.Second)
			acquired := false
			select {
			case slots <- struct{}{}:
				acquired = true
			case <-r.Context().Done():
			case <-timer.C:
			}
			timer.Stop()
			<-s.ingressWaiters
			if !acquired {
				w.Header().Set("Retry-After", "1")
				writeError(w, fail(503, "server admission wait expired; retry shortly"))
				return
			}
		}
		defer releaseIngress()

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, fail(413, "request too large or incomplete"))
			return
		}
		var req kb.Request
		if err := decode(bytes.NewReader(body), &req); err != nil {
			writeError(w, err)
			return
		}
		if req.Action == "community.stats" {
			if len(body) > 4096 || data(req.Data, &struct{}{}) != nil {
				writeError(w, fail(400, "community.stats accepts an empty payload"))
				return
			}
			// Bound even authentication work separately from searches. Metadata
			// waiters must not occupy normal ingress or retrieval execution slots.
			releaseIngress()
			release, err := s.admitStats(ctx)
			if err != nil {
				w.Header().Set("Retry-After", "5")
				writeError(w, err)
				return
			}
			defer release()
			var stopStats context.CancelFunc
			ctx, stopStats = context.WithTimeout(ctx, time.Second)
			defer stopStats()
			r = r.WithContext(ctx)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		p, err := s.readerIdentity(r, req)
		// Anonymous handshakes and invalid credentials have a separate abuse
		// bucket. Valid readers behind the same NAT are never charged to it.
		if !p.operator && p.tokenID == "" && !s.allowUntrusted(r) {
			w.Header().Set("Retry-After", "60")
			writeError(w, fail(429, "connection setup request limit reached"))
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		if !p.operator && p.proof.PublicKey != "" {
			if err := s.consumeProof(ctx, p.proof); err != nil {
				writeError(w, err)
				return
			}
		}
		if req.Action == "community.leave" {
			result, err := s.leaveReader(ctx, p, req)
			if err != nil {
				writeError(w, err)
			} else {
				writeJSON(w, 200, result)
			}
			return
		}
		if req.Action == "community.connect" {
			result, err := s.connectReader(ctx, p, req)
			if err != nil {
				writeError(w, err)
			} else {
				writeJSON(w, 200, result)
			}
			return
		}
		if !p.operator {
			if p.tokenID == "" {
				writeError(w, fail(401, "connect to this community first"))
				return
			}
			if !readerAction(req.Action) {
				writeError(w, fail(403, "this connection is read-only"))
				return
			}
			// UUIDs are the wire identity after connecting; a reader may never select a
			// different community, even one that is public.
			if req.Community != p.communityID {
				writeError(w, fail(404, "resource unavailable"))
				return
			}
		} else if !operatorAction(req.Action) {
			writeError(w, fail(400, "operation is not supported by the remote search service"))
			return
		}
		ctx = context.WithValue(ctx, principalKey{}, p)
		if req.Action == "community.archive.chunk" {
			s.archiveChunk(w, r.WithContext(ctx), p.userID, req)
			return
		}
		if req.Action == "community.delete" {
			result, err := s.deletionOperation(ctx, p.userID, req)
			if err != nil {
				writeError(w, err)
			} else {
				writeJSON(w, 200, result)
			}
			return
		}
		if req.Action == "server.stats" && p.operator {
			stats := s.scheduler.stats()
			stats["uploads"] = s.uploadScheduler.stats()
			writeJSON(w, 200, stats)
			return
		}
		if req.Community != "" && req.Action != "community.create" {
			if p.operator {
				tx, e := s.begin(ctx, p.userID)
				if e != nil {
					writeError(w, e)
					return
				}
				l, e := library(ctx, tx, req.Community)
				tx.Rollback(ctx)
				if e != nil {
					writeError(w, e)
					return
				}
				req.Community = l.ID
			}
			var done func()
			ctx, done = s.trackCommunityWork(ctx, req.Community)
			defer done()
		}
		if strings.HasPrefix(req.Action, "access.") {
			result, err := s.manageAccess(ctx, req)
			if err != nil {
				writeError(w, err)
			} else {
				writeJSON(w, 200, result)
			}
			return
		}
		if req.Action == "community.stats" {
			result, err := s.communityStats(ctx, p, req.Community)
			if err != nil {
				writeError(w, err)
			} else if cause := context.Cause(ctx); cause != nil {
				writeError(w, cause)
			} else {
				writeJSON(w, 200, result)
			}
			return
		}
		if readerAction(req.Action) {
			if req.Action == "query" {
				if _, err := parseSearch(req.Data); err != nil {
					writeError(w, err)
					return
				}
			}
			// Waiting searches hold neither an ingress slot nor a DB connection.
			releaseIngress()
			readerID := p.tokenID
			if p.operator {
				readerID = "operator"
			}
			release, wait, err := s.scheduler.acquire(ctx, req.Community, readerID, len(req.Data))
			if err != nil {
				if cause := context.Cause(ctx); cause == errCommunityDeleted || cause == errCommunityLocked {
					err = cause
				} else {
					w.Header().Set("Retry-After", "1")
				}
				writeError(w, err)
				return
			}
			defer release()
			w.Header().Set("Server-Timing", fmt.Sprintf("queue;dur=%.3f", float64(wait.Microseconds())/1000))
			var cancelExecution context.CancelFunc
			ctx, cancelExecution = context.WithTimeout(ctx, s.budget.Execution)
			defer cancelExecution()
			if deadline, ok := ctx.Deadline(); ok {
				_ = http.NewResponseController(w).SetWriteDeadline(deadline)
			}
		}
		if req.Action == "knowledge.put" || req.Action == "knowledge.delete" {
			releaseIngress()
			release, _, err := s.uploadScheduler.acquire(ctx, req.Community, "operator", len(body))
			if err != nil {
				w.Header().Set("Retry-After", "1")
				writeError(w, err)
				return
			}
			defer release()
			if s.scheduler.stats()["queued"].(int) > 0 {
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, ioBody{bytes.NewReader(raw(req))}, 2*1024*1024)
		s.operation(w, r)
	})
}

type ioBody struct{ *bytes.Reader }

func (ioBody) Close() error { return nil }

func readerAction(a string) bool {
	switch a {
	case "community.get", "query", "knowledge.get", "community.stats":
		return true
	}
	return false
}
func operatorAction(a string) bool {
	if readerAction(a) {
		return true
	}
	switch a {
	case "server.stats", "community.create", "community.list", "community.update", "community.delete", "community.archive.chunk", "access.create", "access.list", "access.revoke", "usage", "audit.list",
		"knowledge.inventory", "knowledge.put", "knowledge.delete":
		return true
	}
	return false
}

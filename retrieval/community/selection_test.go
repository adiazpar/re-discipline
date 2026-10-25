package community

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func TestQuerySelectsCommunitiesBeforeAnyTraffic(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("RE_DISCIPLINE_SERVICE", "")
	t.Setenv("RE_DISCIPLINE_TOKEN", "")
	var doom, godzilla atomic.Int32
	newHost := func(count *atomic.Int32, token string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("request used another origin's credential")
			}
			count.Add(1)
			json.NewEncoder(w).Encode(QueryResult{Hits: []Result{{FindingID: "fixture", Fingerprint: "1", Title: "Fixture"}}})
		}))
	}
	firstToken, secondToken := strings.Repeat("a", 43), strings.Repeat("b", 43)
	a, b := newHost(&doom, firstToken), newHost(&godzilla, secondToken)
	defer a.Close()
	defer b.Close()
	for origin, token := range map[string]string{a.URL: firstToken, b.URL: secondToken} {
		digest := sha256.Sum256([]byte(origin))
		path := filepath.Join(home, ".re-discipline", "servers", "credentials", hex.EncodeToString(digest[:])+".json")
		if err := atomicJSON(path, LocalOwnerCredential{Service: origin, Token: token}); err != nil {
			t.Fatal(err)
		}
	}
	connections := []Connection{{Alias: "doom", Service: a.URL, CommunityID: "doom"}, {Alias: "godzilla", Service: b.URL, CommunityID: "godzilla"}}
	if err := SaveSettings(root, Settings{Mode: "external", Connections: connections}); err != nil {
		t.Fatal(err)
	}
	if _, err := Query(context.Background(), root, "fixture", "external", false, engine.Options{Communities: []string{"doom", "unknown"}, Assistance: "off"}); err == nil {
		t.Fatal("unknown selection allowed")
	}
	if doom.Load() != 0 || godzilla.Load() != 0 {
		t.Fatal("invalid selection caused traffic")
	}
	out, err := Query(context.Background(), root, "fixture", "external", false, engine.Options{Communities: []string{"doom", "doom"}, Assistance: "off"})
	if err != nil || len(out.Hits) != 1 || out.Hits[0].CommunityID != "doom" || doom.Load() != 1 || godzilla.Load() != 0 {
		t.Fatalf("subset: %+v %v", out, err)
	}
	out, err = Query(context.Background(), root, "fixture", "external", false, engine.Options{Assistance: "off"})
	if err != nil || len(out.Hits) != 2 || doom.Load() != 2 || godzilla.Load() != 1 {
		t.Fatalf("all linked: %+v %v", out, err)
	}
	if _, err := Execute(context.Background(), root, Command{Action: "knowledge.upload"}); err == nil {
		t.Fatal("ambiguous upload destination accepted")
	}

	if _, err := Execute(context.Background(), root, Command{Action: "query", Alias: "doom", Service: b.URL}); err == nil {
		t.Fatal("conflicting destination silently overridden")
	}
}

func TestRemoteQueriesBoundConcurrencyAndKeepSuccessfulHosts(t *testing.T) {
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var active, peak atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(QueryResult{Hits: []Result{{FindingID: "fixture"}}})
	}))
	defer h.Close()
	connections := []Connection{}
	for i := 0; i < 8; i++ {
		connections = append(connections, Connection{Alias: fmt.Sprint(i), Service: h.URL, CommunityID: fmt.Sprint(i)})
	}
	done := make(chan []remoteResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { done <- remoteQueries(ctx, connections, "fixture", engine.Options{}) }()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			close(release)
			t.Fatal("workers did not start")
		}
	}
	close(release)
	results := <-done
	if peak.Load() != 4 {
		t.Fatalf("peak concurrency: %d", peak.Load())
	}
	for _, r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
	}
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(QueryResult{Hits: []Result{{FindingID: "fast"}}})
	}))
	defer fast.Close()
	slowRelease := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-slowRelease:
		}
	}))
	defer func() { close(slowRelease); slow.Close() }()
	ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	results = remoteQueries(ctx, []Connection{{Service: slow.URL}, {Service: fast.URL}}, "fixture", engine.Options{})
	if results[0].err == nil || results[1].err != nil || len(results[1].result.Hits) != 1 {
		t.Fatalf("partial results: %+v", results)
	}
}

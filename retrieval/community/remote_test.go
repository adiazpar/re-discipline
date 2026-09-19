package community

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/google/uuid"
)

func TestRemoteQuerySupportsOlderStrictServices(t *testing.T) {
	calls := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		calls++
		if strings.Contains(string(req.Data), `"assistance"`) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": `json: unknown field "assistance"`})
			return
		}
		json.NewEncoder(w).Encode(QueryResult{Hits: []Result{{FindingID: "one", Title: "Clock"}}})
	}))
	defer h.Close()
	result, err := RemoteQuery(context.Background(), Connection{Service: h.URL, Alias: "older"}, "clock", engine.Options{Limit: 8})
	if err != nil || calls != 2 || len(result.Hits) != 1 {
		t.Fatalf("old service query failed: %+v %v (%d calls)", result, err, calls)
	}
}

func TestRemoteConnectQueryAndOfflineNeverSynchronize(t *testing.T) {
	root := t.TempDir()
	cid, fid, rid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	var mu sync.Mutex
	calls := []string{}
	status := 200
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, req.Action)
		code := status
		mu.Unlock()
		switch req.Action {
		case "community.get":
			json.NewEncoder(w).Encode(Library{ID: cid, Slug: "doom", Name: "DOOM"})
		case "finding.match":
			json.NewEncoder(w).Encode([]FindingMatch{})
		case "query":
			if code != 200 {
				w.WriteHeader(code)
				json.NewEncoder(w).Encode(map[string]string{"error": "unavailable"})
				return
			}
			var args struct {
				Query       string
				Limit       int
				Kind, Grade string
			}
			json.Unmarshal(req.Data, &args)
			if req.Community != cid || args.Query != "timescale" || args.Limit < 2 || args.Limit > 128 || args.Kind != "fact" || args.Grade != "direct" {
				t.Errorf("query payload: %+v %+v", req, args)
			}
			json.NewEncoder(w).Encode(QueryResult{Hits: []Result{{FindingID: fid, Revision: rid, Title: "Remote timescale", Snippet: "Remote evidence", Kind: "fact", Grade: "direct", Contributions: []Result{{FindingID: uuid.NewString(), Revision: uuid.NewString(), Title: "Independent observation"}}}}})
		default:
			t.Errorf("unexpected full-library operation %s", req.Action)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer h.Close()
	if _, err := Execute(context.Background(), root, Command{Action: "connect", Service: h.URL, Community: "doom", Alias: "doom"}); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadSettings(root)
	if err != nil || settings.Mode != "both" || settings.Retrieval != "remote" {
		t.Fatalf("settings: %+v %v", settings, err)
	}
	docs := filepath.Join(root, ".re-discipline", "docs")
	os.MkdirAll(docs, 0700)
	os.WriteFile(filepath.Join(docs, "time.md"), []byte(portable().Markdown), 0600)
	opts := engine.Options{Limit: 2, Kind: "fact", Grade: "direct"}
	result, err := Query(context.Background(), root, "timescale", "", false, opts)
	if err != nil || len(result.Hits) != 2 {
		t.Fatalf("combined query: %+v %v", result, err)
	}
	if result.Hits[0].Source != "local" || result.Hits[1].Source != "doom" || result.Hits[1].Service != h.URL || result.Hits[1].Contributions[0].Source != "doom" {
		t.Fatalf("source attribution: %+v", result.Hits)
	}
	if _, err = os.Stat(cacheRoot(root, settings.Connections[0])); !os.IsNotExist(err) {
		t.Fatalf("remote query created a KB cache: %v", err)
	}
	if _, err = Execute(context.Background(), root, Command{Action: "sync", Alias: "doom"}); err == nil {
		t.Fatal("sync bypassed remote preference")
	}
	result, err = Query(context.Background(), root, "timescale", "external", true, opts)
	if err != nil || len(result.Hits) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("offline remote: %+v %v", result, err)
	}
	result, err = Query(context.Background(), root, "timescale", "local", false, opts)
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("local query: %+v %v", result, err)
	}
	mu.Lock()
	before := len(calls)
	mu.Unlock()
	if before != 3 {
		t.Fatalf("offline/local/sync attempted network access: %d", before)
	}
	for _, code := range []int{403, 503} {
		mu.Lock()
		status = code
		mu.Unlock()
		result, err = Query(context.Background(), root, "timescale", "external", false, opts)
		if err != nil || len(result.Hits) != 0 || len(result.Warnings) != 1 || result.Sources[0]["available"] != false {
			t.Fatalf("failed remote query: %+v %v", result, err)
		}
	}
	if _, err = os.Stat(cacheRoot(root, settings.Connections[0])); !os.IsNotExist(err) {
		t.Fatal("failure created cache")
	}
}

func TestRetrievalPreferenceMigrationAndValidation(t *testing.T) {
	root := t.TempDir()
	if err := atomicJSON(SettingsPath(root), map[string]any{"mode": "external", "connections": []Connection{}}); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(root)
	if err != nil || s.Retrieval != "remote" {
		t.Fatalf("legacy settings: %+v %v", s, err)
	}
	for _, choice := range []string{"remote", "sync"} {
		if _, err = Execute(context.Background(), root, Command{Action: "retrieval.set", Retrieval: choice}); err != nil {
			t.Fatal(err)
		}
		s, err = LoadSettings(root)
		if err != nil || s.Retrieval != choice || s.Mode != "external" {
			t.Fatalf("preference not persisted independently: %+v %v", s, err)
		}
	}
	for _, choice := range []string{"", "invalid"} {
		if _, err = Execute(context.Background(), root, Command{Action: "retrieval.set", Retrieval: choice}); err == nil {
			t.Fatalf("accepted invalid preference %q", choice)
		}
	}
	if _, err = Execute(context.Background(), root, Command{Action: "connect", Service: "https://invalid.example", Community: "doom", Retrieval: "invalid"}); err == nil {
		t.Fatal("invalid connect preference accepted")
	}
	s, err = LoadSettings(root)
	if err != nil || s.Retrieval != "sync" || len(s.Connections) != 0 {
		t.Fatalf("failed action changed settings: %+v %v", s, err)
	}
}

func TestExplicitSyncConnectStillSynchronizes(t *testing.T) {
	root := t.TempDir()
	cid := uuid.NewString()
	var mu sync.Mutex
	actions := map[string]int{}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		actions[req.Action]++
		mu.Unlock()
		switch req.Action {
		case "community.get":
			json.NewEncoder(w).Encode(Library{ID: cid, Slug: "doom"})
		case "changes":
			json.NewEncoder(w).Encode(Changes{Library: Library{ID: cid}, Changes: []Change{}})
		case "finding.relations":
			json.NewEncoder(w).Encode([]Relation{})
		default:
			t.Errorf("unexpected action %s", req.Action)
		}
	}))
	defer h.Close()
	if _, err := Execute(context.Background(), root, Command{Action: "connect", Service: h.URL, Community: "doom", Alias: "doom", Retrieval: "sync"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	changes := actions["changes"]
	mu.Unlock()
	if changes != 1 {
		t.Fatalf("explicit sync connect skipped synchronization: %d", changes)
	}
	settings, err := LoadSettings(root)
	if err != nil || settings.Retrieval != "sync" {
		t.Fatalf("default transport: %+v %v", settings, err)
	}
	if _, err = os.Stat(filepath.Join(cacheRoot(root, settings.Connections[0]), "cache.db")); err != nil {
		t.Fatal(err)
	}
}

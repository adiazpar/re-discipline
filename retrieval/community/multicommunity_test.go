package community

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/connectionauth"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/zalando/go-keyring"
)

func TestMultipleCommunitiesResolveGlobally(t *testing.T) {
	cases := []struct {
		name, local   string
		remote, bases []string
		want, reason  string
		variants      int
	}{
		{"identical", "probe original", []string{"probe original", "probe original", "probe original"}, nil, "probe original", "", 0},
		{"local corrected", "probe corrected", []string{"probe original", "probe original", "probe original"}, []string{"probe original", "probe original", "probe original"}, "probe corrected", "local_changed", 0},
		{"single remote update", "probe original", []string{"probe original", "probe corrected", "probe original"}, []string{"probe original", "probe original", "probe original"}, "probe corrected", "remote_changed", 0},
		{"same remote update twice", "probe original", []string{"probe corrected", "probe corrected", "probe original"}, []string{"probe original", "probe original", "probe original"}, "probe corrected", "remote_changed", 0},
		{"competing updates", "probe original", []string{"probe correction A", "probe correction B", "probe original"}, []string{"probe original", "probe original", "probe original"}, "probe original", "local_preferred_diverged", 0},
		{"different baselines", "probe original", []string{"probe old", "probe corrected", "probe original"}, []string{"probe old", "probe original", "probe original"}, "probe corrected", "remote_changed", 0},
		{"contradictory link lineage", "probe local", []string{"probe other", "probe other", "probe local"}, []string{"probe other", "probe local", "probe local"}, "probe local", "local_preferred_diverged", 0},
		{"equal mirror carries contrary base", "probe local", []string{"probe local", "probe other", "probe local"}, []string{"probe other", "probe local", "probe local"}, "probe local", "local_preferred_diverged", 0},
		{"unknown lineage", "probe local", []string{"probe original", "probe correction", "probe third"}, nil, "probe local", "local_preferred_no_baseline", 0},
		{"obsolete remote matches", "corrected terminology", []string{"probe original", "probe original", "probe original"}, []string{"probe original", "probe original", "probe original"}, "", "", 0},
		{"obsolete local and mirror", "probe original", []string{"probe original", "corrected terminology", "probe original"}, []string{"probe original", "probe original", "probe original"}, "", "", 0},
		{"no local identical", "", []string{"probe original", "probe original", "probe original"}, nil, "probe original", "", 0},
		{"no local divergent", "", []string{"probe original", "probe corrected", "probe original"}, nil, "", "community_variants", 2},
		{"nonmatching remote variant", "", []string{"probe original", "corrected terminology", "probe original"}, nil, "", "community_variants", 2},
	}
	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".re-discipline", "docs")
			os.MkdirAll(dir, 0700)
			if tc.local != "" {
				os.WriteFile(filepath.Join(dir, "clock.md"), []byte(copyMarkdown(copyID, tc.local, "local evidence")), 0600)
			}
			if _, _, err := engine.Query(root, "probe", engine.Options{}); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			calls := map[string]int{}
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req Request
				json.NewDecoder(r.Body).Decode(&req)
				if req.Action != "query" {
					t.Error("unexpected document download", req.Action)
					w.WriteHeader(400)
					return
				}
				var p struct {
					Query      string
					CompareIDs []string `json:"compare_ids"`
				}
				json.Unmarshal(req.Data, &p)
				var n int
				fmt.Sscanf(req.Community, "community-%d", &n)
				mu.Lock()
				calls[req.Community]++
				mu.Unlock()
				d, _ := PrepareKnowledge("docs/clock.md", copyMarkdown(copyID, tc.remote[n], ""))
				out := QueryResult{ComparisonComplete: true}
				if strings.Contains(tc.remote[n], p.Query) {
					out.Hits = []Result{{FindingID: copyID, Fingerprint: d.Fingerprint, Sequence: int64(n + 2), Title: "Clock", Snippet: tc.remote[n], Kind: "fact", KnowledgeUpdatedAt: fmt.Sprintf("209%d-01-01T00:00:00Z", n)}}
				}
				for _, id := range p.CompareIDs {
					if id == copyID {
						out.Compared = append(out.Compared, KnowledgeEntry{ID: id, Fingerprint: d.Fingerprint, Sequence: int64(n + 2)})
					}
				}
				json.NewEncoder(w).Encode(out)
			}))
			defer host.Close()
			conns := make([]Connection, 3)
			for i := range conns {
				conns[i] = Connection{Alias: fmt.Sprintf("link-%d", i), Service: host.URL, CommunityID: fmt.Sprintf("community-%d", i)}
				if tc.bases != nil {
					d, _ := PrepareKnowledge("docs/clock.md", copyMarkdown(copyID, tc.bases[i], ""))
					if err := rememberCopies(context.Background(), root, conns[i], []KnowledgeEntry{{ID: copyID, Fingerprint: d.Fingerprint, Sequence: 1}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			var original []Result
			for _, order := range orders {
				if tc.bases != nil {
					db, e := sql.Open("sqlite", filepath.Join(root, ".re-discipline", "index.db"))
					if e != nil {
						t.Fatal(e)
					}
					if _, e = db.Exec("DELETE FROM copy_bases"); e != nil {
						t.Fatal(e)
					}
					db.Close()
					for i, base := range tc.bases {
						d, _ := PrepareKnowledge("docs/clock.md", copyMarkdown(copyID, base, ""))
						if e := rememberCopies(context.Background(), root, conns[i], []KnowledgeEntry{{ID: copyID, Fingerprint: d.Fingerprint, Sequence: 1}}); e != nil {
							t.Fatal(e)
						}
					}
				}
				settings := Settings{Mode: "both"}
				for _, i := range order {
					settings.Connections = append(settings.Connections, conns[i])
				}
				if err := SaveSettings(root, settings); err != nil {
					t.Fatal(err)
				}
				out, err := Query(context.Background(), root, "probe", "both", false, engine.Options{Limit: 1, Assistance: "off"})
				if err != nil || len(out.Warnings) != 0 {
					t.Fatalf("query failed: %+v %v", out, err)
				}
				if tc.want == "" && tc.variants == 0 {
					if len(out.Hits) != 0 {
						t.Fatal("obsolete match survived", out.Hits)
					}
					continue
				}
				if len(out.Hits) != 1 {
					t.Fatalf("finding did not occupy one slot: %+v", out.Hits)
				}
				again, e := Query(context.Background(), root, "probe", "both", false, engine.Options{Limit: 1, Assistance: "off"})
				if e != nil || !reflect.DeepEqual(out.Hits, again.Hits) {
					t.Fatalf("unchanged repeated query changed authority: %+v %+v %v", out.Hits, again.Hits, e)
				}
				h := out.Hits[0]
				snippet := strings.NewReplacer("«", "", "»", "").Replace(h.Snippet)
				if (tc.want != "" && !strings.Contains(snippet, tc.want)) || (tc.variants > 0 && h.Snippet != "") || h.Resolution != tc.reason || len(h.Variants) != tc.variants {
					t.Fatalf("wrong resolution: %+v", h)
				}
				if tc.variants > 0 {
					if h.Source != "multiple" || h.Fingerprint != "" {
						t.Fatal("group falsely chose authority", h)
					}
					locations := 0
					for _, v := range h.Variants {
						locations += len(v.Locations)
						if !validCopyIdentity(v) || len(v.Variants) > 0 {
							t.Fatal(v)
						}
					}
					if locations != 3 {
						t.Fatal("lost provenance", h)
					}
				}
				if original == nil {
					original = out.Hits
				} else if !reflect.DeepEqual(original, out.Hits) {
					t.Fatalf("configuration order changed reconciliation:\n%+v\n%+v", original, out.Hits)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			for _, n := range calls {
				if n > 24 {
					t.Fatal("unbounded comparison requests", calls)
				}
			}
		})
	}
}

func TestMultipleConnectionKeysIsolationAndLegacyReuse(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	root := t.TempDir()
	var mu sync.Mutex
	keys := map[string]string{}
	leaves := map[string]int{}
	var host *httptest.Server
	host = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proof, err := connectionauth.Verify(r, host.URL, time.Now())
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("leaked owner token")
		}
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		if old := keys[req.Community]; old != "" && old != proof.PublicKey {
			t.Error("allocated second key", req.Community)
		}
		switch req.Action {
		case "community.connect":
			keys[req.Community] = proof.PublicKey
			json.NewEncoder(w).Encode(map[string]any{"community": Library{ID: req.Community, Slug: req.Community}})
		case "community.get":
			json.NewEncoder(w).Encode(Library{ID: req.Community, Slug: req.Community})
		case "community.leave":
			leaves[req.Community]++
			json.NewEncoder(w).Encode(map[string]bool{"disconnected": true})
		case "query":
			if keys[req.Community] != proof.PublicKey {
				t.Error("wrong community key")
			}
			json.NewEncoder(w).Encode(QueryResult{Hits: []Result{{FindingID: req.Community, Title: req.Community}}})
		default:
			t.Error("unexpected action", req.Action)
		}
	}))
	defer host.Close()
	_, private, _ := ed25519.GenerateKey(nil)
	ref, _ := connectionKeyRef(root, host.URL)
	saveProjectKey(ref, projectKey{Service: host.URL, Community: "doom", Seed: base64.RawURLEncoding.EncodeToString(private.Seed())})
	pub := base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
	keys["doom"] = pub
	if err := SaveSettings(root, Settings{Mode: "both", Connections: []Connection{{Alias: "doom", Service: host.URL, CommunityID: "doom", KeyRef: ref, PublicKey: pub}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := NewClient(host.URL)
	first, err := c.Connect(context.Background(), root, "doom", "doom")
	if err != nil || first.PublicKey != pub {
		t.Fatal("legacy key replaced", err)
	}
	second, err := c.Connect(context.Background(), root, "godzilla", "godzilla")
	if err != nil {
		t.Fatal(err)
	}
	if first.KeyRef == second.KeyRef || first.PublicKey == second.PublicKey {
		t.Fatal("key shared across communities")
	}
	if _, err := c.Connect(context.Background(), root, "another-alias", "godzilla"); err == nil {
		t.Fatal("duplicate link allowed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("fixture-%d", i)
			if _, e := c.Connect(context.Background(), root, name, name); e != nil {
				t.Error(e)
			}
		}(i)
	}
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := Execute(context.Background(), root, Command{Action: "mode.set", Mode: "both"}); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	settings, err := LoadSettings(root)
	if err != nil || len(settings.Connections) != 8 {
		t.Fatal("concurrent links lost", settings, err)
	}
	out, err := Query(context.Background(), root, "probe", "external", false, engine.Options{Assistance: "off"})
	if err != nil || len(out.Hits) != 8 || len(out.Warnings) != 0 {
		t.Fatal(out, err)
	}
	if _, err := Disconnect(context.Background(), root, "doom"); err != nil {
		t.Fatal(err)
	}
	if _, err := readProjectKey(second.KeyRef); err != nil {
		t.Fatal("unrelated key removed", err)
	}
	if _, err := readProjectKey(ref); err != keyring.ErrNotFound {
		t.Fatal("legacy key retained", err)
	}
	settings, err = LoadSettings(root)
	if err != nil || len(settings.Connections) != 7 || settings.Mode != "both" {
		t.Fatal(settings, err)
	}
	if _, err := RemoteQuery(context.Background(), second, "probe", engine.Options{}); err != nil {
		t.Fatal(err)
	}
	if leaves["doom"] != 1 || leaves["godzilla"] != 0 {
		t.Fatal("wrong connection revoked", leaves)
	}
}

func TestCopyComparisonRefreshAndPartialFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			root := t.TempDir()
			var firstCalls, secondCalls atomic.Int32
			old := strings.Repeat("a", 64)
			updated := strings.Repeat("b", 64)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req Request
				json.NewDecoder(r.Body).Decode(&req)
				if req.Community == "first" {
					firstCalls.Add(1)
					json.NewEncoder(w).Encode(QueryResult{ComparisonComplete: true, Hits: []Result{{FindingID: copyID, Fingerprint: old, Snippet: "old probe", Sequence: 1}}})
					return
				}
				n := secondCalls.Add(1)
				if n == 1 {
					json.NewEncoder(w).Encode(QueryResult{ComparisonComplete: true})
					return
				}
				if failed {
					w.WriteHeader(403)
					json.NewEncoder(w).Encode(map[string]string{"error": "revoked"})
					return
				}
				json.NewEncoder(w).Encode(QueryResult{ComparisonComplete: true, Compared: []KnowledgeEntry{{ID: copyID, Fingerprint: updated, Sequence: 2}}})
			}))
			defer host.Close()
			SaveSettings(root, Settings{Mode: "external", Connections: []Connection{{Alias: "first", Service: host.URL, CommunityID: "first"}, {Alias: "second", Service: host.URL, CommunityID: "second"}}})
			out, err := Query(context.Background(), root, "probe", "external", false, engine.Options{Assistance: "off"})
			if err != nil || len(out.Hits) != 1 || firstCalls.Load() != 1 || secondCalls.Load() != 2 {
				t.Fatal(out, err, firstCalls.Load(), secondCalls.Load())
			}
			if failed {
				if len(out.Warnings) != 1 || len(out.Hits[0].Variants) != 0 || out.Sources[1]["available"] != false {
					t.Fatal("failed source retained", out)
				}
			} else if len(out.Hits[0].Variants) != 2 || !out.Hits[0].Variants[1].MetadataOnly {
				t.Fatal("nonmatching correction hidden", out)
			}
		})
	}
}

func TestConnectionValidationAndMutationDestinations(t *testing.T) {
	root := t.TempDir()
	base := Connection{Alias: "doom", Service: "https://one.example", CommunityID: "doom"}
	for _, second := range []Connection{
		{Alias: "doom", Service: "https://two.example", CommunityID: "godzilla"},
		{Alias: "another", Service: base.Service, CommunityID: base.CommunityID},
		{Alias: "local", Service: base.Service, CommunityID: "godzilla"},
		{Alias: "multiple", Service: base.Service, CommunityID: "godzilla"},
	} {
		if err := SaveSettings(root, Settings{Mode: "both", Connections: []Connection{base, second}}); err == nil {
			t.Fatal("ambiguous configuration saved", second)
		}
	}
	conns := []Connection{base, {Alias: "godzilla", Service: base.Service, CommunityID: "godzilla"}}
	if err := SaveSettings(root, Settings{Mode: "both", Connections: conns}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"knowledge.upload", "knowledge.status", "knowledge.get", "community.get"} {
		if _, err := Execute(context.Background(), root, Command{Action: action}); err == nil || !strings.Contains(err.Error(), "select a community alias") {
			t.Fatal("missing target not rejected", action, err)
		}
	}
	selected, err := selectConnections(conns, []string{"godzilla", "doom", "godzilla"})
	if err != nil || len(selected) != 2 {
		t.Fatal(selected, err)
	}
}

func TestSameSlugAcrossOwnersAndReconnect(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	root := t.TempDir()
	for n := 0; n < 2; n++ {
		cid := fmt.Sprintf("00000000-0000-4000-8000-%012d", n+100)
		keys := map[string]bool{}
		var host *httptest.Server
		host = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proof, err := connectionauth.Verify(r, host.URL, time.Now())
			if err != nil {
				t.Error(err)
				w.WriteHeader(401)
				return
			}
			keys[proof.PublicKey] = true
			json.NewEncoder(w).Encode(map[string]any{"community": Library{ID: cid, Slug: "doom"}})
		}))
		defer host.Close()
		client, _ := NewClient(host.URL)
		first, err := client.Connect(context.Background(), root, "", "doom")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Execute(context.Background(), root, Command{Action: "connect", Service: host.URL, Alias: first.Alias, Community: "doom"}); err != nil {
			t.Fatal("MCP reconnect by slug failed", err)
		}
		again, err := client.Connect(context.Background(), root, "", cid)
		if err != nil || again.PublicKey != first.PublicKey || len(keys) != 1 {
			t.Fatal("reconnect created identity", again, err, len(keys))
		}
	}
	settings, err := LoadSettings(root)
	if err != nil || len(settings.Connections) != 2 || settings.Connections[0].Alias == settings.Connections[1].Alias {
		t.Fatal("owner alias collision", settings, err)
	}
	copyRoot := t.TempDir()
	if err := SaveSettings(copyRoot, settings); err != nil {
		t.Fatal(err)
	}
	copied, err := LoadSettings(copyRoot)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range copied.Connections {
		if c.KeyRef == settings.Connections[i].KeyRef {
			t.Fatal("copied project borrowed a scoped key")
		}
		if _, err := NewConnectionClient(c); err == nil {
			t.Fatal("copied project authenticated with original key")
		}
	}
}

func TestUnresolvedVariantsBypassSemanticAuthority(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TYPESAFE_API_KEY", "fixture-not-a-real-key")
	if err := atomicJSON(filepath.Join(root, ".re-discipline", "assistance.json"), map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Action != "query" {
			t.Error("variants triggered arbitrary full read", req.Action)
			w.WriteHeader(400)
			return
		}
		fp := strings.Repeat("a", 64)
		if req.Community == "second" {
			fp = strings.Repeat("b", 64)
		}
		json.NewEncoder(w).Encode(QueryResult{ComparisonComplete: true, Hits: []Result{{FindingID: copyID, Fingerprint: fp, Snippet: "Source-specific knowledge", Variants: []Result{{Source: "injected", Snippet: "Malicious authority"}}}}})
	}))
	defer host.Close()
	SaveSettings(root, Settings{Mode: "external", Connections: []Connection{{Alias: "first", Service: host.URL, CommunityID: "first"}, {Alias: "second", Service: host.URL, CommunityID: "second"}}})
	out, err := Query(context.Background(), root, "probe", "external", false, engine.Options{})
	if err != nil || len(out.Hits) != 1 || out.Assistance.Mode != "baseline_variants" || len(out.Warnings) != 0 {
		t.Fatal(out, err)
	}
	if out.Hits[0].Snippet != "" || len(out.Hits[0].Variants) != 2 {
		t.Fatal("false group authority", out)
	}
	for _, v := range out.Hits[0].Variants {
		if len(v.Variants) != 0 || v.Source == "injected" {
			t.Fatal("server injected grouped provenance", v)
		}
	}
}

func TestComparisonWindowIsBoundedAcrossManySources(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		var p struct {
			CompareIDs []string `json:"compare_ids"`
		}
		json.Unmarshal(req.Data, &p)
		if len(p.CompareIDs) > 128 {
			t.Error("unbounded comparison payload", len(p.CompareIDs))
		}
		var source int
		fmt.Sscanf(req.Community, "source-%d", &source)
		out := QueryResult{ComparisonComplete: true}
		for i := 0; i < 50; i++ {
			out.Hits = append(out.Hits, Result{FindingID: fmt.Sprintf("00000000-0000-4000-8000-%012d", source*50+i+1), Fingerprint: strings.Repeat("a", 64)})
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer host.Close()
	connections := []Connection{}
	for n := 0; n < 16; n++ {
		name := fmt.Sprintf("source-%d", n)
		connections = append(connections, Connection{Alias: name, Service: host.URL, CommunityID: name})
	}
	SaveSettings(root, Settings{Mode: "external", Connections: connections})
	out, err := Query(context.Background(), root, "probe", "external", false, engine.Options{Limit: 128, Assistance: "off"})
	if err != nil || len(out.Hits) != 128 || len(out.Sources) != 16 || calls.Load() != 32 {
		t.Fatal("unbounded or underfilled query", len(out.Hits), len(out.Sources), calls.Load(), err)
	}
	seen := map[string]bool{}
	for _, h := range out.Hits {
		if seen[h.FindingID] {
			t.Fatal("duplicate slot", h)
		}
		seen[h.FindingID] = true
	}
}

func TestCommunityCommandReadsPortableStandardInput(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	previous := os.Stdin
	os.Stdin = input
	t.Cleanup(func() { os.Stdin = previous })
	writer.Write([]byte(`{"action":"connections"}`))
	writer.Close()
	got, err := ReadCommandFile("-")
	if err != nil || string(got) != `{"action":"connections"}` {
		t.Fatal(string(got), err)
	}
}

func TestDisconnectRefusesMisboundKey(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	root := t.TempDir()
	service := "https://unused.invalid"
	scope := "godzilla"
	ref, _ := connectionKeyRef(root, service, scope)
	if err := saveProjectKey(ref, projectKey{Service: service, Community: "godzilla", Seed: "not-needed"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(root, Settings{Mode: "both", Connections: []Connection{{Alias: "doom", Service: service, CommunityID: "doom", KeyScope: scope, KeyRef: ref}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Disconnect(context.Background(), root, "doom"); err == nil {
		t.Fatal("misbound link deleted a different key")
	}
	if _, err := readProjectKey(ref); err != nil {
		t.Fatal("other key lost", err)
	}
	settings, err := LoadSettings(root)
	if err != nil || len(settings.Connections) != 1 {
		t.Fatal(settings, err)
	}
}

func TestLocalAliasNeverSelectsServerCommunity(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	root := t.TempDir()
	ids := map[string]string{"godzilla": "00000000-0000-4000-8000-000000000001", "doom": "00000000-0000-4000-8000-000000000002"}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		slug := req.Community
		for name, id := range ids {
			if id == req.Community {
				slug = name
			}
		}
		if req.Action == "community.get" {
			json.NewEncoder(w).Encode(Library{ID: ids[slug], Slug: slug})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"community": Library{ID: ids[slug], Slug: slug}})
	}))
	defer host.Close()
	c, _ := NewClient(host.URL)
	first, err := c.Connect(context.Background(), root, "doom", "godzilla")
	if err != nil {
		t.Fatal(err)
	}
	// A legacy link has no stored server slug; resolve it with its own key.
	settings, e := LoadSettings(root)
	if e != nil {
		t.Fatal(e)
	}
	settings.Connections[0].Slug = ""
	if e := SaveSettings(root, settings); e != nil {
		t.Fatal(e)
	}
	second, err := c.Connect(context.Background(), root, "", "doom")
	if err != nil {
		t.Fatal(err)
	}
	if second.CommunityID != ids["doom"] || second.PublicKey == first.PublicKey || second.Alias == first.Alias {
		t.Fatal("alias silently selected a different server community", first, second)
	}
}

func TestConnectRejectsWrongImmutableIdentity(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	root := t.TempDir()
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"community": Library{ID: "00000000-0000-4000-8000-000000000002", Slug: "doom"}})
	}))
	defer host.Close()
	client, _ := NewClient(host.URL)
	if _, err := client.Connect(context.Background(), root, "doom", "00000000-0000-4000-8000-000000000001"); err == nil {
		t.Fatal("wrong immutable destination linked")
	}
	settings, err := LoadSettings(root)
	if err != nil || len(settings.Connections) != 0 {
		t.Fatal(settings, err)
	}
}

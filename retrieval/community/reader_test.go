package community

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/connectionauth"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/zalando/go-keyring"
)

func TestProjectKeyScopeRenewalAndDisconnect(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	root := t.TempDir()
	ctx := context.Background()
	active, public, revoked := false, true, false
	connects, leaves := 0, 0
	key := ""
	nonces := map[string]bool{}
	var h *httptest.Server
	h = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proof, err := connectionauth.Verify(r, h.URL, time.Now())
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("project request leaked operator credential")
		}
		if key == "" {
			key = proof.PublicKey
		}
		if key != proof.PublicKey {
			t.Error("key changed during renewal")
		}
		if nonces[proof.Nonce] {
			t.Error("retry reused nonce")
		}
		nonces[proof.Nonce] = true
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		reject := func(code int) { w.WriteHeader(code); json.NewEncoder(w).Encode(map[string]string{"error": "denied"}) }
		switch req.Action {
		case "community.connect":
			connects++
			if revoked {
				reject(403)
				return
			}
			if !public {
				reject(404)
				return
			}
			active = true
			json.NewEncoder(w).Encode(map[string]any{"community": Library{ID: "doom", Slug: "doom", Role: "reader"}})
		case "query":
			if revoked {
				reject(403)
				return
			}
			if !active {
				reject(401)
				return
			}
			if req.Community != "doom" {
				t.Error("wrong community")
			}
			json.NewEncoder(w).Encode(QueryResult{Hits: []Result{{CommunityID: "doom", FindingID: "one"}}})
		case "community.leave":
			leaves++
			active = false
			json.NewEncoder(w).Encode(map[string]bool{"disconnected": true})
		default:
			t.Errorf("unexpected action %s", req.Action)
		}
	}))
	defer h.Close()
	c, err := NewClient(h.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := c.Connect(ctx, root, "doom", "doom")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readProjectKey(conn.KeyRef)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(SettingsPath(root))
	if strings.Contains(string(b), stored.Seed) {
		t.Fatal("private key leaked into project")
	}
	if _, err = RemoteQuery(ctx, conn, "clock", engine.Options{}); err != nil {
		t.Fatal(err)
	}
	active = false
	if _, err = RemoteQuery(ctx, conn, "clock", engine.Options{}); err != nil || connects != 2 {
		t.Fatal("public lease renewal", err, connects)
	}
	if _, err = c.Connect(ctx, root, "other", "godzilla"); err == nil || connects != 2 {
		t.Fatal("second community contacted")
	}
	changed := conn
	changed.Service = "https://other.example"
	if _, err = NewConnectionClient(changed); err == nil {
		t.Fatal("key crossed server")
	}
	changed = conn
	changed.CommunityID = "godzilla"
	if _, err = NewConnectionClient(changed); err == nil {
		t.Fatal("key crossed community")
	}
	revoked = true
	if _, err = RemoteQuery(ctx, conn, "clock", engine.Options{}); err == nil || connects != 2 {
		t.Fatal("revocation tried to rejoin", err, connects)
	}
	revoked = false
	public = false
	active = false
	if _, err = RemoteQuery(ctx, conn, "clock", engine.Options{}); err == nil || connects != 3 {
		t.Fatal("private expiry was ignored", err, connects)
	}
	if _, err = Disconnect(ctx, root, "doom"); err != nil || leaves != 1 {
		t.Fatal(err, leaves)
	}
	if _, err = readProjectKey(conn.KeyRef); err != keyring.ErrNotFound {
		t.Fatal("local key retained", err)
	}
	settings, _ := LoadSettings(root)
	if len(settings.Connections) != 0 {
		t.Fatal("local connection retained")
	}
}

func TestConcurrentConnectReusesProjectKey(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	var mu sync.Mutex
	keys := map[string]bool{}
	var h *httptest.Server
	h = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proof, err := connectionauth.Verify(r, h.URL, time.Now())
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		keys[proof.PublicKey] = true
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"community": Library{ID: "doom", Slug: "doom"}})
	}))
	defer h.Close()
	root := t.TempDir()
	var workers sync.WaitGroup
	for range 10 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			c, _ := NewClient(h.URL)
			if _, err := c.Connect(context.Background(), root, "doom", "doom"); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if len(keys) != 1 {
		t.Fatal("concurrent agents allocated separate keys", len(keys))
	}
}

func TestOfflineDisconnectDeletesLocalKeyAndExplainsExpiry(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	root := t.TempDir()
	ref, _ := connectionKeyRef(root, "https://unused.invalid")
	if err := saveProjectKey(ref, projectKey{Service: "https://unused.invalid", Community: "doom", Seed: "damaged"}); err != nil {
		t.Fatal(err)
	}
	SaveSettings(root, Settings{Mode: "external", Connections: []Connection{{Alias: "doom", Service: "https://unused.invalid", CommunityID: "doom", KeyRef: ref}}})
	result, err := Disconnect(context.Background(), root, "doom")
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["server_removed"] != false || result.(map[string]any)["notice"] == nil {
		t.Fatal(result)
	}
	if _, err = readProjectKey(ref); err != keyring.ErrNotFound {
		t.Fatal("key retained", err)
	}
}

func TestCopiedProjectCannotBorrowOrDeleteOriginalKey(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	original, copyRoot := t.TempDir(), t.TempDir()
	ref, _ := connectionKeyRef(original, "https://kb.example")
	if err := saveProjectKey(ref, projectKey{Service: "https://kb.example", Community: "doom", Seed: "not-needed-for-this-test"}); err != nil {
		t.Fatal(err)
	}
	settings := Settings{Mode: "external", Connections: []Connection{{Alias: "doom", Service: "https://kb.example", CommunityID: "doom", KeyRef: ref}}}
	if err := SaveSettings(copyRoot, settings); err != nil {
		t.Fatal(err)
	}
	copied, err := LoadSettings(copyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if copied.Connections[0].KeyRef == ref {
		t.Fatal("copied configuration borrowed original lookup")
	}
	if _, err = NewConnectionClient(copied.Connections[0]); err == nil {
		t.Fatal("copied project used original key")
	}
	if _, err = Disconnect(context.Background(), copyRoot, "doom"); err != nil {
		t.Fatal(err)
	}
	if _, err = readProjectKey(ref); err != nil {
		t.Fatal("copied project deleted original key", err)
	}
}

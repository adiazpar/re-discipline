package community

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
)

func TestDeleteCommunityUsesOwnerAndRemovesOnlyMatchingLink(t *testing.T) {
	for _, scenario := range []string{"normal", "missing_reader_key", "changed_link", "rejected", "wrong_response", "preview"} {
		t.Run(scenario, func(t *testing.T) {
			keyring.MockInit()
			t.Cleanup(keyring.MockInit)
			root := t.TempDir()
			cid := uuid.NewString()
			var linked Connection
			calls := 0
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer test-operator" || r.Header.Get("Signature") != "" {
					t.Error("deletion did not use operator authority")
				}
				var req Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Action != "community.delete" || req.Community != cid {
					t.Error("incorrect deletion request", req, err)
				}
				if scenario == "rejected" {
					w.WriteHeader(403)
					json.NewEncoder(w).Encode(map[string]string{"error": "permission denied"})
					return
				}
				if scenario == "changed_link" {
					replacement := linked
					replacement.CommunityID = uuid.NewString()
					if err := SaveSettings(root, Settings{Mode: "both", Retrieval: "remote", Connections: []Connection{replacement}}); err != nil {
						t.Error(err)
					}
				}
				returned := cid
				if scenario == "wrong_response" {
					returned = uuid.NewString()
				}
				json.NewEncoder(w).Encode(map[string]any{"community_id": returned, "deleted": scenario != "preview", "cleanup_pending": false})
			}))
			defer h.Close()
			t.Setenv("RE_DISCIPLINE_SERVICE", h.URL)
			t.Setenv("RE_DISCIPLINE_TOKEN", "test-operator")
			ref, err := connectionKeyRef(root, h.URL)
			if err != nil {
				t.Fatal(err)
			}
			linked = Connection{Alias: "fixture", Service: h.URL, CommunityID: cid, KeyRef: ref}
			if err = SaveSettings(root, Settings{Mode: "both", Retrieval: "remote", Connections: []Connection{linked}}); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing_reader_key" {
				if err = keyring.Set(connectionKeyService, ref, "private fixture value"); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(root, ".re-discipline", "local-research.md")
			if err = os.WriteFile(source, []byte("retain local research"), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := Execute(context.Background(), root, Command{Action: "community.delete", Alias: "fixture"})
			if scenario == "rejected" || scenario == "wrong_response" {
				if err == nil {
					t.Fatal("unconfirmed deletion succeeded", out)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			settings, e := LoadSettings(root)
			if e != nil {
				t.Fatal(e)
			}
			if scenario == "normal" || scenario == "missing_reader_key" {
				if len(settings.Connections) != 0 || settings.Mode != "local" || out.(map[string]any)["local_disconnected"] != true {
					t.Fatal(settings, out)
				}
				if _, e = keyring.Get(connectionKeyService, ref); e != keyring.ErrNotFound {
					t.Fatal("deleted project key retained", e)
				}
			} else {
				if len(settings.Connections) != 1 {
					t.Fatal("unrelated or unconfirmed link removed", settings)
				}
				if _, e = keyring.Get(connectionKeyService, ref); e != nil {
					t.Fatal("key removed despite retained link", e)
				}
			}
			if b, e := os.ReadFile(source); e != nil || string(b) != "retain local research" {
				t.Fatal("local corpus changed", e)
			}
			if calls != 1 {
				t.Fatal("unexpected extra requests", calls)
			}
		})
	}
}

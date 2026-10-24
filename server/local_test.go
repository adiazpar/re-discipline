package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

func TestLocalOwnerConfigurationAndRoutes(t *testing.T) {
	c := Config{LocalOnly: true, PublicURL: "http://127.0.0.1:7346", LocalOwnerID: id(), LocalOwnerToken: randomToken()}
	if err := ValidateLocalOwner(c); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.PublicURL = "https://public.example" },
		func(c *Config) { c.LocalOwnerToken = "short" },
		func(c *Config) { c.LocalOwnerID = "invalid" },
		func(c *Config) { c.GitHubClientID = "oauth" },
		func(c *Config) { c.JevEnabled = true },
		func(c *Config) { c.SetupOnly = true },
	} {
		next := c
		change(&next)
		if ValidateLocalOwner(next) == nil {
			t.Fatal("unsafe local configuration accepted")
		}
	}
	s := New(nil, c)
	for _, path := range []string{"/", "/v1/config", "/v1/auth/device/start", "/v1/auth/github/start"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("local route %s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/operations", strings.NewReader(`{"action":"community.list"}`))
	r.Header.Set("Authorization", "Bearer "+c.LocalOwnerToken)
	r.Header.Set("Origin", "https://unrelated.example")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("foreign browser origin accepted: %d", w.Code)
	}
}

func TestLocalTwoCommunityRetrievalIsolation(t *testing.T) {
	s, h := fixture(t)
	token := randomToken()
	s.Config.LocalOnly, s.Config.LocalOwnerID, s.Config.LocalOwnerToken = true, id(), token
	s.Config.PublicURL = h.URL
	// Local mode must not use fixture authentication, cookies, or anonymous reads.
	op(t, h, "", "community.list", "", nil, 401)
	op(t, h, id(), "community.list", "", nil, 401)
	op(t, h, token, "invite.create", id(), nil, 403)
	op(t, h, token, "query", "", map[string]any{"query": "clock"}, 400)
	makeCommunity := func(name, slug string) kb.Library {
		return unpack[kb.Library](t, op(t, h, token, "community.create", "", map[string]any{
			"name": name, "slug": slug, "policy": map[string]any{"scope": name + " reverse engineering; synthetic isolation tests."},
		}, 200))
	}
	doom := makeCommunity("Doom 2016", "doom-2016")
	godzilla := makeCommunity("Godzilla PS4", "godzilla-ps4")
	if doom.ID == godzilla.ID || doom.Visibility != "private" || doom.Policy.Mode != "trusted" {
		t.Fatal("invalid community defaults or identities")
	}
	op(t, h, token, "community.create", "", doom, 409)
	public := doom
	public.Slug = "public-test"
	public.Visibility = "public"
	op(t, h, token, "community.create", "", public, 400)
	if got := unpack[[]kb.Library](t, op(t, h, token, "community.list", "", nil, 200)); len(got) != 2 {
		t.Fatalf("communities: %+v", got)
	}
	query := func(c, term string) []kb.Result {
		result := unpack[struct {
			Hits []kb.Result `json:"hits"`
		}](t, op(t, h, token, "query", c, map[string]any{"query": term}, 200))
		return result.Hits
	}
	if len(query(doom.ID, "clock")) != 0 || len(query(godzilla.ID, "clock")) != 0 {
		t.Fatal("new corpus was not empty")
	}
	key := id()
	publish := func(c, key string, doc kb.Document) kb.Submission {
		v := submit(t, h, token, c, key, doc)
		runJob(t, s)
		v = unpack[kb.Submission](t, op(t, h, token, "submission.get", c, map[string]string{"id": v.ID}, 200))
		if v.State != "accepted" {
			t.Fatalf("publication: %+v", v)
		}
		return v
	}
	doc := document()
	doc.Markdown += "\nSynthetic marker doomonlyfixture."
	d := publish(doom.ID, key, doc)
	gdoc := document()
	gdoc.Build = "Godzilla PS4 synthetic fixture"
	gdoc.Markdown += "\nSynthetic marker godzillaonlyfixture."
	g := publish(godzilla.ID, key, gdoc)
	if d.FindingID == g.FindingID {
		t.Fatal("source path/idempotency key merged communities")
	}
	if again := submit(t, h, token, doom.ID, key, doc); again.ID != d.ID {
		t.Fatal("retry duplicated publication")
	}
	for _, test := range []struct{ community, foreign string }{{doom.ID, "godzillaonlyfixture"}, {godzilla.ID, "doomonlyfixture"}} {
		hits := query(test.community, "timescale")
		if len(hits) != 1 || hits[0].CommunityID != test.community {
			t.Fatalf("cross-community query: %+v", hits)
		}
		if len(query(test.community, test.foreign)) != 0 {
			t.Fatal("foreign evidence retrieved")
		}
		if _, err := os.Stat(filepath.Join(s.Config.CacheDir, test.community, "1", ".re-discipline", "index.db")); err != nil {
			t.Fatal(err)
		}
	}
	if got := unpack[[]kb.Change](t, op(t, h, token, "finding.get", godzilla.ID, map[string]string{"id": d.FindingID}, 200)); len(got) != 0 {
		t.Fatal("foreign finding readable")
	}
	// Identical content is still owned and indexed separately in each community.
	common := document()
	common.SourcePath = "docs/shared-fixture.md"
	common.Markdown = "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Common fixture\nIdentical isolationfixture payload for test purposes."
	a := publish(doom.ID, id(), common)
	b := publish(godzilla.ID, id(), common)
	if a.FindingID == b.FindingID {
		t.Fatal("exact payload dedup crossed communities")
	}
	if len(query(doom.ID, "isolationfixture")) != 1 || len(query(godzilla.ID, "isolationfixture")) != 1 {
		t.Fatal("independent identical findings missing")
	}
	// A revision advances only its own corpus; withdrawal by slug purges only its index.
	doc.FindingID, doc.BaseRevision = d.FindingID, d.Revision
	doc.Markdown += "\nDoom correctionfixture."
	updated := publish(doom.ID, id(), doc)
	if len(query(doom.ID, "correctionfixture")) != 1 || len(query(godzilla.ID, "correctionfixture")) != 0 {
		t.Fatal("correction crossed communities")
	}
	before := unpack[kb.Library](t, op(t, h, token, "community.get", godzilla.ID, nil, 200))
	op(t, h, token, "finding.withdraw", doom.Slug, map[string]any{"id": d.FindingID, "revision": updated.Revision, "reason": "Remove synthetic fixture."}, 200)
	if len(query(doom.ID, "doomonlyfixture")) != 0 || len(query(godzilla.ID, "godzillaonlyfixture")) != 1 {
		t.Fatal("withdrawal crossed communities or left stale results")
	}
	after := unpack[kb.Library](t, op(t, h, token, "community.get", godzilla.ID, nil, 200))
	if before.Sequence != after.Sequence {
		t.Fatal("other community sequence changed")
	}
	var count int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM community.libraries`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("shared database: %d %v", count, err)
	}
	res, err := http.Get(h.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
}

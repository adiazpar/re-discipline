package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

func TestQueryMetadataTracksIndexedReplacement(t *testing.T) {
	s, h, owner := searchFixture(t)
	l := create(t, h, owner, "indexed-metadata", "private", "")
	fid := id()
	makeDoc := func(version int) kb.KnowledgeDocument {
		t.Helper()
		raw := fmt.Sprintf("---\nid: %s\nstatus: promoted\nkind: fact\ngrade: direct\nbuild: build-%d\ncreated_at: 2026-01-01T00:00:00Z\nknowledge_updated_at: 2026-01-%02dT00:00:00Z\n---\n# Metadata probe\n\n## Knowledge\nMotion operates at %d Hz.\n", fid, version, version, version*60)
		d, err := kb.PrepareKnowledge("docs/original-name.md", raw)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	old, next := makeDoc(1), makeDoc(2)
	receipt := unpack[kb.KnowledgeReceipt](t, op(t, h, owner, "knowledge.put", l.ID, testBatch(old), 200))
	warmTestKnowledge(t, s, l.ID)
	check := func(d kb.KnowledgeDocument, seq int64) {
		t.Helper()
		got := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "motion", "compare_ids": []string{fid}}, 200))
		if len(got.Hits) != 1 || len(got.Compared) != 1 {
			t.Fatalf("missing hit or comparison: %+v", got)
		}
		parsed := engine.Parse(d.Path, d.Markdown)
		want := kb.Result{FindingID: fid, Fingerprint: d.Fingerprint, CommunityID: l.ID, Build: parsed.Build, Status: "active", Title: parsed.Title, Snippet: got.Hits[0].Snippet, Grade: parsed.Grade, Kind: parsed.Kind, TextDigest: parsed.TextDigest, Sequence: seq, CreatedAt: parsed.CreatedAt, KnowledgeUpdatedAt: parsed.KnowledgeUpdatedAt}
		if !reflect.DeepEqual(got.Hits[0], want) || got.Compared[0].Fingerprint != d.Fingerprint || got.Compared[0].Sequence != seq {
			t.Fatalf("metadata mixed versions: %+v; want %+v", got, want)
		}
	}
	check(old, receipt.Sequence)
	replacement := unpack[kb.KnowledgeReceipt](t, op(t, h, owner, "knowledge.put", l.ID, testBatch(next), 200))
	check(old, receipt.Sequence) // Stored replacement must not leak into the old search snapshot.
	warmTestKnowledge(t, s, l.ID)
	check(next, replacement.Sequence)
	// Incomplete derived data must fail closed, not invent zero-valued metadata.
	db, err := engine.OpenRemoteIndex(context.Background(), s.indexRoot(l.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM remote_records WHERE id=?`, fid); err != nil {
		t.Fatal(err)
	}
	db.Close()
	op(t, h, owner, "query", l.ID, map[string]string{"query": "motion"}, 500)
}

type claimTransport func(*http.Request) (*http.Response, error)

func (f claimTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQueryOnlyProjectsClaimsForEnabledAssistance(t *testing.T) {
	s, h, owner := searchFixture(t)
	l := create(t, h, owner, "claim-projection", "private", "")
	d := putTestKnowledge(t, s, h, owner, l.ID, "", "movement", "Motion uses a complete indexed claim.")
	parsed := engine.Parse(d.Path, d.Markdown)
	want := kb.ProjectClaim(kb.Document{Markdown: d.Markdown, SourcePath: d.Path, Build: parsed.Build, Kind: parsed.Kind, Grade: parsed.Grade}, "active")
	calls := atomic.Int32{}
	client := semantic.New(semantic.Config{Enabled: true}, t.TempDir())
	client.Key = func() string { return "test-only" }
	client.HTTP = &http.Client{Transport: claimTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		var request struct {
			State struct{ Claim semantic.Claim }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(request.State.Claim, want) {
			t.Errorf("claim projection changed: %+v", request.State.Claim)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"model":"` + semantic.Model + `","answers":{"relevance":{"type":"noul","noul":0.9}}}`))}, nil
	})}
	query := func(assistance *bool) kb.QueryResult {
		t.Helper()
		return unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": "How does motion work?", "assistance": assistance}, 200))
	}
	baseline := query(nil)
	s.Semantic = client
	if got := query(nil); got.Assistance.Mode != "disabled" {
		t.Fatal("community policy ignored", got)
	}
	l.Policy.Assistance = true
	l = unpack[kb.Library](t, op(t, h, owner, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": l.Visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}, 200))
	off := false
	if got := query(&off); got.Assistance.Mode != "disabled" || !reflect.DeepEqual(got.Hits, baseline.Hits) {
		t.Fatal("request opt-out ignored", got)
	}
	if calls.Load() != 0 {
		t.Fatal("disabled search called assistance")
	}
	got := query(nil)
	if calls.Load() != 1 || got.Assistance.Mode != "reranked" || len(got.Hits) != 1 || got.Hits[0].Relevance == nil {
		t.Fatal("enabled assistance lost its full claim", got, calls.Load())
	}
	got.Hits[0].Relevance = nil
	if !reflect.DeepEqual(got.Hits, baseline.Hits) {
		t.Fatal("assistance changed identity or metadata", got.Hits, baseline.Hits)
	}
	s.Semantic = nil
	if got := query(nil); got.Assistance.Mode != "disabled" || !reflect.DeepEqual(got.Hits, baseline.Hits) {
		t.Fatal("missing provider changed baseline", got)
	}
}

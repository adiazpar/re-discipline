package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

type updateTransport func(*http.Request) (*http.Response, error)

func (f updateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Run only against the disposable installation created by update-smoke.py.
// Keys exist in memory; no private corpus or credential store is used.
func TestManagedUpdateLiveRetrieval(t *testing.T) {
	origin, state := os.Getenv("TEST_UPDATE_ORIGIN"), os.Getenv("TEST_UPDATE_STATE")
	if origin == "" || state == "" {
		t.Skip("requires disposable managed update fixture")
	}
	var credential struct {
		Token string `json:"token"`
	}
	b, err := os.ReadFile(os.Getenv("TEST_UPDATE_OWNER_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &credential); err != nil {
		t.Fatal(err)
	}
	clientOrigin := origin
	if public := os.Getenv("TEST_UPDATE_PUBLIC"); public != "" {
		clientOrigin = public
	}
	newClient := func() (*kb.Client, error) {
		c, err := kb.NewClient(clientOrigin)
		if err != nil {
			return nil, err
		}
		if clientOrigin != origin {
			destination, _ := url.Parse(origin)
			c.HTTP.Transport = updateTransport(func(r *http.Request) (*http.Response, error) {
				copy := r.Clone(r.Context())
				target := *r.URL
				target.Scheme = destination.Scheme
				target.Host = destination.Host
				copy.Host = r.URL.Host
				copy.URL = &target
				return http.DefaultTransport.RoundTrip(copy)
			})
		}
		return c, nil
	}
	owner, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	owner.Token = credential.Token
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	type fixture struct {
		community string
		doc       kb.KnowledgeDocument
		marker    string
	}
	fixtures := make([]fixture, 2)
	for i := range fixtures {
		var library kb.Library
		if err := owner.Operation(ctx, "community.create", "", map[string]any{"name": fmt.Sprintf("Update fixture %d", i), "slug": fmt.Sprintf("update-%d", i), "policy": map[string]any{"scope": "Disposable update fixture"}}, &library); err != nil {
			t.Fatal(err)
		}
		if err := owner.Operation(ctx, "community.update", library.ID, map[string]any{"name": library.Name, "visibility": "public", "policy": library.Policy, "expected_version": library.PolicyVersion}, &library); err != nil {
			t.Fatal(err)
		}
		doc := testKnowledge(t, "", fmt.Sprintf("updateuniqueterm%d", i), fmt.Sprintf("Community %d preserves this exact finding across deployment", i))
		var receipt kb.KnowledgeReceipt
		if err := owner.Operation(ctx, "knowledge.put", library.ID, testBatch(doc), &receipt); err != nil {
			t.Fatal(err)
		}
		fixtures[i] = fixture{library.ID, doc, fmt.Sprintf("updateuniqueterm%d", i)}
	}
	for _, f := range fixtures {
		for deadline := time.Now().Add(20 * time.Second); ; {
			var result kb.QueryResult
			if err := owner.Operation(ctx, "query", f.community, map[string]string{"query": f.marker}, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Hits) == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("initial index did not warm")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	var passed, transient, corrupt, scope atomic.Int64
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		f, other := fixtures[i%2], fixtures[(i+1)%2]
		_, key, _ := ed25519.GenerateKey(nil)
		reader, _ := newClient()
		reader.Token = ""
		reader.PrivateKey = key
		var grant any
		if err := reader.Operation(ctx, "community.connect", f.community, nil, &grant); err != nil {
			t.Fatal(err)
		}
		var forbidden any
		if err := reader.Operation(ctx, "knowledge.get", other.community, map[string]string{"id": other.doc.ID}, &forbidden); err == nil {
			t.Fatal("cross-community read allowed")
		}
		scope.Add(1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for n := 0; n < 500; n++ {
				if _, err := os.Stat(filepath.Join(state, "stop")); err == nil {
					break
				}
				var result kb.QueryResult
				if err := reader.Operation(ctx, "query", f.community, map[string]any{"query": f.marker, "limit": 8}, &result); err != nil {
					transient.Add(1)
					time.Sleep(30 * time.Millisecond)
					continue
				}
				if len(result.Hits) != 1 || result.Hits[0].FindingID != f.doc.ID || result.Hits[0].Fingerprint != f.doc.Fingerprint {
					corrupt.Add(1)
					continue
				}
				var document kb.KnowledgeDocument
				if err := reader.Operation(ctx, "knowledge.get", f.community, map[string]string{"id": f.doc.ID}, &document); err != nil {
					transient.Add(1)
					continue
				}
				if document.Markdown != f.doc.Markdown || document.Fingerprint != f.doc.Fingerprint {
					corrupt.Add(1)
					continue
				}
				passed.Add(1)
			}
			// Retained reader keys must remain valid after the replacement.
			for {
				if _, err := os.Stat(filepath.Join(state, "stop")); err == nil {
					break
				}
				if ctx.Err() != nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			var document kb.KnowledgeDocument
			if err := reader.Operation(ctx, "knowledge.get", f.community, map[string]string{"id": f.doc.ID}, &document); err != nil || document.Markdown != f.doc.Markdown {
				corrupt.Add(1)
			}
			var forbidden any
			if err := reader.Operation(ctx, "knowledge.get", other.community, map[string]string{"id": other.doc.ID}, &forbidden); err == nil {
				corrupt.Add(1)
			} else {
				scope.Add(1)
			}
		}()
	}
	if err := os.WriteFile(filepath.Join(state, "ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	close(start)
	workers.Wait()
	report := map[string]any{"successful_workflows": passed.Load(), "transient_failures": transient.Load(), "integrity_failures": corrupt.Load(), "scope_checks": scope.Load(), "reader_keys": 32, "communities": 2}
	out, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(state, "report.json"), out, 0600)
	if corrupt.Load() != 0 || passed.Load() < 100 || scope.Load() != 64 {
		t.Fatalf("update retrieval failed: %s", out)
	}
	t.Logf("update retrieval: %s", out)
}

package community

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSummaryStatisticsCacheIsolationExpiryAndFailure(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(403)
			return
		}
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(CommunityStats{CommunityID: req.Community, Documents: 7, KnowledgeBytes: 1234, ObservedAt: time.Now().UTC()})
	}))
	defer h.Close()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".re-discipline"), 0700)
	conn := Connection{Alias: "doom", Service: h.URL, CommunityID: "doom", PublicKey: "first"}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := ReadCommunityStats(context.Background(), root, conn)
			if err != nil || got.Documents != 7 || got.CommunityID != "doom" {
				t.Error(got, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent summaries bypassed cache", calls.Load())
	}
	got, _ := ReadCommunityStats(context.Background(), root, conn)
	if !got.Cached {
		t.Fatal("missing cached indicator")
	}
	other := conn
	other.CommunityID = "godzilla"
	if got, err := ReadCommunityStats(context.Background(), root, other); err != nil || got.CommunityID != "godzilla" || got.Cached {
		t.Fatal(got, err)
	}
	rotated := conn
	rotated.PublicKey = "new key"
	if got, err := ReadCommunityStats(context.Background(), root, rotated); err != nil || got.Cached {
		t.Fatal(got, err)
	}
	got.FetchedAt = time.Now().Add(-6 * time.Minute)
	if err := atomicJSON(statsCachePath(root, conn), got); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if _, err := ReadCommunityStats(context.Background(), root, conn); err == nil {
		t.Fatal("returned stale statistics after revocation")
	}
	fail.Store(false)
	if got, err := ReadCommunityStats(context.Background(), root, conn); err != nil || got.Cached {
		t.Fatal(got, err)
	}
	// An incorrect community response must never poison this link's cache.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CommunityStats{CommunityID: "other", ObservedAt: time.Now()})
	}))
	defer bad.Close()
	conn.Service = bad.URL
	if _, err := ReadCommunityStats(context.Background(), root, conn); err == nil {
		t.Fatal("accepted wrong scope")
	}
	if _, err := os.Stat(statsCachePath(root, conn)); !os.IsNotExist(err) {
		t.Fatal("cached wrong scope", err)
	}
}

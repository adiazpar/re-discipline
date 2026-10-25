package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/zalando/go-keyring"
)

// Exercise the plugin coordinator, actual signed requests and both databases.
// The credential backend is in-memory here; the MCP smoke covers the real OS store.
func TestLinkedCommunitiesRetrievalUnderLoad(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	t.Setenv("MAX_UNAUTHENTICATED_REQUESTS_PER_IP_MINUTE", "100000")
	servers := []*Server{}
	hosts := []*httptest.Server{}
	owners := []string{}
	for range 2 {
		s, h, owner := searchFixture(t)
		servers = append(servers, s)
		hosts = append(hosts, h)
		owners = append(owners, owner)
	}
	ctx := context.Background()
	shared := testKnowledge(t, "", "linkedprobe", "Identical shared knowledge.")
	divergentID := id()
	communities := []kb.Library{}
	expected := map[string]kb.KnowledgeDocument{}
	for n := 0; n < 4; n++ {
		host := n / 2
		visibility := "public"
		if n%2 == 1 {
			visibility = "private"
		}
		l := create(t, hosts[host], owners[host], fmt.Sprintf("linked-%d", n), visibility, "")
		communities = append(communities, l)
		divergent := testKnowledge(t, divergentID, "linkedprobe", fmt.Sprintf("Independent community %d variant.", n))
		unique := testKnowledge(t, "", "linkedprobe", fmt.Sprintf("Unique community %d fact.", n))
		op(t, hosts[host], owners[host], "knowledge.put", l.ID, testBatch(shared, divergent, unique), 200)
		warmTestKnowledge(t, servers[host], l.ID)
		for _, d := range []kb.KnowledgeDocument{shared, divergent, unique} {
			expected[l.ID+"/"+d.ID] = d
		}
	}
	const projects, requests, concurrency = 16, 256, 32
	roots := []string{}
	connections := [][]kb.Connection{}
	for n := 0; n < projects; n++ {
		root := t.TempDir()
		roots = append(roots, root)
		dir := filepath.Join(root, ".re-discipline", "docs")
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, "shared.md"), []byte(shared.Markdown), 0600)
		row := []kb.Connection{}
		for i, l := range communities {
			c, err := kb.NewClient(hosts[i/2].URL)
			if err != nil {
				t.Fatal(err)
			}
			c.Token = ""
			if l.Visibility == "private" {
				grant := unpack[map[string]any](t, op(t, hosts[i/2], owners[i/2], "access.create", l.ID, nil, 200))
				c.Invitation = grant["invitation"].(string)
			}
			conn, err := c.Connect(ctx, root, fmt.Sprintf("link-%d", i), l.ID)
			if err != nil {
				t.Fatal(err)
			}
			row = append(row, conn)
		}
		connections = append(connections, row)
		// Warm the local index before simultaneous readers; this isn't a first-run benchmark.
		if _, _, err := engine.Query(root, "linkedprobe", engine.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errors := make(chan error, requests*8+16)
	latencies := make(chan time.Duration, requests)
	started := time.Now()
	// Writes share the real server with the read workload, but do not alter its anchors.
	for i, l := range communities {
		wg.Add(1)
		go func(i int, l kb.Library) {
			defer wg.Done()
			docs := []kb.KnowledgeDocument{}
			for n := 0; n < 40; n++ {
				docs = append(docs, testKnowledge(t, "", "unrelatedbulk", fmt.Sprintf("Bulk community %d document %d", i, n)))
			}
			c := kb.Client{URL: hosts[i/2].URL, HTTP: hosts[i/2].Client(), Token: owners[i/2]}
			var receipt kb.KnowledgeReceipt
			if err := c.Operation(ctx, "knowledge.put", l.ID, testBatch(docs...), &receipt); err != nil || receipt.Updated != 40 {
				errors <- fmt.Errorf("bulk upload: %v %+v", err, receipt)
			}
		}(i, l)
	}
	jobs := make(chan int)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				begin := time.Now()
				project := n % projects
				mode := "external"
				if n%2 == 0 {
					mode = "both"
				}
				out, err := kb.Query(ctx, roots[project], "linkedprobe", mode, false, engine.Options{Limit: 8, Assistance: "off"})
				if err != nil || len(out.Warnings) != 0 || len(out.Hits) != 6 {
					errors <- fmt.Errorf("group query: %v %+v", err, out)
					latencies <- time.Since(begin)
					continue
				}
				foundShared, foundVariants := false, false
				for _, hit := range out.Hits {
					if hit.FindingID == shared.ID {
						foundShared = true
						wantLocations := 4
						if mode == "both" {
							wantLocations = 5
						}
						if hit.Fingerprint != shared.Fingerprint || len(hit.Locations) != wantLocations || (mode == "both" && hit.Source != "local") {
							errors <- fmt.Errorf("shared copy: %+v", hit)
						}
					}
					if hit.FindingID == divergentID {
						foundVariants = true
						if len(hit.Variants) != 4 || hit.Source != "multiple" || hit.Snippet != "" {
							errors <- fmt.Errorf("variants: %+v", hit)
							continue
						}
						for _, v := range hit.Variants {
							want, ok := expected[v.CommunityID+"/"+v.FindingID]
							if !ok || v.Fingerprint != want.Fingerprint || len(v.Locations) != 1 {
								errors <- fmt.Errorf("variant scope/hash: %+v", v)
								continue
							}
							var conn kb.Connection
							for _, linked := range connections[project] {
								if linked.CommunityID == v.CommunityID && linked.Service == v.Service {
									conn = linked
								}
							}
							c, e := kb.NewConnectionClient(conn, roots[project])
							var doc kb.KnowledgeDocument
							if e == nil {
								e = c.Operation(ctx, "knowledge.get", conn.CommunityID, map[string]string{"id": v.FindingID}, &doc)
							}
							if e != nil || doc.Fingerprint != want.Fingerprint || doc.Markdown != want.Markdown {
								errors <- fmt.Errorf("variant full read: %v %+v", e, doc)
							}
						}
					} else if hit.FindingID != shared.ID {
						want, ok := expected[hit.CommunityID+"/"+hit.FindingID]
						if !ok || hit.Fingerprint != want.Fingerprint {
							errors <- fmt.Errorf("unexpected unique result: %+v", hit)
						}
					}
				}
				if !foundShared || !foundVariants {
					errors <- fmt.Errorf("missing expected groups")
				}
				latencies <- time.Since(begin)
			}
		}()
	}
	for n := 0; n < requests; n++ {
		jobs <- n
	}
	close(jobs)
	wg.Wait()
	close(errors)
	close(latencies)
	failures := 0
	for err := range errors {
		failures++
		t.Error(err)
	}
	times := []time.Duration{}
	for d := range latencies {
		times = append(times, d)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if len(times) != requests {
		t.Fatal("incomplete workload", len(times))
	}
	report := map[string]any{"projects": projects, "connections": projects * 4, "servers": 2, "communities": 4, "workflows": requests, "concurrency": concurrency, "variant_full_reads": requests * 4, "uploaded_documents": 160, "failures": failures, "p50_ms": times[len(times)/2].Milliseconds(), "p95_ms": times[len(times)*95/100].Milliseconds(), "elapsed_ms": time.Since(started).Milliseconds()}
	fmt.Printf("LINKED_LOAD_REPORT=%s\n", raw(report))
	// Disconnect exactly one link and retain the remaining three signed keys.
	if _, err := kb.Disconnect(ctx, roots[0], "link-0"); err != nil {
		t.Fatal(err)
	}
	out, err := kb.Query(ctx, roots[0], "linkedprobe", "external", false, engine.Options{Assistance: "off"})
	if err != nil || len(out.Warnings) != 0 || len(out.Hits) != 5 {
		t.Fatal("disconnect damaged other links", out, err)
	}
	for _, hit := range out.Hits {
		for _, variant := range hit.Variants {
			if variant.CommunityID == communities[0].ID {
				t.Fatal("disconnected source returned", variant)
			}
		}
	}
	out, err = kb.Query(ctx, roots[1], "linkedprobe", "external", false, engine.Options{Communities: []string{"link-2"}, Assistance: "off"})
	if err != nil || len(out.Hits) != 3 || len(out.Sources) != 1 {
		t.Fatal("subset broadened", out, err)
	}
}

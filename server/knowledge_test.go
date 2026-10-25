package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func testKnowledge(t *testing.T, fid, term, content string) kb.KnowledgeDocument {
	t.Helper()
	if fid == "" {
		fid = id()
	}
	d, err := kb.PrepareKnowledge("docs/"+fid+".md", fmt.Sprintf("---\nid: %s\nstatus: promoted\nkind: fact\ngrade: direct\nidents: [%s]\n---\n# %s\n\n## Knowledge\n%s\n\n## Evidence\nPrivate local research.\n", fid, term, term, content))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func testBatch(docs ...kb.KnowledgeDocument) kb.KnowledgeBatch {
	return kb.KnowledgeBatch{Key: id(), CreatedAt: time.Now().Unix(), Documents: docs}
}
func warmTestKnowledge(t *testing.T, s *Server, cid string) {
	t.Helper()
	for n := 0; n < 10000; n++ {
		worked, e := s.stepIndex(context.Background(), cid)
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			return
		}
	}
	t.Fatal("index did not finish")
}
func putTestKnowledge(t *testing.T, s *Server, h *httptest.Server, operator, cid, fid, term, content string) kb.KnowledgeDocument {
	t.Helper()
	d := testKnowledge(t, fid, term, content)
	op(t, h, operator, "knowledge.put", cid, testBatch(d), 200)
	warmTestKnowledge(t, s, cid)
	return d
}

func TestCurrentKnowledgeOwnerOnlyAndRetry(t *testing.T) {
	s, h, owner := searchFixture(t)
	a := create(t, h, owner, "current-doom", "public", "")
	b := create(t, h, owner, "current-godzilla", "private", "")
	first := testKnowledge(t, "", "timescale", "Doom timescale original")
	batch := testBatch(first)
	receipt := unpack[kb.KnowledgeReceipt](t, op(t, h, owner, "knowledge.put", a.ID, batch, 200))
	if receipt.Updated != 1 {
		t.Fatal(receipt)
	}
	changed := testKnowledge(t, first.ID, "timescale", "Doom timescale corrected")
	op(t, h, owner, "knowledge.put", a.ID, testBatch(changed), 200)
	replay := unpack[kb.KnowledgeReceipt](t, op(t, h, owner, "knowledge.put", a.ID, batch, 200))
	if replay.Sequence != receipt.Sequence {
		t.Fatal("retry did not return original receipt")
	}
	got := unpack[kb.KnowledgeDocument](t, op(t, h, owner, "knowledge.get", a.ID, map[string]string{"id": first.ID}, 200))
	if got.Fingerprint != changed.Fingerprint {
		t.Fatal("old retry overwrote current content")
	}
	batch.Documents = []kb.KnowledgeDocument{changed}
	op(t, h, owner, "knowledge.put", a.ID, batch, 409)
	same := unpack[kb.KnowledgeReceipt](t, op(t, h, owner, "knowledge.put", a.ID, testBatch(changed), 200))
	if same.Unchanged != 1 || same.Updated != 0 {
		t.Fatal(same)
	}
	reader := joinReader(t, h, owner, a, false)
	for _, action := range []string{"knowledge.put", "knowledge.delete", "knowledge.inventory", "submission.create", "submission.review", "member.set", "finding.assess", "community.create", "community.update", "community.delete"} {
		op(t, h, reader, action, a.ID, testBatch(changed), 403)
	}
	for _, action := range []string{"submission.create", "submission.review", "finding.history", "publication.resolve", "health.review", "member.set"} {
		op(t, h, owner, action, a.ID, nil, 400)
	}
	op(t, h, reader, "knowledge.get", b.ID, map[string]string{"id": first.ID}, 404)
	// Database policies independently reject a reader even if an internal route is wrong.
	var p principal
	if e := s.DB.QueryRow(context.Background(), `SELECT id::text,community_id::text FROM community.reader_connections WHERE public_key=$1`, testPublicKey(reader)).Scan(&p.tokenID, &p.communityID); e != nil {
		t.Fatal(e)
	}
	ctx := context.WithValue(context.Background(), principalKey{}, p)
	tx, e := s.begin(ctx, "")
	if e != nil {
		t.Fatal(e)
	}
	result, e := tx.Exec(ctx, `DELETE FROM community.documents WHERE community_id=$1`, a.ID)
	if e != nil || result.RowsAffected() != 0 {
		t.Fatalf("reader write: %v %v", result, e)
	}
	tx.Rollback(ctx)
	warmTestKnowledge(t, s, a.ID)
	resultQuery := unpack[kb.QueryResult](t, op(t, h, reader, "query", a.ID, map[string]string{"query": "timescale"}, 200))
	if len(resultQuery.Hits) != 1 || resultQuery.Hits[0].Fingerprint != changed.Fingerprint || !strings.Contains(resultQuery.Hits[0].Snippet, "corrected") {
		t.Fatal(resultQuery)
	}
	invalid := changed
	invalid.Fingerprint = strings.Repeat("0", 64)
	op(t, h, owner, "knowledge.put", a.ID, testBatch(invalid), 400)
	old := testBatch(changed)
	old.CreatedAt = time.Now().Add(-25 * time.Hour).Unix()
	op(t, h, owner, "knowledge.put", a.ID, old, 409)
	duplicate := testBatch(changed, changed)
	op(t, h, owner, "knowledge.put", a.ID, duplicate, 400)
	op(t, h, owner, "knowledge.delete", a.ID, map[string]any{"ids": []string{first.ID}}, 200)
	op(t, h, reader, "knowledge.get", a.ID, map[string]string{"id": first.ID}, 404)
	warmTestKnowledge(t, s, a.ID)
	empty := unpack[kb.QueryResult](t, op(t, h, reader, "query", a.ID, map[string]string{"query": "timescale"}, 200))
	if len(empty.Hits) != 0 {
		t.Fatal("deleted document remained searchable", empty)
	}
	var tables int
	if e = s.DB.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='community' AND table_name IN ('revisions','submissions','members','reviews')`).Scan(&tables); e != nil || tables != 0 {
		t.Fatal("legacy workflow tables remain", tables, e)
	}
}

func TestInventoryPaginationCapacityAndCommunityIsolation(t *testing.T) {
	s, h, owner := searchFixture(t)
	a := create(t, h, owner, "inventory-doom", "private", "")
	b := create(t, h, owner, "inventory-godzilla", "private", "")
	shared := id()
	for start := 0; start < 520; start += 40 {
		docs := []kb.KnowledgeDocument{}
		for n := start; n < min(start+40, 520); n++ {
			fid := ""
			if n == 0 {
				fid = shared
			}
			docs = append(docs, testKnowledge(t, fid, fmt.Sprintf("term%d", n), "Doom knowledge"))
		}
		op(t, h, owner, "knowledge.put", a.ID, testBatch(docs...), 200)
	}
	putTestKnowledge(t, s, h, owner, b.ID, shared, "godzilla", "Godzilla separate knowledge")
	first := unpack[kb.KnowledgePage](t, op(t, h, owner, "knowledge.inventory", a.ID, nil, 200))
	if len(first.Items) != 500 || first.Next == "" {
		t.Fatal(len(first.Items), first.Next)
	}
	second := unpack[kb.KnowledgePage](t, op(t, h, owner, "knowledge.inventory", a.ID, map[string]any{"after": first.Next, "sequence": first.Sequence}, 200))
	if len(second.Items) != 20 || second.Next != "" {
		t.Fatal(second)
	}
	t.Setenv("MAX_COMMUNITY_FINDINGS", "520")
	op(t, h, owner, "knowledge.put", a.ID, testBatch(testKnowledge(t, "", "extra", "capacity")), 507)
	update := testKnowledge(t, shared, "term0", "changed Doom")
	op(t, h, owner, "knowledge.put", a.ID, testBatch(update), 200)
	op(t, h, owner, "knowledge.inventory", a.ID, map[string]any{"after": first.Next, "sequence": first.Sequence}, 409)
	other := unpack[kb.KnowledgeDocument](t, op(t, h, owner, "knowledge.get", b.ID, map[string]string{"id": shared}, 200))
	if !strings.Contains(other.Markdown, "Godzilla") {
		t.Fatal("same UUID crossed communities")
	}
}

// Test real retrieval accuracy under concurrent authenticated readers and bulk
// writes. Unique probe terms make missing, wrong, or cross-community hits failures.
func TestBulkUploadWithConcurrentAccurateRetrieval(t *testing.T) {
	runKnowledgeLoad(t, 48, 1200, 480, 2, 48)
}
func TestRemoteSearchLoad(t *testing.T) {
	if os.Getenv("RUN_SEARCH_LOAD") != "1" {
		t.Skip("opt-in load test")
	}
	runKnowledgeLoad(t, setting("LOAD_USERS", 1000), setting("LOAD_REQUESTS", 2000), setting("LOAD_DOCUMENTS", 2000), 4, 128)
}
func runKnowledgeLoad(t *testing.T, users, requests, documents, communityCount, concurrency int) {
	t.Setenv("MAX_UNAUTHENTICATED_REQUESTS_PER_IP_MINUTE", "100000")
	s, h, owner := searchFixture(t)
	ctx := context.Background()
	communities := []kb.Library{}
	anchors := [][]kb.KnowledgeDocument{}
	anchorsPerCommunity := setting("LOAD_ANCHORS", 1)
	anchorIDs := make([]string, anchorsPerCommunity)
	for i := range anchorIDs {
		anchorIDs[i] = id()
	}
	anchorTerm := func(i int) string {
		if anchorsPerCommunity == 1 {
			return "anchorprobe"
		}
		return fmt.Sprintf("anchorprobe_%d", i)
	}
	readers := make([][]string, communityCount)
	probes := make([]kb.KnowledgeDocument, communityCount)
	for i := 0; i < communityCount; i++ {
		visibility := "public"
		if i%2 == 1 {
			visibility = "private"
		}
		l := create(t, h, owner, fmt.Sprintf("load-%d", i), visibility, "")
		communities = append(communities, l)
		seed := []kb.KnowledgeDocument{}
		for j := range anchorIDs {
			// Deliberately reuse IDs across communities with different content.
			seed = append(seed, testKnowledge(t, anchorIDs[j], anchorTerm(j), fmt.Sprintf("Correct community %d anchor %d knowledge", i, j)))
		}
		op(t, h, owner, "knowledge.put", l.ID, testBatch(seed...), 200)
		warmTestKnowledge(t, s, l.ID)
		anchors = append(anchors, seed)
	}
	for n := 0; n < users; n++ {
		i := n % communityCount
		l := communities[i]
		readers[i] = append(readers[i], joinReader(t, h, owner, l, l.Visibility == "private"))
	}
	stop, cancel := context.WithCancel(ctx)
	defer cancel()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); s.runIndexWorker(stop) }()
	errs := make(chan error, requests*2+communityCount*documents)
	durations := make(chan time.Duration, requests)
	var wg sync.WaitGroup
	started := time.Now()
	for i := range communities {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for start := 0; start < documents; start += 40 {
				docs := []kb.KnowledgeDocument{}
				for n := start; n < min(start+40, documents); n++ {
					docs = append(docs, testKnowledge(t, "", fmt.Sprintf("bulk%d_%d", i, n), strings.Repeat("Synthetic bulk knowledge paragraph. ", 600)))
				}
				if start == 0 {
					probes[i] = docs[0]
				}
				client := kb.Client{URL: h.URL, Token: owner, HTTP: h.Client()}
				var receipt kb.KnowledgeReceipt
				if e := client.Operation(ctx, "knowledge.put", communities[i].ID, testBatch(docs...), &receipt); e != nil {
					errs <- e
					return
				}
				if receipt.Updated != len(docs) {
					errs <- fmt.Errorf("incomplete batch: %+v", receipt)
				}
			}
		}(i)
	}
	jobs := make(chan int)
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				i := n % communityCount
				l := communities[i]
				reader := readers[i][(n/communityCount)%len(readers[i])]
				client := kb.Client{URL: h.URL, HTTP: h.Client(), PrivateKey: testPrivateKey(reader), KeyCommunity: l.ID}
				begin := time.Now()
				anchorIndex := (n / communityCount) % anchorsPerCommunity
				anchor := anchors[i][anchorIndex]
				var result kb.QueryResult
				e := client.Operation(ctx, "query", l.ID, map[string]any{"query": anchorTerm(anchorIndex), "limit": 1, "compare_ids": anchorIDs}, &result)
				if e != nil || !result.ComparisonComplete || len(result.Compared) != len(anchorIDs) || len(result.Hits) != 1 || result.Hits[0].CommunityID != l.ID || result.Hits[0].FindingID != anchor.ID || result.Hits[0].Fingerprint != anchor.Fingerprint {
					errs <- fmt.Errorf("incorrect retrieval: %v %+v", e, result)
					durations <- time.Since(begin)
					continue
				}
				for _, entry := range result.Compared {
					correct := false
					for _, expected := range anchors[i] {
						if entry.ID == expected.ID && entry.Fingerprint == expected.Fingerprint && entry.Sequence > 0 {
							correct = true
							break
						}
					}
					if !correct {
						errs <- fmt.Errorf("incorrect comparison metadata: %+v", entry)
						break
					}
				}
				var doc kb.KnowledgeDocument
				e = client.Operation(ctx, "knowledge.get", l.ID, map[string]string{"id": result.Hits[0].FindingID}, &doc)
				if e != nil || doc.ID != anchor.ID || doc.Fingerprint != anchor.Fingerprint || doc.Markdown != anchor.Markdown {
					errs <- fmt.Errorf("incorrect full document: %v %+v", e, doc)
				}
				durations <- time.Since(begin)
			}
		}()
	}
	for n := 0; n < requests; n++ {
		jobs <- n
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(started)
	// Let the real background worker catch up at its normal pace. Forcing
	// synchronous index steps here would hide upload-to-search latency.
	catchupStarted := time.Now()
	for {
		ready := true
		for _, l := range communities {
			var stored int64
			if err := s.DB.QueryRow(ctx, `SELECT sequence FROM community.libraries WHERE id=$1`, l.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			release, err := s.lockIndex(ctx, l.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			db, err := engine.ReadRemoteIndex(s.indexRoot(l.ID))
			indexed := ""
			if err == nil {
				indexed = progress(ctx, db, "sequence")
				db.Close()
			}
			release()
			if err != nil || indexed != fmt.Sprint(stored) {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Since(catchupStarted) > 2*time.Minute {
			t.Fatal("background indexing did not catch up")
		}
		time.Sleep(50 * time.Millisecond)
	}
	indexCatchup := time.Since(catchupStarted)
	cancel()
	<-workerDone
	close(errs)
	close(durations)
	failures := 0
	for e := range errs {
		failures++
		t.Error(e)
	}
	latencies := []time.Duration{}
	for d := range durations {
		latencies = append(latencies, d)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) != requests {
		t.Fatal("missing requests", len(latencies))
	}
	for _, l := range communities {
		var count int
		if e := s.DB.QueryRow(ctx, `SELECT count(*) FROM community.documents WHERE community_id=$1`, l.ID).Scan(&count); e != nil || count != documents+anchorsPerCommunity {
			t.Fatal(count, e)
		}
	}
	for i, l := range communities {
		got := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": fmt.Sprintf("bulk%d_0", i), "limit": 1}, 200))
		if len(got.Hits) != 1 || got.Hits[0].FindingID != probes[i].ID || got.Hits[0].Fingerprint != probes[i].Fingerprint {
			t.Fatal("uploaded bulk content did not retrieve correctly", got)
		}
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	report := map[string]any{"reader_keys": users, "communities": communityCount, "queried_documents": anchorsPerCommunity * communityCount, "concurrent_workflows": concurrency, "query_and_read_workflows": requests, "uploaded_documents": documents * communityCount, "failures": failures, "elapsed_seconds": elapsed.Seconds(), "p50_ms": float64(latencies[len(latencies)/2].Microseconds()) / 1000, "p95_ms": float64(latencies[len(latencies)*95/100].Microseconds()) / 1000, "max_ms": float64(latencies[len(latencies)-1].Microseconds()) / 1000, "go_heap_bytes_after": m.HeapAlloc, "reader_scheduler": s.scheduler.stats(), "upload_scheduler": s.uploadScheduler.stats()}
	report["index_catchup_seconds"] = indexCatchup.Seconds()
	report["indexed_total_seconds"] = elapsed.Seconds() + indexCatchup.Seconds()
	if peak, e := os.ReadFile("/sys/fs/cgroup/memory.peak"); e == nil {
		report["cgroup_peak_bytes"] = strings.TrimSpace(string(peak))
	}
	fmt.Printf("LOAD_REPORT=%s\n", raw(report))
	if s.scheduler.stats()["running"].(int) != 0 || s.uploadScheduler.stats()["running"].(int) != 0 {
		t.Fatal("capacity leaked")
	}
}

func TestDerivedIndexRecoversAfterCorruption(t *testing.T) {
	s, h, owner := searchFixture(t)
	l := create(t, h, owner, "repair-index", "private", "")
	d := putTestKnowledge(t, s, h, owner, l.ID, "", "repairterm", "Retained PostgreSQL knowledge")
	p := filepath.Join(s.indexRoot(l.ID), ".re-discipline", "index.db")
	if e := os.WriteFile(p, []byte("damaged derived index"), 0600); e != nil {
		t.Fatal(e)
	}
	op(t, h, owner, "query", l.ID, map[string]string{"query": "repairterm"}, 503)
	warmTestKnowledge(t, s, l.ID)
	got := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]string{"query": "repairterm"}, 200))
	if len(got.Hits) != 1 || got.Hits[0].Fingerprint != d.Fingerprint {
		t.Fatal("recovery changed knowledge", got)
	}
}

func TestQueriesServeAvailableIndexWithoutLagWarnings(t *testing.T) {
	s, h, owner := searchFixture(t)
	l := create(t, h, owner, "quiet-index", "public", "")
	warmTestKnowledge(t, s, l.ID) // An available empty index is a valid answer too.
	old := testKnowledge(t, "", "clockprobe", "Clock used 60 Hz")
	op(t, h, owner, "knowledge.put", l.ID, testBatch(old), 200)
	empty := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]string{"query": "clockprobe"}, 200))
	if len(empty.Hits) != 0 || len(empty.Warnings) != 0 {
		t.Fatal("available empty index did not serve quietly", empty)
	}
	warmTestKnowledge(t, s, l.ID)
	updated := testKnowledge(t, old.ID, "clockprobe", "Clock uses 120 Hz")
	op(t, h, owner, "knowledge.put", l.ID, testBatch(updated), 200)
	query := func(term string) kb.QueryResult {
		t.Helper()
		got := unpack[kb.QueryResult](t, op(t, h, owner, "query", l.ID, map[string]any{"query": term, "limit": 50}, 200))
		if len(got.Warnings) != 0 {
			t.Fatal("ordinary indexing emitted a warning", got.Warnings)
		}
		return got
	}
	got := query("clockprobe")
	if len(got.Hits) != 1 || got.Hits[0].Fingerprint != old.Fingerprint || !strings.Contains(got.Hits[0].Snippet, "60 Hz") {
		t.Fatal("available indexed content changed before indexing", got)
	}
	current := unpack[kb.KnowledgeDocument](t, op(t, h, owner, "knowledge.get", l.ID, map[string]string{"id": old.ID}, 200))
	if current.Fingerprint != updated.Fingerprint {
		t.Fatal("stored content was delayed", current)
	}
	warmTestKnowledge(t, s, l.ID)
	got = query("clockprobe")
	if len(got.Hits) != 1 || got.Hits[0].Fingerprint != updated.Fingerprint || !strings.Contains(got.Hits[0].Snippet, "120 Hz") {
		t.Fatal("indexed replacement mismatch", got)
	}
	// A bulk upload becomes visible in bounded chunks; each hit must have
	// matching text and identity even before the complete batch is indexed.
	docs := []kb.KnowledgeDocument{}
	expected := map[string]kb.KnowledgeDocument{}
	for n := 0; n < 40; n++ {
		d := testKnowledge(t, "", "batchprobe", fmt.Sprintf("Batch knowledge item %d", n))
		docs = append(docs, d)
		expected[d.ID] = d
	}
	op(t, h, owner, "knowledge.put", l.ID, testBatch(docs...), 200)
	if _, err := s.stepIndex(context.Background(), l.ID); err != nil {
		t.Fatal(err)
	}
	got = query("batchprobe")
	if len(got.Hits) != 32 {
		t.Fatalf("expected one bounded index batch, got %d", len(got.Hits))
	}
	for _, hit := range got.Hits {
		d, ok := expected[hit.FindingID]
		if !ok || hit.Fingerprint != d.Fingerprint || hit.CommunityID != l.ID {
			t.Fatal("index/text identity mismatch", hit)
		}
	}
	warmTestKnowledge(t, s, l.ID)
	if got := query("batchprobe"); len(got.Hits) != 40 {
		t.Fatal("bulk indexing did not finish", got)
	}
}

func TestQueryComparisonIsScopedBoundedAndFromIndexedContent(t *testing.T) {
	s, h, owner := searchFixture(t)
	a := create(t, h, owner, "comparison-doom", "public", "")
	b := create(t, h, owner, "comparison-godzilla", "public", "")
	original := putTestKnowledge(t, s, h, owner, a.ID, "", "oldprobe", "Old Doom claim")
	putTestKnowledge(t, s, h, owner, b.ID, original.ID, "newprobe", "Other community claim")
	privateOnly := putTestKnowledge(t, s, h, owner, b.ID, "", "secretprobe", "Other community only")
	reader := joinReader(t, h, owner, a, false)
	query := func() kb.QueryResult {
		return unpack[kb.QueryResult](t, op(t, h, reader, "query", a.ID, map[string]any{"query": "newprobe", "compare_ids": []string{original.ID, privateOnly.ID}}, 200))
	}
	first := query()
	if !first.ComparisonComplete || len(first.Hits) != 0 || len(first.Compared) != 1 || first.Compared[0].Fingerprint != original.Fingerprint || first.Compared[0].Sequence <= 0 {
		t.Fatal("comparison leaked another community", first)
	}
	updated := testKnowledge(t, original.ID, "newprobe", "Corrected Doom claim")
	receipt := unpack[kb.KnowledgeReceipt](t, op(t, h, owner, "knowledge.put", a.ID, testBatch(updated), 200))
	pending := query()
	if len(pending.Hits) != 0 || len(pending.Compared) != 1 || pending.Compared[0].Fingerprint != original.Fingerprint || pending.Compared[0].Sequence >= receipt.Sequence || len(pending.Warnings) != 0 {
		t.Fatal("comparison bypassed index snapshot", pending)
	}
	warmTestKnowledge(t, s, a.ID)
	current := query()
	if len(current.Hits) != 1 || len(current.Compared) != 1 || current.Hits[0].Fingerprint != updated.Fingerprint || current.Hits[0].Sequence != current.Compared[0].Sequence || current.Compared[0].Sequence != receipt.Sequence {
		t.Fatal("comparison/text mismatch", current)
	}
	ids := []string{}
	for range 128 {
		ids = append(ids, id())
	}
	op(t, h, reader, "query", a.ID, map[string]any{"query": "newprobe", "compare_ids": ids}, 200)
	ids = append(ids, id())
	op(t, h, reader, "query", a.ID, map[string]any{"query": "newprobe", "compare_ids": ids}, 400)
	op(t, h, reader, "query", a.ID, map[string]any{"query": "newprobe", "compare_ids": []string{"invalid"}}, 400)
	op(t, h, reader, "query", b.ID, map[string]any{"query": "newprobe", "compare_ids": []string{original.ID}}, 404)
}

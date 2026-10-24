package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

// Opt-in saturation test of the production HTTP handler, real PostgreSQL RLS and
// native retrieval engine. Every run owns a disposable database and cache.
func TestRemoteSearchLoad(t *testing.T) {
	if os.Getenv("RUN_SEARCH_LOAD") != "1" {
		t.Skip("run scripts/community-load.py")
	}
	s, h, operator := searchFixture(t)
	// Match the production connection pool and worker-role setup.
	poolConfig := s.DB.Config()
	poolConfig.MaxConns = int32(s.budget.DBConnections)
	poolConfig.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE re_community_worker`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	s.DB = pool
	t.Cleanup(pool.Close)
	users, documents, requests := setting("LOAD_USERS", 1000), setting("LOAD_DOCUMENTS", 2000), setting("LOAD_REQUESTS", 2000)
	communities := []kb.Library{}
	for _, slug := range []string{"load-doom", "load-godzilla", "load-third", "load-fourth"} {
		communities = append(communities, create(t, h, operator, slug, "public", "trusted"))
	}
	ctx, cancelMaintenance := context.WithCancel(context.Background())
	defer cancelMaintenance()
	truth := make([]map[string]loadTruth, 4)
	background := make([][]loadTruth, 4)
	qualityQuestions := make([][]loadQuestion, 4)
	totalDocuments := documents + len(loadAccuracyDocuments(0, ""))
	for ci, l := range communities {
		truth[ci] = map[string]loadTruth{}
		fixtures := []kb.Document{}
		for n := 0; n < documents; n++ {
			doc := document()
			doc.SourcePath = fmt.Sprintf("docs/load-%d.md", n)
			doc.Build = l.Slug
			doc.Markdown = fmt.Sprintf("---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Engine clock subsystem %d\nloadcommunity%d timescale controls simulation timing for subsystem %d.\n", n, ci, n) + fmt.Sprintf("Unique term loadmarker%d.\n", n)
			for j := 0; j < 8; j++ {
				doc.Markdown += fmt.Sprintf("Observation %d: a simulation step of 16 milliseconds with scale 0.5 advances the subsystem by 8 milliseconds. The recorded sequence preserves ordering and does not change the render clock.\n", j)
			}
			fixtures = append(fixtures, doc)
		}
		fixtures = append(fixtures, loadAccuracyDocuments(ci, l.Slug)...)
		qualityDocs := []loadTruth{}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for n, doc := range fixtures {
			fid, rid := id(), id()
			d := loadTruth{fid, rid, kb.TextDigest(doc.Markdown), kb.ContentDigest(doc), doc.Build, doc.Kind, doc.Grade}
			truth[ci][fid] = d
			if n < documents {
				background[ci] = append(background[ci], d)
			} else {
				qualityDocs = append(qualityDocs, d)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO community.findings(community_id,id,revision) VALUES($1,$2,$3)`, l.ID, fid, rid); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,author_id,content_digest,text_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, rid, l.ID, fid, n+1, raw(doc), s.Config.OperatorID, kb.ContentDigest(doc), kb.TextDigest(doc.Markdown)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE community.libraries SET sequence=$2 WHERE id=$1`, l.ID, totalDocuments); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		qualityQuestions[ci] = loadAccuracyQuestions(ci, qualityDocs)
		op(t, h, operator, "query", l.ID, map[string]any{"query": "timescale", "limit": 5}, 200)
	}
	// Exercise real signed public/private establishment before retrieval. Each
	// loopback test client is a separate project; the shared-IP setup guard is
	// tested independently and raised here to represent many source addresses.
	t.Setenv("MAX_UNAUTHENTICATED_REQUESTS_PER_IP_MINUTE", strconv.Itoa(users+100))
	for i := 2; i < 4; i++ {
		l := communities[i]
		l.Visibility = "private"
		communities[i] = unpack[kb.Library](t, op(t, h, operator, "community.update", l.ID, map[string]any{"name": l.Name, "visibility": "private", "policy": l.Policy, "expected_version": l.PolicyVersion}, 200))
	}
	tokens := make([][]string, 4)
	for i := 0; i < users; i++ {
		tokens[i%4] = append(tokens[i%4], newReaderKey())
	}
	invitations := make(map[string]string)
	for ci := 2; ci < 4; ci++ {
		for _, key := range tokens[ci] {
			grant := unpack[map[string]any](t, op(t, h, operator, "access.create", communities[ci].ID, nil, 200))
			invitations[key] = grant["invitation"].(string)
		}
	}
	connectStart := time.Now()
	connectJobs := make(chan int)
	connectErrors := make(chan error, users)
	var connectWorkers sync.WaitGroup
	for range 64 {
		connectWorkers.Add(1)
		go func() {
			defer connectWorkers.Done()
			for i := range connectJobs {
				ci := i % 4
				key := tokens[ci][i/4]
				r, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(raw(kb.Request{Action: "community.connect", Community: communities[ci].ID, Data: raw(map[string]string{"invitation": invitations[key]})})))
				authorizeTestRequest(r, key)
				res, e := h.Client().Do(r)
				if e != nil {
					connectErrors <- e
					continue
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != 200 {
					connectErrors <- fmt.Errorf("connect returned %d", res.StatusCode)
				}
			}
		}()
	}
	for i := 0; i < users; i++ {
		connectJobs <- i
	}
	close(connectJobs)
	connectWorkers.Wait()
	close(connectErrors)
	for err := range connectErrors {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	connectSeconds := time.Since(connectStart).Seconds()
	go s.runConnectionMaintenance(ctx)
	// Use the test lifetime for cleanup rather than leaving a maintenance goroutine.
	var peakHeap atomic.Uint64
	stopMemory := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopMemory:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for old := peakHeap.Load(); m.HeapAlloc > old; old = peakHeap.Load() {
					if peakHeap.CompareAndSwap(old, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	defer close(stopMemory)
	transport := &http.Transport{MaxIdleConns: 1024, MaxIdleConnsPerHost: 1024, MaxConnsPerHost: 1024}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 35 * time.Second}
	phases := []map[string]any{}
	scenarios := []loadScenario{{"broad_baseline", 4, "baseline"}, {"single", 64, "single"}, {"single_reader", 64, "single_reader"}, {"reader_skewed_90_percent", 128, "reader_skewed"}, {"equal", 128, "equal"}, {"skewed_90_percent", 128, "skewed"}, {"burst", 1000, "equal"}, {"agent_research", 128, "agent"}, {"accuracy", 128, "accuracy"}, {"accuracy_burst", 1000, "accuracy"}, {"archive_baseline", 128, "archive"}, {"archive_active", 128, "archive"}}
	// Record every distinct planned query once, serially, with the same signed
	// reader endpoint. This establishes stability, not relevance: seed-time
	// judgments above independently establish which answers are correct.
	baselines := make([]map[string][32]byte, 4)
	quietQuality := loadQuality{}
	for ci := range baselines {
		baselines[ci] = map[string][32]byte{}
	}
	for _, scenario := range scenarios {
		for i := 0; i < requests; i++ {
			ci := loadCommunity(scenario.mode, i)
			q := loadSelectQuestion(scenario.mode, i, background[ci], qualityQuestions[ci])
			if _, exists := baselines[ci][q.key()]; exists {
				continue
			}
			r := unpack[loadQueryResult](t, op(t, h, tokens[ci][0], "query", communities[ci].ID, q.data(), 200))
			if !loadIntegrity(r, communities[ci].ID, totalDocuments, truth[ci]) || (!q.empty && len(r.Hits) == 0) {
				t.Fatalf("invalid quiet reference: community=%d question=%s", ci, q.name)
			}
			baselines[ci][q.key()] = loadFingerprint(r)
			quietQuality.observe(q, true, true, loadAnswerRank(q, r), len(r.Hits) == 0)
		}
	}
	t.Logf("quiet reference: %+v", quietQuality)
	if !quietQuality.correct() {
		t.Error("quiet reference missed a seeded answer; load must not conceal baseline relevance failures")
	}
	var archiveTicket string
	var archiveReport map[string]any
	var archiveEvidenceBytes int64
	for _, scenario := range scenarios {
		var finishArchive func() map[string]any
		if scenario.name == "archive_baseline" {
			archiveEvidenceBytes = seedArchiveLoad(t, s, communities[0].ID, totalDocuments)
		}
		if scenario.name == "archive_active" {
			archiveTicket, finishArchive = startArchiveLoad(t, s, h, operator, communities[0].ID)
		}
		phaseRequests := requests
		if scenario.mode == "archive" {
			phaseRequests = max(requests, 10000)
		}
		concurrency := scenario.concurrency
		type outcome struct {
			code        int
			ms          float64
			invalid     bool
			community   int
			readerClass string
			waitMS      float64
			documents   int
			question    loadQuestion
			completed   bool
			same        bool
			rank        int
			empty       bool
		}
		jobs := make(chan int)
		results := make(chan outcome, phaseRequests)
		var workers sync.WaitGroup
		start := time.Now()
		for w := 0; w < concurrency; w++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := range jobs {
					ci := loadCommunity(scenario.mode, i)
					user := (i / 4) % len(tokens[ci])
					if scenario.mode == "single_reader" || (scenario.mode == "reader_skewed" && i%100 < 90) {
						user = 0
					}
					community := communities[ci].ID
					q := loadSelectQuestion(scenario.mode, i, background[ci], qualityQuestions[ci])
					body := raw(kb.Request{Action: "query", Community: community, Data: raw(q.data())})
					req, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(body))
					authorizeTestRequest(req, tokens[ci][user])
					before := time.Now()
					res, err := client.Do(req)
					o := outcome{community: ci, question: q, ms: float64(time.Since(before).Microseconds()) / 1000}
					if scenario.mode == "reader_skewed" {
						o.readerClass = "other_readers"
						if user == 0 {
							o.readerClass = "dominant_reader"
						}
					}
					if err != nil {
						results <- o
						continue
					}
					o.code = res.StatusCode
					o.waitMS, _ = strconv.ParseFloat(strings.TrimPrefix(res.Header.Get("Server-Timing"), "queue;dur="), 64)
					if res.StatusCode == 200 {
						var r loadQueryResult
						err = json.NewDecoder(res.Body).Decode(&r)
						o.completed = err == nil
						o.invalid = err != nil || !loadIntegrity(r, community, totalDocuments, truth[ci]) || (!q.empty && len(r.Hits) == 0)
						o.same = loadFingerprint(r) == baselines[ci][q.key()]
						o.rank, o.empty = loadAnswerRank(q, r), len(r.Hits) == 0
						if scenario.mode == "agent" && !o.invalid {
							for _, hit := range r.Hits[:min(2, len(r.Hits))] {
								fetchBody := raw(kb.Request{Action: "finding.get", Community: community, Data: raw(map[string]string{"id": hit.FindingID})})
								fetch, _ := http.NewRequest("POST", h.URL+"/v1/operations", bytes.NewReader(fetchBody))
								authorizeTestRequest(fetch, tokens[ci][user])
								docResponse, e := client.Do(fetch)
								if e != nil {
									o.code = 0
									break
								}
								if docResponse.StatusCode != 200 {
									o.code = docResponse.StatusCode
									io.Copy(io.Discard, docResponse.Body)
									docResponse.Body.Close()
									break
								}
								var docs []kb.Change
								e = json.NewDecoder(docResponse.Body).Decode(&docs)
								docResponse.Body.Close()
								if e != nil || len(docs) != 1 || docs[0].FindingID != hit.FindingID || docs[0].Revision != hit.Revision || kb.ContentDigest(docs[0].Document) != truth[ci][hit.FindingID].contentDigest {
									o.invalid = true
									break
								}
								o.documents++
							}
						}

					} else {
						io.Copy(io.Discard, res.Body)
					}
					res.Body.Close()
					o.ms = float64(time.Since(before).Microseconds()) / 1000
					results <- o
				}
			}()
		}
		for i := 0; i < phaseRequests; i++ {
			jobs <- i
		}
		close(jobs)
		workers.Wait()
		close(results)
		elapsed := time.Since(start).Seconds()
		codes := map[int]int{}
		latencies := []float64{}
		waits := []float64{}
		perCommunity := map[int]map[int]int{}
		communityLatency := map[int][]float64{}
		readerLatency := map[string][]float64{}
		readerStatus := map[string]map[int]int{}
		invalid := 0
		fetched := 0
		quality := loadQuality{}
		perQuestion := map[string]*loadQuality{}
		for result := range results {
			quality.observe(result.question, result.completed, result.same, result.rank, result.empty)
			if perQuestion[result.question.name] == nil {
				perQuestion[result.question.name] = &loadQuality{}
			}
			perQuestion[result.question.name].observe(result.question, result.completed, result.same, result.rank, result.empty)
			codes[result.code]++
			fetched += result.documents
			if perCommunity[result.community] == nil {
				perCommunity[result.community] = map[int]int{}
			}
			perCommunity[result.community][result.code]++
			if result.readerClass != "" {
				if readerStatus[result.readerClass] == nil {
					readerStatus[result.readerClass] = map[int]int{}
				}
				readerStatus[result.readerClass][result.code]++
				if result.code == 200 {
					readerLatency[result.readerClass] = append(readerLatency[result.readerClass], result.ms)
				}
			}
			if result.code == 200 {
				latencies = append(latencies, result.ms)
				waits = append(waits, result.waitMS)
				communityLatency[result.community] = append(communityLatency[result.community], result.ms)
			}
			if result.invalid {
				invalid++
			}
		}
		sort.Float64s(latencies)
		sort.Float64s(waits)
		wait95 := float64(0)
		if len(waits) > 0 {
			wait95 = waits[int(float64(len(waits)-1)*.95)]
		}
		percentile := func(p float64) float64 {
			if len(latencies) == 0 {
				return 0
			}
			return latencies[int(float64(len(latencies)-1)*p)]
		}
		communityP95 := map[int]float64{}
		for ci, values := range communityLatency {
			sort.Float64s(values)
			communityP95[ci] = values[int(float64(len(values)-1)*.95)]
		}
		phase := map[string]any{"full_documents_fetched": fetched, "per_community_p95_ms": communityP95, "scenario": scenario.name, "queue_p95_ms": wait95, "per_community_status": perCommunity, "scheduler": s.scheduler.stats(), "concurrency": concurrency, "requests": phaseRequests, "seconds": elapsed, "status_counts": codes, "successful_queries_per_second": float64(codes[200]) / elapsed, "success_p50_ms": percentile(.5), "success_p95_ms": percentile(.95), "success_p99_ms": percentile(.99), "invalid_or_cross_community_results": invalid}
		phase["retrieval_quality"], phase["quality_by_question"] = quality, perQuestion
		if len(readerStatus) > 0 {
			readerP95 := map[string]float64{}
			for group, values := range readerLatency {
				sort.Float64s(values)
				readerP95[group] = values[int(float64(len(values)-1)*.95)]
			}
			phase["reader_class_status"], phase["reader_class_p95_ms"] = readerStatus, readerP95
		}
		if finishArchive != nil {
			archiveReport = finishArchive()
			archiveReport["seeded_evidence_bytes"] = archiveEvidenceBytes
			phase["archive"] = archiveReport
		}
		phases = append(phases, phase)
		t.Logf("load scenario=%s concurrency=%d status=%v successful_qps=%.1f p95=%.1fms queue95=%.1fms per_community=%v", scenario.name, concurrency, codes, float64(codes[200])/elapsed, percentile(.95), wait95, perCommunity)
		t.Logf("quality scenario=%s %+v", scenario.name, quality)
		if !quality.correct() {
			t.Errorf("retrieval accuracy or stability failed: %+v", quality)
		}
		if invalid != 0 || codes[200] == 0 || codes[0] != 0 {
			t.Errorf("invalid load results: %+v", phase)
		}
		if !strings.Contains(scenario.name, "burst") {
			if codes[200] != phaseRequests {
				t.Errorf("normal traffic was rejected: %v", codes)
			}
		}
		for ci, counts := range perCommunity {
			if counts[200] == 0 {
				t.Errorf("community %d received no successful service", ci)
			}
		}
		for code := range codes {
			if code != 200 && code != 429 && code != 503 {
				t.Errorf("unexpected HTTP %d", code)
			}
		}
	}
	peakContainer, _ := os.ReadFile("/sys/fs/cgroup/memory.peak")
	deleteStart := time.Now()
	deleted := unpack[deletionResult](t, confirmDeletion(t, h, operator, communities[0].ID, archiveTicket))
	deleteSeconds := time.Since(deleteStart).Seconds()
	if !deleted.Deleted || deleted.CleanupPending {
		t.Fatal("loaded corpus deletion incomplete", deleted)
	}
	for ci := 1; ci < len(communities); ci++ {
		q := loadSelectQuestion("equal", 1, background[ci], qualityQuestions[ci])
		r := unpack[loadQueryResult](t, op(t, h, tokens[ci][0], "query", communities[ci].ID, q.data(), 200))
		if !loadIntegrity(r, communities[ci].ID, totalDocuments, truth[ci]) || loadAnswerRank(q, r) != 1 {
			t.Fatal("deletion affected another community", ci)
		}
	}
	report := map[string]any{"peak_go_heap_bytes": peakHeap.Load(), "container_memory_peak_bytes": strings.TrimSpace(string(peakContainer)), "users": users, "authentication": "Ed25519 signed requests", "public_communities": 2, "private_communities": 2, "connection_seconds": connectSeconds, "communities": 4, "documents_per_community": totalDocuments, "background_documents_per_community": documents, "quiet_reference_quality": quietQuality, "search_budget": s.budget, "max_in_flight_requests": cap(s.requestSlots), "credentials_seeded_as_fixtures": false, "reader_traffic_limits_overridden": false, "setup_ip_limit": users + 100, "phases": phases, "notes": "Warmed immutable indexes; eight throughput scenarios, two judged accuracy scenarios, and matched baseline/export scenarios. Exact ordered response fingerprints compared with serial signed-reader references, seeded answer IDs checked independently, fetched documents checked against seeded content digests. Synthetic judgments, optional semantic assistance disabled, no concurrent corpus updates; not a real-corpus relevance score or production capacity guarantee. One HTTP service, real PostgreSQL; generator shares app resources."}
	report["archive_export"] = archiveReport
	report["community_deletion"] = map[string]any{"seconds": deleteSeconds, "documents_removed": totalDocuments, "cleanup_pending": deleted.CleanupPending, "other_communities_verified": 3}
	b, _ := json.Marshal(report)
	fmt.Println("LOAD_REPORT=" + string(b))
}

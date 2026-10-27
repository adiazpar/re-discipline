package server

// The fixture and generator run in separate processes. Only the fixture's
// container is CPU/memory constrained; extra signing work cannot steal its CPU.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type statsLoadCommunity struct {
	ID        string
	Keys      []string
	Anchors   []kb.KnowledgeDocument
	Documents int64
	Bytes     int64
}
type statsLoadFixture struct {
	URL         string
	Communities []statsLoadCommunity
}

func TestCommunityStatsLoadServer(t *testing.T) {
	dir := os.Getenv("STATS_LOAD_FIXTURE")
	if dir == "" || os.Getenv("STATS_LOAD_MODE") != "server" {
		t.Skip("opt-in external load fixture")
	}
	t.Setenv("MAX_UNAUTHENTICATED_REQUESTS_PER_IP_MINUTE", "100000")
	s, h, owner := searchFixture(t)
	config := s.DB.Config().Copy()
	config.MaxConns = int32(s.budget.DBConnections)
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE re_community_worker`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s.DB = pool
	f := statsLoadFixture{URL: os.Getenv("STATS_LOAD_URL")}
	ids := make([]string, 32)
	for i := range ids {
		ids[i] = id()
	}
	for i := 0; i < 4; i++ {
		visibility := "public"
		if i%2 == 1 {
			visibility = "private"
		}
		l := create(t, h, owner, fmt.Sprintf("summary-load-%d", i), visibility, "")
		c := statsLoadCommunity{ID: l.ID}
		for start := 0; start < 512; start += 32 {
			docs := []kb.KnowledgeDocument{}
			for j := start; j < start+32; j++ {
				fid := ""
				if j < len(ids) {
					fid = ids[j]
				}
				d := testKnowledge(t, fid, fmt.Sprintf("summaryanchor_%d", j), fmt.Sprintf("Community %d unique knowledge %d. %s", i, j, string(bytes.Repeat([]byte("Synthetic documented engine behavior. "), 40))))
				docs = append(docs, d)
				c.Documents++
				c.Bytes += int64(len(d.Markdown))
				if j < len(ids) {
					c.Anchors = append(c.Anchors, d)
				}
			}
			op(t, h, owner, "knowledge.put", l.ID, testBatch(docs...), 200)
		}
		warmTestKnowledge(t, s, l.ID)
		for j := 0; j < 250; j++ {
			c.Keys = append(c.Keys, joinReader(t, h, owner, l, visibility == "private"))
		}
		f.Communities = append(f.Communities, c)
	}
	// Setup used the internal listener. External signed requests use this origin.
	s.Config.PublicURL = f.URL
	background, stopBackground := context.WithCancel(context.Background())
	var backgroundWorkers sync.WaitGroup
	for _, worker := range []func(context.Context){s.runIndexWorker, s.runConnectionMaintenance, s.runDeletionCleanup} {
		backgroundWorkers.Add(1)
		go func(worker func(context.Context)) { defer backgroundWorkers.Done(); worker(background) }(worker)
	}
	t.Cleanup(func() { stopBackground(); backgroundWorkers.Wait() })
	listener := &http.Server{Addr: ":8080", Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	go func() {
		if err := listener.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw(f), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	resources := map[string]any{"budget": s.budget, "scheduler": s.scheduler.stats()}
	for _, name := range []string{"memory.peak", "cpu.stat"} {
		if b, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", name)); err == nil {
			resources[name] = string(b)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), raw(resources), 0600); err != nil {
		t.Error(err)
	}
	if s.scheduler.stats()["running"].(int) != 0 {
		t.Error("search slot leaked")
	}
}

type statsLoadResult struct {
	Name              string    `json:"name"`
	Seconds           float64   `json:"seconds"`
	Workflows         int       `json:"workflows"`
	Failures          int       `json:"failures"`
	IntegrityFailures int       `json:"integrity_failures"`
	P50               float64   `json:"p50_ms"`
	P95               float64   `json:"p95_ms"`
	P99               float64   `json:"p99_ms"`
	Rate              float64   `json:"workflows_per_second"`
	StatsOK           int64     `json:"stats_ok"`
	StatsBusy         int64     `json:"stats_busy"`
	StatsErrors       int64     `json:"stats_errors"`
	StatsDropped      int64     `json:"stats_generator_dropped"`
	CommunityP95      []float64 `json:"community_p95_ms"`
}

func TestCommunityStatsLoadClient(t *testing.T) {
	path := os.Getenv("STATS_LOAD_MANIFEST")
	if path == "" {
		t.Skip("opt-in external load generator")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f statsLoadFixture
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 256
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	seconds := setting("STATS_LOAD_SECONDS", 15)
	rounds := setting("STATS_LOAD_ROUNDS", 3)
	type phase struct {
		name                               string
		concurrency, searchRate, statsRate int
		hot                                bool
	}
	phases := []phase{{"normal", 8, 20, 0, false}, {"normal_with_summaries", 8, 20, 4, false}, {"loaded", 64, 0, 0, false}, {"loaded_with_summaries", 64, 0, 4, false}, {"loaded_with_stats_flood", 64, 0, 1000, false}, {"loaded_with_one_community_flood", 64, 0, 1000, true}}
	reports := []statsLoadResult{}
	for round := 0; round < rounds; round++ {
		// Reverse alternate rounds to reduce systematic warm-up/order bias.
		for k := 0; k < len(phases); k++ {
			idx := k
			if round%2 == 1 {
				idx = len(phases) - 1 - k
			}
			p := phases[idx]
			r := statsLoadResult{Name: fmt.Sprintf("%s_round_%d", p.name, round+1)}
			started := time.Now()
			stop := started.Add(time.Duration(seconds) * time.Second)
			var wg sync.WaitGroup
			var seq atomic.Int64
			var mu sync.Mutex
			var ok, busy, other, dropped atomic.Int64
			latencies := []float64{}
			perCommunity := make([][]float64, len(f.Communities))
			jobs := make(chan int)
			for worker := 0; worker < p.concurrency; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for n := range jobs {
						i := n % len(f.Communities)
						c := f.Communities[i]
						anchorIndex := (n / len(f.Communities)) % len(c.Anchors)
						anchor := c.Anchors[anchorIndex]
						ids := []string{}
						for _, d := range c.Anchors {
							ids = append(ids, d.ID)
						}
						key := c.Keys[(n/len(f.Communities))%len(c.Keys)]
						api := kb.Client{URL: f.URL, HTTP: client, PrivateKey: testPrivateKey(key), KeyCommunity: c.ID}
						begin := time.Now()
						var result kb.QueryResult
						err := api.Operation(context.Background(), "query", c.ID, map[string]any{"query": fmt.Sprintf("summaryanchor_%d", anchorIndex), "limit": 1, "compare_ids": ids}, &result)
						bad := false
						if err == nil {
							bad = !result.ComparisonComplete || len(result.Compared) != len(ids) || len(result.Hits) != 1
							if !bad {
								hit := result.Hits[0]
								bad = hit.CommunityID != c.ID || hit.FindingID != anchor.ID || hit.Fingerprint != anchor.Fingerprint
							}
							for _, entry := range result.Compared {
								match := false
								for _, expected := range c.Anchors {
									if entry.ID == expected.ID && entry.Fingerprint == expected.Fingerprint {
										match = true
										break
									}
								}
								bad = bad || !match
							}
							var doc kb.KnowledgeDocument
							err = api.Operation(context.Background(), "knowledge.get", c.ID, map[string]string{"id": anchor.ID}, &doc)
							if err == nil {
								bad = bad || doc.ID != anchor.ID || doc.Fingerprint != anchor.Fingerprint || doc.Markdown != anchor.Markdown
							}
						}
						ms := float64(time.Since(begin).Microseconds()) / 1000
						mu.Lock()
						r.Workflows++
						if err != nil {
							if r.Failures == 0 {
								t.Logf("first workflow failure in %s: %v", r.Name, err)
							}
							r.Failures++
						}
						if bad {
							r.IntegrityFailures++
						}
						latencies = append(latencies, ms)
						perCommunity[i] = append(perCommunity[i], ms)
						mu.Unlock()
					}
				}()
			}
			statsDone := make(chan struct{})
			go func() {
				defer close(statsDone)
				if p.statsRate == 0 {
					return
				}
				ticker := time.NewTicker(time.Second / time.Duration(p.statsRate))
				defer ticker.Stop()
				slots := make(chan struct{}, 64)
				var swg sync.WaitGroup
				defer swg.Wait()
				for time.Now().Before(stop) {
					<-ticker.C
					select {
					case slots <- struct{}{}:
					default:
						dropped.Add(1)
						continue
					}
					n := int(seq.Add(1))
					swg.Add(1)
					go func() {
						defer swg.Done()
						defer func() { <-slots }()
						i := n % len(f.Communities)
						if p.hot {
							i = 0
						}
						c := f.Communities[i]
						body := raw(kb.Request{Action: "community.stats", Community: c.ID})
						req, _ := http.NewRequest("POST", f.URL+"/v1/operations", bytes.NewReader(body))
						authorizeTestRequest(req, c.Keys[(n/len(f.Communities))%len(c.Keys)])
						res, e := client.Do(req)
						if e != nil {
							other.Add(1)
							return
						}
						defer res.Body.Close()
						b, _ := io.ReadAll(res.Body)
						switch res.StatusCode {
						case 200:
							var out kb.CommunityStats
							if json.Unmarshal(b, &out) != nil || out.CommunityID != c.ID || out.Documents != c.Documents || out.KnowledgeBytes != c.Bytes {
								other.Add(1)
							} else {
								ok.Add(1)
							}
						case 503:
							busy.Add(1)
						default:
							other.Add(1)
						}
					}()
				}
			}()
			var ticker *time.Ticker
			if p.searchRate > 0 {
				ticker = time.NewTicker(time.Second / time.Duration(p.searchRate))
			}
			n := 0
			for time.Now().Before(stop) {
				if ticker != nil {
					<-ticker.C
				}
				jobs <- n
				n++
			}
			if ticker != nil {
				ticker.Stop()
			}
			close(jobs)
			wg.Wait()
			<-statsDone
			r.Seconds = time.Since(started).Seconds()
			r.Rate = float64(r.Workflows) / r.Seconds
			r.StatsOK = ok.Load()
			r.StatsBusy = busy.Load()
			r.StatsErrors = other.Load()
			r.StatsDropped = dropped.Load()
			percentile := func(values []float64, p int) float64 {
				if len(values) == 0 {
					return 0
				}
				sort.Float64s(values)
				return values[min(len(values)-1, len(values)*p/100)]
			}
			r.P50 = percentile(latencies, 50)
			r.P95 = percentile(latencies, 95)
			r.P99 = percentile(latencies, 99)
			for _, values := range perCommunity {
				r.CommunityP95 = append(r.CommunityP95, percentile(values, 95))
			}
			reports = append(reports, r)
			fmt.Printf("STATS_PHASE=%s\n", raw(r))
			if r.Failures+r.IntegrityFailures > 0 || r.StatsErrors > 0 {
				t.Errorf("traffic correctness failed: %s", r.Name)
			}
		}
	}
	if path := os.Getenv("STATS_LOAD_REPORT"); path != "" {
		if err := os.WriteFile(path, raw(reports), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

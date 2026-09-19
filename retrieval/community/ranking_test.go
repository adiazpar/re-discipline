package community

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

type judgmentTransport func(*http.Request) (*http.Response, error)

func (f judgmentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRankingRequiresCompleteJudgmentsAndReusesPrivateCache(t *testing.T) {
	ctx := context.Background()
	client := semantic.New(semantic.Config{Enabled: true}, t.TempDir())
	client.Key = func() string { return "test-key" }
	calls := 0
	client.HTTP = &http.Client{Transport: judgmentTransport(func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		// Run one candidate at a time while seeding, so this counter is serial.
		calls++
		body := `{"model":"` + semantic.Model + `","answers":{"relevance":{"type":"noul","noul":0.99}},"usage":{"input_tokens":12,"output_tokens":4}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	claims := []semantic.Claim{{Title: "Baseline", Body: "Clock can stall", Build: "build 1"}, {Title: "Useful", Body: "Clock damage explains the stall", Build: "build 1"}}
	q := "Why does the clock stop?"
	_, err := client.Evaluate(ctx, "project/private", map[string]any{"question": q, "claim": claims[1]}, semantic.RelevanceQuestions(), false)
	if err != nil {
		t.Fatal(err)
	}
	hits := []Result{{Title: "Baseline"}, {Title: "Useful"}}
	result, status := RankClaims(ctx, client, q, hits, claims, []string{"project/private", "project/private"}, true)
	if status.Mode != "fallback" || result[0].Title != "Baseline" || calls != 1 {
		t.Fatalf("partial judgments reordered or called provider offline: %+v", status)
	}
	client.HTTP = &http.Client{Transport: judgmentTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"model":"` + semantic.Model + `","answers":{"relevance":{"type":"noul","noul":0.1}}}`))}, nil
	})}
	if _, err = client.Evaluate(ctx, "project/private", map[string]any{"question": q, "claim": claims[0]}, semantic.RelevanceQuestions(), false); err != nil {
		t.Fatal(err)
	}
	result, status = RankClaims(ctx, client, q, hits, claims, []string{"project/private", "project/private"}, true)
	if status.Mode != "reranked" || result[0].Title != "Useful" || status.CacheHits != 2 || calls != 2 {
		t.Fatalf("complete cached ranking: %+v %+v", status, result)
	}
	_, status = RankClaims(ctx, client, q, []Result{{Title: "Useful"}}, claims[1:], []string{"another/private"}, true)
	if status.Mode != "fallback" {
		t.Fatal("judgments crossed community scope")
	}
	claims[0].Idents = []string{"clock_stall"}
	_, status = RankClaims(ctx, client, "clock_stall", []Result{{Title: "Baseline"}}, claims[:1], []string{"project/private"}, false)
	if status.Mode != "exact_identifier" || calls != 2 {
		t.Fatal("identifier lookup used probabilistic ranking")
	}
}

func TestRankingReservesTimeForOrdinaryFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	client := semantic.New(semantic.Config{Enabled: true}, t.TempDir())
	client.Key = func() string { return "test-key" }
	client.HTTP = &http.Client{Transport: judgmentTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	hits := make([]Result, 32)
	claims := make([]semantic.Claim, 32)
	namespaces := make([]string, 32)
	for i := range hits {
		hits[i].Title = "Baseline"
		claims[i] = semantic.Claim{Title: "Claim", Body: "Content", Build: "build 1"}
		namespaces[i] = "timeout"
	}
	result, status := RankClaims(ctx, client, "Why?", hits, claims, namespaces, false)
	if status.Mode != "fallback" || len(result) != 32 || ctx.Err() != nil {
		t.Fatalf("provider consumed ordinary response deadline: %+v %v", status, ctx.Err())
	}
}

func TestCommunityExplicitProjectRootIsNotPluginDirectory(t *testing.T) {
	workspace := t.TempDir()
	plugin := t.TempDir()
	result, err := Execute(context.Background(), plugin, Command{Root: workspace, Action: "status"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := ProjectRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["root"] != want {
		t.Fatal("plugin launch directory selected the wrong workspace")
	}
	if _, err = Execute(context.Background(), plugin, Command{Root: "relative-project", Action: "status"}); err == nil {
		t.Fatal("invalid explicit root silently fell back")
	}
}

func TestRankingFreshnessIncludesNestedClaimsAndIgnoresScores(t *testing.T) {
	a := Result{FindingID: "canonical", Revision: "r1", Contributions: []Result{{FindingID: "member", Revision: "m1"}}}
	b := a
	score := .91
	b.Relevance = &score
	if !sameCandidates([]Result{a}, []Result{b}) {
		t.Fatal("ranking score invalidated unchanged source")
	}
	b.Contributions = []Result{{FindingID: "member", Revision: "m2"}}
	if sameCandidates([]Result{a}, []Result{b}) {
		t.Fatal("nested correction escaped freshness validation")
	}
	b = a
	b.Status = "disputed"
	if sameCandidates([]Result{a}, []Result{b}) {
		t.Fatal("validity change escaped freshness validation")
	}
}

package semantic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testClient(t *testing.T, c Config) (*Client, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	client := New(c, t.TempDir())
	client.Key = func() string { return "test-key" }
	client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != Endpoint || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("request did not use the fixed provider and key")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"model":"` + Model + `","answers":{"relevance":{"type":"noul","noul":0.9}},"usage":{"input_tokens":42,"output_tokens":2}}`))}, nil
	})}
	return client, calls
}
func TestOptionalCacheAndAtomicBudget(t *testing.T) {
	client, calls := testClient(t, Config{Enabled: false, DailyRequests: 1})
	if _, err := client.Evaluate(context.Background(), "one", "claim", RelevanceQuestions(), false); err == nil || calls.Load() != 0 {
		t.Fatal("disabled provider was called")
	}
	client.Config.Enabled = true
	if _, err := client.Evaluate(context.Background(), "one", "claim", RelevanceQuestions(), true); err == nil || calls.Load() != 0 {
		t.Fatal("offline cache miss made a call")
	}
	j, err := client.Evaluate(context.Background(), "one", "claim", RelevanceQuestions(), false)
	if err != nil || j.Cached || j.Usage.InputTokens == nil || *j.Usage.InputTokens != 42 {
		t.Fatalf("first judgment: %+v %v", j, err)
	}
	client.Key = func() string { return "" }
	j, err = client.Evaluate(context.Background(), "one", "claim", RelevanceQuestions(), true)
	if err != nil || !j.Cached || calls.Load() != 1 {
		t.Fatal("offline cache did not reuse the exact judgment")
	}
	client.Key = func() string { return "test-key" }
	if _, err = client.Evaluate(context.Background(), "other-community", "claim", RelevanceQuestions(), false); err == nil || calls.Load() != 1 {
		t.Fatal("private namespace reused judgment or budget was bypassed")
	}
}
func TestConcurrentBudgetCannotOverspend(t *testing.T) {
	client, calls := testClient(t, Config{Enabled: true, DailyRequests: 1})
	// Initialize schema independently of racing first opens.
	db, err := client.cache()
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() { client.Evaluate(context.Background(), "community", i, RelevanceQuestions(), false) })
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("sent %d requests against budget 1", calls.Load())
	}
}

func TestPurgeInvalidatesInflightCacheWritesButRetainsBudget(t *testing.T) {
	client, _ := testClient(t, Config{Enabled: true, DailyRequests: 1})
	client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if err := client.Purge(r.Context()); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"model":"` + Model + `","answers":{"relevance":{"type":"noul","noul":0.9}}}`))}, nil
	})}
	if _, err := client.Evaluate(context.Background(), "private", "claim", RelevanceQuestions(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Evaluate(context.Background(), "private", "claim", RelevanceQuestions(), true); err == nil {
		t.Fatal("purged in-flight answer was retained")
	}
	if _, err := client.Evaluate(context.Background(), "private", "claim", RelevanceQuestions(), false); err == nil || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("purge reset daily budget: %v", err)
	}
}

func TestUnresolvedApplicabilityIsNotKnownScope(t *testing.T) {
	for _, build := range []string{"TBD", "?", "not yet determined", "DOOM build unknown", "unconfirmed", "pending"} {
		if KnownScope(build) {
			t.Fatalf("unresolved applicability accepted: %q", build)
		}
	}
	if !KnownScope("DOOM 2016 PC Vulkan build 1123") {
		t.Fatal("specific applicability rejected")
	}
}
func TestMalformedAnswersCannotBecomeJudgments(t *testing.T) {
	questions := map[string]Question{"relation": {Type: "choice", Criteria: map[string]string{"same": "same", "different": "different"}}}
	cases := []string{
		`{"type":"choice","choice":"same","confidence":0.9,"probabilities":{"same":0.1,"different":0.9}}`,
		`{"type":"choice","choice":"same","confidence":0.9,"probabilities":{"same":1,"different":1}}`,
		`{"type":"choice","choice":"invented","confidence":0.9,"probabilities":{"same":1,"different":0}}`,
		`{"type":"choice","choice":"same","probabilities":{"same":1,"different":0}}`,
	}
	for _, body := range cases {
		var a Answer
		json.Unmarshal([]byte(body), &a)
		if Validate(Judgment{Model: Model, Answers: map[string]Answer{"relation": a}}, questions) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if Validate(Judgment{Model: Model, Answers: map[string]Answer{"relevance": {Type: "noul"}}}, RelevanceQuestions()) == nil {
		t.Fatal("missing probability accepted")
	}
}
func TestConfigurationAbsentDoesNotEnableInference(t *testing.T) {
	root := t.TempDir()
	c, err := LoadConfig(root)
	if err != nil || c.Enabled {
		t.Fatalf("optional default: %+v %v", c, err)
	}
	t.Setenv("TYPESAFE_API_KEY", "configured-but-not-consent")
	c, err = LoadConfig(root)
	if err != nil || c.Enabled {
		t.Fatal("a key enabled inference")
	}
	if _, err = os.Stat(ConfigPath(root)); !os.IsNotExist(err) {
		t.Fatal("read created project configuration")
	}
}

func TestScopeAndTruncationPreventEquivalence(t *testing.T) {
	confidence := .99
	j := Judgment{Answers: map[string]Answer{"scope": {Choice: "same", Confidence: &confidence}, "relationship": {Choice: "equivalent", Confidence: &confidence}}}
	left := Claim{Build: "DOOM Vulkan build 1"}
	right := left
	if Relation(j, left, right) != "equivalent" {
		t.Fatal("lost complete comparison")
	}
	right.Truncated = true
	if Relation(j, left, right) != "unknown" {
		t.Fatal("truncation certified equivalence")
	}
	right.Truncated = false
	right.Build = "unknown"
	if Relation(j, left, right) != "unknown" {
		t.Fatal("unknown applicability became a wildcard")
	}
	j.Answers["scope"] = Answer{Choice: "overlap", Confidence: &confidence}
	right = left
	if Relation(j, left, right) != "partial" {
		t.Fatal("overlapping applicability collapsed")
	}
}

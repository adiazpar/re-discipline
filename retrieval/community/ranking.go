package community

import (
	"context"
	"sort"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

// RankClaims shares the typed ranking policy between local and service queries.
// Partial/unavailable judgments retain the entire baseline order.
func RankClaims(ctx context.Context, client *semantic.Client, question string, hits []Result, claims []semantic.Claim, namespaces []string, offline bool) ([]Result, AssistanceStatus) {
	status := AssistanceStatus{Mode: "baseline", Model: semantic.Model}
	if len(hits) != len(claims) || len(hits) != len(namespaces) {
		status.Mode = "fallback"
		status.Reason = "Candidate projection unavailable; baseline preserved."
		return hits, status
	}
	if semantic.IdentifierLookup(question, claims) {
		status.Mode = "exact_identifier"
		return hits, status
	}
	deadline := time.Now().Add(12 * time.Second)
	if parent, ok := ctx.Deadline(); ok && parent.Add(-3*time.Second).Before(deadline) {
		deadline = parent.Add(-3 * time.Second)
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	judgments := make([]semantic.Judgment, len(claims))
	failures := make([]error, len(claims))
	boundedEach(len(claims), func(i int) {
		judgments[i], failures[i] = client.Evaluate(callCtx, namespaces[i], map[string]any{"question": semantic.Redact(question), "claim": claims[i]}, semantic.RelevanceQuestions(), offline)
	})
	best := 0.0
	complete := true
	for i, j := range judgments {
		if failures[i] != nil {
			complete = false
			status.Reason = failures[i].Error()
			continue
		}
		status.Evaluated++
		if j.Cached {
			status.CacheHits++
		} else {
			if j.Usage.InputTokens != nil {
				status.InputTokens += *j.Usage.InputTokens
			} else {
				status.UsageIncomplete = true
			}
			if j.Usage.OutputTokens != nil {
				status.OutputTokens += *j.Usage.OutputTokens
			} else {
				status.UsageIncomplete = true
			}
		}
		p := *j.Answers["relevance"].Noul
		hits[i].Relevance = &p
		if claims[i].Status != "superseded" && claims[i].Status != "refuted" && p > best {
			best = p
		}
	}
	if !complete {
		status.Mode = "fallback"
		return hits, status
	}
	if best < .8 {
		status.Mode = "baseline_retained"
		return hits, status
	}
	status.Mode = "reranked"
	sort.SliceStable(hits, func(i, j int) bool {
		inactive := func(h Result) bool { return h.Status == "superseded" || h.Status == "refuted" }
		if inactive(hits[i]) != inactive(hits[j]) {
			return !inactive(hits[i])
		}
		return *hits[i].Relevance > *hits[j].Relevance
	})
	return hits, status
}

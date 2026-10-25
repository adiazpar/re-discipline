package community

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

type AssistanceStatus struct {
	Mode            string `json:"mode"`
	Model           string `json:"model,omitempty"`
	Evaluated       int    `json:"evaluated"`
	CacheHits       int    `json:"cache_hits"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	UsageIncomplete bool   `json:"usage_incomplete,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func AssistanceInfo(root string) (any, error) {
	config, err := semantic.LoadConfig(root)
	if err != nil {
		return nil, err
	}
	settings, err := LoadSettings(root)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "sources": settings.Mode, "retrieval": settings.Retrieval, "connections": settings.Connections, "assistance": config, "key_configured": semantic.APIKey() != "", "provider": "jev", "model": semantic.Model}, nil
}

func localDocument(root, rel string) (Document, error) {
	var d Document
	p := strings.ReplaceAll(rel, "\\", "/")
	if !strings.HasPrefix(p, "docs/") || strings.Contains(p, "../") || !strings.HasSuffix(p, ".md") {
		return d, fmt.Errorf("expected a curated docs/ Markdown path")
	}
	base, err := filepath.EvalSymlinks(filepath.Join(root, ".re-discipline", "docs"))
	if err != nil {
		return d, err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, ".re-discipline", filepath.FromSlash(p)))
	if err != nil {
		return d, err
	}
	inside, err := filepath.Rel(base, target)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return d, fmt.Errorf("source escaped curated docs")
	}
	st, err := os.Stat(target)
	if err != nil {
		return d, err
	}
	if !st.Mode().IsRegular() || st.Size() > 512*1024 {
		return d, fmt.Errorf("source exceeds 512 KiB read limit")
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return d, err
	}
	parsed := engine.Parse(p, string(b))
	d = Document{SourcePath: p, Markdown: string(b), Kind: parsed.Kind, Grade: parsed.Grade, Build: parsed.Build}
	return d, nil
}

func queryClaim(ctx context.Context, root string, hit Result, settings Settings, offline bool) (semantic.Claim, error) {
	if hit.Source == "local" {
		d, err := localDocument(root, hit.Path)
		if err != nil {
			return semantic.Claim{}, err
		}
		status := engine.Parse(d.SourcePath, d.Markdown).Status
		if hit.TextDigest == "" || hit.TextDigest != TextDigest(d.Markdown) {
			return semantic.Claim{}, fmt.Errorf("local claim changed after indexing")
		}
		if hit.Status != "" {
			status = hit.Status
		}
		return ProjectClaim(d, status), nil
	}
	var conn Connection
	for _, c := range settings.Connections {
		if c.Service == hit.Service && c.CommunityID == hit.CommunityID {
			conn = c
			break
		}
	}
	if conn.CommunityID == "" {
		return semantic.Claim{}, fmt.Errorf("source is no longer connected")
	}
	if offline {
		return semantic.Claim{}, fmt.Errorf("remote claim unavailable offline")
	}
	client, err := NewConnectionClient(conn, root)
	if err != nil {
		return semantic.Claim{}, err
	}
	var doc KnowledgeDocument
	if err = client.Operation(ctx, "knowledge.get", conn.CommunityID, map[string]string{"id": hit.FindingID}, &doc); err != nil {
		return semantic.Claim{}, err
	}
	if doc.ID != hit.FindingID || doc.Fingerprint != hit.Fingerprint {
		return semantic.Claim{}, fmt.Errorf("remote claim changed during query")
	}
	parsed := engine.Parse(doc.Path, doc.Markdown)
	return ProjectClaim(Document{Markdown: doc.Markdown, SourcePath: doc.Path, Kind: parsed.Kind, Grade: parsed.Grade, Build: parsed.Build}, "active"), nil

}

// Query is the common local/community orchestration path. Identity and validity
// metadata survive ranking unchanged; the final limit applies to groups.
func Query(ctx context.Context, root, q, mode string, offline bool, opts engine.Options) (QueryResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 8
	}
	if limit > 128 {
		limit = 128
	}
	config, configErr := semantic.LoadConfig(root)
	if opts.Assistance == "off" {
		config.Enabled = false
	}
	if opts.Assistance != "" && opts.Assistance != "off" && opts.Assistance != "configured" {
		return QueryResult{}, fmt.Errorf("assistance must be configured or off; enable explicitly with assistance.set")
	}
	pool := limit * 4
	if pool < 32 {
		pool = 32
	}
	if config.Enabled && config.Candidates > pool {
		pool = config.Candidates
	}
	if pool > 128 {
		pool = 128
	}
	opts.Limit = pool
	out, err := queryCandidates(ctx, root, q, mode, offline, opts)
	out.Root = root
	out.Assistance = &AssistanceStatus{Mode: "disabled"}
	if err != nil {
		return out, err
	}
	if configErr != nil {
		out.Warnings = append(out.Warnings, configErr.Error())
		config.Enabled = false
	}
	if config.Enabled && len(out.Hits) > 0 {
		out.Assistance = &AssistanceStatus{Mode: "baseline", Model: semantic.Model}
		// Freshness failures are not inputs to a semantic reranker.
		if semantic.APIKey() == "" && !offline {
			out.Assistance.Mode = "fallback"
			out.Assistance.Reason = "TYPESAFE_API_KEY is not configured; baseline preserved."
		} else if hasVariants(out.Hits) {
			out.Assistance.Mode = "baseline_variants"
			out.Assistance.Reason = "Differing community copies retain one grouped result; assistance cannot choose an authority."
		} else if len(out.Warnings) > 0 {
			out.Assistance.Mode = "fallback"
			out.Assistance.Reason = "Source warnings require attention; baseline preserved."
		} else {
			settings, e := LoadSettings(root)
			if e != nil {
				return out, e
			}
			claims := make([]semantic.Claim, len(out.Hits))
			failures := make([]error, len(out.Hits))
			boundedEach(len(out.Hits), func(i int) { claims[i], failures[i] = queryClaim(ctx, root, out.Hits[i], settings, offline) })
			complete := true
			for _, e := range failures {
				if e != nil {
					complete = false
					break
				}
			}
			if !complete {
				out.Assistance.Mode = "fallback"
				out.Assistance.Reason = "A selected claim changed or could not be read; baseline preserved."
			} else if semantic.IdentifierLookup(q, claims) {
				out.Assistance.Mode = "exact_identifier"
			} else {
				client := semantic.New(config, filepath.Join(root, ".re-discipline", "cache", "jev"))
				namespaces := make([]string, len(out.Hits))
				for i, hit := range out.Hits {
					namespaces[i] = root + "/" + hit.Service + "/" + hit.CommunityID
				}
				var status AssistanceStatus
				out.Hits, status = RankClaims(ctx, client, q, out.Hits, claims, namespaces, offline)
				out.Assistance = &status
				if !offline {
					// A provider round trip must not freeze access, corrections or
					// data at the pre-inference state. A fresh ordinary query
					// rechecks each source and fingerprint.
					fresh, e := queryCandidates(ctx, root, q, mode, false, opts)
					if e != nil {
						out.Hits = nil
						return out, e
					}
					latest, e := LoadSettings(root)
					if e != nil {
						out.Hits = nil
						return out, e
					}
					currentConfig, e := semantic.LoadConfig(root)
					if e != nil || !currentConfig.Enabled || semantic.Hash(latest) != semantic.Hash(settings) || len(fresh.Warnings) > 0 || !sameCandidates(out.Hits, fresh.Hits) {
						status.Mode = "fallback"
						status.Reason = "Sources, access or claims changed during assistance; refreshed ordinary results returned."
						out = fresh
						out.Root = root
						out.Assistance = &status
					} else {
						out.Sources = fresh.Sources
					}
				}
			}
		}
		if out.Assistance.Mode == "fallback" {
			out.Warnings = append(out.Warnings, out.Assistance.Reason)
		}
	}
	if len(out.Hits) > limit {
		out.Hits = out.Hits[:limit]
	}
	return out, nil
}

func sameCandidates(left, right []Result) bool {
	if len(left) != len(right) {
		return false
	}
	var strip func(Result) Result
	strip = func(v Result) Result {
		v.Relevance = nil
		return v
	}
	counts := map[string]int{}
	for _, v := range left {
		counts[semantic.Hash(strip(v))]++
	}
	for _, v := range right {
		key := semantic.Hash(strip(v))
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func boundedEach(n int, f func(int)) {
	var wg sync.WaitGroup
	jobs := make(chan int)
	for range min(n, 6) {
		wg.Go(func() {
			for i := range jobs {
				f(i)
			}
		})
	}
	for i := range n {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

func hasVariants(hits []Result) bool {
	for _, h := range hits {
		if len(h.Variants) > 0 {
			return true
		}
	}
	return false
}

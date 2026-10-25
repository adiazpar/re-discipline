package community

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

var regexpAlias = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

type Result struct {
	OtherCopies        []Location `json:"other_copies,omitempty"`
	CreatedAt          string     `json:"created_at,omitempty"`
	KnowledgeUpdatedAt string     `json:"knowledge_updated_at,omitempty"`
	Sequence           int64      `json:"sequence,omitempty"`
	Resolution         string     `json:"resolution,omitempty"`
	ContentDiffers     bool       `json:"content_differs,omitempty"`
	Fingerprint        string     `json:"fingerprint,omitempty"`
	Build              string     `json:"build,omitempty"`
	Relevance          *float64   `json:"relevance,omitempty"`
	TextDigest         string     `json:"text_digest,omitempty"`
	Status             string     `json:"status,omitempty"`
	Warnings           []string   `json:"warnings,omitempty"`
	Service            string     `json:"service,omitempty"`
	Locations          []Location `json:"locations,omitempty"`
	Source             string     `json:"source"`
	CommunityID        string     `json:"community_id,omitempty"`
	FindingID          string     `json:"finding_id,omitempty"`
	Path               string     `json:"path,omitempty"`
	Title              string     `json:"title"`
	Snippet            string     `json:"snippet"`
	Kind               string     `json:"kind"`
	Grade              string     `json:"grade"`
	URL                string     `json:"url,omitempty"`
}
type QueryResult struct {
	Compared           []KnowledgeEntry  `json:"compared,omitempty"`
	ComparisonComplete bool              `json:"comparison_complete,omitempty"`
	Root               string            `json:"root,omitempty"`
	Assistance         *AssistanceStatus `json:"assistance,omitempty"`
	Hits               []Result          `json:"hits"`
	Warnings           []string          `json:"warnings"`
	Sources            []map[string]any  `json:"sources"`
}

func queryCandidates(ctx context.Context, root, q, mode string, offline bool, opts engine.Options) (QueryResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 8
	}
	// Fetch a full page from each source, then collapse before the final limit.
	// Copies across sources cannot consume slots in the resulting page.
	opts.Limit = limit
	s, err := LoadSettings(root)
	out := QueryResult{Hits: []Result{}, Warnings: []string{}, Sources: []map[string]any{}}
	if err != nil {
		return out, err
	}
	if mode == "" {
		mode = s.Mode
	}
	if mode != "local" && mode != "external" && mode != "both" {
		return out, fmt.Errorf("invalid source mode")
	}
	if mode != "local" {
		s.Connections, err = selectConnections(s.Connections, opts.Communities)
		if err != nil {
			return out, err
		}
	} else if len(opts.Communities) > 0 {
		return out, fmt.Errorf("community selection requires external or both source mode")
	}
	if mode != "external" {
		hits, warnings, e := engine.Query(root, q, opts)
		if e != nil {
			return out, e
		}
		out.Warnings = append(out.Warnings, warnings...)
		for _, h := range hits {
			out.Hits = append(out.Hits, Result{Source: "local", Path: h.Path, FindingID: h.FindingID, Fingerprint: h.Fingerprint, Title: h.Title, Snippet: h.Snippet, Kind: h.Kind, Grade: h.Grade, Status: h.Status, Build: h.Build, TextDigest: h.TextDigest, CreatedAt: h.CreatedAt, KnowledgeUpdatedAt: h.KnowledgeUpdatedAt})
		}
	}
	if mode != "local" {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if mode == "both" {
			seen := map[string]bool{}
			for _, h := range out.Hits {
				if validCopyIdentity(h) && !seen[h.FindingID] {
					opts.CompareIDs = append(opts.CompareIDs, h.FindingID)
					seen[h.FindingID] = true
				}
			}
		}
		var remoteResults []remoteResult
		if !offline {
			remoteResults = remoteQueries(ctx, s.Connections, q, opts)
		}
		for i, conn := range s.Connections {
			source := map[string]any{"source": conn.Alias, "retrieval": "remote", "cached": false, "available": false}
			if offline {
				out.Warnings = append(out.Warnings, conn.Alias+": remote community unavailable offline")
			} else {
				remote, e := remoteResults[i].result, remoteResults[i].err
				if e != nil {
					out.Warnings = append(out.Warnings, conn.Alias+": "+e.Error())
				} else {
					source["available"] = true
					if mode == "both" {
						out.Hits, e = reconcileCopies(ctx, root, conn, out.Hits, remote)
						if e != nil {
							return out, e
						}
					} else {
						out.Hits = append(out.Hits, remote.Hits...)
					}
					out.Warnings = append(out.Warnings, remote.Warnings...)
				}
			}
			out.Sources = append(out.Sources, source)
		}
	}
	out.Hits, err = collapseCopies(root, interleaveSources(out.Hits), 128)
	return out, err
}

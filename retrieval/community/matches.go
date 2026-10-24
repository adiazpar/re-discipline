package community

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Matching sends hashes and relative promoted-document paths, never local bodies.
// Server matches are disposable observations, not client assertions of identity.
func matchLocal(ctx context.Context, root string, hits []Result, c Connection, cached bool) ([]Result, error) {
	digests := []string{}
	paths := []string{}
	for i := range hits {
		h := &hits[i]
		if h.Source != "local" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, ".re-discipline", filepath.FromSlash(h.Path)))
		if err != nil {
			continue
		}
		current := TextDigest(string(b))
		if h.TextDigest != "" && h.TextDigest != current {
			return hits, fmt.Errorf("local source changed after indexing; repeat query")
		}
		h.TextDigest = current
		digests = append(digests, h.TextDigest)
		path := ""
		if h.Kind != "ops" && strings.HasPrefix(h.Path, "docs/") && !strings.HasPrefix(h.Path, "docs/ops/") {
			path = h.Path
		}
		paths = append(paths, path)
	}
	if len(digests) == 0 {
		return hits, nil
	}
	matches := []FindingMatch{}
	for offset := 0; offset < len(digests); offset += 50 {
		end := offset + 50
		if end > len(digests) {
			end = len(digests)
		}
		{
			client, err := NewConnectionClient(c, root)
			if err != nil {
				return hits, err
			}
			var page []FindingMatch
			if err = client.Operation(ctx, "finding.match", c.CommunityID, map[string]any{"digests": digests[offset:end], "source_paths": paths[offset:end], "source_namespace": c.SourceNamespace}, &page); err != nil {
				return hits, err
			}
			matches = append(matches, page...)
		}
	}
	for i := range hits {
		h := &hits[i]
		if h.Source != "local" {
			continue
		}
		builds := map[string]bool{}
		for _, m := range matches {
			if m.TextDigest == h.TextDigest && m.Current {
				builds[m.Build] = true
			}
		}
		sourceMatches := []FindingMatch{}
		for _, m := range matches {
			if m.MatchKind == "source" && m.SourcePath == h.Path {
				sourceMatches = append(sourceMatches, m)
			}
		}
		if len(sourceMatches) > 1 {
			h.Warnings = append(h.Warnings, c.Alias+": source path maps to multiple findings; variants remain separate")
		}
		if len(sourceMatches) == 1 {
			m := sourceMatches[0]
			if m.TextDigest != h.TextDigest {
				message := fmt.Sprintf("%s: server source registry links this path to finding %s, but local text differs from the published revision. Treat it as an unverified local variant; compare the published content and evidence before relying on it. Published status: %s. %s", c.Alias, m.FindingID, m.Status, m.Reason)
				if m.ReplacementID != "" {
					message += " Replacement: " + m.ReplacementID
				}
				h.Warnings = append(h.Warnings, message)
				h.Warnings = append(h.Warnings, m.Warnings...)
				if m.Status == "active" || m.Status == "disputed" {
					h.Locations = append(h.Locations, Location{Source: c.Alias, Service: c.Service, CommunityID: c.CommunityID, FindingID: m.FindingID, Revision: m.Revision, DifferentText: true, URL: c.Service + "/communities/" + c.CommunityID + "/findings/" + m.FindingID})
				}
			}
		}
		ambiguousBuild := len(builds) > 1
		if ambiguousBuild {
			h.Warnings = append(h.Warnings, c.Alias+": identical text has multiple applicability records; community variants remain separate")
		}
		for _, m := range matches {
			if m.MatchKind == "source" || h.TextDigest != m.TextDigest {
				continue
			}
			h.Warnings = append(h.Warnings, m.Warnings...)
			if !m.Current || m.Status == "refuted" || m.Status == "superseded" || m.Status == "disputed" {
				message := fmt.Sprintf("%s: matching community finding %s is %s", c.Alias, m.FindingID, m.Status)
				if !m.Current {
					message += "; this local copy matches an older revision"
				}
				if m.Reason != "" {
					message += ". " + m.Reason
				}
				if m.ReplacementID != "" {
					message += " Replacement: " + m.ReplacementID
				}
				if cached {
					message += " (cached assessment; newer changes may exist)"
				}
				h.Warnings = append(h.Warnings, message)
			}
			if !ambiguousBuild && m.Current && (m.Status == "active" || m.Status == "disputed" || m.Status == "") {
				h.Locations = append(h.Locations, Location{Service: c.Service, CommunityID: c.CommunityID, FindingID: m.FindingID, Revision: m.Revision, Source: c.Alias, URL: c.Service + "/communities/" + c.CommunityID + "/findings/" + m.FindingID})
			}
		}
		if len(h.Locations) > 0 {
			local := Location{Source: "local", Path: h.Path}
			present := false
			for _, v := range h.Locations {
				if v.Source == "local" {
					present = true
				}
			}
			if !present {
				h.Locations = append([]Location{local}, h.Locations...)
			}
		}
	}
	return hits, nil
}

package community

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func validateRetrieval(mode string) error {
	if mode != "" && mode != "remote" {
		return fmt.Errorf("community retrieval is remote-only; synchronization is no longer supported")
	}
	return nil
}

// RemoteQuery retrieves only matching results, without synchronizing the library.
func RemoteQuery(ctx context.Context, conn Connection, query string, opts engine.Options) (QueryResult, error) {
	var out QueryResult
	client, err := NewConnectionClient(conn)
	if err != nil {
		return out, err
	}
	payload := map[string]any{"query": query, "limit": opts.Limit, "kind": opts.Kind, "grade": opts.Grade, "assistance": false}
	if len(opts.CompareIDs) > 0 {
		payload["compare_ids"] = opts.CompareIDs
	}
	for attempt := 0; attempt < 3; attempt++ {
		out = QueryResult{}
		err = client.Operation(ctx, "query", conn.CommunityID, payload, &out)
		var api *APIError
		if !errors.As(err, &api) || api.Status != 400 {
			break
		}
		if strings.Contains(api.Message, `unknown field "compare_ids"`) {
			delete(payload, "compare_ids")
			continue
		}
		if strings.Contains(api.Message, `unknown field "assistance"`) {
			delete(payload, "assistance")
			continue
		}
		break
	}
	if err != nil {
		return out, err
	}
	if _, ok := payload["compare_ids"]; !ok {
		out.Compared = nil
		out.ComparisonComplete = false
	}
	requested := map[string]bool{}
	for _, id := range opts.CompareIDs {
		requested[id] = true
	}
	seen := map[string]KnowledgeEntry{}
	for _, entry := range out.Compared {
		if !requested[entry.ID] || seen[entry.ID].ID != "" || !validCopyIdentity(Result{FindingID: entry.ID, Fingerprint: entry.Fingerprint}) || entry.Sequence < 0 {
			return QueryResult{}, fmt.Errorf("invalid community comparison identity")
		}
		seen[entry.ID] = entry
	}
	hitIDs := map[string]bool{}
	for i := range out.Hits {
		h := out.Hits[i]
		if h.Sequence < 0 || (h.FindingID != "" && hitIDs[h.FindingID]) {
			return QueryResult{}, fmt.Errorf("invalid community result identity")
		}
		hitIDs[h.FindingID] = true
		if e, ok := seen[h.FindingID]; ok && (e.Fingerprint != h.Fingerprint || e.Sequence != h.Sequence) {
			return QueryResult{}, fmt.Errorf("community comparison does not match indexed result")
		}

		if !resultScopeMatches(out.Hits[i], conn.CommunityID) {
			return QueryResult{}, fmt.Errorf("community response contains conflicting source identity")
		}
		remoteOrigin(&out.Hits[i], conn)
	}
	return out, nil
}

// Older services may omit identity fields. Explicit contradictory identities
// must never be silently relabeled as evidence from the requested community.
func resultScopeMatches(hit Result, cid string) bool {
	if hit.CommunityID != "" && hit.CommunityID != cid {
		return false
	}
	for _, location := range hit.Locations {
		if location.CommunityID != "" && location.CommunityID != cid {
			return false
		}
	}
	return true
}

func remoteOrigin(hit *Result, conn Connection) {
	hit.OtherCopies = nil
	hit.Resolution = ""
	hit.ContentDiffers = false
	hit.Source, hit.Service, hit.CommunityID = conn.Alias, conn.Service, conn.CommunityID
	hit.Path = ""
	hit.Locations = []Location{{Source: conn.Alias, Service: conn.Service, CommunityID: conn.CommunityID, FindingID: hit.FindingID, Fingerprint: hit.Fingerprint}}
}

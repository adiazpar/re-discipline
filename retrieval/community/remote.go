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
	err = client.Operation(ctx, "query", conn.CommunityID, map[string]any{"query": query, "limit": opts.Limit, "kind": opts.Kind, "grade": opts.Grade, "assistance": false}, &out)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 400 && strings.Contains(apiErr.Message, `unknown field "assistance"`) {
		// Older services have no inference option. Retry this read-only query
		// with their original schema; never retry authentication or server errors.
		err = client.Operation(ctx, "query", conn.CommunityID, map[string]any{"query": query, "limit": opts.Limit, "kind": opts.Kind, "grade": opts.Grade}, &out)
	}
	if err != nil {
		return out, err
	}
	for i := range out.Hits {
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
	for _, nested := range hit.Contributions {
		if !resultScopeMatches(nested, cid) {
			return false
		}
	}
	for _, nested := range hit.Versions {
		if !resultScopeMatches(nested, cid) {
			return false
		}
	}
	return true
}

func remoteOrigin(hit *Result, conn Connection) {
	hit.Source, hit.Service, hit.CommunityID = conn.Alias, conn.Service, conn.CommunityID
	hit.Path = ""
	hit.URL = conn.Service + "/communities/" + conn.CommunityID + "/findings/" + hit.FindingID
	hit.Locations = []Location{{Source: conn.Alias, Service: conn.Service, CommunityID: conn.CommunityID, FindingID: hit.FindingID, Revision: hit.Revision, CanonicalID: hit.CanonicalID, URL: hit.URL}}
	for i := range hit.Contributions {
		remoteOrigin(&hit.Contributions[i], conn)
	}
	for i := range hit.Versions {
		remoteOrigin(&hit.Versions[i], conn)
	}
}

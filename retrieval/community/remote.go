package community

import (
	"context"
	"fmt"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func validateRetrieval(mode string) error {
	if mode != "" && mode != "sync" && mode != "remote" {
		return fmt.Errorf("retrieval must be remote or sync")
	}
	return nil
}

// RemoteQuery retrieves only matching results, without synchronizing the library.
func RemoteQuery(ctx context.Context, conn Connection, query string, opts engine.Options) (QueryResult, error) {
	var out QueryResult
	client, err := NewClient(conn.Service)
	if err != nil {
		return out, err
	}
	err = client.Operation(ctx, "query", conn.CommunityID, map[string]any{"query": query, "limit": opts.Limit, "kind": opts.Kind, "grade": opts.Grade}, &out)
	if err != nil {
		return out, err
	}
	for i := range out.Hits {
		remoteOrigin(&out.Hits[i], conn)
	}
	return out, nil
}

func remoteOrigin(hit *Result, conn Connection) {
	hit.Source, hit.Service, hit.CommunityID = conn.Alias, conn.Service, conn.CommunityID
	hit.Path = ""
	hit.URL = conn.Service + "/communities/" + conn.CommunityID + "/findings/" + hit.FindingID
	hit.Locations = []Location{{Source: conn.Alias, Service: conn.Service, CommunityID: conn.CommunityID, FindingID: hit.FindingID, Revision: hit.Revision, CanonicalID: hit.CanonicalID, URL: hit.URL}}
	for i := range hit.Contributions {
		remoteOrigin(&hit.Contributions[i], conn)
	}
}

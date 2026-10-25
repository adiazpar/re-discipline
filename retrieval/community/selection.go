package community

import (
	"context"
	"fmt"
	"sync"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func selectConnections(connected []Connection, aliases []string) ([]Connection, error) {
	if err := validateConnections(connected); err != nil {
		return nil, err
	}
	if len(aliases) == 0 {
		return connected, nil
	}
	selected := []Connection{}
	seen := map[string]bool{}
	for _, alias := range aliases {
		if seen[alias] {
			continue
		}
		seen[alias] = true
		found := false
		for _, c := range connected {
			if c.Alias == alias {
				selected = append(selected, c)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown community alias %q", alias)
		}
	}
	return selected, nil
}

type remoteResult struct {
	result QueryResult
	err    error
}

// Limit concurrent requests while preserving caller order. The shared deadline
// cancels slow hosts and queued requests; successful sources remain available.
func remoteQueries(ctx context.Context, connections []Connection, query string, opts engine.Options) []remoteResult {
	results := make([]remoteResult, len(connections))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for n := 0; n < min(4, len(connections)); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					results[i].err = ctx.Err()
					continue
				}
				results[i].result, results[i].err = RemoteQuery(ctx, connections[i], query, opts)
			}
		}()
	}
	for i := range connections {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return results
}

// Reject ambiguous or repeated links before any requests or credential access.
func validateConnections(connections []Connection) error {
	aliases, destinations, keys := map[string]bool{}, map[[2]string]bool{}, map[[2]string]bool{}
	for _, c := range connections {
		if !regexpAlias.MatchString(c.Alias) || c.Alias == "local" || c.Alias == "multiple" || aliases[c.Alias] {
			return fmt.Errorf("community aliases must be unique lowercase names; local and multiple are reserved")
		}
		aliases[c.Alias] = true
		if c.CommunityID == "" {
			return fmt.Errorf("community link requires an identity")
		}
		service, err := serviceOrigin(c.Service)
		if err != nil {
			return err
		}
		destination := [2]string{copyService(service), c.CommunityID}
		if destinations[destination] {
			return fmt.Errorf("community already linked; use its existing alias")
		}
		destinations[destination] = true
		if c.KeyRef != "" {
			key := [2]string{copyService(service), c.KeyScope}
			if keys[key] {
				return fmt.Errorf("communities must have separate connection keys; reconnect the duplicate link")
			}
			keys[key] = true
		}
	}
	return nil
}

// A second bounded query compares identities discovered on other communities,
// including copies whose corrected text no longer matches the query. Replace
// the entire source snapshot; never combine old hits with newer comparison hashes.
func completeComparisons(ctx context.Context, connections []Connection, results []remoteResult, query string, opts engine.Options, ids []string) {
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(connections)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				r := results[i]
				if r.err != nil {
					continue
				}
				covered := map[string]bool{}
				for _, h := range r.result.Hits {
					if validCopyIdentity(h) {
						covered[h.FindingID] = true
					}
				}
				if r.result.ComparisonComplete {
					for _, id := range opts.CompareIDs {
						covered[id] = true
					}
				}
				missing := false
				for _, id := range ids {
					if !covered[id] {
						missing = true
						break
					}
				}
				if !missing {
					continue
				}
				compare := opts
				compare.CompareIDs = ids
				fresh, err := RemoteQuery(ctx, connections[i], query, compare)
				if err == nil && !fresh.ComparisonComplete {
					fresh.Warnings = append(fresh.Warnings, connections[i].Alias+": server lacks complete copy comparison; unresolved copies retain their source identity")
				}
				results[i] = remoteResult{result: fresh, err: err}
			}
		}()
	}
	for i := range connections {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
}

package community

import (
	"context"
	"fmt"
	"sync"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func selectConnections(connected []Connection, aliases []string) ([]Connection, error) {
	if len(connected) > 1 {
		return nil, fmt.Errorf("one project connects to one community; disconnect extra legacy links before retrieval")
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

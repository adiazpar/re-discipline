package community

import (
	"context"
	"github.com/adiazpar/re-discipline/retrieval/semantic"
)

// PrepareSelected is one publication route for one or many explicitly selected files.
// Transport chunks and optional selection judgments are implementation details.
func PrepareSelected(ctx context.Context, root string, conn Connection, paths []string, build string, offline bool) (any, error) {
	prepared, err := PrepareBatch(root, conn, paths, build)
	if err != nil {
		return nil, err
	}
	out := prepared.(map[string]any)
	config, err := semantic.LoadConfig(root)
	if err != nil {
		out["assistance_warning"] = err.Error()
		return out, nil
	}
	if !config.Enabled {
		return out, nil
	}
	selections := []Selection{}
	for start := 0; start < len(paths); start += 128 {
		part, err := SelectPublication(ctx, root, conn, paths[start:min(start+128, len(paths))], build, offline)
		if err != nil {
			out["assistance_warning"] = err.Error()
			break
		}
		selections = append(selections, part.(map[string]any)["items"].([]Selection)...)
	}
	out["selection"] = selections
	return out, nil
}

package community

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/adiazpar/re-discipline/retrieval/buildinfo"
	"io"
	"net/http"
	"time"
)

type ServerHealth struct {
	Service          string         `json:"service"`
	OK               bool           `json:"ok"`
	Protocol         int            `json:"protocol"`
	Mode             string         `json:"mode,omitempty"`
	CommunityEnabled *bool          `json:"community_enabled,omitempty"`
	Software         buildinfo.Info `json:"software"`
}

// Ping sends no credentials or local project data, and never follows redirects.
func Ping(ctx context.Context, endpoint string) (ServerHealth, error) {
	var health ServerHealth
	origin, err := serviceOrigin(endpoint)
	if err != nil {
		return health, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/healthz", nil)
	if err != nil {
		return health, err
	}
	client := &http.Client{Transport: communityHTTPTransport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return health, fmt.Errorf("server health check failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return health, fmt.Errorf("server health check returned HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil {
		return health, err
	}
	if len(body) > 8192 || json.Unmarshal(body, &health) != nil {
		return ServerHealth{}, fmt.Errorf("invalid server health response")
	}
	if !health.OK || health.Protocol != ProtocolVersion {
		return ServerHealth{}, fmt.Errorf("server is unhealthy or uses an unsupported protocol (expected %d)", ProtocolVersion)
	}
	health.Service = origin
	return health, nil
}

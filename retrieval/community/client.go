package community

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"crypto/ed25519"
	"github.com/adiazpar/re-discipline/retrieval/connectionauth"
	"github.com/google/uuid"
)

type APIError struct {
	Status     int
	Message    string
	RetryAfter string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("community HTTP %d: %s", e.Status, e.Message)
	if e.RetryAfter != "" {
		message += "; Retry-After: " + e.RetryAfter
	}
	return message
}

type Client struct {
	URL          string
	Token        string
	HTTP         *http.Client
	PrivateKey   ed25519.PrivateKey
	KeyCommunity string
	Invitation   string
}

// Bound connection establishment independently of a connected service's cold
// start. Keep one transport so successive operations reuse healthy connections.
var communityHTTPTransport = connectBoundTransport(http.DefaultTransport, 5*time.Second)

func connectBoundTransport(base http.RoundTripper, timeout time.Duration) http.RoundTripper {
	transport, ok := base.(*http.Transport)
	if !ok {
		return base // Preserve an explicitly supplied custom transport.
	}
	clone := transport.Clone()
	dial := clone.DialContext
	if dial == nil {
		dial = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext
	}
	clone.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return dial(connection, network, address)
	}
	return clone
}

func serviceOrigin(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("service URL must be an origin, for example https://knowledge.example.com")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return "", fmt.Errorf("service requires HTTPS except on localhost")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func NewClient(endpoint string) (*Client, error) {
	base, err := serviceOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	token, explicit := os.LookupEnv("RE_DISCIPLINE_TOKEN")
	if !explicit || strings.TrimRight(os.Getenv("RE_DISCIPLINE_SERVICE"), "/") != base {
		token, err = localOwnerToken(base)
		if err != nil {
			return nil, err
		}
	}
	// A matching explicit empty token selects reader-only use on an operator's
	// machine without changing its home directory or OS credential store.
	// Free hosting can take over 50 seconds to wake an idle service.
	return &Client{URL: base, Token: token, HTTP: &http.Client{Transport: communityHTTPTransport, Timeout: 90 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) callOnce(ctx context.Context, path string, payload, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if len(c.PrivateKey) != 0 {
		if err := connectionauth.Sign(req, c.PrivateKey, time.Now()); err != nil {
			return err
		}
	} else if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var v struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&v)
		return &APIError{Status: res.StatusCode, Message: v.Error, RetryAfter: res.Header.Get("Retry-After")}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64*1024*1024)).Decode(out)
}
func (c *Client) Operation(ctx context.Context, action, library string, payload, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, retryBudgetKey{}, &readRetryBudget{})
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request := Request{Action: action, Community: library, Data: b}
	if c.KeyCommunity != "" && library != c.KeyCommunity {
		return fmt.Errorf("request is outside this project's community")
	}
	use := *c
	if !readOperation(action) && action != "community.connect" && action != "community.leave" && use.Token != "" {
		use.PrivateKey = nil
	}
	err = use.call(ctx, "/v1/operations", request, out)
	var api *APIError
	if len(use.PrivateKey) != 0 && readOperation(action) && errors.As(err, &api) && api.Status == 401 {
		// Once-only public renewal retains the same project key and retry budget.
		// Private or revoked connections cannot silently regain access.
		var grant struct {
			Community Library `json:"community"`
		}
		if e := use.call(ctx, "/v1/operations", Request{Action: "community.connect", Community: library}, &grant); e == nil && grant.Community.ID == library {
			return use.call(ctx, "/v1/operations", request, out)
		} else if errors.As(e, &api) && (api.Status == 403 || api.Status == 404) {
			return fmt.Errorf("connection needs renewed access; private communities require a new invitation: %w", e)
		}
	}
	return err
}
func readOperation(action string) bool {
	switch action {
	case "community.get", "query", "knowledge.get":
		return true
	}
	return false
}

type Connection struct {
	SourceNamespace string `json:"source_namespace,omitempty"`
	KeyRef          string `json:"key_ref,omitempty"`
	PublicKey       string `json:"public_key,omitempty"`
	Alias           string `json:"alias"`
	Service         string `json:"service"`
	CommunityID     string `json:"community_id"`
	Name            string `json:"name"`
}
type Settings struct {
	Retrieval   string       `json:"retrieval"`
	Mode        string       `json:"mode"`
	Connections []Connection `json:"connections"`
	Exclude     []string     `json:"exclude"`
}

func SettingsPath(root string) string { return filepath.Join(root, ".re-discipline", "community.json") }
func LoadSettings(root string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(SettingsPath(root))
	if os.IsNotExist(err) {
		return Settings{Retrieval: "remote", Mode: "local", Connections: []Connection{}, Exclude: []string{}}, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	if err == nil {
		for i := range s.Connections {
			if s.Connections[i].KeyRef != "" {
				s.Connections[i].KeyRef, err = connectionKeyRef(root, s.Connections[i].Service)
				if err != nil {
					return s, err
				}
			}
		}
	}
	if err == nil {
		if s.Retrieval == "sync" {
			s.Retrieval = "remote"
		}
		err = validateRetrieval(s.Retrieval)
	}
	if s.Retrieval == "" {
		s.Retrieval = "remote"
	}
	return s, err
}
func SaveSettings(root string, s Settings) error {
	if len(s.Connections) > 1 {
		return fmt.Errorf("one project connects to one community; disconnect the existing community first")
	}
	if s.Mode != "local" && s.Mode != "external" && s.Mode != "both" {
		return fmt.Errorf("mode must be local, external, or both")
	}
	if err := validateRetrieval(s.Retrieval); err != nil {
		return err
	}
	if s.Retrieval == "" {
		s.Retrieval = "remote"
	}
	return atomicJSON(SettingsPath(root), s)
}
func atomicJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + "." + uuid.NewString() + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

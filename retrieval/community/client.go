package community

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
)

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("community HTTP %d: %s", e.Status, e.Message) }

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func NewClient(endpoint string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("service URL must be an origin, for example https://knowledge.example.com")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return nil, fmt.Errorf("service requires HTTPS except on localhost")
	}
	base := strings.TrimRight(u.String(), "/")
	token, _ := keyring.Get("re-discipline-community", base)
	if e := os.Getenv("RE_DISCIPLINE_TOKEN"); e != "" && strings.TrimRight(os.Getenv("RE_DISCIPLINE_SERVICE"), "/") == base {
		token = e
	}
	// Free hosting can take over 50 seconds to wake an idle service.
	return &Client{URL: base, Token: token, HTTP: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, path string, payload, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
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
		return &APIError{res.StatusCode, v.Error}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64*1024*1024)).Decode(out)
}
func (c *Client) Operation(ctx context.Context, action, library string, payload, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.call(ctx, "/v1/operations", Request{Action: action, Community: library, Data: b}, out)
}
func (c *Client) LoginStart(ctx context.Context) (any, error) {
	var v struct {
		Secret  string `json:"device_secret"`
		Code    string `json:"user_code"`
		URL     string `json:"verification_url"`
		Expires int    `json:"expires_in"`
	}
	if err := c.call(ctx, "/v1/auth/device/start", map[string]any{}, &v); err != nil {
		return nil, err
	}
	if err := keyring.Set("re-discipline-community", c.URL+"/pending", v.Secret); err != nil {
		return nil, fmt.Errorf("cannot store device secret in OS credential store: %w", err)
	}
	return map[string]any{"verification_url": v.URL, "user_code": v.Code, "expires_in": v.Expires, "next": "Approve this code in your browser, then run login.finish."}, nil
}
func (c *Client) LoginFinish(ctx context.Context) (any, error) {
	secret, err := keyring.Get("re-discipline-community", c.URL+"/pending")
	if err != nil {
		return nil, fmt.Errorf("no pending sign-in; run login.start")
	}
	var v struct {
		Token   string `json:"access_token"`
		Pending bool   `json:"pending"`
		Expires string `json:"expires_at"`
	}
	if err = c.call(ctx, "/v1/auth/device/poll", map[string]string{"device_secret": secret}, &v); err != nil {
		return nil, err
	}
	if v.Pending {
		return map[string]bool{"pending": true}, nil
	}
	if v.Token == "" {
		return nil, fmt.Errorf("sign-in returned no token")
	}
	if err = keyring.Set("re-discipline-community", c.URL, v.Token); err != nil {
		return nil, err
	}
	keyring.Delete("re-discipline-community", c.URL+"/pending")
	return map[string]any{"authenticated": true, "expires_at": v.Expires}, nil
}

type Connection struct {
	Alias       string `json:"alias"`
	Service     string `json:"service"`
	CommunityID string `json:"community_id"`
	Name        string `json:"name"`
}
type Settings struct {
	Mode        string       `json:"mode"`
	Connections []Connection `json:"connections"`
	Exclude     []string     `json:"exclude"`
}

func SettingsPath(root string) string { return filepath.Join(root, ".re-discipline", "community.json") }
func LoadSettings(root string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(SettingsPath(root))
	if os.IsNotExist(err) {
		return Settings{Mode: "local", Connections: []Connection{}, Exclude: []string{}}, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func SaveSettings(root string, s Settings) error {
	if s.Mode != "local" && s.Mode != "external" && s.Mode != "both" {
		return fmt.Errorf("mode must be local, external, or both")
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
func (c *Client) Connect(ctx context.Context, root, alias, library string) (Connection, error) {
	var l Library
	if err := c.Operation(ctx, "community.get", library, map[string]any{}, &l); err != nil {
		return Connection{}, err
	}
	if alias == "" {
		alias = l.Slug
	}
	if !regexpAlias.MatchString(alias) {
		return Connection{}, fmt.Errorf("alias must contain 1 to 64 lowercase letters, digits, or hyphens")
	}
	s, err := LoadSettings(root)
	if err != nil {
		return Connection{}, err
	}
	next := Connection{alias, c.URL, l.ID, l.Name}
	for _, v := range s.Connections {
		if v.Alias == alias {
			if v.Service == next.Service && v.CommunityID == next.CommunityID {
				return v, nil
			}
			return Connection{}, fmt.Errorf("alias already belongs to another community")
		}
	}
	s.Connections = append(s.Connections, next)
	if s.Mode == "local" {
		s.Mode = "both"
	}
	return next, SaveSettings(root, s)
}

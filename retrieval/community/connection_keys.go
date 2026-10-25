package community

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
)

const connectionKeyService = "re-discipline-project-keys"

type projectKey struct {
	Seed      string `json:"seed"`
	Service   string `json:"service"`
	Community string `json:"community,omitempty"`
}

func (k projectKey) private() (ed25519.PrivateKey, error) {
	seed, err := base64.RawURLEncoding.DecodeString(k.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("project key is damaged; disconnect and reconnect")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
func readProjectKey(ref string) (projectKey, error) {
	var k projectKey
	value, err := keyring.Get(connectionKeyService, ref)
	if err != nil {
		return k, err
	}
	err = json.Unmarshal([]byte(value), &k)
	return k, err
}
func saveProjectKey(ref string, k projectKey) error {
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	if err = keyring.Set(connectionKeyService, ref, string(b)); err != nil {
		return fmt.Errorf("cannot save project key in the OS credential store: %w", err)
	}
	return nil
}

// Derive the local lookup from the current project path. A copied config cannot
// borrow or delete the original project's key on the same machine.
func connectionKeyRef(root, service string, scope ...string) (string, error) {
	canonical, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if real, e := filepath.EvalSymlinks(canonical); e == nil {
		canonical = real
	}
	material := canonical + "\x00" + service
	if len(scope) > 0 && scope[0] != "" {
		material += "\x00community\x00" + scope[0]
	}
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:]), nil
}
func NewConnectionClient(conn Connection, roots ...string) (*Client, error) {
	if conn.KeyRef != "" && len(roots) > 0 {
		var err error
		conn.KeyRef, err = connectionKeyRef(roots[0], conn.Service, conn.KeyScope)
		if err != nil {
			return nil, err
		}
	}
	c, err := NewClient(conn.Service)
	if err != nil {
		return nil, err
	}
	c.KeyCommunity = conn.CommunityID
	if conn.KeyRef == "" {
		return c, nil
	} // Old owner links require reconnecting to obtain a project key.
	k, err := readProjectKey(conn.KeyRef)
	if err != nil {
		return nil, fmt.Errorf("project key unavailable in the OS credential store; unlock the store or reconnect this project: %w", err)
	}
	if k.Service != c.URL || k.Community != conn.CommunityID {
		return nil, fmt.Errorf("project key belongs to a different community or server")
	}
	c.PrivateKey, err = k.private()
	return c, err
}
func lockProjectConnection(ctx context.Context, root string) (func(), error) {
	path := filepath.Join(root, ".re-discipline")
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	// The lock contains no credentials. Keep the cross-process coordination file
	// outside the repository so agents cannot accidentally commit it.
	canonical, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if real, e := filepath.EvalSymlinks(canonical); e == nil {
		canonical = real
	}
	digest := sha256.Sum256([]byte(canonical))
	user, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	locks := filepath.Join(user, "re-discipline", "connection-locks")
	if err = os.MkdirAll(locks, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(locks, hex.EncodeToString(digest[:])+".lock"))
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ok, err := lock.TryLockContext(wait, 50*time.Millisecond)
	if err != nil || !ok {
		return nil, fmt.Errorf("another agent is updating this project's connection; retry shortly")
	}
	return func() { _ = lock.Unlock() }, nil
}
func (c *Client) Connect(ctx context.Context, root, alias, library string) (Connection, error) {
	unlock, err := lockProjectConnection(ctx, root)
	if err != nil {
		return Connection{}, err
	}
	defer unlock()
	settings, err := LoadSettings(root)
	if err != nil {
		return Connection{}, err
	}
	if alias != "" && (!regexpAlias.MatchString(alias) || alias == "local" || alias == "multiple") {
		return Connection{}, fmt.Errorf("alias must be 1 to 64 lowercase letters, digits or hyphens; local and multiple are reserved")
	}
	if library == "" {
		return Connection{}, fmt.Errorf("select a community")
	}
	previousIndex := -1
	for i, previous := range settings.Connections {
		if previous.Service != c.URL {
			continue
		}
		// Legacy links did not retain the server slug. Read their own metadata when
		// needed so a different spelling cannot allocate a second public connection.
		if previous.Slug == "" && library != previous.CommunityID {
			existing, e := NewConnectionClient(previous, root)
			var info Library
			if e == nil {
				e = existing.Operation(ctx, "community.get", previous.CommunityID, nil, &info)
			}
			if e != nil {
				return Connection{}, fmt.Errorf("cannot resolve existing link %s: %w", previous.Alias, e)
			}
			if info.ID != previous.CommunityID {
				return Connection{}, fmt.Errorf("server returned a different community identity")
			}
			previous.Slug = info.Slug
			settings.Connections[i].Slug = info.Slug
		}
		if library == previous.CommunityID || library == previous.Slug {
			previousIndex = i
			library = previous.CommunityID
			if alias != "" && alias != previous.Alias {
				return Connection{}, fmt.Errorf("community already linked as %s", previous.Alias)
			}
			alias = previous.Alias
			break
		}
	}
	for i, previous := range settings.Connections {
		if alias != "" && previous.Alias == alias && i != previousIndex {
			return Connection{}, fmt.Errorf("alias %s already names another community", alias)
		}
	}
	scope := library
	if previousIndex >= 0 {
		scope = settings.Connections[previousIndex].KeyScope
	}
	ref, err := connectionKeyRef(root, c.URL, scope)
	if err != nil {
		return Connection{}, err
	}
	k, err := readProjectKey(ref)
	if errors.Is(err, keyring.ErrNotFound) {
		_, private, e := ed25519.GenerateKey(nil)
		if e != nil {
			return Connection{}, e
		}
		k = projectKey{Seed: base64.RawURLEncoding.EncodeToString(private.Seed()), Service: c.URL}
		if err = saveProjectKey(ref, k); err != nil {
			return Connection{}, err
		}
	} else if err != nil {
		return Connection{}, fmt.Errorf("cannot open OS credential store: %w", err)
	}
	if k.Service != c.URL {
		return Connection{}, fmt.Errorf("project key belongs to another server")
	}
	private, err := k.private()
	if err != nil {
		return Connection{}, err
	}
	reader := *c
	reader.PrivateKey = private
	reader.Token = ""
	reader.KeyCommunity = ""
	var response struct {
		Community Library `json:"community"`
	}
	err = reader.Operation(ctx, "community.connect", library, map[string]string{"invitation": c.Invitation}, &response)
	var denied *APIError
	if c.Invitation == "" && c.Token != "" && errors.As(err, &denied) && (denied.Status == 403 || denied.Status == 404) {
		// Owner linking is automatic, but retrying an existing connection must not
		// generate abandoned invitations. Ask for one only when private access needs it.
		var grant struct {
			Invitation string `json:"invitation"`
		}
		owner := *c
		owner.PrivateKey = nil
		if err = owner.Operation(ctx, "access.create", library, map[string]string{"label": "Operator project"}, &grant); err != nil {
			return Connection{}, err
		}
		err = reader.Operation(ctx, "community.connect", library, map[string]string{"invitation": grant.Invitation}, &response)
	}
	if err != nil {
		return Connection{}, err
	}
	l := response.Community
	if l.ID == "" {
		return Connection{}, fmt.Errorf("server returned no community identity")
	}
	if requested, e := uuid.Parse(library); e == nil && requested.String() == library && l.ID != library {
		return Connection{}, fmt.Errorf("server returned a different community identity")
	}
	if previousIndex >= 0 && settings.Connections[previousIndex].CommunityID != l.ID {
		return Connection{}, fmt.Errorf("server returned a different community identity")
	}
	if k.Community != "" && k.Community != l.ID {
		return Connection{}, fmt.Errorf("key already belongs to another community; disconnect before relinking")
	}
	if alias == "" {
		alias = l.Slug
		for _, existing := range settings.Connections {
			if existing.Alias == alias {
				digest := sha256.Sum256([]byte(c.URL + "\x00" + l.ID))
				base := alias
				if len(base) > 54 {
					base = base[:54]
				}
				alias = base + "-" + hex.EncodeToString(digest[:4])
			}
		}
	}
	if !regexpAlias.MatchString(alias) || alias == "local" || alias == "multiple" {
		return Connection{}, fmt.Errorf("server returned an invalid alias; supply a local alias")
	}
	k.Community = l.ID
	if err = saveProjectKey(ref, k); err != nil {
		return Connection{}, err
	}
	next := Connection{KeyScope: scope, Slug: l.Slug, Alias: alias, Service: c.URL, CommunityID: l.ID, Name: l.Name, KeyRef: ref, PublicKey: base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))}
	if previousIndex >= 0 {
		settings.Connections[previousIndex] = next
	} else {
		settings.Connections = append(settings.Connections, next)
	}
	if settings.Mode == "local" {
		settings.Mode = "both"
	}
	return next, SaveSettings(root, settings)
}
func Disconnect(ctx context.Context, root, alias string) (any, error) {
	return disconnect(ctx, root, alias, nil)
}

func disconnect(ctx context.Context, root, alias string, deleted *Connection) (any, error) {
	unlock, err := lockProjectConnection(ctx, root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	settings, err := LoadSettings(root)
	if err != nil {
		return nil, err
	}
	var target Connection
	next := []Connection{}
	for _, c := range settings.Connections {
		if c.Alias == alias {
			target = c
		} else {
			next = append(next, c)
		}
	}
	if target.Alias == "" {
		if deleted != nil {
			return map[string]any{"already_disconnected": true}, nil
		}
		return nil, fmt.Errorf("unknown community alias")
	}
	if deleted != nil && (target.Service != deleted.Service || target.CommunityID != deleted.CommunityID) {
		return nil, fmt.Errorf("project connection changed during deletion; current connection retained")
	}
	result := map[string]any{"disconnected": alias, "server_removed": false}
	if deleted != nil {
		result["server_removed"] = true
	}
	if target.KeyRef != "" {
		// A damaged or hand-edited link must never delete another community's
		// private key, even if its nonsecret lookup happens to point there.
		key, e := readProjectKey(target.KeyRef)
		if e != nil && !errors.Is(e, keyring.ErrNotFound) {
			return nil, fmt.Errorf("cannot inspect local project key: %w", e)
		}
		if e == nil && (key.Service != target.Service || key.Community != target.CommunityID) {
			return nil, fmt.Errorf("connection key belongs to another community; link and key retained")
		}
		e = nil
		if deleted == nil {
			var c *Client
			c, e = NewConnectionClient(target)
			if e == nil {
				var out any
				call, cancel := context.WithTimeout(ctx, 5*time.Second)
				e = c.Operation(call, "community.leave", target.CommunityID, nil, &out)
				cancel()
			}
		}
		if e == nil {
			result["server_removed"] = true
		} else {
			result["notice"] = "Local link removed. Server removal could not be confirmed; its unused connection expires within 180 days."
		}
		if e = keyring.Delete(connectionKeyService, target.KeyRef); e != nil && !errors.Is(e, keyring.ErrNotFound) {
			return nil, fmt.Errorf("cannot delete local project key: %w", e)
		}
	}
	settings.Connections = next
	if len(next) == 0 {
		settings.Mode = "local"
	}
	// Removing one link leaves every other community and credential untouched.
	if err = atomicJSON(SettingsPath(root), settings); err != nil {
		return nil, err
	}
	return result, nil
}

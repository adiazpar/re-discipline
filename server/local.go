package server

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
)

// ValidateLocalOwner prevents a local development credential from becoming an
// alternative login on a public deployment. The installer binds host loopback.
func ValidateLocalOwner(c Config) error {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("serve-local requires an http://127.0.0.1 origin")
	}
	if validUUID(c.LocalOwnerID) != nil || len(c.LocalOwnerToken) < 43 {
		return errors.New("serve-local requires a provisioned owner UUID and random credential")
	}
	if c.SetupOnly || c.GitHubClientID != "" || c.GitHubClientSecret != "" || c.SupabaseURL != "" || c.SupabaseKey != "" || c.JevEnabled || c.ReviewURL != "" || c.ReviewKey != "" {
		return errors.New("serve-local cannot enable external identity or model services")
	}
	return nil
}

func (s *Server) localIdentity(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if s.Config.LocalOwnerID == "" || s.Config.LocalOwnerToken == "" || !strings.HasPrefix(h, "Bearer ") ||
		subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(s.Config.LocalOwnerToken)) != 1 {
		return "", fail(401, "local owner credential required; run the local hosting helper")
	}
	return s.Config.LocalOwnerID, nil
}

func localOperation(action string) bool {
	switch action {
	case "community.create", "community.list", "community.get", "community.update", "usage", "audit.list",
		"query", "changes", "export", "submission.create", "submission.batch", "submission.list", "submission.get",
		"submission.packet", "submission.review", "submission.resolve", "publication.resolve", "publication.receipts",
		"finding.get", "finding.history", "finding.match", "finding.candidates", "finding.assess", "finding.relations",
		"finding.relate", "finding.consolidate", "finding.contributions", "finding.withdraw", "health.list", "health.review":
		return true
	}
	return false
}

func localPolicy(l kb.Library) error {
	if l.Visibility != "private" || l.Policy.Mode != "trusted" || l.Policy.Assistance {
		return fail(400, "local owner mode requires private visibility, trusted owner publication, and assistance disabled")
	}
	return nil
}

package server

import (
	"errors"
	"net/url"
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
	if c.SetupOnly || c.JevEnabled {
		return errors.New("serve-local cannot enable external identity or model services")
	}
	return nil
}

// Package connectionauth defines the plugin's strict RFC 9421 request profile.
// Keys are Ed25519; signatures bind the destination, method and complete body.
package connectionauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/common-fate/httpsig/contentdigest"
	"github.com/common-fate/httpsig/sigbase"
	"github.com/common-fate/httpsig/signature"
	"github.com/common-fate/httpsig/sigparams"
	"github.com/common-fate/httpsig/sigset"
)

const Tag = "re-discipline-connection-v1"
const Lifetime = 2 * time.Minute
const ClockSkew = 15 * time.Second

var covered = []string{"@method", "@target-uri", "content-type", "content-length", "content-digest"}
var invalid = errors.New("invalid request signature or clock outside allowed window")

type Proof struct {
	PublicKey string
	Nonce     string
	Expires   time.Time
}

func Sign(r *http.Request, key ed25519.PrivateKey, now time.Time) error {
	if len(key) != ed25519.PrivateKeySize {
		return invalid
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	p := sigparams.Params{KeyID: base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), Tag: Tag, Alg: "ed25519", CoveredComponents: covered,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce[:]), Created: now.UTC().Truncate(time.Second), Expires: now.Add(Lifetime).UTC().Truncate(time.Second)}
	r.Header.Set("Content-Type", "application/json")
	d := contentdigest.SHA256
	d.MaxBytes = 2 * 1024 * 1024
	base, err := sigbase.Derive(p, nil, r, d)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Digest", base.Values["content-digest"])
	canonical, err := base.CanonicalString(p)
	if err != nil {
		return err
	}
	set := sigset.Set{Messages: map[string]*signature.Message{"connection": {Input: p, Signature: ed25519.Sign(key, []byte(canonical))}}}
	return set.Include(r)
}

// Verify checks the proof, without consuming its nonce. The caller must atomically
// consume a verified nonce before authorizing any operation (including joining).
// Invalid signatures must never reserve nonces on behalf of another key.
func Verify(r *http.Request, origin string, now time.Time) (Proof, error) {
	var empty Proof
	if len(r.Header.Values("Signature")) != 1 || len(r.Header.Values("Signature-Input")) != 1 || len(r.Header.Get("Signature-Input")) > 1024 || len(r.Header.Get("Signature")) > 256 {
		return empty, invalid
	}
	set, err := sigset.Unmarshal(r)
	if err != nil || len(set.Messages) != 1 {
		return empty, invalid
	}
	m, err := set.Find(Tag)
	if err != nil {
		return empty, invalid
	}
	p := m.Input
	key, err := base64.RawURLEncoding.DecodeString(p.KeyID)
	if err != nil || len(key) != ed25519.PublicKeySize || p.KeyID != base64.RawURLEncoding.EncodeToString(key) || p.Alg != "ed25519" || !slices.Equal(p.CoveredComponents, covered) {
		return empty, invalid
	}
	nonce, err := base64.RawURLEncoding.DecodeString(p.Nonce)
	if err != nil || len(nonce) != 24 || len(p.Nonce) != 32 || p.Nonce != base64.RawURLEncoding.EncodeToString(nonce) {
		return empty, invalid
	}
	if p.Created.After(now.Add(ClockSkew)) || p.Created.Before(now.Add(-Lifetime)) || !p.Expires.Equal(p.Created.Add(Lifetime)) || !p.Expires.After(now) {
		return empty, invalid
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || r.Host != u.Host || (u.Scheme != "https" && u.Scheme != "http") || r.ContentLength < 0 {
		return empty, invalid
	}
	// PUBLIC_URL is operator configuration; forwarded/client-supplied host headers
	// cannot redirect the proof to another server. Reverse proxies preserve Host.
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	d := contentdigest.SHA256
	d.MaxBytes = 2 * 1024 * 1024
	base, err := sigbase.Derive(p, nil, r, d)
	if err != nil {
		return empty, invalid
	}
	if len(r.Header.Values("Content-Digest")) != 1 || r.Header.Get("Content-Digest") != base.Values["content-digest"] {
		return empty, invalid
	}
	canonical, err := base.CanonicalString(p)
	if err != nil || !ed25519.Verify(key, []byte(canonical), m.Signature) {
		return empty, invalid
	}
	return Proof{PublicKey: p.KeyID, Nonce: p.Nonce, Expires: p.Expires.Add(ClockSkew)}, nil
}

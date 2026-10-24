package connectionauth

import (
	"bytes"
	"crypto/ed25519"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignedRequestBindsDestinationBodyAndFreshness(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	now := time.Now().Truncate(time.Second)
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		origin string
		delta  time.Duration
		valid  bool
	}{
		{name: "valid", valid: true}, {name: "body", mutate: func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"community":"godzilla"}`)) }},
		{name: "method", mutate: func(r *http.Request) { r.Method = "DELETE" }},
		{name: "path", mutate: func(r *http.Request) { r.URL.Path = "/other" }},
		{name: "query", mutate: func(r *http.Request) { r.URL.RawQuery = "community=other" }},
		{name: "destination", origin: "https://other.example"},
		{name: "scheme", origin: "http://kb.example"},
		{name: "host", mutate: func(r *http.Request) { r.Host = "other.example" }},
		{name: "digest", mutate: func(r *http.Request) { r.Header.Set("Content-Digest", "sha-256=:fake:") }},
		{name: "nonce", mutate: func(r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), "nonce=\"", "nonce=\"A", 1))
		}},
		{name: "duplicate header", mutate: func(r *http.Request) { r.Header.Add("Signature", r.Header.Get("Signature")) }},
		{name: "expired", delta: Lifetime + time.Second}, {name: "future", delta: -ClockSkew - time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "https://kb.example/v1/operations", bytes.NewBufferString(`{"community":"doom"}`))
			if err := Sign(r, key, now); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(r)
			}
			origin := test.origin
			if origin == "" {
				origin = "https://kb.example"
			}
			proof, err := Verify(r, origin, now.Add(test.delta))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if test.valid {
				if proof.PublicKey == "" || proof.Nonce == "" {
					t.Fatal("missing proof")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"community":"doom"}` {
					t.Fatal("verification consumed body")
				}
			}
		})
	}
}

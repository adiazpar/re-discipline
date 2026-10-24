package community

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalCredentialIsPrivateAndOriginBound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	origin := "http://127.0.0.1:7346"
	digest := sha256.Sum256([]byte(origin))
	p := filepath.Join(home, ".re-discipline", "servers", "credentials", hex.EncodeToString(digest[:])+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	credential := LocalOwnerCredential{Service: origin, Token: strings.Repeat("x", 43), OwnerID: "test-owner"}
	write := func() {
		b, _ := json.Marshal(credential)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if got, err := localOwnerToken(origin); err != nil || got != credential.Token {
		t.Fatalf("local credential: %q %v", got, err)
	}
	for _, other := range []string{"http://127.0.0.1:7347", "https://elsewhere.example", "http://localhost:7346"} {
		if got, err := localOwnerToken(other); err != nil || got != "" {
			t.Fatalf("credential escaped origin: %s %v", other, err)
		}
	}
	credential.Service = "https://elsewhere.example"
	write()
	if _, err := localOwnerToken(origin); err == nil {
		t.Fatal("credential origin mismatch accepted")
	}
	credential.Service = origin
	write()
	if runtime.GOOS != "windows" {
		os.Chmod(p, 0644)
		if _, err := localOwnerToken(origin); err == nil {
			t.Fatal("public credential accepted")
		}
	}
}

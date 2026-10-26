package community

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

// LocalOwnerCredential is provisioned by the hosting helper (or registered over
// operator-controlled SSH), never by a project or an HTTP response. It is usable
// only at its exact saved origin.
type LocalOwnerCredential struct {
	Service      string `json:"service"`
	OwnerID      string `json:"owner_id"`
	Token        string `json:"token"`
	Installation string `json:"installation"`
	SSH          string `json:"ssh,omitempty"`
}

func localOwnerToken(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Scheme != "https") {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(origin))
	p := filepath.Join(home, ".re-discipline", "servers", "credentials", hex.EncodeToString(digest[:])+".json")
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return "", fmt.Errorf("local owner credential must be a private regular file: %s", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	var credential LocalOwnerCredential
	if json.Unmarshal(b, &credential) != nil || credential.Service != origin || len(credential.Token) < 43 {
		return "", fmt.Errorf("invalid local owner credential for %s; repair the local installation", origin)
	}
	return credential.Token, nil
}

package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataCacheBoundedAndDetectsReplacement(t *testing.T) {
	c := newMetadataCache(900)
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		p := filepath.Join(root, name)
		os.WriteFile(p, []byte(`{"finding":"revision"}`), 0600)
		m, err := c.read(p)
		if err != nil || m["finding"] != "revision" {
			t.Fatal(m, err)
		}
	}
	if c.used > c.budget || len(c.entries) > 2 {
		t.Fatal("metadata budget exceeded")
	}
	p := filepath.Join(root, "d")
	os.WriteFile(p, []byte(`{"finding":"replacement revision"}`), 0600)
	m, err := c.read(p)
	if err != nil || m["finding"] != "replacement revision" {
		t.Fatal("stale metadata")
	}
	os.WriteFile(p, []byte(`broken`), 0600)
	if _, err = c.read(p); err == nil {
		t.Fatal("corruption hidden by cache")
	}
}

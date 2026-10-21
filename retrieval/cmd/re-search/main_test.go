package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuotedQueryHonorsLimitAndLocalSource(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, ".re-discipline", "docs")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		body := "---\nstatus: promoted\nkind: fact\ngrade: direct\n---\n# Embedded physics " + name + "\n\nThe constructor initializes embedded physics.\n"
		if err := os.WriteFile(filepath.Join(docs, name+".md"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	captured, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()
	original := os.Stdout
	os.Stdout = captured
	defer func() { os.Stdout = original }()
	if err := run([]string{"query", "--root", root, "--sources", "local", "--baseline", "--json", "--limit", "1", "embedded physics"}); err != nil {
		t.Fatal(err)
	}
	if _, err := captured.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var hits []struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	}
	if err := json.NewDecoder(captured).Decode(&hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "local" || !strings.HasPrefix(hits[0].Path, "docs/") {
		t.Fatalf("expected one local result, got %+v", hits)
	}
}

func TestQueryCommandsRejectIgnoredArgumentsBeforeOpeningProject(t *testing.T) {
	for _, command := range []string{"query", "symbol", "explain"} {
		for _, tail := range [][]string{
			{"physics constructor", "--limit", "1"},
			{"physics constructor", "--sources", "local"},
			{"physics", "constructor"},
		} {
			t.Run(command+"/"+strings.Join(tail, "_"), func(t *testing.T) {
				// A missing project proves invalid arguments fail before any index
				// work or source access. The old CLI silently used only tail[0].
				args := append([]string{command, "--root", filepath.Join(t.TempDir(), "missing")}, tail...)
				err := run(args)
				if err == nil || !strings.Contains(err.Error(), "put flags before") {
					t.Fatalf("expected actionable argument error, got %v", err)
				}
			})
		}
	}
}

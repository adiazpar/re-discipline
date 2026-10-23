package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionStatusUsesHookWorkspaceWithoutCreatingAnIndex(t *testing.T) {
	root := t.TempDir()
	rd := filepath.Join(root, ".re-discipline")
	for _, path := range []string{"active/campaign with spaces", "docs/ops"} {
		if err := os.MkdirAll(filepath.Join(rd, path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"docs/fact.md", "docs/INDEX.md", "docs/ops/note.MD", "docs/ops/readme.txt"} {
		if err := os.WriteFile(filepath.Join(rd, path), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	input, _ := json.Marshal(map[string]string{"cwd": root})
	var out bytes.Buffer
	writeSessionStatus(bytes.NewReader(input), &out)
	if got := out.String(); !strings.Contains(got, "1 active campaign(s) - campaign with spaces") || !strings.Contains(got, "2 docs curated.") {
		t.Fatalf("unexpected summary: %q", got)
	}
	entries, _ := os.ReadDir(rd)
	if len(entries) != 2 {
		t.Fatalf("hook wrote project state: %v", entries)
	}
}

func TestSessionStatusIsSilentForMissingProjectOrInvalidInput(t *testing.T) {
	missing, _ := json.Marshal(map[string]string{"cwd": t.TempDir()})
	for _, input := range []string{string(missing), "invalid json"} {
		var out bytes.Buffer
		writeSessionStatus(strings.NewReader(input), &out)
		if out.Len() != 0 {
			t.Fatalf("unexpected output: %q", out.String())
		}
	}
}

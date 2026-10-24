package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryHealth(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	if checkRecoveryHealth("", now) != nil {
		t.Fatal("optional monitoring must remain optional")
	}
	if checkRecoveryHealth(dir, now) == nil {
		t.Fatal("missing recovery evidence accepted")
	}
	for _, n := range []string{"backup", "restore"} {
		if err := os.WriteFile(filepath.Join(dir, n+"-last-success"), []byte(now.Format(time.RFC3339)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRecoveryHealth(dir, now); err != nil {
		t.Fatal(err)
	}
	if checkRecoveryHealth(dir, now.Add(29*time.Hour)) == nil {
		t.Fatal("stale backup accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "restore-failed"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if checkRecoveryHealth(dir, now) == nil {
		t.Fatal("failed restore accepted")
	}
}

func TestAutomaticDeploymentFailureHealth(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	for _, name := range []string{"backup", "restore"} {
		if err := os.WriteFile(filepath.Join(dir, name+"-last-success"), []byte(now.Format(time.RFC3339)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "auto-update-failed")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if checkRecoveryHealth(dir, now) == nil {
		t.Fatal("failed deployment did not raise monitoring alert")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := checkRecoveryHealth(dir, now); err != nil {
		t.Fatal(err)
	}
}

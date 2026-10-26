package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/buildinfo"
)

func TestRefreshFailureBacksOffAndRecoversWithoutBlockingOtherQueries(t *testing.T) {
	var state projectRefreshState
	now := time.Now()
	attempts := 0
	locked := errors.New("executable still running")
	attempt := func() error { attempts++; return locked }
	if err := state.run(now, attempt); !errors.Is(err, locked) {
		t.Fatal("missing initial failure", err)
	}
	for i := 0; i < 100; i++ {
		if err := state.run(now.Add(30*time.Second), attempt); err != nil {
			t.Fatal("query retried during cooldown", err)
		}
	}
	if attempts != 1 {
		t.Fatal("repeated failed refresh on every query", attempts)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		state.run(now.Add(time.Minute), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	defer func() { close(release); <-done }()
	returned := make(chan struct{})
	go func() {
		state.run(now.Add(time.Minute), func() error { panic("concurrent refresh") })
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("query blocked behind in-progress refresh")
	}
}

func TestProjectRefreshRepairsAndDoesNotDowngrade(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".re-discipline", "docs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	knowledge := filepath.Join(dir, "knowledge.md")
	os.WriteFile(knowledge, []byte("private knowledge"), 0644)
	source := filepath.Join(t.TempDir(), "engine")
	os.WriteFile(source, []byte("new engine"), 0755)
	build := buildinfo.Info{Version: "1.11.0", Build: "new", Schema: "schema"}
	if err := refreshFrom(root, source, build, false); err != nil {
		t.Fatal(err)
	}
	name := "re-search"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(root, ".re-discipline", "bin", name)
	check := func(want string) {
		t.Helper()
		b, err := os.ReadFile(dest)
		if err != nil || string(b) != want {
			t.Fatalf("%q %v; want %q", b, err, want)
		}
	}
	check("new engine")
	before, _ := os.Stat(dest)
	if err := refreshFrom(root, source, build, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(dest)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged engine was recopied")
	}
	os.WriteFile(dest, []byte("corrupt"), 0755)
	if err := refreshFrom(root, source, build, false); err != nil {
		t.Fatal(err)
	}
	check("new engine")
	os.WriteFile(source, []byte("old engine"), 0755)
	old := buildinfo.Info{Version: "1.10.0", Build: "old", Schema: "schema"}
	if err := refreshFrom(root, source, old, false); err != nil {
		t.Fatal(err)
	}
	check("new engine")
	old.Version = build.Version
	if err := refreshFrom(root, source, old, false); err != nil {
		t.Fatal(err)
	}
	check("new engine")
	if err := refreshFrom(root, source, old, true); err != nil {
		t.Fatal(err)
	}
	check("old engine")
	if b, _ := os.ReadFile(knowledge); string(b) != "private knowledge" {
		t.Fatal("knowledge changed")
	}
}

func TestProjectRefreshSerializesConcurrentWritersAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".re-discipline"), 0755)
	source := filepath.Join(t.TempDir(), "engine")
	os.WriteFile(source, []byte("complete engine"), 0755)
	build := buildinfo.Info{Version: "1.11.0", Build: "same"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = refreshFrom(root, source, build, false) }()
	}
	wg.Wait()
	if err := refreshFrom(root, source, build, false); err != nil {
		t.Fatal(err)
	}
	name := "re-search"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(root, ".re-discipline", "bin", name)
	if b, err := os.ReadFile(dest); err != nil || string(b) != "complete engine" {
		t.Fatalf("partial engine: %q %v", b, err)
	}
	os.Remove(dest)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("untouched"), 0644)
	if err := os.Symlink(outside, dest); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := refreshFrom(root, source, build, false); err == nil {
		t.Fatal("followed project symlink")
	}
	if b, _ := os.ReadFile(outside); string(b) != "untouched" {
		t.Fatal("wrote outside project")
	}
}

func TestRefreshDoesNotInitializeMissingProject(t *testing.T) {
	root := t.TempDir()
	if err := refreshFrom(root, "/not-used", buildinfo.Info{}, false); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("initialized a missing project")
	}
}

func TestProjectCopyCannotDiscoverDeploymentCodeInProject(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(`{}`), 0644)
	name := "re-search"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if got := pluginRootFor(filepath.Join(root, ".re-discipline", "bin", name)); got != "" {
		t.Fatal("project copy acquired plugin execution rights", got)
	}
	os.MkdirAll(filepath.Join(root, ".re-discipline", ".claude-plugin"), 0755)
	os.WriteFile(filepath.Join(root, ".re-discipline", ".claude-plugin", "plugin.json"), []byte(`{}`), 0644)
	if got := pluginRootFor(filepath.Join(root, ".re-discipline", "bin", name)); got != "" {
		t.Fatal("private project folder spoofed plugin root", got)
	}
	path := filepath.Join(root, "bin", runtime.GOOS+"-"+runtime.GOARCH, name)
	if runtime.GOOS == "windows" {
		path = filepath.Join(root, "bin", name)
	}
	if got := pluginRootFor(path); got != root {
		t.Fatal("bundled engine not recognized", got)
	}
}

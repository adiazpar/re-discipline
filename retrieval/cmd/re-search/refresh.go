package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/buildinfo"
	"github.com/gofrs/flock"
)

var refreshed sync.Map

type projectRefreshState struct {
	mu    sync.Mutex
	done  bool
	retry time.Time
}

func (s *projectRefreshState) run(now time.Time, attempt func() error) error {
	// Concurrent queries keep using the plugin engine while one refresh runs.
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	if s.done || now.Before(s.retry) {
		return nil
	}
	if err := attempt(); err != nil {
		// A long-running Windows project executable can remain locked for hours.
		// Do not add the replacement retry delay to every subsequent query.
		s.retry = now.Add(time.Minute)
		return err
	}
	s.done = true
	return nil
}

// A project copy never searches for or downloads code. Only the running plugin
// package can refresh it, from its own native executable.
func pluginRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return pluginRootFor(exe)
}

func pluginRootFor(exe string) string {
	expected := filepath.Join("bin", runtime.GOOS+"-"+runtime.GOARCH, "re-search")
	if runtime.GOOS == "windows" {
		expected = filepath.Join("bin", "re-search.exe")
	}
	for _, root := range []string{filepath.Dir(filepath.Dir(exe)), filepath.Dir(filepath.Dir(filepath.Dir(exe)))} {
		if strings.EqualFold(filepath.Base(root), ".re-discipline") {
			continue
		}
		relative, err := filepath.Rel(root, exe)
		if err != nil || relative != expected {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, ".claude-plugin", "plugin.json")); err == nil {
			return root
		}
	}
	return ""
}

func refreshProject(root string) {
	if pluginRoot() == "" {
		return
	}
	state, _ := refreshed.LoadOrStore(root, &projectRefreshState{})
	err := state.(*projectRefreshState).run(time.Now(), func() error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return refreshFrom(root, exe, buildinfo.Current(), false)
	})
	if err != nil {
		// A locked Windows binary or read-only checkout must not break MCP
		// retrieval, which uses the current plugin binary already.
		fmt.Fprintln(os.Stderr, "re-search: project engine refresh deferred:", err)
		return
	}
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func refreshFrom(root, source string, build buildinfo.Info, explicit bool) error {
	rd := filepath.Join(root, ".re-discipline")
	info, err := os.Lstat(rd)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("knowledge directory is not a real directory")
	}
	dir := filepath.Join(rd, "bin")
	if err := os.Mkdir(dir, 0755); err != nil && !os.IsExist(err) {
		return err
	}
	info, err = os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("engine directory is not a real directory")
	}
	lockPath := filepath.Join(dir, ".update.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe engine lock")
	}
	lock := flock.New(lockPath)
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("another process is refreshing this project")
	}
	defer lock.Unlock()
	name := "re-search"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(dir, name)
	if info, err := os.Lstat(dest); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("engine path is not a regular file")
	}
	var old buildinfo.Info
	metadata := filepath.Join(dir, "build.json")
	if info, err := os.Lstat(metadata); err == nil && (!info.Mode().IsRegular() || info.Size() > 4096) {
		return fmt.Errorf("unsafe engine metadata")
	}
	if b, err := os.ReadFile(metadata); err == nil {
		_ = json.Unmarshal(b, &old)
	}
	// Never let an older host session replace a newer installed release.
	if !explicit && buildinfo.Compare(old.Version, build.Version) > 0 {
		return nil
	}
	if !explicit && old.Version == build.Version && old.Build != "" && old.Build != build.Build {
		return nil
	}
	wanted, err := fileDigest(source)
	if err != nil {
		return err
	}
	current, _ := fileDigest(dest)
	if wanted != current {
		if err := copyProjectBinary(source, dest); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(build)
	tmp, err := os.CreateTemp(dir, ".build-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return replaceProjectBinary(tmp.Name(), metadata)
}

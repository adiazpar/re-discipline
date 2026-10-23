package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// Copy the running native engine, never the dispatcher or another platform's
// artifact. The init skill owns project policy and templates; this only installs
// its plugin-owned executable after the knowledge directory has been created.
func installProjectBinary(root string) (string, error) {
	rd := filepath.Join(root, ".re-discipline")
	if info, err := os.Stat(rd); err != nil || !info.IsDir() {
		return "", fmt.Errorf("no .re-discipline directory; run init-project first")
	}
	source, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "re-search"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	relative := filepath.Join(".re-discipline", "bin", name)
	dest := filepath.Join(root, relative)
	if srcInfo, err := os.Stat(source); err == nil {
		if destInfo, err := os.Stat(dest); err == nil && os.SameFile(srcInfo, destInfo) {
			return filepath.ToSlash(relative), nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dest), ".re-search-*")
	if err != nil {
		return "", err
	}
	temp := out.Name()
	defer os.Remove(temp)
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	if err := out.Chmod(0755); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := replaceProjectBinary(temp, dest); err != nil {
		return "", fmt.Errorf("replace project binary (close running copies before retrying): %w", err)
	}
	return filepath.ToSlash(relative), nil
}

func replaceProjectBinary(source, dest string) error {
	// Windows can retain an executable's file lock briefly after its process
	// exits. Keep the old binary intact and retry the replacement; a
	// genuinely running copy still produces the actionable error above.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Rename(source, dest)
		// ERROR_SHARING_VIOLATION is 32; it is not covered by os.IsPermission.
		if err == nil || runtime.GOOS != "windows" ||
			(!os.IsPermission(err) && !errors.Is(err, syscall.Errno(32))) ||
			!time.Now().Before(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

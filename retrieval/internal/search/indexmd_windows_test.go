//go:build windows

package search

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWriteIndexMDWithMappedReader(t *testing.T) {
	root := buildTestCorpus(t)
	oldDocs := []Doc{{Path: "docs/old.md", Title: "Old complete index"}}
	if err := WriteIndexMD(root, oldDocs); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".re-discipline", "docs", "INDEX.md")
	old, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	f, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(f)
	mapping, err := windows.CreateFileMapping(f, nil, windows.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(mapping)
	view, err := windows.MapViewOfFile(mapping, windows.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.UnmapViewOfFile(view)

	if err := WriteIndexMD(root, []Doc{{Path: "docs/new.md", Title: "New complete index"}}); err != nil {
		t.Fatalf("publish while old index is mapped: %v", err)
	}
	current, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(current, []byte("New complete index")) || bytes.Contains(current, []byte("Old complete index")) {
		t.Fatalf("new readers did not receive the complete replacement: %q", current)
	}
	mapped := make([]byte, len(old))
	var read uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), view, &mapped[0], uintptr(len(mapped)), &read); err != nil {
		t.Fatal(err)
	}
	if read != uintptr(len(old)) || !bytes.Equal(mapped, old) {
		t.Fatal("publishing changed the existing reader's mapped snapshot")
	}
}

func TestReplaceFileLongUnicodePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("segment", 20), strings.Repeat("segment", 20), "é漢")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(dir, "new.tmp"), filepath.Join(dir, "INDEX.md")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "new" {
		t.Fatalf("long Unicode destination: %q, %v", got, err)
	}
}

func TestWriteIndexMDSharingViolationPreservesPrevious(t *testing.T) {
	root := buildTestCorpus(t)
	if err := WriteIndexMD(root, []Doc{{Path: "docs/old.md", Title: "Old complete index"}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".re-discipline", "docs", "INDEX.md")
	old, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	f, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(f)
	if err := WriteIndexMD(root, []Doc{{Path: "docs/new.md", Title: "New complete index"}}); err == nil {
		t.Fatal("expected a sharing violation")
	}
	current, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(current, old) {
		t.Fatalf("failed publication damaged the previous index: %q, %v", current, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(p), ".index-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary markdown left after failure: %v, %v", leftovers, err)
	}
}

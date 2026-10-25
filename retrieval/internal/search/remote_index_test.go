package search

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadRemoteIndexEscapesNativePathAndRemainsReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge # 100% & copy")
	if err := os.MkdirAll(filepath.Join(root, ".re-discipline", "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildIndexFile(root, IndexPath(root)); err != nil {
		t.Fatal(err)
	}
	db, err := ReadRemoteIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err = db.QueryRow(`SELECT value FROM indexmeta WHERE key='format'`).Scan(&version); err != nil || version != indexFormatVersion {
		t.Fatal("native path was not opened", version, err)
	}
	if _, err = db.Exec(`CREATE TABLE forbidden(value TEXT)`); err == nil {
		t.Fatal("comparison connection allowed a write")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	absent, err := ReadRemoteIndex(missing)
	if err != nil {
		t.Fatal(err)
	}
	defer absent.Close()
	if err = absent.Ping(); err == nil {
		t.Fatal("read-only lookup created an index")
	}
	if _, err = os.Stat(IndexPath(missing)); !os.IsNotExist(err) {
		t.Fatal("lookup modified missing project", err)
	}
}

package indexdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestReadWaitIsBoundedAndDoesNotChangeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index #Δ %.db")
	writer, err := OpenWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.Exec(`CREATE TABLE facts(value TEXT); INSERT INTO facts VALUES('unchanged'); BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer writer.Exec(`ROLLBACK`)
	reader, err := OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var value string
	start := time.Now()
	err = ReadRow(ctx, reader, `SELECT value FROM facts`).Scan(&value)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&255 != 5 || time.Since(start) > 7*time.Second {
		t.Fatalf("persistent contention must return busy within its budget: %v after %s", err, time.Since(start))
	}
	if _, err = writer.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err = ReadRow(ctx, reader, `SELECT value FROM facts`).Scan(&value); err != nil || value != "unchanged" {
		t.Fatal(value, err)
	}
	if _, err = reader.Exec(`DELETE FROM facts`); err == nil {
		t.Fatal("reader was writable")
	}
	missing, err := OpenRead(filepath.Join(filepath.Dir(path), "absent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	if err = missing.PingContext(ctx); err == nil {
		t.Fatal("missing index silently created")
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(path) {
		t.Fatal(files, err)
	}
}

package community

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
)

func TestRememberCopiesWaitsForActiveReader(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	p := filepath.Join(root, ".re-discipline", "docs", "clock.md")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(copyMarkdown(copyID, "clock", "")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Query(root, "clock", engine.Options{}); err != nil {
		t.Fatal(err)
	}
	conn := Connection{Service: "https://example.test", CommunityID: "doom"}
	entry := KnowledgeEntry{ID: copyID, Fingerprint: strings.Repeat("a", 64), Sequence: 3}
	if err := rememberCopies(ctx, root, conn, []KnowledgeEntry{entry}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, ".re-discipline", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM copy_bases`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	entry.Fingerprint, entry.Sequence = strings.Repeat("b", 64), 4
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() { close(started); done <- rememberCopies(ctx, root, conn, []KnowledgeEntry{entry}) }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("bookkeeping failed while brief reader was active: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bookkeeping did not resume")
	}
	bases, err := readCopyBases(ctx, root, conn, []string{copyID})
	if err != nil || bases[copyID].Fingerprint != entry.Fingerprint || bases[copyID].Sequence != entry.Sequence {
		t.Fatal(bases, err)
	}
	// Cancellation during a later attempted acknowledgement must leave the
	// previously committed baseline intact, even when commit itself was busy.
	tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = tx.QueryRow(`SELECT count(*) FROM copy_bases`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	newer := entry
	newer.Fingerprint, newer.Sequence = strings.Repeat("c", 64), 5
	startedAt := time.Now()
	err = rememberCopies(cancelled, root, conn, []KnowledgeEntry{newer})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(startedAt) > time.Second {
		t.Fatalf("cancelled bookkeeping: %v after %s", err, time.Since(startedAt))
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	bases, err = readCopyBases(ctx, root, conn, []string{copyID})
	if err != nil || bases[copyID].Fingerprint != entry.Fingerprint || bases[copyID].Sequence != entry.Sequence {
		t.Fatal("cancelled update changed baseline", bases, err)
	}

}

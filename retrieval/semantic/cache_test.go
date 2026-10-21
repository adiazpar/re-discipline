package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCacheFailureRetainsFilesystemCause(t *testing.T) {
	client, calls := testClient(t, Config{Enabled: true})
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	client.CacheDir = path
	_, err := client.Evaluate(context.Background(), "project", "claim", RelevanceQuestions(), false)
	var pathError *os.PathError
	if !errors.As(err, &pathError) || pathError.Path != path {
		t.Fatalf("cache failure lost filesystem cause: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("cache failure made a provider request")
	}
}

func TestConcurrentCacheInitialization(t *testing.T) {
	for _, populated := range []bool{false, true} {
		name := "fresh"
		if populated {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if populated {
				db, err := New(Config{}, dir).cache()
				if err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range 16 {
				wg.Go(func() {
					<-start
					db, err := New(Config{}, dir).cache()
					if err != nil {
						t.Errorf("open shared cache: %v", err)
						return
					}
					defer db.Close()
					var epoch int
					if err := db.QueryRow("SELECT generation FROM cache_epoch WHERE id=1").Scan(&epoch); err != nil || epoch != 0 {
						t.Errorf("shared cache epoch: %d, %v", epoch, err)
					}
				})
			}
			close(start)
			wg.Wait()
		})
	}
}

func TestConcurrentOfflineCacheReads(t *testing.T) {
	client, calls := testClient(t, Config{Enabled: true, DailyRequests: 1})
	if _, err := client.Evaluate(context.Background(), "project", "claim", RelevanceQuestions(), false); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			judgment, err := client.Evaluate(context.Background(), "project", "claim", RelevanceQuestions(), true)
			if err != nil || !judgment.Cached {
				t.Errorf("offline cached judgment: cached=%v, %v", judgment.Cached, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("offline reads made provider calls: %d", calls.Load())
	}
}

func TestOfflineCacheReadWhileWriterReserved(t *testing.T) {
	client, calls := testClient(t, Config{Enabled: true, DailyRequests: 1})
	if _, err := client.Evaluate(context.Background(), "project", "claim", RelevanceQuestions(), false); err != nil {
		t.Fatal(err)
	}
	writer, err := client.cache()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer writer.Exec("ROLLBACK")
	// A reserved writer leaves committed data readable. A cached offline answer
	// must not require schema writes or a successful last-used timestamp update.
	judgment, err := client.Evaluate(context.Background(), "project", "claim", RelevanceQuestions(), true)
	if err != nil || !judgment.Cached {
		t.Fatalf("readable cached judgment rejected: cached=%v, %v", judgment.Cached, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("offline lookup made provider calls: %d", calls.Load())
	}
}

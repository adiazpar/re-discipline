package community

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

type CommunityStats struct {
	CommunityID    string    `json:"community_id"`
	Documents      int64     `json:"documents"`
	KnowledgeBytes int64     `json:"knowledge_bytes"`
	ObservedAt     time.Time `json:"observed_at"`
}

type StatsSnapshot struct {
	CommunityStats
	FetchedAt time.Time `json:"fetched_at"`
	Cached    bool      `json:"cached"`
}

const statsCacheTTL = 5 * time.Minute

func statsCachePath(root string, conn Connection) string {
	identity := root + "\x00" + conn.Service + "\x00" + conn.CommunityID + "\x00" + conn.KeyScope + "\x00" + conn.PublicKey
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(root, ".re-discipline", "community", "summary-stats-"+hex.EncodeToString(digest[:])+".json")
}

func validStats(stats CommunityStats, cid string) bool {
	return stats.CommunityID == cid && stats.Documents >= 0 && stats.KnowledgeBytes >= 0 && !stats.ObservedAt.IsZero()
}

// Project summaries cache metadata, never documents or credentials. The cache is
// a dated observation; it does not prove present access or server availability.
func ReadCommunityStats(ctx context.Context, root string, conn Connection) (StatsSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	path := statsCachePath(root, conn)
	dir := filepath.Dir(path)
	cacheable := false
	if err := os.Mkdir(dir, 0700); err == nil || os.IsExist(err) {
		info, err := os.Lstat(dir)
		cacheable = err == nil && info.IsDir()
	}
	if cacheable {
		lockPath := filepath.Join(dir, ".summary-stats.lock")
		if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
			cacheable = false
		} else {
			lock := flock.New(lockPath)
			ok, err := lock.TryLockContext(ctx, 25*time.Millisecond)
			if err != nil || !ok {
				return StatsSnapshot{}, fmt.Errorf("summary refresh is busy or unavailable; retry later")
			}
			defer lock.Unlock()
		}
	}
	if cacheable {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() <= 8192 {
			var cached StatsSnapshot
			b, err := os.ReadFile(path)
			if err == nil && json.Unmarshal(b, &cached) == nil && validStats(cached.CommunityStats, conn.CommunityID) {
				age := time.Since(cached.FetchedAt)
				if age >= 0 && age < statsCacheTTL {
					cached.Cached = true
					return cached, nil
				}
			}
		}
		// Retired links and rotated keys must not accumulate metadata indefinitely.
		paths, _ := filepath.Glob(filepath.Join(dir, "summary-stats-*.json"))
		for _, old := range paths {
			if info, err := os.Lstat(old); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) >= statsCacheTTL {
				_ = os.Remove(old)
			}
		}
	}
	client, err := NewConnectionClient(conn, root)
	if err != nil {
		return StatsSnapshot{}, err
	}
	var stats CommunityStats
	if err := client.Operation(ctx, "community.stats", conn.CommunityID, nil, &stats); err != nil {
		return StatsSnapshot{}, err
	}
	if !validStats(stats, conn.CommunityID) {
		return StatsSnapshot{}, fmt.Errorf("server returned invalid or incorrectly scoped statistics")
	}
	out := StatsSnapshot{CommunityStats: stats, FetchedAt: time.Now().UTC()}
	if cacheable {
		_ = atomicJSON(path, out)
	}
	return out, nil
}

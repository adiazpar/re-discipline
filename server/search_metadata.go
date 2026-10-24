package server

import (
	"container/list"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Only immutable revision-ID maps are cached, never authorization or answers.
// A shared byte-budgeted LRU lets busy communities borrow unused cache space.
type metadataCache struct {
	mu           sync.Mutex
	budget, used int64
	entries      map[string]*list.Element
	lru          list.List
}

func (c *metadataCache) forgetTree(root string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := filepath.Clean(root) + string(os.PathSeparator)
	for path, e := range c.entries {
		if strings.HasPrefix(path, prefix) {
			c.used -= e.Value.(*metadataEntry).cost
			c.lru.Remove(e)
			delete(c.entries, path)
		}
	}
}

type metadataEntry struct {
	path       string
	modified   time.Time
	size, cost int64
	meta       map[string]string
}

func newMetadataCache(budget int64) *metadataCache {
	return &metadataCache{budget: budget, entries: map[string]*list.Element{}}
}
func (c *metadataCache) read(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[path]; e != nil {
		v := e.Value.(*metadataEntry)
		if v.modified.Equal(info.ModTime()) && v.size == info.Size() {
			c.lru.MoveToFront(e)
			return v.meta, nil
		}
		c.used -= v.cost
		c.lru.Remove(e)
		delete(c.entries, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta := map[string]string{}
	if err = json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	cost := int64(len(meta)*256 + len(path) + 128)
	if cost <= c.budget {
		for c.used+cost > c.budget && c.lru.Len() > 0 {
			e := c.lru.Back()
			v := e.Value.(*metadataEntry)
			c.used -= v.cost
			delete(c.entries, v.path)
			c.lru.Remove(e)
		}
		c.entries[path] = c.lru.PushFront(&metadataEntry{path, info.ModTime(), info.Size(), cost, meta})
		c.used += cost
	}
	return meta, nil
}

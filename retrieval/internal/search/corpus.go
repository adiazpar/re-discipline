package search

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
)

// FileMeta identifies one corpus file for staleness comparison.
type FileMeta struct {
	Path  string // relative to .re-discipline/, forward slashes
	MTime int64  // unix nanoseconds
	Size  int64
}

// ScanDocs lists every *.md under <root>/.re-discipline/docs/ except
// docs/INDEX.md, sorted by path. A missing docs dir yields an empty list.
func ScanDocs(root string) ([]FileMeta, error) { return scanDocsContext(context.Background(), root) }
func scanDocsContext(ctx context.Context, root string) ([]FileMeta, error) {
	base := filepath.Join(root, ".re-discipline")
	docsDir := filepath.Join(base, "docs")
	var metas []FileMeta
	err := filepath.WalkDir(docsDir, func(p string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return filepath.SkipAll
			}
			return walkErr
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(base, p)
		if relErr != nil {
			return relErr
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == "docs/INDEX.md" {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil // vanished mid-scan: skip
		}
		metas = append(metas, FileMeta{Path: relSlash, MTime: info.ModTime().UnixNano(), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Path < metas[j].Path })
	return metas, nil
}

// LoadDocs reads and parses each listed file. Unreadable files are
// skipped with a warning — corpus content never fails a build.
func LoadDocs(root string, metas []FileMeta) ([]Doc, []string) {
	return loadDocsContext(context.Background(), root, metas)
}
func loadDocsContext(ctx context.Context, root string, metas []FileMeta) ([]Doc, []string) {
	var docs []Doc
	var warnings []string
	for _, m := range metas {
		if ctx.Err() != nil {
			return nil, nil
		}
		raw, err := os.ReadFile(filepath.Join(root, ".re-discipline", filepath.FromSlash(m.Path)))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v (skipped)", m.Path, err))
			continue
		}
		text := string(raw)
		d := ParseDoc(m.Path, text)
		// Compute copy metadata only during indexing, from the same bytes as FTS.
		// Ordinary parsing/query hydration never hashes the document again.
		if id, err := knowledge.FindingID(text); err == nil && id != "" {
			d.FindingID = id
			d.Fingerprint = knowledge.KnowledgeFingerprint(knowledge.ProjectKnowledge(text).Markdown)
		}
		if d.Warning != "" {
			warnings = append(warnings, m.Path+": "+d.Warning)
		}
		docs = append(docs, d)
	}
	return docs, warnings
}

// ManifestDiffers reports whether the stored index manifest disagrees
// with the current scan — including deletions and moves.
func ManifestDiffers(stored map[string]FileMeta, current []FileMeta) bool {
	if len(stored) != len(current) {
		return true
	}
	for _, m := range current {
		s, ok := stored[m.Path]
		if !ok || s.MTime != m.MTime || s.Size != m.Size {
			return true
		}
	}
	return false
}

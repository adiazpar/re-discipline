package main

import (
	"encoding/json"
	"fmt"
	"github.com/adiazpar/re-discipline/retrieval/internal/search"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Both hosts send the active workspace in hook stdin. Refresh an existing
// project's engine without opening an index or contacting remote services.
func writeSessionStatus(input io.Reader, output io.Writer) {
	var event struct {
		Cwd string `json:"cwd"`
	}
	if err := json.NewDecoder(io.LimitReader(input, 1024*1024)).Decode(&event); err != nil && err != io.EOF {
		return
	}
	root := event.Cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	root, err := search.FindRoot(root)
	if err != nil {
		return
	}
	rd := filepath.Join(root, ".re-discipline")
	if info, err := os.Stat(rd); err != nil || !info.IsDir() {
		return
	}
	refreshProject(root)
	var campaigns []string
	entries, _ := os.ReadDir(filepath.Join(rd, "active"))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		age := max(0, int(time.Since(info.ModTime()).Hours()/24))
		campaigns = append(campaigns, fmt.Sprintf("%s (updated %dd ago)", entry.Name(), age))
	}
	docs := 0
	_ = filepath.WalkDir(filepath.Join(rd, "docs"), func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") && !strings.EqualFold(entry.Name(), "INDEX.md") {
			docs++
		}
		return nil
	})
	part := fmt.Sprintf("%d active campaign(s)", len(campaigns))
	if len(campaigns) > 0 {
		part += " - " + strings.Join(campaigns, ", ")
	}
	fmt.Fprintf(output, "re-discipline: %s. %d docs curated.\n", part, docs)
}

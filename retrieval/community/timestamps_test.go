package community

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
)

func TestFindingTimestampsTrackKnowledgeNotEvidence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".re-discipline", "docs", "clock.md")
	os.MkdirAll(filepath.Dir(path), 0700)
	raw := strings.ReplaceAll(copyMarkdown(copyID, "Clock original", "Private proof"), "\n", "\r\n")
	os.WriteFile(path, []byte(raw), 0640)
	originalInfo, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(root, ".re-discipline", "index.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.Exec(knowledge.CacheTables["finding_stamps"])
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	stamp := func(at time.Time) (string, bool) {
		t.Helper()
		raw, info, e := readFinding(path)
		if e != nil {
			t.Fatal(e)
		}
		next, changed, e := stampFinding(context.Background(), db, path, raw, info, at)
		if e != nil {
			t.Fatal(e)
		}
		return next, changed
	}
	first, changed := stamp(now)
	if !changed {
		t.Fatal("missing initial dates")
	}
	c, u, e := knowledge.TimestampFields(first)
	if e != nil || c != u || c != now.Format(time.RFC3339Nano) {
		t.Fatal(c, u, e)
	}
	if strings.Contains(strings.ReplaceAll(first, "\r\n", ""), "\n") {
		t.Fatal("newline style changed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != originalInfo.Mode().Perm() {
		t.Fatal("permissions changed")
	}
	fp := KnowledgeFingerprint(ProjectKnowledge(first).Markdown)
	os.WriteFile(path, []byte(strings.ReplaceAll(first, "Private proof", "Updated evidence")), 0640)
	evidence, changed := stamp(now.Add(time.Hour))
	if changed {
		t.Fatal("evidence advanced date")
	}
	if KnowledgeFingerprint(ProjectKnowledge(evidence).Markdown) != fp {
		t.Fatal("evidence changed fingerprint")
	}
	os.WriteFile(path, []byte(strings.ReplaceAll(evidence, "Clock original", "Clock corrected")), 0640)
	corrected, changed := stamp(now.Add(2 * time.Hour))
	if !changed {
		t.Fatal("knowledge date not advanced")
	}
	c, u, _ = knowledge.TimestampFields(corrected)
	if c != now.Format(time.RFC3339Nano) || u != now.Add(2*time.Hour).Format(time.RFC3339Nano) {
		t.Fatal(c, u)
	}
	dated, _ := knowledge.WithTimestamps(corrected, "2000-01-01T00:00:00Z", "2099-01-01T00:00:00Z")
	if KnowledgeFingerprint(ProjectKnowledge(dated).Markdown) != KnowledgeFingerprint(ProjectKnowledge(corrected).Markdown) {
		t.Fatal("dates changed content fingerprint")
	}
	db.Close()
	if _, _, e := engine.Query(root, "Clock", engine.Options{}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(path)
	r, e := IdentifyKnowledge(root, []string{"docs/clock.md"})
	if e != nil || r.Timestamped != 0 {
		t.Fatal("cache lost at rebuild", r, e)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("unchanged finding rewritten")
	}
	os.Remove(filepath.Join(root, ".re-discipline", "index.db"))
	r, e = IdentifyKnowledge(root, []string{"docs/clock.md"})
	if e != nil || r.Timestamped != 0 {
		t.Fatal("cache loss fabricated dates", r, e)
	}
}

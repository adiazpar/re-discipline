package community

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type BatchItem struct {
	Key      string   `json:"idempotency_key"`
	Document Document `json:"document"`
}
type BatchOutcome struct {
	Key        string      `json:"idempotency_key"`
	Submission *Submission `json:"submission,omitempty"`
	Error      string      `json:"error,omitempty"`
	Status     int         `json:"status,omitempty"`
}
type PreflightItem struct {
	Path    string  `json:"path"`
	DraftID string  `json:"draft_id,omitempty"`
	State   string  `json:"state"`
	Checks  []Check `json:"checks,omitempty"`
	Error   string  `json:"error,omitempty"`
	Bytes   int     `json:"bytes,omitempty"`
}

func includedEvidence(body string) string {
	lines := strings.Split(body, "\n")
	start := -1
	fence := ""
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			marker := trim[:3]
			if fence == marker {
				fence = ""
			} else if fence == "" {
				fence = marker
			}
			continue
		}
		if fence != "" {
			continue
		}
		heading := strings.ToLower(strings.TrimSpace(line))
		if start < 0 && (heading == "## evidence" || heading == "## supporting evidence") {
			start = i + 1
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
	}
	if start >= 0 {
		return strings.TrimSpace(strings.Join(lines[start:], "\n"))
	}
	return ""
}

// PrepareBatch only visits the explicitly selected files. It neither publishes
// nor interprets a deterministic portability check as semantic approval.
func PrepareBatch(root string, c Connection, paths []string, build string) (any, error) {
	if len(paths) == 0 || len(paths) > 20000 {
		return nil, fmt.Errorf("select 1 to 20000 explicit finding paths")
	}
	items := []PreflightItem{}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		d, checks, err := Prepare(root, c, p, build)
		item := PreflightItem{Path: p, DraftID: d.ID, Checks: checks, State: "new"}
		if err != nil {
			item.State = "excluded"
			item.Error = err.Error()
		} else {
			item.Bytes = len(mustJSON(d.Document))
			if d.Document.FindingID != "" {
				item.State = "updated"
			}
			if d.State != "draft" {
				item.State = d.State
				item.Error = d.Error
			} else {
				for _, check := range checks {
					if check.Blocking {
						item.State = "needs_attention"
						break
					}
				}
			}
		}
		counts[item.State]++
		items = append(items, item)
	}
	return map[string]any{"counts": counts, "items": items, "uploaded": false}, nil
}

// FlushBatch sends bounded chunks with per-document idempotency. It records each
// response before the next request; a lost response is safe to resend. A rate or
// service failure stops the transfer, leaving all outstanding work resumable.
func FlushBatch(ctx context.Context, root, alias, grant string) (any, error) {
	var destination Connection
	if alias != "" {
		settings, e := LoadSettings(root)
		if e != nil {
			return nil, e
		}
		for _, c := range settings.Connections {
			if c.Alias == alias {
				destination = c
				break
			}
		}
		if destination.Alias == "" {
			return nil, fmt.Errorf("unknown community alias %q", alias)
		}
	}
	paths, err := filepath.Glob(filepath.Join(root, ".re-discipline", "community", "drafts", "*.json"))
	if err != nil {
		return nil, err
	}
	groups := map[string][]Draft{}
	order := []string{}
	issues := []map[string]string{}
	sent := 0
	for _, p := range paths {
		d, e := ReadDraft(root, strings.TrimSuffix(filepath.Base(p), ".json"))
		if e != nil {
			return nil, e
		}
		if d.State != "queued" || (alias != "" && d.Connection.Alias != alias) {
			continue
		}
		if alias != "" && (d.Connection.Service != destination.Service || d.Connection.CommunityID != destination.CommunityID) {
			issues = append(issues, map[string]string{"draft_id": d.ID, "error": "The alias now points to a different destination; review the saved draft."})
			continue
		}
		if Digest(d.Document) != d.Digest {
			issues = append(issues, map[string]string{"draft_id": d.ID, "error": "Queued content changed; preview and queue again."})
			continue
		}
		key := d.Connection.Service + "/" + d.Connection.CommunityID
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], d)
	}
	for _, key := range order {
		drafts := groups[key]
		client, e := NewClient(drafts[0].Connection.Service)
		if e != nil {
			return nil, e
		}
		for len(drafts) > 0 {
			n, size := 0, 0
			for n < len(drafts) && n < 50 {
				next := len(mustJSON(drafts[n].Document)) + 150
				if n > 0 && size+next > 1024*1024 {
					break
				}
				size += next
				n++
			}
			chunk := drafts[:n]
			items := []BatchItem{}
			for _, d := range chunk {
				items = append(items, BatchItem{Key: submissionKey(d), Document: d.Document})
			}
			var result struct {
				Items []BatchOutcome `json:"items"`
			}
			e = client.Operation(ctx, "submission.batch", chunk[0].Connection.CommunityID, map[string]any{"items": items, "import_grant": grant}, &result)
			if e != nil {
				return map[string]any{"submitted": sent, "issues": issues, "resumable": true}, e
			}
			byKey := map[string]BatchOutcome{}
			for _, item := range result.Items {
				byKey[item.Key] = item
			}
			stop := false
			for _, d := range chunk {
				item, ok := byKey[submissionKey(d)]
				if !ok {
					return nil, fmt.Errorf("incomplete batch response; retry safely")
				}
				if item.Submission != nil {
					if item.Submission.CommunityID != d.Connection.CommunityID || item.Submission.Digest != d.Digest {
						return nil, fmt.Errorf("batch response does not match submitted content")
					}
					d.State = "submitted"
					d.SubmissionID = item.Submission.ID
					d.FindingID, d.Revision = item.Submission.FindingID, item.Submission.Revision
					d.Error = ""
					d.LastStatus = 200
					sent++
				} else {
					d.LastStatus = item.Status
					d.Error = item.Error
					if d.Error == "" {
						d.Error = "Publication failed"
					}
					issues = append(issues, map[string]string{"draft_id": d.ID, "path": d.Document.SourcePath, "error": d.Error})
					if item.Status == 429 || item.Status >= 500 {
						stop = true
					}
				}
				if e = atomicJSON(draftPath(root, d.ID), d); e != nil {
					return nil, e
				}
			}
			if stop {
				return map[string]any{"submitted": sent, "issues": issues, "resumable": true}, nil
			}
			drafts = drafts[n:]
		}
	}
	return map[string]any{"submitted": sent, "issues": issues, "resumable": len(issues) > 0}, nil
}

// AttachEvidence copies only a selected line range from a selected regular file.
// No globbing, directory upload, recursive dependency resolution, or implicit
// acceptance of local operational material is involved.
func AttachEvidence(root, id, path, label string, start, end int) (Draft, error) {
	d, err := ReadDraft(root, id)
	if err != nil {
		return d, err
	}
	if d.State != "draft" {
		return d, fmt.Errorf("only an unqueued draft can be edited")
	}
	if start < 1 || end < start || end-start >= 1000 || strings.TrimSpace(label) == "" {
		return d, fmt.Errorf("select a labeled range of 1 to 1000 lines")
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return d, err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return d, err
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return d, fmt.Errorf("evidence must be inside the project")
	}
	clean := strings.ToLower(filepath.ToSlash(rel))
	for _, part := range strings.Split(clean, "/") {
		if part == "ops" || part == ".git" || part == ".codex" || part == "community" || strings.HasPrefix(part, ".env") || part == "local-paths.md" {
			return d, fmt.Errorf("local operational material cannot be attached")
		}
	}
	f, err := os.Open(target)
	if err != nil {
		return d, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return d, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4*1024*1024 {
		return d, fmt.Errorf("select a regular evidence file of at most 4 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil {
		return d, err
	}
	if len(b) > 4*1024*1024 {
		return d, fmt.Errorf("evidence grew beyond the 4 MiB limit")
	}
	if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return d, fmt.Errorf("select text evidence")
	}
	lower := strings.ToLower(string(b))
	if strings.Contains(lower, "publish: false") || strings.Contains(lower, "audience: local") || strings.Contains(lower, "visibility: private") {
		return d, fmt.Errorf("evidence explicitly excludes publication")
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if end > len(lines) {
		return d, fmt.Errorf("range exceeds evidence length")
	}
	d.Document.Evidence = append(d.Document.Evidence, Evidence{Label: label, Excerpt: strings.Join(lines[start-1:end], "\n")})
	for _, check := range Validate(d.Document) {
		if check.Code == "local_environment" || check.Code == "secret" || check.Code == "evidence_size" || check.Code == "evidence" {
			return d, errors.New(check.Message)
		}
	}
	return d, atomicJSON(draftPath(root, id), d)
}

package community

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/adiazpar/re-discipline/retrieval/internal/knowledge"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type UploadOptions struct {
	Paths  []string `json:"paths"`
	Verify bool     `json:"verify"`
}
type UploadReport struct {
	Timestamped    int      `json:"timestamped"`
	New            int      `json:"new"`
	Changed        int      `json:"changed"`
	Renamed        int      `json:"renamed"`
	CommunityID    string   `json:"community_id,omitempty"`
	Scanned        int      `json:"scanned"`
	Read           int      `json:"files_read"`
	ReadBytes      int64    `json:"bytes_read"`
	Identified     int      `json:"identified"`
	Unchanged      int      `json:"unchanged"`
	Uploaded       int      `json:"uploaded"`
	UploadBytes    int64    `json:"upload_bytes"`
	RemoteOnly     int      `json:"remote_only"`
	Batches        int      `json:"batches"`
	Issues         []string `json:"issues"`
	IssueCount     int      `json:"issue_count"`
	Complete       bool     `json:"complete"`
	StoredSequence int64    `json:"stored_sequence"`
}

func (r *UploadReport) issue(v string) {
	r.IssueCount++
	if len(r.Issues) < 100 {
		r.Issues = append(r.Issues, v)
	}
}

// Stat hints are acceleration only. Verify bypasses them. Changes close to a
// cache write are always re-read to avoid the ordinary coarse-timestamp race.
func uploadStamp(info os.FileInfo) string {
	parts := []any{info.Size(), info.ModTime().UnixNano(), info.Mode()}
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.IsValid() && v.Kind() == reflect.Struct {
		for _, name := range []string{"Dev", "Ino", "Ctim", "Ctimespec", "CreationTime"} {
			f := v.FieldByName(name)
			if f.IsValid() && f.CanInterface() {
				parts = append(parts, f.Interface())
			}
		}
	}
	return fmt.Sprint(parts...)
}

func readFinding(path string) (string, os.FileInfo, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, e := os.Lstat(path)
		if e != nil {
			return "", nil, e
		}
		if !before.Mode().IsRegular() || before.Size() > 8<<20 {
			return "", nil, fmt.Errorf("finding must be a regular file of at most 8 MiB")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return "", nil, e
		}
		after, e := os.Lstat(path)
		if e != nil {
			return "", nil, e
		}
		if os.SameFile(before, after) && uploadStamp(before) == uploadStamp(after) && int64(len(b)) == after.Size() {
			return string(b), after, nil
		}
	}
	return "", nil, fmt.Errorf("file is changing; retry after the edit finishes")
}

func identifyFinding(path, raw string, info os.FileInfo, preferred ...string) (string, error) {
	id, e := FindingID(raw)
	if e != nil {
		return "", e
	}
	if id != "" {
		return raw, nil
	}
	newID := uuid.NewString()
	if len(preferred) > 0 && preferred[0] != "" {
		newID = preferred[0]
	}
	updated, e := WithFindingID(raw, newID)
	if e != nil {
		return "", e
	}
	return replaceFinding(path, raw, updated, info)
}

func replaceFinding(path, raw, updated string, info os.FileInfo) (string, error) {
	f, e := os.CreateTemp(filepath.Dir(path), ".finding-metadata-*")
	if e != nil {
		return "", e
	}
	temp := f.Name()
	defer os.Remove(temp)
	if e = f.Chmod(info.Mode().Perm()); e == nil {
		_, e = f.WriteString(updated)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return "", e
	}
	current, currentInfo, e := readFinding(path)
	if e != nil {
		return "", e
	}
	if current != raw || !os.SameFile(info, currentInfo) {
		return "", fmt.Errorf("file changed while maintaining finding metadata; retry")
	}
	if e = engine.ReplaceFile(temp, path); e != nil {
		return "", e
	}
	return updated, nil
}

func selectedFinding(rel string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		p = strings.TrimPrefix(filepath.ToSlash(p), ".re-discipline/")
		if p == rel || strings.HasPrefix(rel, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

func UploadKnowledge(ctx context.Context, root string, conn Connection, options UploadOptions, write bool) (UploadReport, error) {
	out := UploadReport{CommunityID: conn.CommunityID, Issues: []string{}}
	c, err := NewClient(conn.Service)
	if err != nil {
		return out, err
	}
	if c.Token == "" {
		return out, fmt.Errorf("only the server operator can upload or reconcile knowledge")
	}
	var library Library
	if err = c.Operation(ctx, "community.get", conn.CommunityID, map[string]any{}, &library); err != nil {
		return out, err
	}
	if library.ID != conn.CommunityID || library.Role != "owner" {
		return out, fmt.Errorf("server did not confirm operator ownership of the connected community")
	}
	remote, err := inventoryKnowledge(ctx, c, conn.CommunityID)
	if err != nil {
		return out, err
	}
	byPath := map[string][]KnowledgeEntry{}
	for _, v := range remote {
		byPath[v.Path] = append(byPath[v.Path], v)
	}
	release, ok := engine.TryLock(root)
	if !ok {
		return out, fmt.Errorf("project index is busy; retry")
	}
	defer release()
	db, err := sql.Open("sqlite", filepath.Join(root, ".re-discipline", "index.db"))
	if err != nil {
		return out, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS upload_files(path TEXT PRIMARY KEY,id TEXT NOT NULL,fingerprint TEXT NOT NULL,stamp TEXT NOT NULL,verified INTEGER NOT NULL,format TEXT NOT NULL,seen TEXT NOT NULL)`); err != nil {
		return out, err
	}
	for _, statement := range knowledge.CacheTables {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			return out, err
		}
	}
	settings, err := LoadSettings(root)
	if err != nil {
		return out, err
	}
	cache, err := db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() {
		if cache != nil {
			cache.Rollback()
		}
	}()
	cachedRows := 0
	run := uuid.NewString()
	local := map[string]KnowledgeEntry{}
	allIDs := map[string]string{}
	collision := false
	base := filepath.Join(root, ".re-discipline")
	err = filepath.WalkDir(filepath.Join(base, "docs"), func(p string, d fs.DirEntry, walkErr error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(base, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.EqualFold(rel, "docs/ops") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(rel), ".md") || strings.EqualFold(rel, "docs/INDEX.md") {
			return nil
		}
		for _, pattern := range settings.Exclude {
			matched, _ := filepath.Match(pattern, rel)
			if matched || strings.HasPrefix(rel, strings.TrimSuffix(pattern, "/")+"/") {
				return nil
			}
		}
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		out.Scanned++
		var id, fp, stamp, format string
		var verified int64
		var stamped bool
		e = cache.QueryRowContext(ctx, `SELECT id,fingerprint,stamp,verified,format,EXISTS(SELECT 1 FROM finding_stamps f WHERE f.id=upload_files.id AND f.fingerprint=upload_files.fingerprint AND f.format=upload_files.format) FROM upload_files WHERE path=?`, rel).Scan(&id, &fp, &stamp, &verified, &format, &stamped)
		cached := e == nil && (!write || !selectedFinding(rel, options.Paths) || stamped) && id != "" && !options.Verify && format == KnowledgeFormat && stamp == uploadStamp(info) && info.ModTime().Before(time.Unix(0, verified).Add(-2*time.Second))
		if !cached {
			raw, current, e := readFinding(p)
			if e != nil {
				out.issue(rel + ": " + e.Error())
				return nil
			}
			out.Read++
			out.ReadBytes += int64(len(raw))
			parsed := engine.Parse(rel, raw)
			id, fp = "", ""
			if parsed.Status == "promoted" && (parsed.Kind == "fact" || parsed.Kind == "reference") {
				id, e = FindingID(raw)
				if e != nil {
					out.issue(rel + ": " + e.Error())
					return nil
				}
				if id == "" && write && selectedFinding(rel, options.Paths) {
					preferred := ""
					if candidates := byPath[rel]; len(candidates) == 1 {
						candidate, _ := WithFindingID(raw, candidates[0].ID)
						projected, pe := PrepareKnowledge(rel, candidate)
						if pe == nil && projected.Fingerprint == candidates[0].Fingerprint {
							preferred = candidates[0].ID
						}
					}
					raw, e = identifyFinding(p, raw, current, preferred)
					if e != nil {
						out.issue(rel + ": " + e.Error())
						return nil
					}
					id, _ = FindingID(raw)
					out.Identified++
					current, e = os.Lstat(p)
					if e != nil {
						return e
					}
				}
				if id == "" {
					if selectedFinding(rel, options.Paths) {
						out.issue(rel + ": missing stable id; upload assigns it automatically")
					}
				} else {
					if write && selectedFinding(rel, options.Paths) {
						var changed bool
						raw, changed, e = stampFinding(ctx, cache, p, raw, current, time.Now())
						if e != nil {
							out.issue(rel + ": " + e.Error())
							return nil
						}
						if changed {
							out.Timestamped++
							current, e = os.Lstat(p)
							if e != nil {
								return e
							}
						}
					}
					prepared, e := PrepareKnowledge(rel, raw)
					if e != nil {
						out.issue(rel + ": " + e.Error())
						return nil
					}
					id, fp = prepared.ID, prepared.Fingerprint
				}
			}
			stamp = uploadStamp(current)
			verified = time.Now().UnixNano()
		}
		if _, e = cache.ExecContext(ctx, `INSERT OR REPLACE INTO upload_files(path,id,fingerprint,stamp,verified,format,seen) VALUES(?,?,?,?,?,?,?)`, rel, id, fp, stamp, verified, KnowledgeFormat, run); e != nil {
			return e
		}
		cachedRows++
		if cachedRows%500 == 0 {
			if e = cache.Commit(); e != nil {
				return e
			}
			cache, e = db.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
		}
		if id != "" {
			if prior := allIDs[id]; prior != "" {
				collision = true
				out.issue("duplicate finding ID: " + prior + " and " + rel)
			}
			allIDs[id] = rel
			if selectedFinding(rel, options.Paths) && fp != "" {
				local[id] = KnowledgeEntry{ID: id, Path: rel, Fingerprint: fp}
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if _, err = cache.ExecContext(ctx, `DELETE FROM upload_files WHERE seen<>?`, run); err != nil {
		return out, err
	}
	if collision {
		return out, fmt.Errorf("duplicate local finding IDs; no files were uploaded")
	}
	if err = cache.Commit(); err != nil {
		return out, err
	}
	agreements := []KnowledgeEntry{}
	for id, l := range local {
		if r, ok := remote[id]; ok && r.Fingerprint == l.Fingerprint {
			agreements = append(agreements, r)
		}
	}
	if err = storeCopyBases(ctx, db, conn, agreements); err != nil {
		return out, err
	}
	for id := range remote {
		if allIDs[id] == "" {
			out.RemoteOnly++
		}
	}

	batch := KnowledgeBatch{}
	batchBytes := 0
	flush := func() error {
		if len(batch.Documents) == 0 {
			return nil
		}
		batch.Key = uuid.NewString()
		batch.CreatedAt = time.Now().Unix()
		var receipt KnowledgeReceipt
		err := c.Operation(ctx, "knowledge.put", conn.CommunityID, batch, &receipt)
		if err != nil {
			return err
		}
		if receipt.CommunityID != conn.CommunityID || receipt.Key != batch.Key || len(receipt.Items) != len(batch.Documents) {
			return fmt.Errorf("server returned a mismatched upload receipt")
		}
		for i, v := range receipt.Items {
			if v.ID != batch.Documents[i].ID || v.Fingerprint != batch.Documents[i].Fingerprint {
				return fmt.Errorf("server did not acknowledge the uploaded content")
			}
		}
		if err = storeCopyBases(ctx, db, conn, receipt.Items); err != nil {
			return err
		}
		out.Uploaded += receipt.Updated
		out.Unchanged += receipt.Unchanged
		out.Batches++
		out.UploadBytes += int64(batchBytes)
		out.StoredSequence = receipt.Sequence
		batch = KnowledgeBatch{}
		batchBytes = 0
		return nil
	}
	ids := make([]string, 0, len(local))
	for id := range local {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := local[id]
		if r, ok := remote[id]; ok && r.Fingerprint == v.Fingerprint && r.Path == v.Path {
			out.Unchanged++
			continue
		}
		if r, ok := remote[id]; !ok {
			out.New++
		} else if r.Fingerprint != v.Fingerprint {
			out.Changed++
		} else {
			out.Renamed++
		}

		if !write {
			continue
		}
		filePath := filepath.Join(base, filepath.FromSlash(v.Path))
		raw, current, e := readFinding(filePath)
		if e != nil {
			out.issue(v.Path + ": " + e.Error())
			continue
		}
		out.Read++
		out.ReadBytes += int64(len(raw))
		var stamped bool
		raw, stamped, e = stampFinding(ctx, db, filePath, raw, current, time.Now())
		if e != nil {
			out.issue(v.Path + ": " + e.Error())
			continue
		}
		if stamped {
			out.Timestamped++
		}
		d, e := PrepareKnowledge(v.Path, raw)
		if e != nil {
			out.issue(v.Path + ": " + e.Error())
			continue
		}
		if d.ID != id {
			out.issue(v.Path + ": identity changed during upload; retry")
			continue
		}
		encoded, _ := json.Marshal(d)
		if len(encoded)+256 > MaxUploadBytes {
			out.issue(v.Path + ": encoded finding exceeds upload batch limit")
			continue
		}
		if len(batch.Documents) >= MaxUploadDocuments || batchBytes+len(encoded)+256 > MaxUploadBytes {
			if err = flush(); err != nil {
				return out, err
			}
		}
		batch.Documents = append(batch.Documents, d)
		batchBytes += len(encoded) + 1
	}
	if err = flush(); err != nil {
		return out, err
	}
	out.Complete = out.IssueCount == 0
	return out, nil
}

func IdentifyKnowledge(root string, paths []string) (UploadReport, error) {
	out := UploadReport{Issues: []string{}}
	if len(paths) == 0 {
		return out, fmt.Errorf("select findings to identify")
	}
	release, ok := engine.TryLock(root)
	if !ok {
		return out, fmt.Errorf("project index is busy")
	}
	defer release()
	base := filepath.Join(root, ".re-discipline")
	var resolveErr error
	base, resolveErr = filepath.EvalSymlinks(base)
	if resolveErr != nil {
		return out, resolveErr
	}
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(base, "index.db"))
	if err != nil {
		return out, err
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, knowledge.CacheTables["finding_stamps"]); err != nil {
		return out, err
	}
	for _, rel := range paths {
		rel = strings.TrimPrefix(filepath.ToSlash(rel), ".re-discipline/")
		if filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || !(strings.HasPrefix(rel, "docs/") || (strings.HasPrefix(rel, "active/") && strings.Contains(rel, "/findings/"))) {
			return out, fmt.Errorf("select local finding paths")
		}
		p := filepath.Join(base, filepath.FromSlash(rel))
		real, e := filepath.EvalSymlinks(p)
		if e != nil {
			return out, e
		}
		inside, e := filepath.Rel(base, real)
		if e != nil || strings.HasPrefix(inside, "..") {
			return out, fmt.Errorf("finding is outside the project")
		}
		raw, info, e := readFinding(p)
		if e != nil {
			return out, e
		}
		out.Scanned++
		id, e := FindingID(raw)
		if e != nil {
			return out, e
		}
		if id == "" {
			raw, e = identifyFinding(p, raw, info)
			if e != nil {
				return out, e
			}
			info, e = os.Lstat(p)
			if e != nil {
				return out, e
			}
			out.Identified++
		}
		_, changed, e := stampFinding(ctx, db, p, raw, info, time.Now())
		if e != nil {
			return out, e
		}
		if changed {
			out.Timestamped++
		} else if id != "" {
			out.Unchanged++
		}
	}
	out.Complete = true
	return out, nil
}

func inventoryKnowledge(ctx context.Context, c *Client, cid string) (map[string]KnowledgeEntry, error) {
	for attempt := 0; attempt < 3; attempt++ {
		out := map[string]KnowledgeEntry{}
		after := ""
		var sequence *int64
		for {
			var page KnowledgePage
			err := c.Operation(ctx, "knowledge.inventory", cid, map[string]any{"after": after, "sequence": sequence}, &page)
			var api *APIError
			if errors.As(err, &api) && api.Status == 409 {
				break
			}
			if err != nil {
				return nil, err
			}
			if sequence == nil {
				v := page.Sequence
				sequence = &v
			}
			for _, v := range page.Items {
				out[v.ID] = v
			}
			if page.Next == "" {
				return out, nil
			}
			if page.Next <= after {
				return nil, fmt.Errorf("inventory cursor did not advance")
			}
			after = page.Next
		}
	}
	return nil, fmt.Errorf("community changed repeatedly during inventory; retry upload")
}

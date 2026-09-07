package community

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/engine"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var regexpAlias = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func cacheRoot(root string, c Connection) string {
	h := sha256.Sum256([]byte(c.Service + "/" + c.CommunityID))
	return filepath.Join(root, ".re-discipline", "community", "cache", hex.EncodeToString(h[:]))
}
func cacheDB(root string, c Connection) (*sql.DB, error) {
	base := cacheRoot(root, c)
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(base, "cache.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS docs(id TEXT PRIMARY KEY,revision TEXT NOT NULL,document TEXT NOT NULL); CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE IF NOT EXISTS pending(id TEXT PRIMARY KEY);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func meta(db *sql.DB, key string) string {
	var s string
	db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&s)
	return s
}
func (c *Client) Sync(ctx context.Context, root string, conn Connection) (any, error) {
	if err := os.MkdirAll(filepath.Join(cacheRoot(root, conn), ".re-discipline"), 0700); err != nil {
		return nil, err
	}
	release, ok := engine.TryLock(cacheRoot(root, conn))
	if !ok {
		return nil, fmt.Errorf("another process is synchronizing this community")
	}
	defer release()
	db, err := cacheDB(root, conn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var cursor int64
	db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='cursor'`).Scan(&cursor)
	through := int64(0)
	count := 0
	for {
		var page Changes
		err = c.Operation(ctx, "changes", conn.CommunityID, map[string]any{"since": cursor, "through": through, "limit": 200}, &page)
		if err != nil {
			var ae *APIError
			if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403 || ae.Status == 404) {
				db.Exec(`INSERT INTO meta(key,value) VALUES('blocked','true') ON CONFLICT(key) DO UPDATE SET value='true'`)
			}
			return nil, err
		}
		if page.Library.ID != conn.CommunityID || page.Through < cursor {
			return nil, fmt.Errorf("invalid synchronization response")
		}
		through = page.Through
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		valid := true
		for _, change := range page.Changes {
			if _, e = uuid.Parse(change.FindingID); e != nil {
				valid = false
				break
			}
			if change.Sequence <= cursor || change.Sequence > through {
				e = fmt.Errorf("invalid change sequence")
				valid = false
				break
			}
			if change.Deleted {
				_, e = tx.Exec(`DELETE FROM docs WHERE id=?`, change.FindingID)
			} else {
				b, _ := json.Marshal(change.Document)
				_, e = tx.Exec(`INSERT INTO docs(id,revision,document) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,document=excluded.document`, change.FindingID, change.Revision, string(b))
			}
			if e != nil {
				valid = false
				break
			}
			if _, e = tx.Exec(`INSERT OR IGNORE INTO pending(id) VALUES(?)`, change.FindingID); e != nil {
				valid = false
				break
			}
			cursor = change.Sequence
			count++
		}
		if !valid {
			tx.Rollback()
			return nil, e
		}
		if page.More && len(page.Changes) == 0 {
			tx.Rollback()
			return nil, fmt.Errorf("sync did not advance")
		}
		if _, e = tx.Exec(`INSERT INTO meta(key,value) VALUES('cursor',?),('synced_at',?),('blocked','false'),('library',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprint(cursor), time.Now().UTC().Format(time.RFC3339), string(mustJSON(page.Library))); e != nil {
			tx.Rollback()
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
		if !page.More {
			break
		}
	}
	var relations []Relation
	if e := c.Operation(ctx, "finding.relations", conn.CommunityID, map[string]any{}, &relations); e == nil {
		if _, e = db.Exec(`INSERT INTO meta(key,value) VALUES('relations',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(mustJSON(relations))); e != nil {
			return nil, e
		}
	} else {
		// Older services have no relationship endpoint. Never retain stale
		// equivalence after a failed refresh; independent results remain useful.
		var ae *APIError
		if errors.As(e, &ae) && (ae.Status == 401 || ae.Status == 403 || ae.Status == 404) {
			db.Exec(`INSERT INTO meta(key,value) VALUES('blocked','true') ON CONFLICT(key) DO UPDATE SET value='true'`)
			return nil, e
		}
		if _, e = db.Exec(`DELETE FROM meta WHERE key='relations'`); e != nil {
			return nil, e
		}
	}
	return map[string]any{"community": conn.Alias, "sequence": cursor, "changes": count, "synced_at": meta(db, "synced_at")}, nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

type Result struct {
	Service       string     `json:"service,omitempty"`
	Locations     []Location `json:"locations,omitempty"`
	Contributions []Result   `json:"contributions,omitempty"`
	CanonicalID   string     `json:"canonical_id,omitempty"`
	Source        string     `json:"source"`
	CommunityID   string     `json:"community_id,omitempty"`
	FindingID     string     `json:"finding_id,omitempty"`
	Revision      string     `json:"revision,omitempty"`
	Path          string     `json:"path,omitempty"`
	Title         string     `json:"title"`
	Snippet       string     `json:"snippet"`
	Kind          string     `json:"kind"`
	Grade         string     `json:"grade"`
	URL           string     `json:"url,omitempty"`
}
type QueryResult struct {
	Hits     []Result         `json:"hits"`
	Warnings []string         `json:"warnings"`
	Sources  []map[string]any `json:"sources"`
}

func CachedQuery(root string, c Connection, q string, opts engine.Options) ([]Result, map[string]any, error) {
	if err := os.MkdirAll(filepath.Join(cacheRoot(root, c), ".re-discipline"), 0700); err != nil {
		return nil, nil, err
	}
	release, ok := engine.TryLock(cacheRoot(root, c))
	if !ok {
		return nil, nil, fmt.Errorf("community cache is in use; retry shortly")
	}
	defer release()
	db, err := cacheDB(root, c)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	if meta(db, "blocked") == "true" {
		return nil, nil, fmt.Errorf("cached access is blocked; reconnect and sync after restoring permission")
	}
	cursor := meta(db, "cursor")
	synced := meta(db, "synced_at")
	if cursor == "" {
		return nil, nil, fmt.Errorf("no offline cache; connect and sync while online")
	}
	gen := filepath.Join(cacheRoot(root, c), "indexes", "current")
	if err = os.MkdirAll(filepath.Join(gen, ".re-discipline", "docs"), 0700); err != nil {
		return nil, nil, err
	}
	if meta(db, "materialized") == "" {
		if _, err = db.Exec(`INSERT OR IGNORE INTO pending SELECT id FROM docs`); err != nil {
			return nil, nil, err
		}
	}
	rows, err := db.Query(`SELECT p.id,COALESCE(d.document,'') FROM pending p LEFT JOIN docs d ON d.id=p.id`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var fid, body string
		if err = rows.Scan(&fid, &body); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if _, err = uuid.Parse(fid); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var d Document
		p := filepath.Join(gen, ".re-discipline", "docs", fid+".md")
		if body == "" {
			if e := os.Remove(p); e != nil && !os.IsNotExist(e) {
				rows.Close()
				return nil, nil, e
			}
			continue
		}
		if err = json.Unmarshal([]byte(body), &d); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if old, e := os.ReadFile(p); e != nil || string(old) != d.Markdown {
			if err = os.WriteFile(p, []byte(d.Markdown), 0600); err != nil {
				rows.Close()
				return nil, nil, err
			}
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	var relations []Relation
	json.Unmarshal([]byte(meta(db, "relations")), &relations)
	canonical := map[string]string{}
	for _, rel := range relations {
		if rel.Kind != "equivalent" || !rel.Current {
			continue
		}
		var a, b string
		if db.QueryRow(`SELECT revision FROM docs WHERE id=?`, rel.SourceID).Scan(&a) == nil && db.QueryRow(`SELECT revision FROM docs WHERE id=?`, rel.TargetID).Scan(&b) == nil && a == rel.SourceRevision && b == rel.TargetRevision {
			canonical[rel.SourceID] = rel.TargetID
		}
	}
	if opts.Limit <= 0 {
		opts.Limit = 8
	}
	opts.Limit += len(canonical)
	hits, warnings, err := engine.Query(gen, q, opts)
	if err != nil {
		return nil, nil, err
	}
	if _, err = db.Exec(`DELETE FROM pending; INSERT INTO meta(key,value) VALUES('materialized','true') ON CONFLICT(key) DO UPDATE SET value='true'`); err != nil {
		return nil, nil, err
	}
	out := []Result{}
	for _, h := range hits {
		fid := strings.TrimSuffix(filepath.Base(h.Path), ".md")
		var rid string
		if err = db.QueryRow(`SELECT revision FROM docs WHERE id=?`, fid).Scan(&rid); err != nil {
			return nil, nil, err
		}
		out = append(out, Result{Source: c.Alias, Service: c.Service, CommunityID: c.CommunityID, FindingID: fid, Revision: rid, CanonicalID: canonical[fid], Title: h.Title, Snippet: h.Snippet, Kind: h.Kind, Grade: h.Grade, URL: c.Service + "/communities/" + c.CommunityID + "/findings/" + fid})
	}
	return out, map[string]any{"source": c.Alias, "sequence": cursor, "synced_at": synced, "cached": true, "warnings": warnings}, nil
}
func Query(ctx context.Context, root, q, mode string, offline bool, opts engine.Options) (QueryResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 8
	}
	// Fetch a full page from each source, then collapse before the final limit.
	// Copies across sources cannot consume slots in the resulting page.
	opts.Limit = limit
	s, err := LoadSettings(root)
	out := QueryResult{Hits: []Result{}, Warnings: []string{}, Sources: []map[string]any{}}
	if err != nil {
		return out, err
	}
	if mode == "" {
		mode = s.Mode
	}
	if mode != "local" && mode != "external" && mode != "both" {
		return out, fmt.Errorf("invalid source mode")
	}
	if mode != "external" {
		hits, warnings, e := engine.Query(root, q, opts)
		if e != nil {
			return out, e
		}
		out.Warnings = append(out.Warnings, warnings...)
		for _, h := range hits {
			out.Hits = append(out.Hits, Result{Source: "local", Path: h.Path, Title: h.Title, Snippet: h.Snippet, Kind: h.Kind, Grade: h.Grade})
		}
	}
	if mode != "local" {
		for _, conn := range s.Connections {
			if !offline {
				client, e := NewClient(conn.Service)
				if e != nil {
					return out, e
				}
				_, e = client.Sync(ctx, root, conn)
				if e != nil {
					var ae *APIError
					if errors.As(e, &ae) && ae.Status < 500 {
						out.Warnings = append(out.Warnings, conn.Alias+": "+e.Error())
						continue
					}
					out.Warnings = append(out.Warnings, conn.Alias+": offline cache used after sync failure")
				}
			}
			hits, source, e := CachedQuery(root, conn, q, opts)
			if e != nil {
				out.Warnings = append(out.Warnings, conn.Alias+": "+e.Error())
				continue
			}
			out.Hits = append(out.Hits, hits...)
			out.Sources = append(out.Sources, source)
		}
	}
	out.Hits, err = collapseCopies(root, interleaveSources(out.Hits), limit)
	return out, err
}

type Draft struct {
	LastStatus   int        `json:"last_status,omitempty"`
	ReplacedBy   string     `json:"replaced_by,omitempty"`
	LocalID      string     `json:"local_id,omitempty"`
	SourceDigest string     `json:"source_digest,omitempty"`
	ID           string     `json:"id"`
	Connection   Connection `json:"connection"`
	Document     Document   `json:"document"`
	State        string     `json:"state"`
	Digest       string     `json:"digest,omitempty"`
	SubmissionID string     `json:"submission_id,omitempty"`
	Error        string     `json:"error,omitempty"`
}

func draftPath(root, id string) string {
	return filepath.Join(root, ".re-discipline", "community", "drafts", id+".json")
}
func Prepare(root string, c Connection, rel, build string) (Draft, []Check, error) {
	clean := strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(clean, ".re-discipline/") {
		clean = strings.TrimPrefix(clean, ".re-discipline/")
	}
	base, err := filepath.EvalSymlinks(filepath.Join(root, ".re-discipline", "docs"))
	if err != nil {
		return Draft{}, nil, err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, ".re-discipline", filepath.FromSlash(clean)))
	if err != nil {
		return Draft{}, nil, err
	}
	inside, err := filepath.Rel(base, target)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return Draft{}, nil, fmt.Errorf("finding must be inside docs/")
	}
	settings, err := LoadSettings(root)
	if err != nil {
		return Draft{}, nil, err
	}
	for _, pattern := range settings.Exclude {
		if matched, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(clean)); matched || strings.HasPrefix(strings.ToLower(clean), strings.TrimSuffix(strings.ToLower(pattern), "/")+"/") {
			return Draft{}, nil, fmt.Errorf("finding excluded by local publication settings")
		}
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return Draft{}, nil, err
	}
	parsed := engine.Parse(clean, string(b))
	if build == "" {
		build = parsed.Build
	}
	d := Document{SourcePath: clean, Markdown: string(b), Kind: parsed.Kind, Grade: parsed.Grade, Build: build, Evidence: []Evidence{}}
	for _, e := range parsed.Evidence {
		if strings.HasPrefix(e, "https://") {
			d.Evidence = append(d.Evidence, Evidence{Label: e, URL: e})
		} else {
			d.Evidence = append(d.Evidence, Evidence{Label: e, Unavailable: true})
		}
	}
	if excerpt := includedEvidence(parsed.Body); excerpt != "" {
		d.Evidence = append(d.Evidence, Evidence{Label: "Supporting evidence included in this finding", Excerpt: excerpt})
	}
	checks := Validate(d)
	if parsed.Status != "promoted" {
		checks = append(checks, Check{"promotion", "Only promoted findings can be published.", true})
	}
	lower := strings.ToLower(string(b))
	if strings.Contains(lower, "publish: false") || strings.Contains(lower, "audience: local") || strings.Contains(lower, "visibility: private") {
		checks = append(checks, Check{"private_marker", "Document explicitly excludes publication.", true})
	}
	// Hard exclusions are never copied into a publication draft.
	for _, check := range checks {
		if check.Code == "source_path" || check.Code == "kind" || check.Code == "private_marker" {
			return Draft{}, checks, fmt.Errorf("finding excluded from publication: %s", check.Message)
		}
	}
	draft, err := identityDraft(root, c, d, b)
	if err != nil {
		return draft, checks, err
	}
	if draft.State != "draft" {
		return draft, Validate(draft.Document), nil
	}
	checks = Validate(draft.Document)
	if parsed.Status != "promoted" {
		checks = append(checks, Check{"promotion", "Only promoted findings can be published.", true})
	}
	if err = atomicJSON(draftPath(root, draft.ID), draft); err != nil {
		return draft, checks, err
	}
	return draft, checks, nil
}
func Queue(root, id string) (Draft, error) {
	d, err := ReadDraft(root, id)
	if err != nil {
		return d, err
	}
	if d.State != "draft" && d.State != "queued" {
		return d, fmt.Errorf("only an active draft can be queued")
	}
	for _, c := range Validate(d.Document) {
		if c.Blocking {
			return d, fmt.Errorf("%s: %s", c.Code, c.Message)
		}
	}
	d.State = "queued"
	d.Digest = Digest(d.Document)
	d.Error = ""
	return d, atomicJSON(draftPath(root, id), d)
}
func ReadDraft(root, id string) (Draft, error) {
	var d Draft
	if _, err := uuid.Parse(id); err != nil {
		return d, fmt.Errorf("invalid draft ID")
	}
	b, err := os.ReadFile(draftPath(root, id))
	if err != nil {
		return d, err
	}
	err = json.Unmarshal(b, &d)
	if err == nil && d.ID != id {
		err = fmt.Errorf("draft ID mismatch")
	}
	return d, err
}
func Flush(ctx context.Context, root string) ([]Draft, error) {
	paths, err := filepath.Glob(filepath.Join(root, ".re-discipline", "community", "drafts", "*.json"))
	if err != nil {
		return nil, err
	}
	out := []Draft{}
	for _, p := range paths {
		d, e := ReadDraft(root, strings.TrimSuffix(filepath.Base(p), ".json"))
		if e != nil {
			return out, e
		}
		if d.State != "queued" {
			continue
		}
		if Digest(d.Document) != d.Digest {
			d.Error = "Queued content changed; preview and queue it again."
			out = append(out, d)
			continue
		}
		c, e := NewClient(d.Connection.Service)
		if e != nil {
			return out, e
		}
		var sub Submission
		e = c.Operation(ctx, "submission.create", d.Connection.CommunityID, map[string]any{"idempotency_key": d.ID, "document": d.Document}, &sub)
		if e != nil {
			d.Error = e.Error()
		} else {
			d.State = "submitted"
			d.SubmissionID = sub.ID
			d.Error = ""
		}
		if e = atomicJSON(p, d); e != nil {
			return out, e
		}
		out = append(out, d)
	}
	return out, nil
}

// PruneIndexes only removes numeric generated index directories under a caller-owned cache.
// Callers hold the cache lock; two generations are retained for recovery.
func PruneIndexes(base, current string) {
	entries, _ := os.ReadDir(base)
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.ParseInt(e.Name(), 10, 64); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, _ := strconv.ParseInt(names[i], 10, 64)
		b, _ := strconv.ParseInt(names[j], 10, 64)
		return a > b
	})
	for i, n := range names {
		if i >= 2 && n != current {
			os.RemoveAll(filepath.Join(base, n))
		}
	}
}

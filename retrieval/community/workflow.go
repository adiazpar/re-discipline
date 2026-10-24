package community

import (
	"context"
	"encoding/json"
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
)

var regexpAlias = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

type Result struct {
	Build         string     `json:"build,omitempty"`
	Relevance     *float64   `json:"relevance,omitempty"`
	Versions      []Result   `json:"versions,omitempty"`
	TextDigest    string     `json:"text_digest,omitempty"`
	Status        string     `json:"status,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
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
	Root       string            `json:"root,omitempty"`
	Assistance *AssistanceStatus `json:"assistance,omitempty"`
	Hits       []Result          `json:"hits"`
	Warnings   []string          `json:"warnings"`
	Sources    []map[string]any  `json:"sources"`
}

func queryCandidates(ctx context.Context, root, q, mode string, offline bool, opts engine.Options) (QueryResult, error) {
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
	if mode != "local" {
		s.Connections, err = selectConnections(s.Connections, opts.Communities)
		if err != nil {
			return out, err
		}
	} else if len(opts.Communities) > 0 {
		return out, fmt.Errorf("community selection requires external or both source mode")
	}
	if mode != "external" {
		hits, warnings, e := engine.Query(root, q, opts)
		if e != nil {
			return out, e
		}
		out.Warnings = append(out.Warnings, warnings...)
		for _, h := range hits {
			out.Hits = append(out.Hits, Result{Source: "local", Path: h.Path, Title: h.Title, Snippet: h.Snippet, Kind: h.Kind, Grade: h.Grade, Status: h.Status, Build: h.Build, TextDigest: h.TextDigest})
		}
	}
	if mode != "local" {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var remoteResults []remoteResult
		if !offline {
			remoteResults = remoteQueries(ctx, s.Connections, q, opts)
		}
		for i, conn := range s.Connections {
			source := map[string]any{"source": conn.Alias, "retrieval": "remote", "cached": false, "available": false}
			if offline {
				out.Warnings = append(out.Warnings, conn.Alias+": remote community unavailable offline")
			} else {
				remote, e := remoteResults[i].result, remoteResults[i].err
				if e != nil {
					out.Warnings = append(out.Warnings, conn.Alias+": "+e.Error())
				} else {
					source["available"] = true
					if matched, me := matchLocal(ctx, root, out.Hits, conn, false); me != nil {
						out.Warnings = append(out.Warnings, conn.Alias+": copy checks unavailable: "+me.Error())
					} else {
						out.Hits = matched
					}
					out.Hits = append(out.Hits, remote.Hits...)
					out.Warnings = append(out.Warnings, remote.Warnings...)
				}
			}
			out.Sources = append(out.Sources, source)
		}
	}
	out.Hits, err = collapseCopies(root, interleaveSources(out.Hits), 128)
	return out, err
}

type Draft struct {
	Projection   *ClaimProjection `json:"projection,omitempty"` // local preview; never part of submitted Document
	TransferKey  string           `json:"transfer_key,omitempty"`
	FindingID    string           `json:"finding_id,omitempty"`
	Revision     string           `json:"revision,omitempty"`
	LastStatus   int              `json:"last_status,omitempty"`
	ReplacedBy   string           `json:"replaced_by,omitempty"`
	LocalID      string           `json:"local_id,omitempty"`
	SourceDigest string           `json:"source_digest,omitempty"`
	ID           string           `json:"id"`
	Connection   Connection       `json:"connection"`
	Document     Document         `json:"document"`
	State        string           `json:"state"`
	Digest       string           `json:"digest,omitempty"`
	SubmissionID string           `json:"submission_id,omitempty"`
	Error        string           `json:"error,omitempty"`
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
	projection := ProjectPublication(string(b))
	d := Document{SourceNamespace: c.SourceNamespace, SourcePath: clean, Markdown: projection.Markdown, Kind: parsed.Kind, Grade: parsed.Grade, Build: build, Evidence: []Evidence{}}
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
	draft.Projection = &projection
	checks = Validate(draft.Document)
	if len(projection.RemovedSections) > 0 {
		checks = append(checks, Check{"claims_projection", "Research stays local. Preview the claim and retain any conditions from removed sections: " + strings.Join(projection.RemovedSections, ", "), projection.NeedsReview})
	}
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
	if d.Projection != nil && d.Projection.NeedsReview {
		return d, fmt.Errorf("review the removed research and preserve its claim qualifications with publish.update before queuing")
	}
	if d.State == "queued" && d.Digest != Digest(d.Document) {
		return d, fmt.Errorf("queued payload is immutable; resolve its transfer before revising")
	}
	for _, c := range Validate(d.Document) {
		if c.Blocking {
			return d, fmt.Errorf("%s: %s", c.Code, c.Message)
		}
	}
	if d.State == "draft" {
		d.TransferKey = uuid.NewSHA1(uuid.NameSpaceURL, []byte(d.ID+"/"+Digest(d.Document))).String()
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
		c, e := NewConnectionClient(d.Connection, root)
		if e != nil {
			return out, e
		}
		var sub Submission
		e = c.Operation(ctx, "submission.create", d.Connection.CommunityID, map[string]any{"idempotency_key": submissionKey(d), "document": d.Document}, &sub)
		if e != nil {
			d.Error = e.Error()
		} else {
			d.State = "submitted"
			d.SubmissionID = sub.ID
			d.FindingID, d.Revision = sub.FindingID, sub.Revision
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

package community

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// The registry is local provenance, never authority supplied by another publisher.
// It is separate from portable documents so preparation does not rewrite findings.
func publicationDB(root string) (*sql.DB, error) {
	p := filepath.Join(root, ".re-discipline", "community", "publications.db")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(p)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS identities(id TEXT PRIMARY KEY,path TEXT UNIQUE NOT NULL,source_digest TEXT NOT NULL);
	CREATE INDEX IF NOT EXISTS identity_digest ON identities(source_digest);
	CREATE TABLE IF NOT EXISTS receipts(service TEXT NOT NULL,community TEXT NOT NULL,local_id TEXT NOT NULL,source_digest TEXT NOT NULL,portable_digest TEXT NOT NULL,finding_id TEXT NOT NULL,revision TEXT NOT NULL,draft_id TEXT NOT NULL,PRIMARY KEY(service,community,local_id));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func sourceDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func identify(root, path, digest string) (string, error) {
	db, err := publicationDB(root)
	if err != nil {
		return "", err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var localID string
	err = tx.QueryRow(`SELECT id FROM identities WHERE path=?`, path).Scan(&localID)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if localID == "" {
		// A byte-identical move may retain identity only when exactly one old
		// location disappeared. Copies and ambiguous matches get new identities.
		rows, e := tx.Query(`SELECT id,path FROM identities WHERE source_digest=?`, digest)
		if e != nil {
			return "", e
		}
		matches := []string{}
		for rows.Next() {
			var id, old string
			if e = rows.Scan(&id, &old); e != nil {
				rows.Close()
				return "", e
			}
			if _, e = os.Stat(filepath.Join(root, ".re-discipline", filepath.FromSlash(old))); os.IsNotExist(e) {
				matches = append(matches, id)
			}
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return "", e
		}
		if len(matches) == 1 {
			localID = matches[0]
		} else {
			localID = uuid.NewString()
		}
	}
	_, err = tx.Exec(`INSERT INTO identities(id,path,source_digest) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET path=excluded.path,source_digest=excluded.source_digest`, localID, path, digest)
	if err != nil {
		return "", err
	}
	return localID, tx.Commit()
}

func identityDraft(root string, c Connection, d Document, source []byte) (Draft, error) {
	sha := sourceDigest(source)
	lid, err := identify(root, d.SourcePath, sha)
	if err != nil {
		return Draft{}, err
	}
	db, err := publicationDB(root)
	if err != nil {
		return Draft{}, err
	}
	defer db.Close()
	var oldSHA, fid, revision, previous string
	err = db.QueryRow(`SELECT source_digest,finding_id,revision,draft_id FROM receipts WHERE service=? AND community=? AND local_id=?`, c.Service, c.CommunityID, lid).Scan(&oldSHA, &fid, &revision, &previous)
	if err != nil && err != sql.ErrNoRows {
		return Draft{}, err
	}
	if oldSHA == sha && previous != "" {
		old, e := ReadDraft(root, previous)
		if e == nil && old.Document.Build == d.Build {
			cache, e := cacheDB(root, c)
			if e != nil {
				return Draft{}, e
			}
			var current string
			if meta(cache, "cursor") != "" {
				e = cache.QueryRow(`SELECT revision FROM docs WHERE id=?`, fid).Scan(&current)
				if e == sql.ErrNoRows {
					cache.Close()
					old.State = "withdrawn"
					old.Error = "The published finding was withdrawn; review before preparing another publication."
					return old, nil
				}
				if e != nil {
					cache.Close()
					return Draft{}, e
				}
				if current != revision {
					cache.Close()
					old.State = "conflict"
					old.Error = "The community has a newer revision; compare it before preparing an update."
					return old, nil
				}
			}
			cache.Close()
			old.State = "unchanged"
			return old, nil
		}
	}
	d.FindingID = fid
	d.BaseRevision = revision
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte(c.Service+"/"+c.CommunityID+"/"+lid+"/"+sha+"/"+d.Build+"/"+revision)).String()
	if old, e := ReadDraft(root, key); e == nil {
		for n := 0; old.ReplacedBy != ""; n++ {
			if n >= 32 {
				return Draft{}, fmt.Errorf("draft replacement chain requires reconciliation")
			}
			old, e = ReadDraft(root, old.ReplacedBy)
			if e != nil {
				return Draft{}, e
			}
		}
		return old, nil
	} else if !os.IsNotExist(e) {
		return Draft{}, e
	}
	return Draft{ID: key, LocalID: lid, SourceDigest: sha, Connection: c, Document: d, State: "draft"}, nil
}

type Adoption struct {
	DraftID      string `json:"draft_id"`
	SourceDigest string `json:"source_digest"`
}

// Reconcile links submitted drafts to accepted cache records by the complete
// payload digest. Legacy drafts need an explicit, verified source hash; matching
// a path or a title alone never establishes equivalence.
func Reconcile(root string, c Connection, legacy []Adoption) (map[string]any, error) {
	cache, err := cacheDB(root, c)
	if err != nil {
		return nil, err
	}
	defer cache.Close()
	if meta(cache, "blocked") == "true" || meta(cache, "cursor") == "" {
		return nil, fmt.Errorf("sync an accessible community before reconciling")
	}
	type accepted struct{ fid, rid string }
	acceptedByDigest := map[string][]accepted{}
	rows, err := cache.Query(`SELECT id,revision,document FROM docs`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var fid, rid, body string
		if err = rows.Scan(&fid, &rid, &body); err != nil {
			rows.Close()
			return nil, err
		}
		var d Document
		if err = json.Unmarshal([]byte(body), &d); err != nil {
			rows.Close()
			return nil, err
		}
		key := Digest(d)
		acceptedByDigest[key] = append(acceptedByDigest[key], accepted{fid, rid})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	adopt := map[string]string{}
	for _, v := range legacy {
		adopt[v.DraftID] = v.SourceDigest
	}
	paths, err := filepath.Glob(filepath.Join(root, ".re-discipline", "community", "drafts", "*.json"))
	if err != nil {
		return nil, err
	}
	count := 0
	warnings := []string{}
	registry, err := publicationDB(root)
	if err != nil {
		return nil, err
	}
	defer registry.Close()
	for _, p := range paths {
		d, e := ReadDraft(root, strings.TrimSuffix(filepath.Base(p), ".json"))
		if e != nil {
			return nil, e
		}
		if d.Connection.Service != c.Service || d.Connection.CommunityID != c.CommunityID || d.State != "submitted" || d.SubmissionID == "" || Digest(d.Document) != d.Digest {
			continue
		}
		matches := acceptedByDigest[d.Digest]
		if len(matches) != 1 {
			continue
		}
		if d.SourceDigest == "" {
			sha := adopt[d.ID]
			if sha == "" {
				continue
			}
			b, e := os.ReadFile(filepath.Join(root, ".re-discipline", filepath.FromSlash(d.Document.SourcePath)))
			if e != nil || sourceDigest(b) != sha {
				warnings = append(warnings, d.Document.SourcePath+": source changed; legacy identity was not adopted")
				continue
			}
			d.SourceDigest = sha
			d.LocalID, e = identify(root, d.Document.SourcePath, sha)
			if e != nil {
				return nil, e
			}
			if e = atomicJSON(p, d); e != nil {
				return nil, e
			}
		}
		if d.LocalID == "" {
			continue
		}
		_, e = registry.Exec(`INSERT INTO receipts(service,community,local_id,source_digest,portable_digest,finding_id,revision,draft_id) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(service,community,local_id) DO UPDATE SET source_digest=excluded.source_digest,portable_digest=excluded.portable_digest,finding_id=excluded.finding_id,revision=excluded.revision,draft_id=excluded.draft_id`, c.Service, c.CommunityID, d.LocalID, d.SourceDigest, d.Digest, matches[0].fid, matches[0].rid, d.ID)
		if e != nil {
			return nil, e
		}
		count++
	}
	return map[string]any{"verified": count, "warnings": warnings}, nil
}

type Location struct {
	CanonicalID string `json:"canonical_id,omitempty"`
	Source      string `json:"source"`
	Service     string `json:"service,omitempty"`
	CommunityID string `json:"community_id,omitempty"`
	FindingID   string `json:"finding_id,omitempty"`
	Revision    string `json:"revision,omitempty"`
	Path        string `json:"path,omitempty"`
	URL         string `json:"url,omitempty"`
}

func collapseCopies(root string, hits []Result, limit int) ([]Result, error) {
	db, err := publicationDB(root)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	aliases := map[string]string{}
	for _, h := range hits {
		if h.Source != "local" {
			continue
		}
		b, e := os.ReadFile(filepath.Join(root, ".re-discipline", filepath.FromSlash(h.Path)))
		if e != nil {
			continue
		}
		rows, e := db.Query(`SELECT r.service,r.community,r.finding_id,r.revision FROM receipts r JOIN identities i ON i.id=r.local_id WHERE i.path=? AND r.source_digest=?`, h.Path, sourceDigest(b))
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var service, c, f, r string
			if e = rows.Scan(&service, &c, &f, &r); e != nil {
				rows.Close()
				return nil, e
			}
			aliases[service+"/"+c+"/"+f+"/"+r] = "local/" + h.Path
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return nil, e
		}
	}
	out := []Result{}
	seen := map[string]int{}
	for _, h := range hits {
		key := h.Service + "/" + h.CommunityID + "/" + h.FindingID + "/" + h.Revision
		if h.Source == "local" {
			key = "local/" + h.Path
		} else if local := aliases[key]; local != "" {
			key = local
		}
		loc := Location{Source: h.Source, Service: h.Service, CommunityID: h.CommunityID, FindingID: h.FindingID, Revision: h.Revision, Path: h.Path, URL: h.URL, CanonicalID: h.CanonicalID}
		if i, ok := seen[key]; ok {
			if h.Source == "local" && out[i].Source != "local" {
				h.Locations = append([]Location{loc}, out[i].Locations...)
				out[i] = h
			} else {
				out[i].Locations = append(out[i].Locations, loc)
			}
			continue
		}
		seen[key] = len(out)
		h.Locations = []Location{loc}
		out = append(out, h)
	}
	out = GroupContributions(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Scores from independently indexed corpora are not directly comparable. Blend
// ranked source pages before deduplication so a full local page cannot starve a
// community-only result. Ties prefer the local read surface.
func interleaveSources(hits []Result) []Result {
	groups := map[string][]Result{}
	order := []string{}
	for _, h := range hits {
		key := h.Source
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], h)
	}
	out := make([]Result, 0, len(hits))
	for rank := 0; len(out) < len(hits); rank++ {
		for _, key := range order {
			if rank < len(groups[key]) {
				out = append(out, groups[key][rank])
			}
		}
	}
	return out
}

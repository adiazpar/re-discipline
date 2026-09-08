package community

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
)

func sourceDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Draft files are a disposable preparation cache. Only the server resolves
// document identity; a missing local ledger never asserts a new finding.
func identityDraft(root string, c Connection, d Document, source []byte) (Draft, error) {
	sha := sourceDigest(source)
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte(c.Service+"/"+c.CommunityID+"/"+d.SourceNamespace+"/"+d.SourcePath+"/"+sha+"/"+d.Build)).String()
	if old, e := ReadDraft(root, key); e == nil {
		for n := 0; old.ReplacedBy != ""; n++ {
			if n >= 32 {
				return Draft{}, fmt.Errorf("draft replacement chain too long")
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
	return Draft{ID: key, SourceDigest: sha, Connection: c, Document: d, State: "draft"}, nil
}

type Adoption struct {
	DraftID      string `json:"draft_id"`
	SourceDigest string `json:"source_digest"`
}

func submissionKey(d Draft) string {
	if d.TransferKey != "" {
		return d.TransferKey
	}
	return d.ID
}

// Reconcile refreshes disposable receipts directly from the authoritative server.
// It never downloads the community corpus or writes publications.db.
func Reconcile(root string, c Connection, legacy []Adoption) (map[string]any, error) {
	client, err := NewClient(c.Service)
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(root, ".re-discipline", "community", "drafts", "*.json"))
	if err != nil {
		return nil, err
	}
	drafts := []Draft{}
	for _, p := range paths {
		d, e := ReadDraft(root, strings.TrimSuffix(filepath.Base(p), ".json"))
		if e != nil {
			return nil, e
		}
		if d.Connection.Service == c.Service && d.Connection.CommunityID == c.CommunityID {
			drafts = append(drafts, d)
		}
	}
	count := 0
	for offset := 0; offset < len(drafts); offset += 200 {
		end := offset + 200
		if end > len(drafts) {
			end = len(drafts)
		}
		keys := []string{}
		for _, d := range drafts[offset:end] {
			keys = append(keys, submissionKey(d))
		}
		receipts := map[string]Submission{}
		if err = client.Operation(context.Background(), "publication.receipts", c.CommunityID, map[string]any{"keys": keys}, &receipts); err != nil {
			return nil, err
		}
		for _, d := range drafts[offset:end] {
			sub, ok := receipts[submissionKey(d)]
			if !ok {
				continue
			}
			if d.Digest != "" && sub.Digest != d.Digest {
				return nil, fmt.Errorf("submission payload mismatch")
			}
			d.FindingID, d.Revision, d.SubmissionID = sub.FindingID, sub.Revision, sub.ID
			d.State = "submitted"
			d.Digest = sub.Digest
			if err = atomicJSON(draftPath(root, d.ID), d); err != nil {
				return nil, err
			}
			count++
		}
	}

	return map[string]any{"verified": count, "authority": "server", "cache_only": true}, nil
}

type Location struct {
	DifferentText bool   `json:"different_text,omitempty"`
	CanonicalID   string `json:"canonical_id,omitempty"`
	Source        string `json:"source"`
	Service       string `json:"service,omitempty"`
	CommunityID   string `json:"community_id,omitempty"`
	FindingID     string `json:"finding_id,omitempty"`
	Revision      string `json:"revision,omitempty"`
	Path          string `json:"path,omitempty"`
	URL           string `json:"url,omitempty"`
}

func collapseCopies(root string, hits []Result, limit int) ([]Result, error) {
	localKey := func(h Result) string {
		if h.TextDigest != "" {
			return "local/text/" + h.TextDigest
		}
		return "local/" + h.Path
	}
	aliases := map[string]string{}
	for _, h := range hits {
		if h.Source == "local" {
			for _, l := range h.Locations {
				if l.FindingID != "" {
					aliases[l.Service+"/"+l.CommunityID+"/"+l.FindingID+"/"+l.Revision] = localKey(h)
				}
			}
		}
	}
	out := []Result{}
	seen := map[string]int{}
	for _, h := range hits {
		key := h.Service + "/" + h.CommunityID + "/" + h.FindingID + "/" + h.Revision
		if h.Source == "local" {
			key = localKey(h)
		} else if local := aliases[key]; local != "" {
			key = local
		}
		loc := Location{Source: h.Source, Service: h.Service, CommunityID: h.CommunityID, FindingID: h.FindingID, Revision: h.Revision, Path: h.Path, URL: h.URL, CanonicalID: h.CanonicalID}
		if i, ok := seen[key]; ok {
			if h.Source == "local" && out[i].Source != "local" {
				if h.TextDigest != "" && out[i].TextDigest != "" && h.TextDigest != out[i].TextDigest {
					prior := out[i]
					prior.Versions = nil
					prior.Contributions = nil
					h.Versions = append(h.Versions, prior)
				}
				h.Locations = append(h.Locations, out[i].Locations...)
				h.Contributions = append(h.Contributions, out[i].Contributions...)
				h.Warnings = append(h.Warnings, out[i].Warnings...)
				out[i] = h
			} else {
				if out[i].Source == "local" && h.Source != "local" && out[i].TextDigest != "" && h.TextDigest != "" && out[i].TextDigest != h.TextDigest {
					version := h
					version.Versions = nil
					version.Contributions = nil
					out[i].Versions = append(out[i].Versions, version)
				}
				out[i].Locations = append(out[i].Locations, loc)
				out[i].Contributions = append(out[i].Contributions, h.Contributions...)
				out[i].Warnings = append(out[i].Warnings, h.Warnings...)
			}
			continue
		}
		seen[key] = len(out)
		if len(h.Locations) == 0 {
			h.Locations = []Location{loc}
		}
		out = append(out, h)
	}
	for i := range out {
		locations := []Location{}
		seenLocations := map[string]int{}
		for _, l := range out[i].Locations {
			key := l.Source + "/" + l.Service + "/" + l.CommunityID + "/" + l.FindingID + "/" + l.Revision + "/" + l.Path
			if j, ok := seenLocations[key]; ok {
				if l.CanonicalID != "" {
					locations[j].CanonicalID = l.CanonicalID
				}
				continue
			}
			seenLocations[key] = len(locations)
			locations = append(locations, l)
		}
		out[i].Locations = locations
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

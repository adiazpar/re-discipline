package community

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

func sourceDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type Location struct {
	Source      string `json:"source"`
	Service     string `json:"service,omitempty"`
	CommunityID string `json:"community_id,omitempty"`
	FindingID   string `json:"finding_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Path        string `json:"path,omitempty"`
	URL         string `json:"url,omitempty"`
}

// Group only exact copies of the same finding. Identity and fingerprint come
// from the indexed text, so no corpus reads or remote fetches happen here.
// A group keeps its earliest rank, never adds votes/scores, and prefers a local
// read surface while retaining every authorized location.
func collapseCopies(_ string, hits []Result, limit int) ([]Result, error) {
	out := make([]Result, 0, len(hits))
	seen := map[[5]string]int{}
	locations := []map[Location]bool{}
	fingerprints := map[string]string{}
	differs := map[string]bool{}
	for n, h := range hits {
		key := [5]string{"remote", h.Service, h.CommunityID, h.FindingID, h.Fingerprint}
		if h.Source == "local" {
			key = [5]string{"local", h.Path}
		}
		if validCopyIdentity(h) {
			key = [5]string{"copy", h.FindingID, h.Fingerprint}
			if fp, ok := fingerprints[h.FindingID]; ok && fp != h.Fingerprint {
				differs[h.FindingID] = true
			}
			fingerprints[h.FindingID] = h.Fingerprint
		} else if h.Source != "local" && (h.FindingID == "" || h.Fingerprint == "") {
			// Incomplete legacy identities cannot establish equivalence.
			key = [5]string{"unknown", fmt.Sprint(n)}
		}
		locs := h.Locations
		if len(locs) == 0 {
			locs = []Location{{Source: h.Source, Service: h.Service, CommunityID: h.CommunityID, FindingID: h.FindingID, Fingerprint: h.Fingerprint, Path: h.Path}}
		}
		i, ok := seen[key]
		if !ok {
			i = len(out)
			seen[key] = i
			h.Locations = nil
			out = append(out, h)
			locations = append(locations, map[Location]bool{})
		} else if h.Source == "local" && out[i].Source != "local" {
			h.Locations = out[i].Locations
			out[i] = h
		}
		for _, loc := range locs {
			if !locations[i][loc] {
				out[i].Locations = append(out[i].Locations, loc)
				locations[i][loc] = true
			}
		}
	}
	for i := range out {
		out[i].ContentDiffers = out[i].ContentDiffers || differs[out[i].FindingID]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func validCopyIdentity(h Result) bool {
	id, err := uuid.Parse(h.FindingID)
	if err != nil || id == uuid.Nil || id.String() != h.FindingID || len(h.Fingerprint) != 64 {
		return false
	}
	for _, c := range h.Fingerprint {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
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

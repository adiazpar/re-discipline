package community

// Relation is a maintainer's assessment of two specific accepted revisions.
// Evidence, authorship and independent finding histories remain intact.
type Relation struct {
	SourceID       string `json:"source_id"`
	TargetID       string `json:"target_id"`
	SourceRevision string `json:"source_revision"`
	TargetRevision string `json:"target_revision"`
	Kind           string `json:"kind"`
	Rationale      string `json:"rationale"`
	Reviewer       string `json:"reviewer"`
	Current        bool   `json:"current"`
}

// Keep distinct evidence lineages as expandable contributions, rather than
// pretending independently authored findings are byte-identical copies.
func GroupContributions(hits []Result) []Result {
	groups := map[string]string{}
	for _, h := range hits {
		for _, l := range h.Locations {
			if l.CanonicalID != "" {
				groups[l.Service+"/"+l.CommunityID+"/"+l.FindingID] = l.Service + "/" + l.CommunityID + "/" + l.CanonicalID
			}
		}
	}
	out := []Result{}
	seen := map[string]int{}
	for _, h := range hits {
		key := ""
		canonical := false
		for _, l := range h.Locations {
			if l.FindingID == "" {
				continue
			}
			k := l.Service + "/" + l.CommunityID + "/" + l.FindingID
			if target := groups[k]; target != "" {
				key = target
				break
			}
			for _, target := range groups {
				if k == target {
					key = k
					canonical = true
					break
				}
			}
		}
		if key == "" {
			out = append(out, h)
			continue
		}
		if i, ok := seen[key]; ok {
			if canonical {
				h.Contributions = append(h.Contributions, out[i].Contributions...)
				old := out[i]
				old.Contributions = nil
				h.Contributions = append(h.Contributions, old)
				out[i] = h
			} else {
				out[i].Contributions = append(out[i].Contributions, h)
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, h)
	}
	return out
}

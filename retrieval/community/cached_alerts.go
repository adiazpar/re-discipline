package community

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

func cachedIntegrityAlerts(db *sql.DB, fid, revision string, relations []Relation) []string {
	out := []string{}
	for _, r := range relations {
		if r.Kind != "equivalent" {
			continue
		}
		peer := ""
		expected := ""
		if r.SourceID == fid && r.SourceRevision == revision {
			peer = r.TargetID
			expected = r.TargetRevision
		}
		if r.TargetID == fid && r.TargetRevision == revision {
			peer = r.SourceID
			expected = r.SourceRevision
		}
		if peer == "" {
			continue
		}
		var state string
		if db.QueryRow(`SELECT state FROM assessments WHERE finding_id=?`, peer).Scan(&state) != nil {
			continue
		}
		var v Change
		if json.Unmarshal([]byte(state), &v) != nil {
			continue
		}
		if (v.Status != "" && v.Status != "active") || v.Revision != expected {
			if v.Status == "" || v.Status == "active" {
				v.Status = "revised since the equivalence review"
			}
			out = append(out, fmt.Sprintf("Previously equivalent finding %s is %s (cached). Reassess this evidence before relying on the claim. %s", peer, v.Status, v.Reason))
		}
	}
	return out
}

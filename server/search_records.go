package server

import (
	"context"
	"encoding/json"
	"fmt"
	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/jackc/pgx/v5"
)

type searchRecord struct {
	digest, status, reason string
	doc                    kb.Document
	alerts                 []string
}

// Batched hydration preserves the same revision and integrity predicates as
// finding.get/integrityAlerts, without two database round trips for every hit.
func queryRecords(ctx context.Context, tx pgx.Tx, cid string, meta map[string]string, ids []string) (map[string]searchRecord, error) {
	out := map[string]searchRecord{}
	revisions := []string{}
	unique := []string{}
	seen := map[string]bool{}
	for _, fid := range ids {
		if !seen[fid] && meta[fid] != "" {
			seen[fid] = true
			unique = append(unique, fid)
			revisions = append(revisions, meta[fid])
		}
	}
	if len(unique) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `WITH requested AS (SELECT * FROM unnest($2::uuid[],$3::uuid[]) AS x(fid,rid))
 SELECT q.fid::text,r.text_digest,r.status,r.reason,r.document,
 COALESCE((SELECT jsonb_agg(jsonb_build_array(other.id::text,CASE WHEN head.status='active' THEN 'revised since the equivalence review' ELSE head.status END,head.reason))
 FROM community.finding_relations rel
 JOIN community.findings other ON other.community_id=rel.community_id AND other.id=CASE WHEN rel.source_id=q.fid THEN rel.target_id ELSE rel.source_id END
 JOIN community.revisions head ON head.id=other.revision
 WHERE rel.community_id=$1 AND rel.kind='equivalent'
 AND ((rel.source_id=q.fid AND rel.source_revision=q.rid) OR (rel.target_id=q.fid AND rel.target_revision=q.rid))
 AND (head.status<>'active' OR other.revision<>CASE WHEN rel.source_id=q.fid THEN rel.target_revision ELSE rel.source_revision END)), '[]'::jsonb)
 FROM requested q JOIN community.revisions r ON r.id=q.rid AND r.community_id=$1`, cid, unique, revisions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fid string
		var v searchRecord
		var body, alerts []byte
		if err = rows.Scan(&fid, &v.digest, &v.status, &v.reason, &body, &alerts); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &v.doc); err != nil {
			return nil, err
		}
		var peers [][]string
		if err = json.Unmarshal(alerts, &peers); err != nil {
			return nil, err
		}
		for _, p := range peers {
			v.alerts = append(v.alerts, fmt.Sprintf("Previously equivalent finding %s is %s. Reassess this evidence before relying on the claim. %s", p[0], p[1], p[2]))
		}
		out[fid] = v
	}
	return out, rows.Err()
}

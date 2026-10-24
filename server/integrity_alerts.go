package server

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// A validity assessment must not leave equivalent corroboration looking
// unquestioned. Preserve its independent lineage, but require reassessment.
func integrityAlerts(ctx context.Context, tx pgx.Tx, c, fid, revision string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT other.id::text,CASE WHEN head.status='active' THEN 'revised since the equivalence review' ELSE head.status END,head.reason FROM community.finding_relations rel JOIN community.findings other ON other.community_id=rel.community_id AND other.id=CASE WHEN rel.source_id::text=$2 THEN rel.target_id ELSE rel.source_id END JOIN community.revisions head ON head.id=other.revision WHERE rel.community_id=$1 AND rel.kind='equivalent' AND ((rel.source_id::text=$2 AND rel.source_revision::text=$3) OR (rel.target_id::text=$2 AND rel.target_revision::text=$3)) AND (head.status<>'active' OR other.revision<>CASE WHEN rel.source_id::text=$2 THEN rel.target_revision ELSE rel.source_revision END)`, c, fid, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var peer, status, reason string
		if err = rows.Scan(&peer, &status, &reason); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("Previously equivalent finding %s is %s. Reassess this evidence before relying on the claim. %s", peer, status, reason))
	}
	return out, rows.Err()
}

// Package maintenance prepares bounded, revision-checked operator migrations.
package maintenance

import (
	"encoding/json"
	"fmt"
	"strings"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/google/uuid"
)

type CleanupItem struct {
	FindingID     string          `json:"finding_id"`
	Base          string          `json:"base_revision"`
	Revision      string          `json:"revision"`
	Status        string          `json:"status"`
	Before        json.RawMessage `json:"before"`
	After         json.RawMessage `json:"after"`
	ContentDigest string          `json:"content_digest"`
	TextDigest    string          `json:"text_digest"`
	ClaimDigest   string          `json:"claim_digest"`
}
type CleanupPlan struct {
	Community string        `json:"community"`
	Items     []CleanupItem `json:"items"`
}

func quote(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

// CleanupSQL emits transactions for an authorized operator. It cannot create a
// finding, restore a disputed claim, change credit, or overwrite a stale head.
func CleanupSQL(plan CleanupPlan, batchSize int) ([]string, error) {
	if _, err := uuid.Parse(plan.Community); err != nil {
		return nil, err
	}
	if len(plan.Items) == 0 || len(plan.Items) > 20000 || batchSize < 1 || batchSize > 250 {
		return nil, fmt.Errorf("bound the maintenance job to 1..20000 findings and batches of 1..250")
	}
	seen := map[string]bool{}
	for i := range plan.Items {
		v := &plan.Items[i]
		for _, id := range []string{v.FindingID, v.Base, v.Revision} {
			if _, err := uuid.Parse(id); err != nil {
				return nil, err
			}
		}
		if seen[v.FindingID] || v.Base == v.Revision {
			return nil, fmt.Errorf("duplicate or unchanged revision")
		}
		seen[v.FindingID] = true
		var before, after map[string]json.RawMessage
		if err := json.Unmarshal(v.Before, &before); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(v.After, &after); err != nil {
			return nil, err
		}
		var d kb.Document
		if err := json.Unmarshal(v.After, &d); err != nil {
			return nil, err
		}
		if len(d.Evidence) != 0 {
			return nil, fmt.Errorf("cleanup must remove the evidence payload")
		}
		for _, check := range kb.Validate(d) {
			if check.Blocking {
				return nil, fmt.Errorf("%s", check.Message)
			}
		}
		delete(before, "markdown")
		delete(after, "markdown")
		delete(before, "evidence")
		delete(after, "evidence")
		b, _ := json.Marshal(before)
		a, _ := json.Marshal(after)
		if string(a) != string(b) {
			return nil, fmt.Errorf("cleanup cannot change applicability or identity metadata")
		}
		v.ContentDigest = kb.ContentDigest(d)
		v.TextDigest = kb.TextDigest(d.Markdown)
		v.ClaimDigest = kb.ClaimDigest(d)
	}
	var out []string
	for start := 0; start < len(plan.Items); start += batchSize {
		items := plan.Items[start:min(start+batchSize, len(plan.Items))]
		data, _ := json.Marshal(items)
		out = append(out, fmt.Sprintf(`BEGIN;
SET LOCAL statement_timeout='45s';
SET LOCAL lock_timeout='5s';
SET LOCAL standard_conforming_strings=on;
SELECT id FROM community.libraries WHERE id=%[1]s::uuid FOR UPDATE;
CREATE TEMP TABLE cleanup_plan ON COMMIT DROP AS SELECT * FROM jsonb_to_recordset(%[2]s::jsonb)
 AS p(finding_id uuid,base_revision uuid,revision uuid,status text,before jsonb,after jsonb,content_digest text,text_digest text,claim_digest text);
DO $cleanup$
BEGIN
 IF (SELECT count(*) FROM cleanup_plan p JOIN community.findings f ON f.community_id=%[1]s::uuid AND f.id=p.finding_id AND NOT f.deleted
  JOIN community.revisions r ON r.id=f.revision WHERE r.status=p.status AND
  ((f.revision=p.base_revision AND r.document=p.before) OR (f.revision=p.revision AND r.document=p.after))) <> %[3]d THEN
  RAISE EXCEPTION 'cleanup source changed or was deleted; refresh the plan';
 END IF;
 IF EXISTS(SELECT 1 FROM community.finding_relations r JOIN cleanup_plan p ON r.source_id=p.finding_id OR r.target_id=p.finding_id WHERE r.community_id=%[1]s::uuid) THEN
  RAISE EXCEPTION 'reviewed relationships require explicit maintenance review';
 END IF;
END $cleanup$;
DELETE FROM cleanup_plan p USING community.findings f WHERE f.community_id=%[1]s::uuid AND f.id=p.finding_id AND f.revision=p.revision;
WITH numbered AS (SELECT p.*,row_number() OVER(ORDER BY finding_id) AS n FROM cleanup_plan p)
INSERT INTO community.revisions(id,community_id,finding_id,sequence,document,author_id,content_digest,text_digest,claim_digest,status,reason,replacement_id,assessment_evidence)
 SELECT p.revision,r.community_id,r.finding_id,l.sequence+p.n,p.after,r.author_id,p.content_digest,p.text_digest,p.claim_digest,r.status,r.reason,r.replacement_id,r.assessment_evidence
 FROM numbered p JOIN community.revisions r ON r.id=p.base_revision JOIN community.libraries l ON l.id=r.community_id;
UPDATE community.libraries SET sequence=sequence+(SELECT count(*) FROM cleanup_plan) WHERE id=%[1]s::uuid;
UPDATE community.findings f SET revision=p.revision FROM cleanup_plan p WHERE f.community_id=%[1]s::uuid AND f.id=p.finding_id;
INSERT INTO community.audit(community_id,actor_id,action,detail)
 SELECT l.id,l.owner_id,'finding.cleanup',jsonb_build_object('id',p.finding_id,'base_revision',p.base_revision,'revision',p.revision,'rationale','Remove publication evidence and unavailable-material boilerplate; retain claims, applicability, authorship and assessment.')
 FROM cleanup_plan p CROSS JOIN community.libraries l WHERE l.id=%[1]s::uuid;
SELECT jsonb_build_object('updated',count(*),'batch',%[4]d) FROM cleanup_plan;
COMMIT;
`, quote(plan.Community), quote(string(data)), len(items), start/batchSize+1))
	}
	return out, nil
}

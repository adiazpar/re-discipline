-- A reader entry includes credit from every current, directly reviewed member.
-- Stale, inactive or erased members never acquire access through an old link.
CREATE OR REPLACE FUNCTION community.finding_contributions(c uuid,f uuid,after_id text DEFAULT '') RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF NOT community.can_read(c) THEN RAISE EXCEPTION 'permission denied' USING ERRCODE='42501'; END IF;
 RETURN (
 WITH edges AS MATERIALIZED (
  SELECT r.source_id,r.target_id FROM community.finding_relations r
  JOIN community.findings a ON a.community_id=r.community_id AND a.id=r.source_id
  JOIN community.findings b ON b.community_id=r.community_id AND b.id=r.target_id
  JOIN community.revisions ar ON ar.id=a.revision
  JOIN community.revisions br ON br.id=b.revision
  WHERE r.community_id=c AND r.kind='equivalent' AND NOT a.deleted AND NOT b.deleted
   AND a.revision=r.source_revision AND b.revision=r.target_revision
   AND ar.status='active' AND br.status='active'
 ), target AS (SELECT COALESCE((SELECT target_id FROM edges WHERE source_id=f),f) AS id),
 members AS (SELECT id FROM target UNION SELECT source_id FROM edges JOIN target ON target.id=edges.target_id)
 SELECT COALESCE(jsonb_agg(jsonb_build_object('submission_id',s.id,'finding_id',s.finding_id,'author',s.author_id,'revision',s.accepted_revision,'source_path',s.document->>'source_path','evidence',s.document->'evidence') ORDER BY s.id),'[]')
 FROM (SELECT s.* FROM community.submissions s
  JOIN community.findings fnd ON fnd.community_id=s.community_id AND fnd.id::text=s.finding_id
  JOIN members m ON m.id=fnd.id
  WHERE s.community_id=c AND s.state='accepted' AND NOT fnd.deleted AND s.id::text>after_id
  ORDER BY s.id LIMIT 200) s);
END $$;

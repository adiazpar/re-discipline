-- Attached copy decisions must be erased with their finding.
CREATE OR REPLACE FUNCTION community.erase_integrity_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE ids uuid[];
BEGIN
 SELECT COALESCE(array_agg(id),'{}'::uuid[]) INTO ids FROM community.submissions
 WHERE community_id=OLD.community_id AND finding_id=OLD.id::text;
 INSERT INTO community.publication_deletions SELECT community_id,author_id,idempotency_key
 FROM community.submissions WHERE id=ANY(ids) ON CONFLICT DO NOTHING;
 DELETE FROM community.jobs WHERE submission_id=ANY(ids);
 DELETE FROM community.reviews WHERE submission_id=ANY(ids);
 DELETE FROM community.audit WHERE community_id=OLD.community_id AND
 (detail->>'id'=OLD.id::text OR detail->>'id'=ANY(ids::text[]) OR detail->>'submission_id'=ANY(ids::text[]));
 UPDATE community.revisions SET content_digest='',text_digest='',reason='',replacement_id='',assessment_evidence='[]',submission_id=NULL
 WHERE community_id=OLD.community_id AND finding_id=OLD.id;
 DELETE FROM community.submissions WHERE id=ANY(ids);
 RETURN OLD;
END $$;

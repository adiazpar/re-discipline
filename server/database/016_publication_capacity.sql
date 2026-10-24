-- Quota checks sum stored sizes instead of serializing every historical document
-- for every item in a publication batch. The accounting formula is unchanged.
ALTER TABLE community.revisions ADD COLUMN document_bytes integer
 GENERATED ALWAYS AS (octet_length(document::text)) STORED;
ALTER TABLE community.submissions ADD COLUMN document_bytes integer
 GENERATED ALWAYS AS (octet_length(document::text)) STORED;

CREATE OR REPLACE FUNCTION community.service_capacity() RETURNS TABLE(libraries bigint,bytes bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF community.actor() IS NULL THEN RAISE EXCEPTION 'permission denied' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(713905222);
 RETURN QUERY SELECT (SELECT count(*) FROM community.libraries),
  (SELECT COALESCE(sum(document_bytes),0)*2 FROM community.submissions) +
  (SELECT COALESCE(sum(document_bytes),0) FROM community.revisions WHERE submission_id IS NULL);
END $$;

CREATE OR REPLACE FUNCTION community.publication_usage(c uuid) RETURNS TABLE(bytes bigint,findings bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT (SELECT COALESCE(sum(document_bytes),0)*2 FROM community.submissions WHERE community_id=c) +
        (SELECT COALESCE(sum(document_bytes),0) FROM community.revisions WHERE community_id=c AND submission_id IS NULL),
        (SELECT count(*) FROM community.findings WHERE community_id=c AND NOT deleted)
 WHERE community.can_read(c)
$$;

CREATE INDEX health_issue_subject ON community.health_issues(community_id,subject_kind,subject_id);
CREATE INDEX health_issue_other ON community.health_issues(community_id,other_kind,other_id);
ANALYZE community.revisions;
ANALYZE community.submissions;
ANALYZE community.health_work;

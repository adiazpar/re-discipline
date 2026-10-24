CREATE TABLE community.import_grants (
 id uuid PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id),
 user_id uuid NOT NULL, created_by uuid NOT NULL, expires_at timestamptz NOT NULL,
 remaining_count integer NOT NULL CHECK (remaining_count>=0),
 remaining_bytes bigint NOT NULL CHECK (remaining_bytes>=0), revoked boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE community.import_grants ENABLE ROW LEVEL SECURITY;
CREATE POLICY import_grants_read ON community.import_grants FOR SELECT USING
 (community.member_role(community_id)='owner' OR (user_id=community.actor() AND community.member_role(community_id) IS NOT NULL));
CREATE POLICY import_grants_create ON community.import_grants FOR INSERT WITH CHECK
 (community.member_role(community_id)='owner' AND created_by=community.actor());
CREATE POLICY import_grants_update ON community.import_grants FOR UPDATE USING (community.member_role(community_id)='owner');
GRANT SELECT,INSERT,UPDATE ON community.import_grants TO re_community_app;

CREATE FUNCTION community.consume_import(g uuid,c uuid,n bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF n<=0 OR COALESCE(community.member_role(c),'') NOT IN ('owner','maintainer','publisher') OR
 NOT EXISTS(SELECT 1 FROM community.libraries WHERE id=c AND policy->>'mode'='trusted') THEN
  RETURN false;
 END IF;
 UPDATE community.import_grants SET remaining_count=remaining_count-1,remaining_bytes=remaining_bytes-n
 WHERE id=g AND community_id=c AND user_id=community.actor() AND NOT revoked AND expires_at>now()
 AND remaining_count>0 AND remaining_bytes>=n;
 RETURN FOUND;
END $$;
REVOKE ALL ON FUNCTION community.consume_import(uuid,uuid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.consume_import(uuid,uuid,bigint) TO re_community_app;

-- Fast repeat-content lookup within one author's submissions. It is a signal
-- for idempotency, never permission to merge independent contributors.
CREATE INDEX submissions_author_digest ON community.submissions(community_id,author_id,digest);

CREATE TABLE community.submission_aliases (
 community_id uuid NOT NULL REFERENCES community.libraries(id), author_id uuid NOT NULL,
 idempotency_key uuid NOT NULL, submission_id uuid NOT NULL REFERENCES community.submissions(id) ON DELETE CASCADE,
 PRIMARY KEY(community_id,author_id,idempotency_key)
);
ALTER TABLE community.submission_aliases ENABLE ROW LEVEL SECURITY;
CREATE POLICY submission_aliases_read ON community.submission_aliases FOR SELECT USING(author_id=community.actor() AND community.member_role(community_id) IS NOT NULL);
CREATE POLICY submission_aliases_create ON community.submission_aliases FOR INSERT WITH CHECK(author_id=community.actor() AND EXISTS(SELECT 1 FROM community.submissions s WHERE s.id=submission_id AND s.community_id=submission_aliases.community_id AND s.author_id=community.actor()));
GRANT SELECT,INSERT ON community.submission_aliases TO re_community_app;

CREATE FUNCTION community.retain_alias_deletions() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 INSERT INTO community.publication_deletions SELECT community_id,author_id,idempotency_key FROM community.submission_aliases WHERE submission_id=OLD.id ON CONFLICT DO NOTHING;
 RETURN OLD;
END $$;
REVOKE ALL ON FUNCTION community.retain_alias_deletions() FROM PUBLIC;
CREATE TRIGGER retain_alias_deletions BEFORE DELETE ON community.submissions FOR EACH ROW EXECUTE FUNCTION community.retain_alias_deletions();

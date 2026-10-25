-- The Go migration between 022 and this file copies only current live knowledge.
-- No publication queue, historical document bodies, or contributor policies remain.
DROP TABLE community.jobs,community.reviews,community.finding_relations,
 community.publication_sources,community.health_work,community.health_issues,
 community.submission_aliases,community.publication_deletions,community.import_grants,
 community.revisions,community.findings,community.submissions,community.invites,
 community.members,community.accounts,community.browser_sessions,community.oauth_states,
 community.tokens,community.devices CASCADE;
DELETE FROM community.audit WHERE action LIKE 'submission.%' OR action LIKE 'finding.%'
 OR action LIKE 'health.%' OR action LIKE 'invite.%' OR action LIKE 'member.%';

CREATE OR REPLACE FUNCTION community.member_role(c uuid) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT 'owner'::text FROM community.libraries WHERE id=c AND owner_id=community.actor()
 AND COALESCE(current_setting('app.read_token',true),'')=''
$$;
CREATE OR REPLACE FUNCTION community.can_read(c uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT CASE WHEN COALESCE(current_setting('app.read_token',true),'')<>'' THEN
  EXISTS(SELECT 1 FROM community.reader_connections t JOIN community.libraries l ON l.id=t.community_id
   WHERE t.id::text=current_setting('app.read_token',true) AND t.community_id=c
   AND t.revoked_at IS NULL AND t.expires_at>now() AND (NOT t.public_connection OR l.visibility='public'))
 ELSE EXISTS(SELECT 1 FROM community.libraries WHERE id=c AND owner_id=community.actor()) END
$$;
DROP POLICY IF EXISTS library_update ON community.libraries;
CREATE POLICY library_update ON community.libraries FOR UPDATE TO re_community_app
 USING(owner_id=community.actor() AND COALESCE(current_setting('app.read_token',true),'')='');
UPDATE community.libraries SET policy=policy-'mode',
 knowledge_bytes=(SELECT COALESCE(sum(document_bytes),0) FROM community.documents d WHERE d.community_id=libraries.id),
 knowledge_count=(SELECT count(*) FROM community.documents d WHERE d.community_id=libraries.id);

CREATE OR REPLACE FUNCTION community.service_capacity() RETURNS TABLE(libraries bigint,bytes bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF community.actor() IS NULL THEN RAISE EXCEPTION 'permission denied' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(713905222);
 RETURN QUERY SELECT count(*),COALESCE(sum(knowledge_bytes),0)::bigint FROM community.libraries;
END $$;
CREATE OR REPLACE FUNCTION community.erase_community_unchecked(c uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 DELETE FROM community.request_nonces WHERE public_key IN
  (SELECT public_key FROM community.reader_connections WHERE community_id=c);
 DELETE FROM community.audit WHERE community_id=c;
 DELETE FROM community.libraries WHERE id=c;
 INSERT INTO community.deletion_cleanup(community_id) VALUES(c) ON CONFLICT DO NOTHING;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION community.erase_community_unchecked(uuid) FROM PUBLIC,re_community_app,re_community_worker;

-- Retire callable security-definer helpers for removed workflows as well.
DO $$ DECLARE f record; BEGIN
 FOR f IN SELECT p.oid::regprocedure AS name FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname='community' AND p.proname NOT IN
  ('actor','member_role','can_read','service_capacity','guard_archive_freeze',
   'erase_community','erase_community_unchecked','revoke_public_connections') LOOP
  EXECUTE format('DROP FUNCTION %s CASCADE',f.name);
 END LOOP;
END $$;

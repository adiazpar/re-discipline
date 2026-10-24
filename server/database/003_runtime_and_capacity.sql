DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='re_community_worker') THEN CREATE ROLE re_community_worker NOLOGIN NOBYPASSRLS; END IF;
END $$;
GRANT re_community_worker TO CURRENT_USER;
GRANT USAGE ON SCHEMA community TO re_community_worker;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA community TO re_community_worker;
REVOKE ALL ON community.schema_migrations FROM re_community_worker;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA community TO re_community_worker;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA community TO re_community_worker;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['libraries','members','invites','submissions','findings','revisions','reviews','audit','jobs','tokens','devices','usage'] LOOP
  EXECUTE format('CREATE POLICY internal_worker ON community.%I FOR ALL TO re_community_worker USING (true) WITH CHECK (true)',t);
 END LOOP;
END $$;

CREATE FUNCTION community.service_capacity() RETURNS TABLE(libraries bigint,bytes bigint) LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF community.actor() IS NULL THEN RAISE EXCEPTION 'permission denied' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(713905222);
 RETURN QUERY SELECT (SELECT count(*) FROM community.libraries),
  (SELECT COALESCE(sum(octet_length(document::text)),0)*2 FROM community.submissions) +
  (SELECT COALESCE(sum(octet_length(document::text)),0) FROM community.revisions WHERE submission_id IS NULL);
END $$;
REVOKE ALL ON FUNCTION community.service_capacity() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.service_capacity() TO re_community_app,re_community_worker;

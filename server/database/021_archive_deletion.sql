-- A durable workflow, not a long-lived database lock or a retained archive.
CREATE TABLE community.deletion_sessions (
 id uuid PRIMARY KEY, community_id uuid NOT NULL UNIQUE REFERENCES community.libraries(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL, ticket_hash text NOT NULL,
 phase text NOT NULL CHECK(phase IN ('preview','locked','downloaded','verified')),
 archive boolean NOT NULL DEFAULT false,
 expires_at timestamptz NOT NULL DEFAULT now()+interval '1 hour',
 section integer NOT NULL DEFAULT 0, after_key text NOT NULL DEFAULT '', next_chunk bigint NOT NULL DEFAULT 0,
 previous_section integer NOT NULL DEFAULT 0, previous_key text NOT NULL DEFAULT '',
 chain text NOT NULL DEFAULT '', previous_chain text NOT NULL DEFAULT '',
 records bigint NOT NULL DEFAULT 0, previous_records bigint NOT NULL DEFAULT 0
);
-- Only one unfinished archive per service, even across processes/restarts.
CREATE UNIQUE INDEX one_active_archive ON community.deletion_sessions ((true)) WHERE archive AND phase='locked';
ALTER TABLE community.deletion_sessions ENABLE ROW LEVEL SECURITY;
GRANT SELECT,INSERT,UPDATE,DELETE ON community.deletion_sessions TO re_community_app,re_community_worker;
CREATE POLICY owner_sessions ON community.deletion_sessions FOR ALL TO re_community_app
 USING(owner_id=community.actor() AND COALESCE(current_setting('app.read_token',true),'')='')
 WITH CHECK(owner_id=community.actor() AND COALESCE(current_setting('app.read_token',true),'')='');
CREATE POLICY internal_worker ON community.deletion_sessions FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
ALTER TABLE community.libraries ADD COLUMN deletion_id uuid;

-- Serialize the start of the freeze with mutations, including background work.
-- Explicit columns in the exporter exclude all identities and access records.
CREATE FUNCTION community.guard_archive_freeze() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE c uuid; frozen uuid;
BEGIN
 IF TG_TABLE_NAME='libraries' THEN
  c=OLD.id; frozen=OLD.deletion_id;
 ELSE
  IF TG_OP='DELETE' THEN c=OLD.community_id; ELSE c=NEW.community_id; END IF;
  SELECT deletion_id INTO frozen FROM community.libraries WHERE id=c FOR SHARE;
 END IF;
 IF frozen IS NOT NULL AND COALESCE(current_setting('app.deletion_control',true),'')<>c::text THEN
  RAISE EXCEPTION 'community pending deletion' USING ERRCODE='55006';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END $$;
REVOKE ALL ON FUNCTION community.guard_archive_freeze() FROM PUBLIC;
CREATE TRIGGER archive_freeze BEFORE UPDATE OR DELETE ON community.libraries
 FOR EACH ROW EXECUTE FUNCTION community.guard_archive_freeze();
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['findings','revisions','submissions','reviews','finding_relations','publication_sources','health_work','health_issues'] LOOP
  EXECUTE format('CREATE TRIGGER archive_freeze BEFORE INSERT OR UPDATE OR DELETE ON community.%I FOR EACH ROW EXECUTE FUNCTION community.guard_archive_freeze()',t);
 END LOOP;
END $$;
CREATE INDEX archive_revisions ON community.revisions(community_id,id);
CREATE INDEX archive_submissions ON community.submissions(community_id,id);
CREATE INDEX archive_reviews ON community.reviews(community_id,id);

-- The old single-call SQL path is no longer callable by the application role.
ALTER FUNCTION community.erase_community(uuid) RENAME TO erase_community_unchecked;
REVOKE ALL ON FUNCTION community.erase_community_unchecked(uuid) FROM PUBLIC,re_community_app,re_community_worker;
CREATE FUNCTION community.erase_community(c uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE s community.deletion_sessions; owner uuid;
BEGIN
 IF community.actor() IS NULL OR COALESCE(current_setting('app.read_token',true),'')<>'' THEN
  RAISE EXCEPTION 'permission denied' USING ERRCODE='42501';
 END IF;
 SELECT owner_id INTO owner FROM community.libraries WHERE id=c FOR UPDATE;
 IF NOT FOUND THEN RETURN false; END IF;
 IF owner<>community.actor() THEN RAISE EXCEPTION 'permission denied' USING ERRCODE='42501'; END IF;
 SELECT * INTO s FROM community.deletion_sessions WHERE community_id=c FOR UPDATE;
 IF NOT FOUND OR s.expires_at<=now() OR s.ticket_hash<>COALESCE(current_setting('app.deletion_ticket_hash',true),'')
  OR NOT (s.phase='verified' OR (s.phase='locked' AND NOT s.archive)) THEN
  RAISE EXCEPTION 'deletion requires confirmation and a verified archive or explicit archive waiver' USING ERRCODE='55006';
 END IF;
 PERFORM set_config('app.deletion_control',c::text,true);
 RETURN community.erase_community_unchecked(c);
END $$;
REVOKE ALL ON FUNCTION community.erase_community(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.erase_community(uuid) TO re_community_app;

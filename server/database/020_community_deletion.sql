-- Content-free cleanup work survives a crash between the database commit and
-- removal of derived files. It is removed once cleanup succeeds, not an archive.
CREATE TABLE community.deletion_cleanup (
 community_id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE community.deletion_cleanup ENABLE ROW LEVEL SECURITY;
GRANT SELECT,DELETE ON community.deletion_cleanup TO re_community_worker;
CREATE POLICY internal_worker ON community.deletion_cleanup FOR ALL
 TO re_community_worker USING(true) WITH CHECK(true);

CREATE FUNCTION community.erase_community(c uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE owner uuid;
BEGIN
 IF community.actor() IS NULL OR COALESCE(current_setting('app.read_token',true),'')<>'' THEN
  RAISE EXCEPTION 'permission denied' USING ERRCODE='42501';
 END IF;
 SELECT owner_id INTO owner FROM community.libraries WHERE id=c FOR UPDATE;
 IF NOT FOUND THEN RETURN false; END IF;
 IF owner<>community.actor() THEN
  RAISE EXCEPTION 'permission denied' USING ERRCODE='42501';
 END IF;

 -- Remove dependent rows in FK order. Existing finding/submission triggers
 -- may create content-free receipts or health work; remove those afterward.
 DELETE FROM community.jobs WHERE community_id=c;
 DELETE FROM community.reviews WHERE community_id=c;
 DELETE FROM community.revisions WHERE community_id=c;
 DELETE FROM community.findings WHERE community_id=c;
 DELETE FROM community.submissions WHERE community_id=c;
 DELETE FROM community.submission_aliases WHERE community_id=c;
 DELETE FROM community.publication_deletions WHERE community_id=c;
 DELETE FROM community.health_work WHERE community_id=c;
 DELETE FROM community.health_issues WHERE community_id=c;
 DELETE FROM community.import_grants WHERE community_id=c;
 DELETE FROM community.invites WHERE community_id=c;
 DELETE FROM community.audit WHERE community_id=c;
 DELETE FROM community.members WHERE community_id=c;
 DELETE FROM community.request_nonces WHERE public_key IN
  (SELECT public_key FROM community.reader_connections WHERE community_id=c);
 DELETE FROM community.reader_connections WHERE community_id=c;
 DELETE FROM community.reader_invitations WHERE community_id=c;
 DELETE FROM community.libraries WHERE id=c;
 INSERT INTO community.deletion_cleanup(community_id) VALUES(c) ON CONFLICT DO NOTHING;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION community.erase_community(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.erase_community(uuid) TO re_community_app;

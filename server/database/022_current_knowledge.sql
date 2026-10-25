-- Current owner-authored knowledge. Stable identity is scoped to one community.
ALTER TABLE community.libraries ADD COLUMN knowledge_bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE community.libraries ADD COLUMN knowledge_count bigint NOT NULL DEFAULT 0;
ALTER TABLE community.libraries ADD COLUMN reset_sequence bigint NOT NULL DEFAULT 0;
CREATE TABLE community.documents (
 community_id uuid NOT NULL REFERENCES community.libraries(id) ON DELETE CASCADE,
 id uuid NOT NULL, source_path text NOT NULL, markdown text NOT NULL,
 fingerprint text NOT NULL CHECK(length(fingerprint)=64),
 generation bigint NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 document_bytes integer GENERATED ALWAYS AS (octet_length(markdown)) STORED,
 PRIMARY KEY(community_id,id)
);
CREATE INDEX documents_generation ON community.documents(community_id,generation,id);
CREATE TABLE community.upload_receipts (
 community_id uuid NOT NULL REFERENCES community.libraries(id) ON DELETE CASCADE,
 key uuid NOT NULL, request_digest text NOT NULL, result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(community_id,key)
);
CREATE INDEX upload_receipts_expiry ON community.upload_receipts(created_at);
ALTER TABLE community.documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE community.upload_receipts ENABLE ROW LEVEL SECURITY;
GRANT SELECT,INSERT,UPDATE,DELETE ON community.documents,community.upload_receipts TO re_community_app,re_community_worker;
CREATE POLICY documents_read ON community.documents FOR SELECT TO re_community_app USING(community.can_read(community_id));
CREATE POLICY documents_owner ON community.documents FOR ALL TO re_community_app
 USING(COALESCE(current_setting('app.read_token',true),'')='' AND EXISTS(SELECT 1 FROM community.libraries l WHERE l.id=community_id AND l.owner_id=community.actor()))
 WITH CHECK(COALESCE(current_setting('app.read_token',true),'')='' AND EXISTS(SELECT 1 FROM community.libraries l WHERE l.id=community_id AND l.owner_id=community.actor()));
CREATE POLICY receipts_owner ON community.upload_receipts FOR ALL TO re_community_app
 USING(COALESCE(current_setting('app.read_token',true),'')='' AND EXISTS(SELECT 1 FROM community.libraries l WHERE l.id=community_id AND l.owner_id=community.actor()))
 WITH CHECK(COALESCE(current_setting('app.read_token',true),'')='' AND EXISTS(SELECT 1 FROM community.libraries l WHERE l.id=community_id AND l.owner_id=community.actor()));
CREATE POLICY internal_worker ON community.documents FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
CREATE POLICY internal_worker ON community.upload_receipts FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
CREATE TRIGGER archive_freeze BEFORE INSERT OR UPDATE OR DELETE ON community.documents
 FOR EACH ROW EXECUTE FUNCTION community.guard_archive_freeze();
CREATE TRIGGER archive_freeze BEFORE INSERT OR UPDATE OR DELETE ON community.upload_receipts
 FOR EACH ROW EXECUTE FUNCTION community.guard_archive_freeze();

-- A v1 archive cannot resume across the knowledge-only format change. Reopen
-- its community safely; owners can start a new deletion preview after upgrading.
DO $$ DECLARE c uuid; BEGIN
 FOR c IN SELECT id FROM community.libraries WHERE deletion_id IS NOT NULL LOOP
  PERFORM set_config('app.deletion_control',c::text,true);
  UPDATE community.libraries SET deletion_id=NULL WHERE id=c;
 END LOOP;
 PERFORM set_config('app.deletion_control','',true);
 DELETE FROM community.deletion_sessions;
END $$;

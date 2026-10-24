CREATE TABLE community.read_tokens (
 id uuid PRIMARY KEY,
 community_id uuid NOT NULL REFERENCES community.libraries(id) ON DELETE CASCADE,
 secret_hash text NOT NULL UNIQUE,
 label text NOT NULL,
 public_connection boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL
);
CREATE INDEX read_tokens_community ON community.read_tokens(community_id);
ALTER TABLE community.read_tokens ENABLE ROW LEVEL SECURITY;
GRANT SELECT,INSERT,DELETE ON community.read_tokens TO re_community_worker;
CREATE POLICY internal_worker ON community.read_tokens FOR ALL TO re_community_worker USING(true) WITH CHECK(true);

-- A reader request carries a verified connection ID, never a client-selected
-- membership or scope. RLS rechecks revocation, expiry and public visibility.
CREATE OR REPLACE FUNCTION community.can_read(c uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT CASE WHEN COALESCE(current_setting('app.read_token',true),'')<>'' THEN
  EXISTS(SELECT 1 FROM community.read_tokens t JOIN community.libraries l ON l.id=t.community_id
   WHERE t.id::text=current_setting('app.read_token',true) AND t.community_id=c
   AND t.expires_at>now() AND (NOT t.public_connection OR l.visibility='public'))
 ELSE EXISTS(SELECT 1 FROM community.libraries WHERE id=c AND
  (owner_id=community.actor() OR visibility='public' OR community.member_role(c) IS NOT NULL)) END
$$;

CREATE FUNCTION community.revoke_public_connections() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF OLD.visibility='public' AND NEW.visibility='private' THEN
  DELETE FROM community.read_tokens WHERE community_id=NEW.id AND public_connection;
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION community.revoke_public_connections() FROM PUBLIC;
CREATE TRIGGER revoke_public_connections AFTER UPDATE OF visibility ON community.libraries
 FOR EACH ROW EXECUTE FUNCTION community.revoke_public_connections();

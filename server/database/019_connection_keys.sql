-- Connections prove possession of a client-held private key. Neither reusable
-- reader secrets nor private keys are kept by the service.
CREATE TABLE community.reader_connections (
 id uuid PRIMARY KEY,
 community_id uuid NOT NULL REFERENCES community.libraries(id) ON DELETE CASCADE,
 public_key text NOT NULL UNIQUE CHECK(length(public_key)=43),
 label text NOT NULL,
 public_connection boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 renewed_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz
);
CREATE INDEX reader_connections_community ON community.reader_connections(community_id);
CREATE INDEX reader_connections_expiry ON community.reader_connections(expires_at);
CREATE TABLE community.reader_invitations (
 id uuid PRIMARY KEY,
 community_id uuid NOT NULL REFERENCES community.libraries(id) ON DELETE CASCADE,
 secret_hash text NOT NULL UNIQUE,
 label text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL
);
CREATE INDEX reader_invitations_community ON community.reader_invitations(community_id);
CREATE INDEX reader_invitations_expiry ON community.reader_invitations(expires_at);
CREATE TABLE community.request_nonces (
 public_key text NOT NULL CHECK(length(public_key)=43),
 nonce text NOT NULL CHECK(length(nonce)=32),
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(public_key,nonce)
);
CREATE INDEX request_nonces_expiry ON community.request_nonces(expires_at);
ALTER TABLE community.request_nonces SET (autovacuum_vacuum_scale_factor=0.05,autovacuum_vacuum_threshold=1000);
ALTER TABLE community.reader_connections SET (autovacuum_vacuum_scale_factor=0.05);
ALTER TABLE community.reader_invitations SET (autovacuum_vacuum_scale_factor=0.05);
ALTER TABLE community.reader_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE community.reader_invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE community.request_nonces ENABLE ROW LEVEL SECURITY;
GRANT SELECT,INSERT,UPDATE,DELETE ON community.reader_connections,community.reader_invitations,community.request_nonces TO re_community_worker;
CREATE POLICY internal_worker ON community.reader_connections FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
CREATE POLICY internal_worker ON community.reader_invitations FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
CREATE POLICY internal_worker ON community.request_nonces FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
CREATE OR REPLACE FUNCTION community.can_read(c uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT CASE WHEN COALESCE(current_setting('app.read_token',true),'')<>'' THEN
  EXISTS(SELECT 1 FROM community.reader_connections t JOIN community.libraries l ON l.id=t.community_id
   WHERE t.id::text=current_setting('app.read_token',true) AND t.community_id=c
   AND t.revoked_at IS NULL AND t.expires_at>now() AND (NOT t.public_connection OR l.visibility='public'))
 ELSE EXISTS(SELECT 1 FROM community.libraries WHERE id=c AND
  (owner_id=community.actor() OR visibility='public' OR community.member_role(c) IS NOT NULL)) END
$$;
CREATE OR REPLACE FUNCTION community.revoke_public_connections() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF OLD.visibility='public' AND NEW.visibility='private' THEN
  UPDATE community.reader_connections SET revoked_at=now(),expires_at=now()+interval '180 days'
   WHERE community_id=NEW.id AND public_connection AND revoked_at IS NULL;
 END IF;
 RETURN NEW;
END $$;
-- This unreleased migration retires bearer reader grants. Reconnect public
-- projects; private projects require a new single-use invitation.
DROP TABLE community.read_tokens;

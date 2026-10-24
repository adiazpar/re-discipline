CREATE TABLE community.accounts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 github_id bigint NOT NULL UNIQUE CHECK (github_id > 0),
 login text NOT NULL CHECK (length(login) BETWEEN 1 AND 100),
 email text NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE community.browser_sessions (
 secret_hash text PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES community.accounts(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL
);
CREATE INDEX browser_sessions_user ON community.browser_sessions(user_id, created_at DESC);
CREATE INDEX browser_sessions_expiry ON community.browser_sessions(expires_at);
CREATE TABLE community.oauth_states (
 state_hash text PRIMARY KEY,
 verifier text NOT NULL,
 return_path text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX oauth_states_expiry ON community.oauth_states(expires_at);
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['accounts','browser_sessions','oauth_states'] LOOP
  EXECUTE format('ALTER TABLE community.%I ENABLE ROW LEVEL SECURITY',t);
  EXECUTE format('ALTER TABLE community.%I FORCE ROW LEVEL SECURITY',t);
  EXECUTE format('REVOKE ALL ON community.%I FROM PUBLIC,re_community_app',t);
  EXECUTE format('GRANT SELECT,INSERT,UPDATE,DELETE ON community.%I TO re_community_worker',t);
  EXECUTE format('CREATE POLICY internal_worker ON community.%I FOR ALL TO re_community_worker USING (true) WITH CHECK (true)',t);
 END LOOP;
END $$;

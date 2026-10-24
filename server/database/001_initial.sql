CREATE SCHEMA IF NOT EXISTS community;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 're_community_app') THEN
  CREATE ROLE re_community_app NOLOGIN NOBYPASSRLS;
 END IF;
END $$;
GRANT re_community_app TO CURRENT_USER;

CREATE TABLE community.libraries (
 id uuid PRIMARY KEY, owner_id uuid NOT NULL, name text NOT NULL,
 slug text NOT NULL UNIQUE, visibility text NOT NULL CHECK (visibility IN ('public','unlisted','private')),
 policy jsonb NOT NULL, policy_version integer NOT NULL DEFAULT 1,
 sequence bigint NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE community.members (
 community_id uuid NOT NULL REFERENCES community.libraries(id), user_id uuid NOT NULL,
 role text NOT NULL CHECK (role IN ('owner','maintainer','publisher','contributor','reader')),
 PRIMARY KEY (community_id,user_id)
);
CREATE TABLE community.invites (
 id uuid PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id),
 secret_hash text NOT NULL UNIQUE, role text NOT NULL CHECK (role IN ('maintainer','publisher','contributor','reader')),
 expires_at timestamptz NOT NULL, used_by uuid, revoked boolean NOT NULL DEFAULT false
);
CREATE TABLE community.submissions (
 id uuid PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id), author_id uuid NOT NULL,
 idempotency_key uuid NOT NULL, digest text NOT NULL, document jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','needs_review','accepted','rejected','changes_requested','conflict')),
 policy_version integer NOT NULL, checks jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(community_id,author_id,idempotency_key)
);
CREATE TABLE community.findings (
 community_id uuid NOT NULL REFERENCES community.libraries(id), id uuid NOT NULL,
 revision uuid NOT NULL, deleted boolean NOT NULL DEFAULT false,
 PRIMARY KEY(community_id,id)
);
CREATE TABLE community.revisions (
 id uuid PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id), finding_id uuid NOT NULL,
 sequence bigint NOT NULL, document jsonb NOT NULL, deleted boolean NOT NULL DEFAULT false,
 submission_id uuid REFERENCES community.submissions(id), author_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(community_id,sequence)
);
CREATE INDEX revisions_finding ON community.revisions(community_id,finding_id,sequence DESC);
CREATE TABLE community.reviews (
 id uuid PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id),
 submission_id uuid NOT NULL REFERENCES community.submissions(id), reviewer_id uuid,
 digest text NOT NULL, policy_version integer NOT NULL, decision text NOT NULL,
 rationale text NOT NULL, automated boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE community.audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id),
 actor_id uuid, action text NOT NULL, detail jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE community.jobs (
 id uuid PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id), submission_id uuid NOT NULL REFERENCES community.submissions(id),
 attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(),
 locked_until timestamptz, lease_id uuid, completed_at timestamptz, last_error text
);
CREATE INDEX jobs_pending ON community.jobs(available_at) WHERE completed_at IS NULL;
CREATE TABLE community.tokens (
 id uuid PRIMARY KEY, user_id uuid NOT NULL, secret_hash text NOT NULL UNIQUE,
 label text NOT NULL, expires_at timestamptz NOT NULL, revoked boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE community.devices (
 secret_hash text PRIMARY KEY, code text NOT NULL UNIQUE, user_id uuid,
 expires_at timestamptz NOT NULL, consumed boolean NOT NULL DEFAULT false
);
CREATE TABLE community.usage (
 user_id uuid NOT NULL, day date NOT NULL, kind text NOT NULL, count integer NOT NULL,
 PRIMARY KEY(user_id,day,kind)
);
GRANT SELECT,INSERT,UPDATE ON community.usage TO re_community_app;

CREATE FUNCTION community.actor() RETURNS uuid LANGUAGE sql STABLE AS $$
 SELECT NULLIF(current_setting('app.user_id',true),'')::uuid
$$;
CREATE FUNCTION community.member_role(c uuid) RETURNS text LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog,community AS $$
 SELECT role FROM community.members WHERE community_id=c AND user_id=community.actor()
$$;
CREATE FUNCTION community.can_read(c uuid) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog,community AS $$
 SELECT EXISTS(SELECT 1 FROM community.libraries WHERE id=c AND (owner_id=community.actor() OR visibility IN ('public','unlisted') OR community.member_role(c) IS NOT NULL))
$$;
REVOKE ALL ON SCHEMA community FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA community FROM PUBLIC;
GRANT USAGE ON SCHEMA community TO re_community_app;
GRANT EXECUTE ON FUNCTION community.actor(),community.member_role(uuid),community.can_read(uuid) TO re_community_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON community.libraries,community.members,community.invites,community.submissions,community.findings,community.revisions,community.reviews,community.audit,community.jobs TO re_community_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA community TO re_community_app;

ALTER TABLE community.libraries ENABLE ROW LEVEL SECURITY;
CREATE POLICY library_read ON community.libraries FOR SELECT USING (community.can_read(id));
CREATE POLICY library_create ON community.libraries FOR INSERT WITH CHECK (owner_id=community.actor());
CREATE POLICY library_update ON community.libraries FOR UPDATE USING (community.member_role(id) IN ('owner','maintainer'));
ALTER TABLE community.members ENABLE ROW LEVEL SECURITY;
CREATE POLICY members_read ON community.members FOR SELECT USING (community.member_role(community_id) IS NOT NULL);
CREATE POLICY members_write ON community.members FOR ALL USING (community.member_role(community_id)='owner') WITH CHECK (
 community.member_role(community_id)='owner' OR
 (user_id=community.actor() AND role='owner' AND EXISTS(SELECT 1 FROM community.libraries WHERE id=community_id AND owner_id=community.actor()))
);
ALTER TABLE community.invites ENABLE ROW LEVEL SECURITY;
CREATE POLICY invites_owner ON community.invites FOR ALL USING (community.member_role(community_id)='owner') WITH CHECK (community.member_role(community_id)='owner');
ALTER TABLE community.submissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY submissions_read ON community.submissions FOR SELECT USING (author_id=community.actor() OR community.member_role(community_id) IN ('owner','maintainer'));
CREATE POLICY submissions_create ON community.submissions FOR INSERT WITH CHECK (author_id=community.actor() AND community.member_role(community_id) IN ('owner','maintainer','publisher','contributor'));
CREATE POLICY submissions_update ON community.submissions FOR UPDATE USING (community.member_role(community_id) IN ('owner','maintainer'));
ALTER TABLE community.findings ENABLE ROW LEVEL SECURITY;
CREATE POLICY findings_read ON community.findings FOR SELECT USING (community.can_read(community_id));
CREATE POLICY findings_write ON community.findings FOR ALL USING (community.member_role(community_id) IN ('owner','maintainer')) WITH CHECK (community.member_role(community_id) IN ('owner','maintainer'));
ALTER TABLE community.revisions ENABLE ROW LEVEL SECURITY;
CREATE POLICY revisions_read ON community.revisions FOR SELECT USING (community.can_read(community_id));
CREATE POLICY revisions_insert ON community.revisions FOR INSERT WITH CHECK (community.member_role(community_id) IN ('owner','maintainer'));
ALTER TABLE community.reviews ENABLE ROW LEVEL SECURITY;
CREATE POLICY reviews_read ON community.reviews FOR SELECT USING (community.member_role(community_id) IS NOT NULL);
CREATE POLICY reviews_insert ON community.reviews FOR INSERT WITH CHECK (reviewer_id=community.actor() AND community.member_role(community_id) IN ('owner','maintainer'));
ALTER TABLE community.audit ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_read ON community.audit FOR SELECT USING (community.member_role(community_id) IN ('owner','maintainer'));
CREATE POLICY audit_insert ON community.audit FOR INSERT WITH CHECK (actor_id=community.actor() AND community.member_role(community_id) IS NOT NULL);
ALTER TABLE community.jobs ENABLE ROW LEVEL SECURITY;
CREATE POLICY jobs_insert ON community.jobs FOR INSERT WITH CHECK (EXISTS(SELECT 1 FROM community.submissions WHERE id=submission_id AND community_id=jobs.community_id AND author_id=community.actor()));
-- Credentials and device exchanges are only accessible to narrowly scoped server code.
ALTER TABLE community.tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE community.devices ENABLE ROW LEVEL SECURITY;
ALTER TABLE community.usage ENABLE ROW LEVEL SECURITY;
CREATE POLICY usage_self ON community.usage FOR ALL USING (user_id=community.actor()) WITH CHECK (user_id=community.actor());

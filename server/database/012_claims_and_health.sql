ALTER TABLE community.revisions ADD COLUMN claim_digest text NOT NULL DEFAULT '';
ALTER TABLE community.submissions ADD COLUMN claim_digest text NOT NULL DEFAULT '',
 ADD COLUMN identity_generation bigint NOT NULL DEFAULT -1,
 ADD COLUMN identity_kind text NOT NULL DEFAULT '',
 ADD COLUMN identity_snapshot text NOT NULL DEFAULT '';
ALTER TABLE community.libraries ADD COLUMN inventory_generation bigint NOT NULL DEFAULT 0;
CREATE INDEX revisions_claim_digest ON community.revisions(community_id,claim_digest) WHERE NOT deleted;
CREATE INDEX submissions_claim_digest ON community.submissions(community_id,claim_digest);
CREATE INDEX submissions_candidate_terms ON community.submissions USING gin(to_tsvector('english',document->>'markdown'))
 WHERE state IN ('queued','needs_review','changes_requested','conflict');
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE INDEX revisions_candidate_spelling ON community.revisions USING gin((document->>'markdown') public.gin_trgm_ops) WHERE NOT deleted;
CREATE INDEX submissions_candidate_spelling ON community.submissions USING gin((document->>'markdown') public.gin_trgm_ops)
 WHERE state IN ('queued','needs_review','changes_requested','conflict');

CREATE TABLE community.health_work (
 community_id uuid NOT NULL REFERENCES community.libraries(id),
 subject_kind text NOT NULL CHECK(subject_kind IN ('finding','submission')),
 subject_id uuid NOT NULL, version text NOT NULL,
 generation bigint NOT NULL DEFAULT 1, available_at timestamptz NOT NULL DEFAULT now(),
 lease_id uuid, locked_until timestamptz, attempts integer NOT NULL DEFAULT 0,
 candidate_offset integer NOT NULL DEFAULT 0, candidate_signature text NOT NULL DEFAULT '',
 analyzed_version text NOT NULL DEFAULT '', analyzed_policy integer NOT NULL DEFAULT 0,
 analyzed_generation bigint NOT NULL DEFAULT 0, scan_error text NOT NULL DEFAULT '',
 last_analyzed timestamptz, last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(community_id,subject_kind,subject_id)
);
CREATE INDEX health_work_ready ON community.health_work(available_at,last_analyzed);
CREATE TABLE community.health_issues (
 id text PRIMARY KEY, community_id uuid NOT NULL REFERENCES community.libraries(id),
 subject_kind text NOT NULL, subject_id uuid NOT NULL, subject_version text NOT NULL,
 other_kind text NOT NULL DEFAULT '', other_id text NOT NULL DEFAULT '', other_version text NOT NULL DEFAULT '',
 kind text NOT NULL, reason text NOT NULL, judgment jsonb NOT NULL DEFAULT '{}',
 policy_version integer NOT NULL, state text NOT NULL DEFAULT 'open' CHECK(state IN ('open','dismissed','resolved')),
 reviewed_by uuid, rationale text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX health_issues_community ON community.health_issues(community_id,state,created_at);
ALTER TABLE community.health_work ENABLE ROW LEVEL SECURITY;
ALTER TABLE community.health_issues ENABLE ROW LEVEL SECURITY;
CREATE POLICY health_work_reviewers ON community.health_work FOR SELECT USING(community.member_role(community_id) IN ('owner','maintainer'));
CREATE POLICY health_issues_reviewers ON community.health_issues FOR ALL
 USING(community.member_role(community_id) IN ('owner','maintainer'))
 WITH CHECK(community.member_role(community_id) IN ('owner','maintainer'));
CREATE POLICY internal_worker ON community.health_work FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
CREATE POLICY internal_worker ON community.health_issues FOR ALL TO re_community_worker USING(true) WITH CHECK(true);
GRANT SELECT ON community.health_work TO re_community_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON community.health_issues TO re_community_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON community.health_work,community.health_issues TO re_community_worker;

CREATE FUNCTION community.queue_health(c uuid,k text,i uuid,v text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 INSERT INTO community.health_work(community_id,subject_kind,subject_id,version) VALUES(c,k,i,v)
 ON CONFLICT(community_id,subject_kind,subject_id) DO UPDATE SET
 version=excluded.version,generation=health_work.generation+1,available_at=now(),
 candidate_offset=0,candidate_signature='',last_error='',scan_error='';
 UPDATE community.libraries SET inventory_generation=inventory_generation+1 WHERE id=c;
 DELETE FROM community.health_issues WHERE community_id=c AND
 ((subject_kind=k AND subject_id=i AND subject_version<>v) OR (other_kind=k AND other_id=i::text AND other_version<>v));
END $$;
REVOKE ALL ON FUNCTION community.queue_health(uuid,text,uuid,text) FROM PUBLIC;

CREATE FUNCTION community.track_claim_health() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE c uuid; i uuid; k text; v text;
BEGIN
 k=CASE WHEN TG_TABLE_NAME='findings' THEN 'finding' ELSE 'submission' END;
 IF TG_OP='DELETE' THEN c=OLD.community_id; i=OLD.id;
 ELSE c=NEW.community_id; i=NEW.id;
 END IF;
 IF TG_OP='DELETE' OR (k='finding' AND COALESCE((to_jsonb(NEW)->>'deleted')::boolean,false)) THEN
  DELETE FROM community.health_work WHERE community_id=c AND subject_kind=k AND subject_id=i;
  DELETE FROM community.health_issues WHERE community_id=c AND ((subject_kind=k AND subject_id=i) OR (other_kind=k AND other_id=i::text));
  UPDATE community.libraries SET inventory_generation=inventory_generation+1 WHERE id=c;
  RETURN NULL;
 END IF;
 IF k='submission' THEN
  IF NEW.state IN ('accepted','rejected') THEN
   DELETE FROM community.health_work WHERE community_id=c AND subject_kind=k AND subject_id=i;
   DELETE FROM community.health_issues WHERE community_id=c AND ((subject_kind=k AND subject_id=i) OR (other_kind=k AND other_id=i::text));
   UPDATE community.libraries SET inventory_generation=inventory_generation+1 WHERE id=c;
   RETURN NULL;
  END IF;
  v=NEW.digest||'/'||NEW.resolution_version::text;
 ELSE v=NEW.revision::text;
 END IF;
 PERFORM community.queue_health(c,k,i,v);
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION community.track_claim_health() FROM PUBLIC;
CREATE TRIGGER track_finding_health AFTER INSERT OR UPDATE OF revision,deleted OR DELETE ON community.findings
 FOR EACH ROW EXECUTE FUNCTION community.track_claim_health();
CREATE TRIGGER track_submission_health AFTER INSERT OR UPDATE OF document,state,resolution_version OR DELETE ON community.submissions
 FOR EACH ROW EXECUTE FUNCTION community.track_claim_health();

CREATE FUNCTION community.track_health_policy() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 UPDATE community.health_work SET generation=generation+1,available_at=now(),candidate_offset=0,candidate_signature='',scan_error='',last_error=''
 WHERE community_id=NEW.id;
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION community.track_health_policy() FROM PUBLIC;
CREATE TRIGGER track_health_policy AFTER UPDATE OF policy_version ON community.libraries
 FOR EACH ROW EXECUTE FUNCTION community.track_health_policy();

CREATE FUNCTION community.track_health_relation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE c uuid; a uuid; b uuid; f record;
BEGIN
 IF TG_OP='DELETE' THEN c=OLD.community_id;a=OLD.source_id;b=OLD.target_id;
 ELSE c=NEW.community_id;a=NEW.source_id;b=NEW.target_id;END IF;
 FOR f IN SELECT id,revision FROM community.findings WHERE community_id=c AND id IN (a,b) AND NOT deleted LOOP
  PERFORM community.queue_health(c,'finding',f.id,f.revision::text);
 END LOOP;
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION community.track_health_relation() FROM PUBLIC;
CREATE TRIGGER track_health_relation AFTER INSERT OR UPDATE OR DELETE ON community.finding_relations
 FOR EACH ROW EXECUTE FUNCTION community.track_health_relation();

INSERT INTO community.health_work(community_id,subject_kind,subject_id,version)
 SELECT community_id,'finding',id,revision::text FROM community.findings WHERE NOT deleted;
INSERT INTO community.health_work(community_id,subject_kind,subject_id,version)
 SELECT community_id,'submission',id,digest||'/'||resolution_version::text FROM community.submissions WHERE state NOT IN ('accepted','rejected');
-- Attached copy decisions must be erased with their finding.
CREATE OR REPLACE FUNCTION community.erase_integrity_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE ids uuid[];
BEGIN
 SELECT COALESCE(array_agg(id),'{}'::uuid[]) INTO ids FROM community.submissions
 WHERE community_id=OLD.community_id AND finding_id=OLD.id::text;
 INSERT INTO community.publication_deletions SELECT community_id,author_id,idempotency_key
 FROM community.submissions WHERE id=ANY(ids) ON CONFLICT DO NOTHING;
 DELETE FROM community.jobs WHERE submission_id=ANY(ids);
 DELETE FROM community.reviews WHERE submission_id=ANY(ids);
 DELETE FROM community.audit WHERE community_id=OLD.community_id AND
 (detail->>'id'=OLD.id::text OR detail->>'id'=ANY(ids::text[]) OR detail->>'submission_id'=ANY(ids::text[]));
 UPDATE community.revisions SET content_digest='',text_digest='',claim_digest='',reason='',replacement_id='',assessment_evidence='[]',submission_id=NULL
 WHERE community_id=OLD.community_id AND finding_id=OLD.id;
 DELETE FROM community.submissions WHERE id=ANY(ids);
 RETURN OLD;
END $$;

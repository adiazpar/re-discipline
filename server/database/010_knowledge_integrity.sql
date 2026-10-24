ALTER TABLE community.revisions ADD COLUMN content_digest text NOT NULL DEFAULT '',
 ADD COLUMN text_digest text NOT NULL DEFAULT '',
 ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','disputed','refuted','superseded')),
 ADD COLUMN reason text NOT NULL DEFAULT '', ADD COLUMN replacement_id text NOT NULL DEFAULT '',
 ADD COLUMN assessment_evidence jsonb NOT NULL DEFAULT '[]';
CREATE INDEX revisions_content_digest ON community.revisions(community_id,content_digest) WHERE NOT deleted;
CREATE INDEX revisions_text_digest ON community.revisions(community_id,text_digest) WHERE NOT deleted;

ALTER TABLE community.submissions ADD COLUMN finding_id text NOT NULL DEFAULT '',
 ADD COLUMN base_revision text NOT NULL DEFAULT '', ADD COLUMN accepted_revision text NOT NULL DEFAULT '',
 ADD COLUMN identity_review boolean NOT NULL DEFAULT false,
 ADD COLUMN resolution_version integer NOT NULL DEFAULT 0,
 ADD COLUMN contribution_target text NOT NULL DEFAULT '', ADD COLUMN contribution_revision text NOT NULL DEFAULT '';
UPDATE community.submissions s SET finding_id=r.finding_id::text,accepted_revision=r.id::text
 FROM community.revisions r WHERE r.submission_id=s.id AND NOT r.deleted;

-- Multiple legacy matches are retained for explicit resolution, never guessed.
CREATE TABLE community.publication_sources (
 community_id uuid NOT NULL, author_id uuid NOT NULL, namespace text NOT NULL DEFAULT '',
 source_path text NOT NULL, finding_id uuid NOT NULL,
 PRIMARY KEY(community_id,author_id,namespace,source_path,finding_id),
 FOREIGN KEY(community_id,finding_id) REFERENCES community.findings(community_id,id) ON DELETE CASCADE
);
INSERT INTO community.publication_sources
 SELECT DISTINCT r.community_id,r.author_id,COALESCE(r.document->>'source_namespace',''),r.document->>'source_path',r.finding_id
 FROM community.revisions r JOIN community.findings f ON f.community_id=r.community_id AND f.id=r.finding_id
 WHERE NOT r.deleted AND r.author_id IS NOT NULL AND r.document->>'source_path' IS NOT NULL;
ALTER TABLE community.publication_sources ENABLE ROW LEVEL SECURITY;
CREATE POLICY sources_read ON community.publication_sources FOR SELECT USING(community.can_read(community_id));
CREATE POLICY sources_write ON community.publication_sources FOR ALL
 USING(community.member_role(community_id) IN ('owner','maintainer'))
 WITH CHECK(community.member_role(community_id) IN ('owner','maintainer'));
GRANT SELECT,INSERT,UPDATE,DELETE ON community.publication_sources TO re_community_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON community.publication_sources TO re_community_worker;
CREATE POLICY internal_worker ON community.publication_sources FOR ALL TO re_community_worker USING(true) WITH CHECK(true);

-- Hard deletion must also erase the newly added metadata and attached copies.
CREATE FUNCTION community.erase_integrity_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE ids uuid[];
BEGIN
 SELECT COALESCE(array_agg(id),'{}'::uuid[]) INTO ids FROM community.submissions
 WHERE community_id=OLD.community_id AND finding_id=OLD.id::text;
 INSERT INTO community.publication_deletions SELECT community_id,author_id,idempotency_key
 FROM community.submissions WHERE id=ANY(ids) ON CONFLICT DO NOTHING;
 DELETE FROM community.jobs WHERE submission_id=ANY(ids);
 DELETE FROM community.reviews WHERE submission_id=ANY(ids);
 DELETE FROM community.audit WHERE community_id=OLD.community_id AND
 (detail->>'id'=OLD.id::text OR detail->>'submission_id'=ANY(ids::text[]));
 UPDATE community.revisions SET content_digest='',text_digest='',reason='',replacement_id='',assessment_evidence='[]',submission_id=NULL
 WHERE community_id=OLD.community_id AND finding_id=OLD.id;
 DELETE FROM community.submissions WHERE id=ANY(ids);
 RETURN OLD;
END $$;
REVOKE ALL ON FUNCTION community.erase_integrity_metadata() FROM PUBLIC;
CREATE TRIGGER erase_integrity_metadata BEFORE DELETE ON community.findings FOR EACH ROW EXECUTE FUNCTION community.erase_integrity_metadata();

-- Accepted attached copies are public to the same audience as the finding;
-- unpublished submissions and reviewer-only data remain inaccessible.
CREATE FUNCTION community.finding_contributions(c uuid,f uuid,after_id text DEFAULT '') RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF NOT community.can_read(c) THEN RAISE EXCEPTION 'permission denied' USING ERRCODE='42501'; END IF;
 RETURN (SELECT COALESCE(jsonb_agg(jsonb_build_object('submission_id',s.id,'author',s.author_id,'revision',s.accepted_revision,'source_path',s.document->>'source_path','evidence',s.document->'evidence') ORDER BY s.id),'[]')
 FROM (SELECT * FROM community.submissions WHERE community_id=c AND finding_id=f::text AND state='accepted' AND id::text>after_id ORDER BY id LIMIT 200) s JOIN community.findings fnd ON fnd.community_id=s.community_id AND fnd.id::text=s.finding_id
 WHERE s.community_id=c AND s.finding_id=f::text AND s.state='accepted' AND NOT fnd.deleted);
END $$;
REVOKE ALL ON FUNCTION community.finding_contributions(uuid,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.finding_contributions(uuid,uuid,text) TO re_community_app;

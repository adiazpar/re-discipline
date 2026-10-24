-- Relationships certify a reviewed pair of revisions, not future edits.
CREATE TABLE community.finding_relations (
 community_id uuid NOT NULL, source_id uuid NOT NULL, target_id uuid NOT NULL,
 source_revision uuid NOT NULL, target_revision uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('equivalent','related','conflicting')),
 rationale text NOT NULL, reviewer_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(community_id,source_id,target_id), CHECK(source_id<>target_id),
 FOREIGN KEY(community_id,source_id) REFERENCES community.findings(community_id,id) ON DELETE CASCADE,
 FOREIGN KEY(community_id,target_id) REFERENCES community.findings(community_id,id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX one_canonical_target ON community.finding_relations(community_id,source_id) WHERE kind='equivalent';
ALTER TABLE community.finding_relations ENABLE ROW LEVEL SECURITY;
CREATE POLICY relations_read ON community.finding_relations FOR SELECT USING(community.can_read(community_id));
CREATE POLICY relations_write ON community.finding_relations FOR ALL USING
 (community.member_role(community_id) IN ('owner','maintainer')) WITH CHECK
 (community.member_role(community_id) IN ('owner','maintainer') AND reviewer_id=community.actor());
GRANT SELECT,INSERT,UPDATE,DELETE ON community.finding_relations TO re_community_app;

-- Candidate search stays deterministic and scoped to the readable community.
CREATE INDEX revisions_candidate_terms ON community.revisions USING gin(to_tsvector('english',document->>'markdown')) WHERE NOT deleted;

CREATE FUNCTION community.erase_relationship_audit() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 DELETE FROM community.audit WHERE community_id=OLD.community_id AND action='finding.relate' AND (detail->>'source_id'=OLD.id::text OR detail->>'target_id'=OLD.id::text);
 RETURN OLD;
END $$;
REVOKE ALL ON FUNCTION community.erase_relationship_audit() FROM PUBLIC;
CREATE TRIGGER erase_relationship_audit AFTER DELETE ON community.findings FOR EACH ROW EXECUTE FUNCTION community.erase_relationship_audit();

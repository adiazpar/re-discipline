-- Deletion retains only content-free synchronization markers, never the finding.
ALTER TABLE community.revisions ALTER COLUMN author_id DROP NOT NULL;

-- Content-free receipts prevent an offline retry from republishing deleted data.
CREATE TABLE community.publication_deletions (
 community_id uuid NOT NULL REFERENCES community.libraries(id), author_id uuid NOT NULL,
 idempotency_key uuid NOT NULL, PRIMARY KEY(community_id,author_id,idempotency_key)
);
ALTER TABLE community.publication_deletions ENABLE ROW LEVEL SECURITY;
CREATE POLICY deletion_receipts_read ON community.publication_deletions FOR SELECT
 USING (author_id=community.actor() AND community.member_role(community_id) IS NOT NULL);
GRANT SELECT ON community.publication_deletions TO re_community_app;

CREATE FUNCTION community.erase_finding(c uuid, f uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE submission_ids uuid[];
BEGIN
 IF COALESCE(community.member_role(c),'') NOT IN ('owner','maintainer') THEN
  RAISE EXCEPTION 'permission denied' USING ERRCODE='42501';
 END IF;
 PERFORM id FROM community.libraries WHERE id=c FOR UPDATE;
 SELECT COALESCE(array_agg(id),'{}'::uuid[]) INTO submission_ids
 FROM community.submissions WHERE community_id=c AND
  (document->>'finding_id'=f::text OR id IN
   (SELECT submission_id FROM community.revisions WHERE community_id=c AND finding_id=f));
 INSERT INTO community.publication_deletions SELECT community_id,author_id,idempotency_key
 FROM community.submissions WHERE community_id=c AND id=ANY(submission_ids) ON CONFLICT DO NOTHING;
 DELETE FROM community.jobs WHERE community_id=c AND submission_id=ANY(submission_ids);
 DELETE FROM community.reviews WHERE community_id=c AND submission_id=ANY(submission_ids);
 DELETE FROM community.audit WHERE community_id=c AND
  ((action LIKE 'submission.%' AND (detail->>'submission_id'=ANY(submission_ids::text[]) OR detail->>'id'=ANY(submission_ids::text[])))
   OR (action='finding.withdraw' AND detail->>'id'=f::text));
 UPDATE community.revisions SET document='{}',deleted=true,submission_id=NULL,author_id=NULL
 WHERE community_id=c AND finding_id=f;
 DELETE FROM community.submissions WHERE community_id=c AND id=ANY(submission_ids);
 DELETE FROM community.findings WHERE community_id=c AND id=f;
END $$;
REVOKE ALL ON FUNCTION community.erase_finding(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.erase_finding(uuid,uuid) TO re_community_app;

CREATE FUNCTION community.erase_invites(c uuid, invitation uuid DEFAULT NULL) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
DECLARE ids uuid[];
BEGIN
 IF COALESCE(community.member_role(c),'') <> 'owner' THEN
  RAISE EXCEPTION 'permission denied' USING ERRCODE='42501';
 END IF;
 SELECT COALESCE(array_agg(id),'{}'::uuid[]) INTO ids FROM community.invites
 WHERE community_id=c AND (id=invitation OR
  (invitation IS NULL AND (revoked OR used_by IS NOT NULL OR expires_at<=now())));
 DELETE FROM community.invites WHERE community_id=c AND id=ANY(ids);
 DELETE FROM community.audit WHERE community_id=c AND action LIKE 'invite.%'
  AND (detail->>'invite_id'=ANY(ids::text[]) OR detail->>'id'=ANY(ids::text[]));
END $$;
REVOKE ALL ON FUNCTION community.erase_invites(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.erase_invites(uuid,uuid) TO re_community_app;

-- Remove inactive records left by earlier service versions.
DELETE FROM community.audit WHERE action LIKE 'invite.%' AND
 (detail->>'invite_id' IN (SELECT id::text FROM community.invites WHERE revoked OR used_by IS NOT NULL OR expires_at<=now())
 OR detail->>'id' IN (SELECT id::text FROM community.invites WHERE revoked OR used_by IS NOT NULL OR expires_at<=now()));
DELETE FROM community.invites WHERE revoked OR used_by IS NOT NULL OR expires_at<=now();
DELETE FROM community.tokens WHERE revoked OR expires_at<=now();
DELETE FROM community.devices WHERE consumed OR expires_at<=now();

DO $$ DECLARE finding record;
BEGIN
 FOR finding IN SELECT f.community_id,f.id,l.owner_id FROM community.findings f
 JOIN community.libraries l ON l.id=f.community_id WHERE f.deleted LOOP
  PERFORM set_config('app.user_id',finding.owner_id::text,true);
  PERFORM community.erase_finding(finding.community_id,finding.id);
 END LOOP;
 PERFORM set_config('app.user_id','',true);
END $$;

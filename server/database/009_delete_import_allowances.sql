-- Revocation removes the credential instead of retaining an inactive row.
CREATE POLICY import_grants_delete ON community.import_grants FOR DELETE USING
 (community.member_role(community_id)='owner');
GRANT DELETE ON community.import_grants TO re_community_app;
DELETE FROM community.import_grants WHERE revoked;

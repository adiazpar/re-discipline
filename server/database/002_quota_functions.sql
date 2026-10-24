CREATE FUNCTION community.lock_library(c uuid) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,community AS $$
BEGIN
 IF COALESCE(community.member_role(c),'') NOT IN ('owner','maintainer','publisher','contributor') THEN RAISE EXCEPTION 'permission denied' USING ERRCODE='42501'; END IF;
 PERFORM 1 FROM community.libraries WHERE id=c FOR UPDATE;
END $$;
CREATE FUNCTION community.publication_usage(c uuid) RETURNS TABLE(bytes bigint,findings bigint) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT (SELECT COALESCE(sum(octet_length(document::text)),0)*2 FROM community.submissions WHERE community_id=c) +
        (SELECT COALESCE(sum(octet_length(document::text)),0) FROM community.revisions WHERE community_id=c AND submission_id IS NULL),
        (SELECT count(*) FROM community.findings WHERE community_id=c AND NOT deleted)
 WHERE community.can_read(c)
$$;
REVOKE ALL ON FUNCTION community.lock_library(uuid),community.publication_usage(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION community.lock_library(uuid),community.publication_usage(uuid) TO re_community_app;

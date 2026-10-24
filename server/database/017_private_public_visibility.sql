-- Retire link-only access without exposing existing communities. Invalidate
-- policy-bound updates and reviews when an existing community becomes private.
UPDATE community.libraries SET visibility='private',policy_version=policy_version+1
 WHERE visibility='unlisted';
ALTER TABLE community.libraries DROP CONSTRAINT libraries_visibility_check;
ALTER TABLE community.libraries ADD CONSTRAINT libraries_visibility_check
 CHECK (visibility IN ('private','public'));
ALTER TABLE community.libraries ALTER COLUMN visibility SET DEFAULT 'private';

CREATE OR REPLACE FUNCTION community.can_read(c uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT EXISTS(SELECT 1 FROM community.libraries WHERE id=c AND
  (owner_id=community.actor() OR visibility='public' OR community.member_role(c) IS NOT NULL))
$$;

-- Maintainers still advance the corpus sequence when reviewing findings. Guard
-- visibility separately, so that permission does not let them expose a corpus.
CREATE FUNCTION community.guard_library_visibility() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,community AS $$
BEGIN
 IF current_user='re_community_app' THEN
  IF TG_OP='INSERT' THEN
   IF NEW.visibility<>'private' THEN
    RAISE EXCEPTION 'new communities must be private' USING ERRCODE='23514';
   END IF;
  ELSIF NEW.visibility IS DISTINCT FROM OLD.visibility AND community.member_role(OLD.id) IS DISTINCT FROM 'owner' THEN
   RAISE EXCEPTION 'only the owner can change visibility' USING ERRCODE='42501';
  END IF;
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION community.guard_library_visibility() FROM PUBLIC;
CREATE TRIGGER guard_library_visibility BEFORE INSERT OR UPDATE OF visibility
 ON community.libraries FOR EACH ROW EXECUTE FUNCTION community.guard_library_visibility();

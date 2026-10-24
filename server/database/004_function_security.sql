ALTER FUNCTION community.actor() SET search_path = pg_catalog;

-- Automatic RLS remains an event trigger; application roles need no direct call.
DO $$
DECLARE f regprocedure;
BEGIN
 SELECT p.oid::regprocedure INTO f
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname='public' AND p.proname='rls_auto_enable'
   AND p.pronargs=0 AND p.prorettype='event_trigger'::regtype;
 IF f IS NOT NULL THEN
  EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC',f);
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='anon') THEN
   EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM anon',f);
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='authenticated') THEN
   EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM authenticated',f);
  END IF;
 END IF;
END $$;

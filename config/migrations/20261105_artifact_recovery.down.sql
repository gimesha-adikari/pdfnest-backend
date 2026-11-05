-- Destructive rollback is refused once operator evidence exists.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:artifact-recovery-v1'));
DO $$
DECLARE action_count bigint;
BEGIN
  IF to_regclass('public.artifact_recovery_actions') IS NULL THEN RETURN; END IF;
  SELECT count(*) INTO action_count FROM public.artifact_recovery_actions;
  IF action_count <> 0 THEN RAISE EXCEPTION 'retained artifact recovery audit evidence prevents rollback'; END IF;
END $$;
DROP TABLE IF EXISTS public.artifact_recovery_actions;
DELETE FROM public.backend_durability_schema WHERE version='20261105_artifact_recovery_v1';
COMMIT;

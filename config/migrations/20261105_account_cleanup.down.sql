-- Never discard pending deletion intent during rollback.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='30s';
SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:account-cleanup-v1'));
DO $$ BEGIN IF EXISTS(SELECT 1 FROM public.account_storage_cleanups) THEN RAISE EXCEPTION 'pending account cleanup; preserve ledger'; END IF; END $$;
DROP TABLE public.account_storage_cleanups;
DELETE FROM public.backend_durability_schema WHERE version='20261105_account_cleanup_v1';
COMMIT;

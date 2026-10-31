-- DRAFT EMPTY-STATE ROLLBACK ONLY. Normally roll back the binary and KEEP durable records.
-- Stop admissions/reconcilers first. Never erase pending or retained ownership to downgrade.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:backend-durability-v1'));
LOCK TABLE public.task_completions, public.worker_result_ownerships, public.studio_storage_object_retirements, public.studio_jobs IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.task_completions) OR EXISTS(SELECT 1 FROM public.worker_result_ownerships)
 OR EXISTS(SELECT 1 FROM public.studio_storage_object_retirements)
 OR EXISTS(SELECT 1 FROM public.studio_jobs WHERE pending_object_keys IS NOT NULL AND pending_object_keys <> '[]'::jsonb)
 THEN RAISE EXCEPTION 'durability rollback refused: retained ownership exists'; END IF;
END $$;
ALTER TABLE public.studio_jobs DROP COLUMN pending_object_keys;
DROP TABLE public.studio_storage_object_retirements;
DROP TABLE public.worker_result_ownerships;
DROP TABLE public.task_completions;
DROP TABLE public.backend_durability_schema;
COMMIT;

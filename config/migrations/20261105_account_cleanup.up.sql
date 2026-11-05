-- Account-only durable discovery. Physical keys use the existing Studio cleanup
-- outbox and retirement tombstones; no object store operation runs in this DDL.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='30s';
SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:account-cleanup-v1'));
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM public.backend_durability_schema WHERE version='20261029_backend_durability_v1') THEN RAISE EXCEPTION 'backend durability marker missing'; END IF;
 IF to_regclass('public.account_storage_cleanups') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM public.backend_durability_schema WHERE version='20261105_account_cleanup_v1') THEN RAISE EXCEPTION 'unversioned account cleanup table'; END IF;
END $$;
CREATE TABLE IF NOT EXISTS public.account_storage_cleanups (
 user_id uuid PRIMARY KEY,
 next_attempt_at timestamptz NOT NULL,
 lease_expires_at timestamptz,
 lease_token uuid,
 attempts bigint NOT NULL DEFAULT 0,
 last_error varchar(128),
 scan_purpose bigint NOT NULL DEFAULT 0,
 scan_cursor text NOT NULL DEFAULT '',
 created_at timestamptz,
 updated_at timestamptz
);
ALTER TABLE public.studio_storage_object_retirements ADD COLUMN IF NOT EXISTS last_delete_confirmed_at timestamptz;
CREATE INDEX IF NOT EXISTS idx_account_storage_cleanups_next_attempt_at ON public.account_storage_cleanups(next_attempt_at);
INSERT INTO public.backend_durability_schema(version,applied_at) VALUES ('20261105_account_cleanup_v1',now()) ON CONFLICT DO NOTHING;
COMMIT;

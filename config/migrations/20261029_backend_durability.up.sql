-- DRAFT. Apply only after separate migration approval and a verified backup.
-- This file is never executed by application startup or the existing migrate command.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:backend-durability-v1'));
-- Refuse accidental adoption of an unversioned partial rollout.
DO $$ DECLARE approved boolean; BEGIN
 IF to_regclass('public.backend_durability_schema') IS NULL THEN
  IF to_regclass('public.task_completions') IS NOT NULL
   OR to_regclass('public.worker_result_ownerships') IS NOT NULL
   OR to_regclass('public.studio_storage_object_retirements') IS NOT NULL
   OR EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='studio_jobs' AND column_name='pending_object_keys')
  THEN RAISE EXCEPTION 'unversioned durability schema exists; inspect before adoption'; END IF;
 ELSE
  EXECUTE 'SELECT EXISTS(SELECT 1 FROM public.backend_durability_schema WHERE version=''20261029_backend_durability_v1'')' INTO approved;
  IF NOT approved THEN RAISE EXCEPTION 'unrecognized durability schema marker'; END IF;
 END IF;
END $$;

CREATE TABLE IF NOT EXISTS public.task_completions (
 id varchar(128) PRIMARY KEY,
 owner_identity text NOT NULL,
 download_token text NOT NULL,
 reservation_id text,
 reservation_kind text,
 state text NOT NULL,
 result_key text,
 result_url text,
 error text,
 artifact_sha256 text,
 artifact_bytes bigint,
 expires_at timestamptz,
 created_at timestamptz,
 updated_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_task_completions_reservation_id ON public.task_completions(reservation_id);
CREATE INDEX IF NOT EXISTS idx_task_completions_state ON public.task_completions(state);
CREATE INDEX IF NOT EXISTS idx_task_completions_expires_at ON public.task_completions(expires_at);
CREATE TABLE IF NOT EXISTS public.worker_result_ownerships (
 id varchar(36) PRIMARY KEY,
 request_id text NOT NULL,
 request_hash text NOT NULL,
 worker_job_id text,
 owner_identity text,
 submit_path text NOT NULL,
 profile text,
 source_keys text NOT NULL,
 state text NOT NULL,
 status_json text,
 result_key text,
 metadata_key text,
 expires_at timestamptz,
 next_attempt_at timestamptz,
 attempts bigint NOT NULL DEFAULT 0,
 last_error text,
 created_at timestamptz,
 updated_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_worker_result_ownerships_request_id ON public.worker_result_ownerships(request_id);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_worker_job_id ON public.worker_result_ownerships(worker_job_id);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_state ON public.worker_result_ownerships(state);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_expires_at ON public.worker_result_ownerships(expires_at);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_next_attempt_at ON public.worker_result_ownerships(next_attempt_at);
ALTER TABLE public.studio_jobs ADD COLUMN IF NOT EXISTS pending_object_keys jsonb;
CREATE TABLE IF NOT EXISTS public.studio_storage_object_retirements (
 object_key varchar(512) PRIMARY KEY,
 created_at timestamptz
);
-- Keep the reviewed historical schema ledger immutable. This extension has its own marker.
CREATE TABLE IF NOT EXISTS public.backend_durability_schema (version varchar(64) PRIMARY KEY, applied_at timestamptz NOT NULL);
INSERT INTO public.backend_durability_schema(version,applied_at) VALUES ('20261029_backend_durability_v1',now()) ON CONFLICT DO NOTHING;
COMMIT;

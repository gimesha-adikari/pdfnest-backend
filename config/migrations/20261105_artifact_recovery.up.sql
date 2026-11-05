-- Additive operator recovery ledger. This migration is separate from the
-- immutable 20261029 durability migration and is never applied at startup.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:artifact-recovery-v1'));
DO $$
DECLARE base_ok boolean; recovery_ok boolean;
BEGIN
  IF to_regclass('public.backend_durability_schema') IS NULL THEN
    RAISE EXCEPTION 'backend durability migration must be applied first';
  END IF;
  SELECT EXISTS(SELECT 1 FROM public.backend_durability_schema WHERE version='20261029_backend_durability_v1') INTO base_ok;
  SELECT EXISTS(SELECT 1 FROM public.backend_durability_schema WHERE version='20261105_artifact_recovery_v1') INTO recovery_ok;
  IF NOT base_ok THEN RAISE EXCEPTION 'backend durability marker is missing'; END IF;
  IF to_regclass('public.artifact_recovery_actions') IS NOT NULL AND NOT recovery_ok THEN
    RAISE EXCEPTION 'unversioned artifact recovery table exists; inspect before adoption';
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS public.artifact_recovery_actions (
  id uuid PRIMARY KEY,
  operation_key varchar(128) NOT NULL,
  request_fingerprint char(64) NOT NULL,
  task_id varchar(128) NOT NULL,
  admin_user_id uuid NOT NULL,
  action varchar(16) NOT NULL,
  state varchar(48) NOT NULL,
  reason varchar(500) NOT NULL,
  owner_identity text NOT NULL,
  original_result_key text NOT NULL,
  artifact_sha256 varchar(64) NOT NULL,
  artifact_bytes bigint NOT NULL,
  original_expires_at timestamptz NOT NULL,
  source_key text,
  recovery_key text,
  last_error_code varchar(64),
  attempts bigint NOT NULL DEFAULT 0,
  lease_expires_at timestamptz,
  created_at timestamptz,
  updated_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_artifact_recovery_actions_operation_key ON public.artifact_recovery_actions(operation_key);
CREATE INDEX IF NOT EXISTS idx_artifact_recovery_actions_task_id ON public.artifact_recovery_actions(task_id);
CREATE INDEX IF NOT EXISTS idx_artifact_recovery_actions_admin_user_id ON public.artifact_recovery_actions(admin_user_id);
INSERT INTO public.backend_durability_schema(version, applied_at)
VALUES ('20261105_artifact_recovery_v1', now()) ON CONFLICT DO NOTHING;
COMMIT;

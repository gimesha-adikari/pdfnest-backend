-- Explicit additive migration; never applied at application startup.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='30s';
CREATE TABLE IF NOT EXISTS recovery_copy_obligations (
 action_id uuid NOT NULL REFERENCES artifact_recovery_actions(id),
 attempt bigint NOT NULL CHECK(attempt > 0),
 task_id text NOT NULL, owner_identity text NOT NULL,
 object_key text NOT NULL UNIQUE, digest text NOT NULL, bytes bigint NOT NULL CHECK(bytes >= 0),
 next_check timestamptz NOT NULL, last_delete_confirmed_at timestamptz, last_error_code text,
 PRIMARY KEY(action_id,attempt)
);
-- Capture legacy keys too; older retries reused the first-attempt key.
INSERT INTO recovery_copy_obligations(action_id,attempt,task_id,owner_identity,object_key,digest,bytes,next_check)
SELECT id,1,task_id,owner_identity,recovery_key,artifact_sha256,artifact_bytes,
 GREATEST(COALESCE(lease_expires_at,now())+interval '1 minute', original_expires_at)
FROM artifact_recovery_actions WHERE action='recover' AND recovery_key IS NOT NULL AND recovery_key<>''
ON CONFLICT DO NOTHING;
CREATE INDEX IF NOT EXISTS idx_recovery_copy_due ON recovery_copy_obligations(next_check);
INSERT INTO backend_durability_schema(version,applied_at) VALUES('20261105_recovery_copies_v1',now()) ON CONFLICT DO NOTHING;
COMMIT;

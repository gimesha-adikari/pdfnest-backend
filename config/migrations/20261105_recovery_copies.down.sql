BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='30s';
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM recovery_copy_obligations) THEN
  RAISE EXCEPTION 'durable recovery-copy obligations prevent rollback';
 END IF;
END $$;
DROP TABLE recovery_copy_obligations;
DELETE FROM backend_durability_schema WHERE version='20261105_recovery_copies_v1';
COMMIT;

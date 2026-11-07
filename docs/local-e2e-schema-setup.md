# Disposable local E2E database schema

The local application needs the additive durability and account-cleanup tables
from the checked-in migrations in addition to the tables created by the normal
GORM initializer. Use this procedure only with a disposable PostgreSQL database
on loopback; the helper refuses production-style database names and non-local
hosts.

1. Create a disposable database whose name starts with `platen_e2e_` and expose
   it on `localhost` or a loopback address.
2. Start the Backend once with `APP_ENV=test` and its `DATABASE_URL` pointing to
   that database. This runs the normal GORM initialization and validators.
3. Apply the reviewed additive migrations with the explicit local-test guard:

   ```sh
   APP_ENV=test \
   PLATEN_LOCAL_E2E_CONFIRM=apply-checked-in-schema \
   DATABASE_URL="$PLATEN_TEST_DATABASE_URL" \
   python3 scripts/setup-local-e2e-schema.py
   ```

   `PLATEN_TEST_DATABASE_URL` must be supplied by the local test environment;
   do not put a production connection string in a shell history, report, or
   repository file.
4. Restart the Backend against the same disposable database and verify readiness
   before browser tests.

The helper applies `20261029_backend_durability`, `20261105_artifact_recovery`,
`20261105_account_cleanup`, and `20261105_recovery_copies` in dependency order.
It checks the database identity, verifies the normal GORM tables exist, rejects
unrecognized or populated unversioned overlaps, and checks the resulting schema
ledger. It does not fabricate migration markers or contact remote storage.

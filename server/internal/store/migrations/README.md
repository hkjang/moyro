# Database migrations

Moyro applies the embedded `*.up.sql` files in numeric order during startup.
The runner records each file's SHA-256 checksum and metadata in
`schema_migrations`, holds a PostgreSQL advisory lock for the complete run, and
uses one transaction per migration.

Rules for adding a migration:

- Name files `NNNNNN_lower_snake_case.up.sql`; never reuse a version.
- Never edit an applied migration. Add the next numbered file instead.
- Prefer expand-contract changes when a release explicitly supports mixed
  application versions. v0.1.1 uses a coordinated stop, migrate, and start
  upgrade; do not claim rolling compatibility without a mixed-version test.
- Put `-- moyro:irreversible` on the first non-empty line only when rollback
  cannot preserve data.
- Do not use statements such as `CREATE INDEX CONCURRENTLY` that cannot run in
  the migration transaction. A future explicit non-transactional mechanism is
  required before those statements are allowed.
- Update migration integration tests for fresh install, upgrade, restart, and
  data preservation behavior.

`000001_v0_1_baseline.up.sql` is the immutable schema shipped by v0.1.0. A
v0.1 database without a migration ledger safely re-executes this idempotent
baseline once and then records it.

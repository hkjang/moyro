package store

import (
	"testing"
)

func TestFlowMembershipIndexMigrationCreatesUsableUserLeadingIndex(t *testing.T) {
	db := newMigrationTestDB(t)
	ctx := testContext(t)

	if err := migrate(ctx, db, embeddedMigrations, "v0.2.1-test"); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}

	var valid, ready bool
	var columns string
	if err := db.Pool.QueryRow(ctx, `
SELECT i.indisvalid,
       i.indisready,
       array_to_string(ARRAY(
           SELECT a.attname
           FROM unnest(i.indkey) WITH ORDINALITY AS key(attnum, position)
           JOIN pg_catalog.pg_attribute AS a
             ON a.attrelid = table_class.oid AND a.attnum = key.attnum
           ORDER BY key.position
       ), ',')
FROM pg_catalog.pg_index AS i
JOIN pg_catalog.pg_class AS index_class ON index_class.oid = i.indexrelid
JOIN pg_catalog.pg_class AS table_class ON table_class.oid = i.indrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
WHERE namespace.nspname = current_schema()
  AND index_class.relname = 'channel_members_user_channel_idx'
  AND table_class.relname = 'channel_members'
`).Scan(&valid, &ready, &columns); err != nil {
		t.Fatalf("read Flow membership index metadata: %v", err)
	}
	if !valid || !ready {
		t.Fatalf("Flow membership index state = valid:%v ready:%v", valid, ready)
	}
	if columns != "user_id,channel_id" {
		t.Fatalf("Flow membership index columns = %q, want user_id,channel_id", columns)
	}

	var name string
	if err := db.Pool.QueryRow(ctx, `
SELECT name FROM schema_migrations WHERE version = 10
`).Scan(&name); err != nil {
		t.Fatalf("read Flow membership migration ledger: %v", err)
	}
	if name != "flow_membership_index" {
		t.Fatalf("migration 10 name = %q", name)
	}
}

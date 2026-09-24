package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// Execute the portable data migration against the same columns. This is not a
// PostgreSQL integration test; it verifies the data transformation, not the driver.
func TestRelayProjectionWireTimeMigrationRebasesAndInvalidatesOnlyMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
 CREATE TABLE nostr_events (id TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
 CREATE TABLE relay_projection_meta (stream TEXT, entity_key TEXT, updated_at INTEGER, source_event_id TEXT);
 CREATE TABLE projected_entities (id TEXT);
 INSERT INTO nostr_events VALUES ('ffff',100),('1111',100);
 INSERT INTO relay_projection_meta VALUES ('service','known',900,'ffff'),('service','missing',900,'missing');
 INSERT INTO projected_entities VALUES ('known'),('missing');`)
	if err != nil {
		t.Fatal(err)
	}
	up, err := migrationsFS.ReadFile("migrations/000068_relay_projection_wire_time.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(up)); err != nil {
		t.Fatal(err)
	}
	var at int
	if err = db.QueryRow(`SELECT updated_at FROM relay_projection_meta WHERE entity_key='known'`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at != 100 {
		t.Fatalf("watermark=%d, want signed timestamp 100", at)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM relay_projection_meta`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("metadata count=%d, want missing-source metadata invalidated", count)
	}
	down, err := migrationsFS.ReadFile("migrations/000068_relay_projection_wire_time.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"nostr_events", "projected_entities"} {
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("%s count=%d; migration must preserve source and entity data", table, count)
		}
	}
}

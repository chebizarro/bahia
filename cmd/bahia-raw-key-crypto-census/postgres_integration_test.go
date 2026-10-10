//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The fixture creates temporary tables on one disposable PG16 connection.
// The production census itself runs only inside read-only transactions.
func TestPG16ReadOnlySnapshotAndBound(t *testing.T) {
	dsn := os.Getenv("BAHIA_CENSUS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set disposable BAHIA_CENSUS_TEST_DATABASE_URL for PG16 integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect disposable PG16 fixture")
	}
	defer conn.Close(context.Background())
	for _, sql := range []string{
		`CREATE TEMP TABLE service_secrets (id int primary key, encryption_method text, encrypted_value bytea)`,
		`CREATE TEMP TABLE secret_versions (id int primary key, encryption_method text, encrypted_value bytea)`,
		`CREATE TEMP TABLE nostr_events (id text primary key, pubkey text, kind int, content text, tags jsonb)`,
		`INSERT INTO service_secrets VALUES (1, 'aes256gcm', decode('0102','hex')), (2, 'nip44', decode('0102','hex'))`,
		`INSERT INTO secret_versions VALUES (1, 'aes256gcm', decode('0102','hex'))`,
		`INSERT INTO nostr_events VALUES ('a', '` + testPubkey + `', 30900, '{"schema":"bahia.confidential.aead.v1","key_ref":"ock/test","key_version":"1"}', '[["t","payment-record"]]')`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal("prepare disposable PG16 fixture:", err)
		}
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var readOnly, isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		t.Fatal(err)
	}
	if readOnly != "on" || isolation != "repeatable read" {
		t.Fatalf("unexpected transaction mode %q/%q", readOnly, isolation)
	}
	r, err := census(ctx, tx, testPubkey, 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Families["service_secrets"].SQLRows != 2 || r.Families["confidential_cp_state"].Classes["ock"] != 1 {
		t.Fatalf("PG16 fixture not counted: %+v", r.Families)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	boundTx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer boundTx.Rollback(context.Background())
	partial, err := census(ctx, boundTx, testPubkey, 1)
	var bounded *boundedError
	if !errors.As(err, &bounded) || len(partial.Families) != 0 {
		t.Fatalf("bound did not fail without partial report: %+v / %v", partial, err)
	}
}

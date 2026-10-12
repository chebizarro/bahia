//go:build integration

package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// The SQL lists and deletes exactly the leaked identity's status events and
// reports archive batches holding copies. It runs on one disposable
// connection with a temporary nostr_events of the production column shape.
func TestPostgresListsAndDeletesLeakedRows(t *testing.T) {
	dsn := os.Getenv("BAHIA_LEAK_DELETE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set a disposable BAHIA_LEAK_DELETE_TEST_DATABASE_URL for the PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(context.Background())
	f := newLeakFixture(t)
	other := nostr.Generate().Public().Hex()
	for _, sql := range []string{
		`CREATE TEMP TABLE nostr_events (id text primary key, kind int not null, pubkey text not null, content text not null,
			tags jsonb not null default '[]', sig text not null, created_at timestamptz not null, archive_batch_id uuid)`,
		`INSERT INTO nostr_events VALUES
			('a1', 31410, '` + f.author.Hex() + `', '{}', '[["d","` + f.dValue + `"]]', 's', now(), NULL),
			('a2', 30360, '` + f.author.Hex() + `', '{}', '[["d","` + f.dValue + `"]]', 's', now() - interval '1 hour', '00000000-0000-0000-0000-000000000001'),
			('a3', 1, '` + f.author.Hex() + `', 'note', '[]', 's', now(), NULL),
			('b1', 31410, '` + other + `', '{}', '[["d","x"]]', 's', now(), NULL)`,
	} {
		_, err := conn.Exec(ctx, sql)
		require.NoError(t, err)
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	require.NoError(t, err)
	rows, err := listPGLeaked(ctx, tx, f.author)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	require.Len(t, rows, 2)
	require.Equal(t, "a1", rows[0].ID)
	require.Equal(t, "00000000-0000-0000-0000-000000000001", rows[1].ArchiveBatch)

	tx, err = conn.Begin(ctx)
	require.NoError(t, err)
	deleted, err := deletePGLeaked(ctx, tx, f.author)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Len(t, deleted, 2)
	var left []string
	rowsLeft, err := conn.Query(ctx, `SELECT id FROM nostr_events ORDER BY id`)
	require.NoError(t, err)
	left, err = pgx.CollectRows(rowsLeft, pgx.RowTo[string])
	require.NoError(t, err)
	require.Equal(t, []string{"a3", "b1"}, left)
}

// purge-local --db-config loads the Bahia config's db section, lists in a
// read-only transaction without --execute, and deletes with it. The test
// creates a scratch database on the disposable server.
func TestPurgeLocalPostgresCommand(t *testing.T) {
	dsn := os.Getenv("BAHIA_LEAK_DELETE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set a disposable BAHIA_LEAK_DELETE_TEST_DATABASE_URL for the PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close(context.Background())
	name := fmt.Sprintf("leakdel_%d", time.Now().UnixNano())
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })

	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	scratch := *parsed
	scratch.Path = "/" + name
	conn, err := pgx.Connect(ctx, scratch.String())
	require.NoError(t, err)
	f := newLeakFixture(t)
	for _, sql := range []string{
		`CREATE TABLE nostr_events (id text primary key, kind int not null, pubkey text not null, content text not null,
			tags jsonb not null default '[]', sig text not null, created_at timestamptz not null, archive_batch_id uuid)`,
		`INSERT INTO nostr_events VALUES
			('a1', 31411, '` + f.author.Hex() + `', '{}', '[["d","` + f.dValue + `"]]', 's', now(), NULL),
			('a2', 1, '` + f.author.Hex() + `', 'note', '[]', 's', now(), NULL)`,
	} {
		_, err := conn.Exec(ctx, sql)
		require.NoError(t, err)
	}
	require.NoError(t, conn.Close(ctx))

	password, _ := parsed.User.Password()
	port, err := strconv.Atoi(parsed.Port())
	require.NoError(t, err)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(
		"dev_mode: true\ndb:\n  host: %s\n  port: %d\n  user: %s\n  password: %s\n  name: %s\n  sslmode: disable\n",
		parsed.Hostname(), port, parsed.User.Username(), password, name)), 0o600))

	args := []string{"purge-local", "--old-pubkey", f.author.Hex(), "--db-config", configPath}
	dry := runCLI(t, testDeps(nil), args...)
	require.Equal(t, exitOK, dry.code, dry.stderr+dry.stdout)
	require.Contains(t, dry.stdout, "postgres nostr_events: 1 leaked rows")
	res := runCLI(t, testDeps(nil), append(args, "--execute")...)
	require.Equal(t, exitOK, res.code, res.stderr+res.stdout)
	require.Contains(t, res.stdout, "postgres nostr_events: deleted 1 leaked rows")
	for _, out := range []string{dry.stdout, dry.stderr, res.stdout, res.stderr} {
		require.NotContains(t, out, f.dValue)
		require.NotContains(t, out, password)
	}
	again := runCLI(t, testDeps(nil), args...)
	require.Contains(t, again.stdout, "postgres nostr_events: 0 leaked rows")
}

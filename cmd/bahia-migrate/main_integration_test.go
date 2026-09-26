//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/stretchr/testify/require"
)

func TestStatusExitCodeAndNoWrites(t *testing.T) {
	dsn := os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL")
	require.NotEmpty(t, dsn, "integration requires disposable PostgreSQL 16")
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close()
	schema := "cli_migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(cleanup, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()
	var output, errors bytes.Buffer
	require.Equal(t, 2, runAction(ctx, pool, "status", db.DownOptions{}, &output, &errors))
	require.Contains(t, output.String(), "pending\t000001_init")
	require.Empty(t, errors.String())
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists))
	require.False(t, exists)
	output.Reset()
	require.Zero(t, runAction(ctx, pool, "up", db.DownOptions{}, &output, &errors))
	output.Reset()
	require.Zero(t, runAction(ctx, pool, "status", db.DownOptions{}, &output, &errors))
	require.Contains(t, output.String(), "applied\t000070_hiveci_initiations\t")
	require.NotContains(t, "\n"+output.String(), "\npending\t")
}

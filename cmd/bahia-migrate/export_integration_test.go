//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLegacyExportPG16ProductionShapedReadOnlySnapshot(t *testing.T) {
	dsn := os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BAHIA_MIGRATE_TEST_DATABASE_URL not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close()
	schema := "cli_export_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()
	for _, statement := range []string{
		`CREATE TABLE services (id uuid PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE environments (id uuid PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE adopted_runtime_identity (id uuid PRIMARY KEY, org_id uuid NOT NULL, service_id uuid NOT NULL, environment_id uuid NOT NULL, container_id text NOT NULL)`,
		`CREATE TABLE hiveci_pipeline_policies (id uuid PRIMARY KEY, repo_coordinate text NOT NULL, workflow_path text NOT NULL, branch_pattern text, service_id uuid NOT NULL, environment_id uuid NOT NULL, enabled boolean NOT NULL)`,
		`CREATE TABLE security_scan_targets (id uuid PRIMARY KEY, target_type text NOT NULL, target_key_hash text NOT NULL, purl text, repository_url text, commit_hash text, subject jsonb, package jsonb, metadata jsonb)`,
	} {
		_, err := pool.Exec(ctx, statement)
		require.NoError(t, err)
	}
	orgID, serviceID, envID := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO services VALUES ($1,'api')`, serviceID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO environments VALUES ($1,'production')`, envID)
	require.NoError(t, err)
	for i := 0; i < 47; i++ {
		_, err = pool.Exec(ctx, `INSERT INTO adopted_runtime_identity VALUES ($1,$2,$3,$4,$5)`, uuid.New(), orgID, serviceID, envID, "container-"+uuid.NewString())
		require.NoError(t, err)
	}
	for i := 0; i < 7; i++ {
		_, err = pool.Exec(ctx, `INSERT INTO hiveci_pipeline_policies VALUES ($1,$2,'ci.yaml',NULL,$3,$4,true)`, uuid.New(), "repo:"+uuid.NewString(), serviceID, envID)
		require.NoError(t, err)
	}
	for i := 0; i < 28; i++ {
		_, err = pool.Exec(ctx, `INSERT INTO security_scan_targets VALUES ($1,'package',$2,NULL,NULL,NULL,NULL,'{"ecosystem":"Go","name":"api","version":"v1"}','{"secret":"must-not-export"}')`, uuid.New(), fmt.Sprintf("%064x", i))
		require.NoError(t, err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	var ro string
	require.NoError(t, tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only')`).Scan(&ro))
	require.Equal(t, "on", ro)
	require.ErrorContains(t, verifyExportReadOnly(ctx, tx), "SELECT-only")
	_, err = tx.Exec(ctx, `UPDATE services SET name='changed'`)
	require.Error(t, err)
	// The failed write aborts this transaction; use a new read-only snapshot.
	require.NoError(t, tx.Rollback(ctx))
	tx, err = pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	source := pgExportSource{tx: tx}
	adoptions, err := source.Adoptions(ctx)
	require.NoError(t, err)
	require.Len(t, adoptions, 47)
	policies, err := source.Policies(ctx)
	require.NoError(t, err)
	require.Len(t, policies, 7)
	targets, err := source.SecurityTargets(ctx)
	require.NoError(t, err)
	require.Len(t, targets, 28)
	index, files, err := draftLegacyExport(adoptions, policies, targets, nil)
	require.NoError(t, err)
	require.Len(t, index.Rows, 82)
	require.Len(t, files, 36)
}

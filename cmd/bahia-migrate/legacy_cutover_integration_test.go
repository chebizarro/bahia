//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestLegacyCutoverPostgresCensusBlocksSecurityPublicationLedger(t *testing.T) {
	dsn := os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BAHIA_MIGRATE_TEST_DATABASE_URL not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close()
	schema := "cli_legacy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	var output, errors bytes.Buffer
	require.Zero(t, runLegacyCutover(ctx, pool, "", false, &output, &errors), errors.String())
	var empty legacyCutoverReport
	require.NoError(t, json.Unmarshal(bytes.SplitN(output.Bytes(), []byte("\ndry-run only:"), 2)[0], &empty))
	require.True(t, empty.EligibleForEmptySeal)
	require.True(t, validEmptyCutoverMarker(empty))
	_, err = pool.Exec(ctx, `INSERT INTO security_observable_publications (observable_type, event_kind, d_tag, schema, publish_state)
        VALUES ('summary', 30900, 'legacy-ledger', 'legacy', 'pending')`)
	require.NoError(t, err)
	output.Reset()
	errors.Reset()
	require.Zero(t, runLegacyCutover(ctx, pool, "", false, &output, &errors), errors.String())
	var blocked legacyCutoverReport
	require.NoError(t, json.Unmarshal(bytes.SplitN(output.Bytes(), []byte("\ndry-run only:"), 2)[0], &blocked))
	require.False(t, blocked.EligibleForEmptySeal)
	require.Equal(t, []string{"security"}, blocked.BlockedFamilies)
	require.False(t, validEmptyCutoverMarker(blocked))
}

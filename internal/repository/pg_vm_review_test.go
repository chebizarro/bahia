//go:build integration

package repository

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
	"time"
)

func TestVMControlPlaneLocksDoNotStarveQueryPool(t *testing.T) {
	pool, _ := vmPostgres(t)
	cfg := pool.Config()
	cfg.MaxConns = 1
	small, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	defer small.Close()
	r := NewPgVirtualizationRepository(small)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	org, id := uuid.New(), uuid.New()
	require.NoError(t, r.WithOperationLock(ctx, org, id, func(ctx context.Context) error {
		require.Zero(t, small.Stat().AcquiredConns(), "long-lived lock is not a query-pool lease")
		require.ErrorIs(t, r.WithOperationLock(ctx, org, id, func(context.Context) error { t.Fatal("duplicate owner"); return nil }), ErrConflict)
		return r.WithOperationLock(ctx, org, uuid.New(), func(ctx context.Context) error {
			var one int
			return small.QueryRow(ctx, "SELECT 1").Scan(&one)
		})
	}))
	require.NoError(t, r.WithOperationLock(ctx, org, id, func(context.Context) error { return nil }))
}

func TestVMControlPlaneRollbackFencesAdmissionBeforeEmptinessCheck(t *testing.T) {
	pool, _ := vmPostgres(t)
	ctx := t.Context()
	org, resource := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name,display_name,owner_pubkey) VALUES($1,$2,'rollback test','operator')`, org, org.String())
	require.NoError(t, err)
	down, err := os.ReadFile("../db/migrations/000066_vm_control_plane.down.sql")
	require.NoError(t, err)
	guard := string(down[:strings.Index(string(down), "DROP TABLE")]) + "END $$;"
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(context.Background())) }()
	_, err = tx.Exec(ctx, guard)
	require.NoError(t, err)
	var locks int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND mode='AccessExclusiveLock' AND relation IN ('virtualization_hosts'::regclass,'vm_images'::regclass,'persistent_vm_deployments'::regclass,'execution_plane_deployments'::regclass,'vm_checkpoints'::regclass,'vm_exports'::regclass,'vm_operation_approvals'::regclass,'vm_operations'::regclass,'virtualization_capacity_reservations'::regclass,'virtualization_resource_changes'::regclass)`).Scan(&locks))
	require.Equal(t, 10, locks)
	admission, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, admission.Rollback(context.Background())) }()
	_, err = admission.Exec(ctx, "SET LOCAL lock_timeout = '100ms'")
	require.NoError(t, err)
	_, err = admission.Exec(ctx, `INSERT INTO virtualization_resource_changes(org_id,resource_kind,resource_id,generation,lifecycle_classes,change_type,document) VALUES($1,'persistent_vm',$2,1,'["persistent_vm"]','created',jsonb_build_object('schema_version',1,'id',$2::uuid::text,'org_id',$1::uuid::text,'generation',1))`, org, resource)
	var pgerr *pgconn.PgError
	require.ErrorAs(t, err, &pgerr)
	require.Equal(t, "55P03", pgerr.Code, "concurrent admission cannot cross rollback's emptiness fence")
}

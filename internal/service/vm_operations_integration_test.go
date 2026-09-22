//go:build integration

package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// This explicit gate uses the real repository and migrations, but a provider
// fake: Item C's transaction/restart proof never needs a hypervisor or Item B.
func TestPersistentVMPostgresLifecycle(t *testing.T) {
	dsn := os.Getenv("BAHIA_VM_TEST_DATABASE_URL")
	require.NotEmpty(t, dsn, "integration requires BAHIA_VM_TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "vm_item_c_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
		admin.Close()
	})
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	repo := repository.NewPgVirtualizationRepository(pool)
	s, memory, provider, v, principal := vmFixture(t)
	s.cfg.Repository = repo
	s.cfg.Now = time.Now
	_, err = pool.Exec(ctx, `INSERT INTO organizations(id,name,display_name,owner_pubkey) VALUES($1,$2,'Item C test','requester')`, v.OrgID, v.OrgID.String())
	require.NoError(t, err)
	h := memory.hosts[v.HostID]
	i := memory.images[v.ImageID]
	require.NoError(t, repo.Hosts().Create(ctx, &h))
	require.NoError(t, repo.Images().Create(ctx, &i))
	session := uuid.New()
	require.NoError(t, repo.RotateObservationSession(ctx, repository.VirtualizationResourceRef{OrgID: v.OrgID, Kind: domain.VirtualizationHostResource, ID: h.ID}, 1, uuid.Nil, session))
	require.NoError(t, repo.AcceptHostObservation(ctx, v.OrgID, h.ID, domain.VirtualizationHostObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: session, Sequence: 1, ObservedAt: time.Now().UTC()}, LifecycleClasses: h.LifecycleClasses, Availability: domain.VMObservationAvailable, Free: h.Capacity}))
	_, created := vmCreate(t, s, v, principal)
	worker, err := NewVMOperationWorker(s)
	require.NoError(t, err)
	require.NoError(t, worker.Process(ctx, v.OrgID, created.ID))
	for index, kind := range []domain.VMOperationKind{domain.VMOperationStart, domain.VMOperationGracefulStop, domain.VMOperationStart} {
		current, e := repo.Deployments().Get(ctx, v.OrgID, v.ID)
		require.NoError(t, e)
		op, e := s.RequestOperation(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: current.Generation, Kind: kind, IdempotencyKey: fmt.Sprintf("%s-%d", kind, index), Reason: "integration lifecycle"})
		require.NoError(t, e)
		require.NoError(t, worker.Process(ctx, v.OrgID, op.ID))
		done, e := repo.GetOperation(ctx, v.OrgID, op.ID)
		require.NoError(t, e)
		require.Equal(t, domain.VMOperationSucceeded, done.Phase)
	}
	current, err := repo.Deployments().Get(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	// Crash after an external reboot effect: recovery must use inspection, not
	// another reboot. The application lifecycle remains independent of admission.
	request := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: current.Generation, Kind: domain.VMOperationReboot, IdempotencyKey: "reboot", Reason: "integration restart recovery"}
	op, err := s.RequestOperation(ctx, principal, request)
	require.NoError(t, err)
	provider.execute = func(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
		provider.complete(q)
		return nil, context.DeadlineExceeded
	}
	require.Error(t, worker.Process(ctx, v.OrgID, op.ID))
	before := len(provider.executed)
	restarted, err := NewVMOperationWorker(s)
	require.NoError(t, err)
	require.NoError(t, restarted.Process(ctx, v.OrgID, op.ID))
	require.Len(t, provider.executed, before)
	provider.execute = nil
	deletion, err := s.Delete(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: current.Generation, IdempotencyKey: "delete", Reason: "integration decommission", DeleteTarget: domain.VMDeleteDeployment, AllowForceStop: true})
	require.NoError(t, err)
	approver := *principal
	approver.Subject = "approver"
	approver.PubKey = "approver"
	_, err = s.ApproveOperation(ctx, &approver, v.OrgID, deletion.ID, "approved exact target")
	require.NoError(t, err)
	require.NoError(t, restarted.Process(ctx, v.OrgID, deletion.ID))
	reservations, err := repo.ListReservations(ctx, v.OrgID, v.HostID)
	require.NoError(t, err)
	require.Empty(t, reservations)
}

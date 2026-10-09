//go:build integration

package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type integrationVMProvider struct {
	mu          sync.Mutex
	observation *domain.VMObservation
	effects     int
	crash       bool
	notify      func()
	watched     chan struct{}
}

func (p *integrationVMProvider) Inspect(_ context.Context, id domain.VMResourceIdentity) (*domain.VMObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.observation != nil {
		copy := *p.observation
		return &copy, nil
	}
	state := domain.VMRuntimeAbsent
	return &domain.VMObservation{Identity: id, LifecycleClass: id.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Drift: domain.VMDriftUnknown, GuestHealth: domain.VMGuestUnknown, Ownership: domain.VMOwnershipUnknown, Diagnostic: domain.VMDiagnostic{EvidenceDigest: "sha256:" + strings.Repeat("a", 64)}}, nil
}
func (p *integrationVMProvider) Inventory(context.Context, domain.VirtualizationHost) ([]domain.VMInventoryEntry, error) {
	return []domain.VMInventoryEntry{}, nil
}
func (p *integrationVMProvider) PlanChange(_ context.Context, q domain.VMChangeRequest) (*domain.VMChangePlan, error) {
	return &domain.VMChangePlan{LifecycleClass: q.Current.LifecycleClass, ExpectedGeneration: q.Current.Generation, CurrentConfigDigest: q.Current.ConfigDigest, DesiredConfigDigest: q.Desired.ConfigDigest, RequiredTier: domain.VMApprovalOperator}, nil
}
func (p *integrationVMProvider) Execute(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.effects++
	state := domain.VMRuntimeStopped
	if q.Operation.Kind == domain.VMOperationReboot || q.Operation.Kind == domain.VMOperationStart {
		state = domain.VMRuntimeRunning
	}
	p.observation = &domain.VMObservation{Identity: q.Deployment.Identity, LifecycleClass: domain.VMLifecyclePersistent, Availability: domain.VMObservationAvailable, RuntimeState: &state, Drift: domain.VMDriftInSync, GuestHealth: domain.VMGuestHealthy, Ownership: domain.VMOwned, AppliedImageDigest: q.Image.ManifestDigest, AppliedConfigDigest: q.Deployment.ConfigDigest, Marker: &domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: q.Deployment.Identity, AppliedGeneration: q.Deployment.Generation, OperationID: q.Operation.ID, ImageDigest: q.Image.ManifestDigest, ConfigDigest: q.Deployment.ConfigDigest}, Diagnostic: domain.VMDiagnostic{EvidenceDigest: "sha256:" + strings.Repeat("a", 64)}}
	if p.crash {
		return nil, context.DeadlineExceeded
	}
	return &domain.VMProviderResult{OperationID: q.Operation.ID, LifecycleClass: domain.VMLifecyclePersistent, Confirmed: true}, nil
}
func (p *integrationVMProvider) WatchPersistentVM(ctx context.Context, _ domain.VMResourceIdentity, notify func()) error {
	p.mu.Lock()
	p.notify = notify
	p.mu.Unlock()
	select {
	case p.watched <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// A live SQL repository must not reactivate the legacy journal-to-signature
// path. Provider/approval recovery is deliberately blocked pending cutover.
func TestVirtualizationPostgresLegacyAssemblySuspended(t *testing.T) {
	dsn := os.Getenv("BAHIA_VM_TEST_DATABASE_URL")
	require.NotEmpty(t, dsn, "integration requires a disposable PostgreSQL database")
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(ctx))

	signer := keyer.NewPlainKeySigner([32]byte{8})
	key, err := signer.GetPublicKey(ctx)
	require.NoError(t, err)
	org := uuid.New()
	store := &vmAppStore{InMemoryNostrEventRepository: repositorytest.NewInMemoryNostrEventRepository(), checkpoint: make(chan struct{}, 1)}
	publisher := &vmPublisherSpy{}
	admission := &vmAdmissionSpy{}
	v, err := NewVirtualization(VirtualizationDependencies{
		Repository: repository.NewPgVirtualizationRepository(pool), PersistentVM: admission,
		RBAC: auth.NewRBAC(vmAppMembers{org}), Bus: events.NewInProcessPublisher(zap.NewNop()),
		Store: store, Publisher: publisher, Organizations: vmAppOrganizations{org}, CanonicalAuthor: key.Hex(),
	})
	require.NoError(t, err)
	defer v.Close()
	require.Nil(t, v.Projector)
	require.False(t, v.Handlers.ProjectionReady)
	payload, err := json.Marshal(controlplane.VirtualizationMutation{OrgID: org, ID: uuid.New()})
	require.NoError(t, err)
	_, err = v.Handlers.Handle(ctx, "persistent-vm/create", controlplane.ContextVMRequest{
		Event: &nostr.Event{PubKey: key}, RPC: controlplane.ContextVMJSONRPCRequest{Params: payload},
	})
	require.ErrorIs(t, err, readmodel.ErrVirtualizationUnavailable)
	require.ErrorIs(t, v.Run(context.Background()), readmodel.ErrVirtualizationUnavailable)
	require.Equal(t, 0, admission.calls)
	require.Equal(t, 0, publisher.calls)
}

package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type vmAppRows[T any] struct {
	repository.VirtualizationResources[T]
}

func (r vmAppRows[T]) List(context.Context, uuid.UUID, int, int) ([]T, error) { return []T{}, nil }

type vmAppRepo struct {
	repository.VirtualizationRepository
	mu     sync.Mutex
	change *repository.VirtualizationResourceChange
}

func (r *vmAppRepo) Hosts() repository.VirtualizationHostRepository {
	return vmAppRows[domain.VirtualizationHost]{}
}
func (r *vmAppRepo) Deployments() repository.PersistentVMDeploymentRepository {
	return vmAppRows[domain.PersistentVMDeployment]{}
}
func (r *vmAppRepo) ExecutionPlanes() repository.ExecutionPlaneDeploymentRepository {
	return vmAppRows[domain.ExecutionPlaneDeployment]{}
}
func (r *vmAppRepo) Checkpoints() repository.VMCheckpointRepository {
	return vmAppRows[domain.VMCheckpoint]{}
}
func (r *vmAppRepo) ListChanges(_ context.Context, org uuid.UUID, after int64, _ int) ([]repository.VirtualizationResourceChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.change != nil && r.change.OrgID == org && r.change.Sequence > after {
		return []repository.VirtualizationResourceChange{*r.change}, nil
	}
	return nil, nil
}

type vmAppOrganizations struct{ org uuid.UUID }

func (o vmAppOrganizations) List(context.Context) ([]domain.Organization, error) {
	return []domain.Organization{{ID: o.org}}, nil
}

type vmAppMembers struct{ org uuid.UUID }

func (m vmAppMembers) GetMember(_ context.Context, org uuid.UUID, key string) (*domain.OrgMember, error) {
	if org != m.org {
		return nil, repository.ErrNotFound
	}
	return &domain.OrgMember{OrgID: org, Pubkey: key, Role: domain.RoleOwner}, nil
}
func (m vmAppMembers) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}

type vmAppStore struct {
	*repository.InMemoryNostrEventRepository
	checkpoint chan struct{}
}

func (s *vmAppStore) SaveMigrationCursor(ctx context.Context, c repository.NostrMigrationCursor) error {
	err := s.InMemoryNostrEventRepository.SaveMigrationCursor(ctx, c)
	if err == nil {
		select {
		case s.checkpoint <- struct{}{}:
		default:
		}
	}
	return err
}

type vmAppPublisher struct {
	store  *vmAppStore
	signer nostr.Signer
}

func (p vmAppPublisher) PublishSignedEvent(ctx context.Context, e *nostr.Event) error {
	if err := p.signer.SignEvent(ctx, e); err != nil {
		return err
	}
	tags, _ := json.Marshal(e.Tags)
	_, err := p.store.Record(ctx, &repository.NostrEventRecord{ID: e.ID.Hex(), Kind: int(e.Kind), PubKey: e.PubKey.Hex(), Content: e.Content, Tags: tags, Sig: hex.EncodeToString(e.Sig[:]), CreatedAt: e.CreatedAt.Time(), PublishState: repository.NostrPublishStatePending})
	return err
}

type vmAppService struct {
	repo *vmAppRepo
	bus  events.Publisher
}

func (s vmAppService) MutatePersistentVM(ctx context.Context, p controlplane.VirtualizationPrincipal, _ string, m controlplane.VirtualizationMutation) (controlplane.VirtualizationAdmission, error) {
	v := domain.PersistentVMDeployment{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: m.ID, OrgID: p.OrgID, Generation: 1}, LifecycleClass: domain.VMLifecyclePersistent, Provider: domain.VMProviderLibvirt, Purpose: domain.VMPurposeDesktop, DesiredPower: domain.VMDesiredRunning}
	document, _ := json.Marshal(v)
	s.repo.mu.Lock()
	s.repo.change = &repository.VirtualizationResourceChange{SchemaVersion: 1, Sequence: 1, OrgID: p.OrgID, ResourceKind: domain.PersistentVMResource, ResourceID: m.ID, Generation: 1, ChangeType: "created", Document: document, OccurredAt: time.Now()}
	s.repo.mu.Unlock()
	s.bus.Publish(ctx, events.Event{Type: events.EventVirtualizationResourceChanged, Data: events.VirtualizationChange{OrgID: p.OrgID.String(), ResourceID: m.ID.String()}})
	return controlplane.VirtualizationAdmission{ResourceID: m.ID, OperationID: uuid.New(), Generation: 1}, nil
}
func TestVirtualizationCompositionAdmissionProjectionAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		org, id := uuid.New(), uuid.New()
		repo := &vmAppRepo{}
		bus := events.NewInProcessPublisher(zap.NewNop())
		signer := keyer.NewPlainKeySigner([32]byte{2})
		key, err := signer.GetPublicKey(ctx)
		if err != nil {
			t.Fatal(err)
		}
		store := &vmAppStore{InMemoryNostrEventRepository: repository.NewInMemoryNostrEventRepository(), checkpoint: make(chan struct{}, 1)}
		v, err := NewVirtualization(VirtualizationDependencies{Repository: repo, PersistentVM: vmAppService{repo, bus}, RBAC: auth.NewRBAC(vmAppMembers{org}), Bus: bus, Store: store, Publisher: vmAppPublisher{store, signer}, Organizations: vmAppOrganizations{org}, CanonicalAuthor: key.Hex()})
		if err != nil {
			t.Fatal(err)
		}
		defer v.Close()
		done := make(chan error, 1)
		go func() { done <- v.Run(ctx) }()
		// Live bus projection can checkpoint before Run finishes startup recovery.
		// Wait for Run's steady-state cancellation wait before admitting the mutation.
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("Run exited before admission: %v", err)
		default:
		}
		payload, _ := json.Marshal(controlplane.VirtualizationMutation{OrgID: org, ID: id})
		response, err := v.Handlers.Handle(ctx, "persistent-vm/create", controlplane.ContextVMRequest{Event: &nostr.Event{PubKey: key}, RPC: controlplane.ContextVMJSONRPCRequest{Params: payload}})
		if err != nil {
			t.Fatal(err)
		}
		if response.(controlplane.VirtualizationAcknowledgment).StateDTag != "persistent-vm:"+id.String() {
			t.Fatal(response)
		}
		<-store.checkpoint
		states, err := store.ListByKind(ctx, kinds.CASControlState, 100)
		if err != nil || len(states) != 1 {
			t.Fatalf("projection %d %v", len(states), err)
		}
		if !strings.Contains(states[0].Content, id.String()) {
			t.Fatal("projection identity")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := v.Projector.Recover(context.Background(), org); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
func TestVirtualizationCompositionMissingDependenciesFailClosed(t *testing.T) {
	v, err := NewVirtualization(VirtualizationDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if v.Handlers.ProjectionReady || v.Projector != nil {
		t.Fatal("advertised unavailable projection")
	}
	if err := v.Run(context.Background()); !errors.Is(err, readmodel.ErrVirtualizationUnavailable) {
		t.Fatal(err)
	}
	if _, err := v.Query.Get(context.Background(), uuid.New(), domain.PersistentVMResource, uuid.New()); !errors.Is(err, readmodel.ErrVirtualizationUnavailable) {
		t.Fatal(err)
	}
}

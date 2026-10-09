package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"go.uber.org/zap"
)

type vmAppRows[T any] struct {
	repository.VirtualizationResources[T]
}

func (r vmAppRows[T]) List(context.Context, uuid.UUID, int, int) ([]T, error) { return []T{}, nil }

type vmAppRepo struct {
	repository.VirtualizationRepository
	mu        sync.Mutex
	change    *repository.VirtualizationResourceChange
	listCalls int
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
	r.listCalls++
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
	*repositorytest.InMemoryNostrEventRepository
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

type vmAdmissionSpy struct{ calls int }

func (s *vmAdmissionSpy) MutatePersistentVM(context.Context, controlplane.VirtualizationPrincipal, string, controlplane.VirtualizationMutation) (controlplane.VirtualizationAdmission, error) {
	s.calls++
	return controlplane.VirtualizationAdmission{}, nil
}

type vmPublisherSpy struct{ calls int }

func (s *vmPublisherSpy) PublishSignedEvent(context.Context, *nostr.Event) error {
	s.calls++
	return nil
}

func TestVirtualizationIncompleteCanonicalAssemblyRejectsSQLBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	org, id := uuid.New(), uuid.New()
	signer := keyer.NewPlainKeySigner([32]byte{3})
	key, err := signer.GetPublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		journal   bool
		store     bool
		publisher bool
	}{
		{name: "postgres absent", store: true, publisher: true},
		{name: "postgres divergent", journal: true, publisher: true},
		{name: "local outbox interrupted", journal: true, store: true},
		{name: "all legacy dependencies supplied", journal: true, store: true, publisher: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &vmAppRepo{}
			if tc.journal {
				repo.change = &repository.VirtualizationResourceChange{OrgID: org, Sequence: 1, ResourceID: id}
			}
			var index repository.VirtualizationRepository
			if tc.journal {
				index = repo
			}
			var store readmodel.VirtualizationProjectionStore
			if tc.store {
				store = &vmAppStore{InMemoryNostrEventRepository: repositorytest.NewInMemoryNostrEventRepository(), checkpoint: make(chan struct{}, 1)}
			}
			publisher := &vmPublisherSpy{}
			var writer readmodel.VirtualizationSignedPublisher
			if tc.publisher {
				writer = publisher
			}
			admission := &vmAdmissionSpy{}
			bus := events.NewInProcessPublisher(zap.NewNop())
			v, err := NewVirtualization(VirtualizationDependencies{
				Repository: index, PersistentVM: admission, RBAC: auth.NewRBAC(vmAppMembers{org}),
				Bus: bus, Store: store, Publisher: writer,
				Organizations: vmAppOrganizations{org}, CanonicalAuthor: key.Hex(),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if v.Projector != nil || v.Handlers.ProjectionReady {
				t.Fatal("legacy SQL assembly became available")
			}
			payload, err := json.Marshal(controlplane.VirtualizationMutation{OrgID: org, ID: id})
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Handlers.Handle(ctx, "persistent-vm/create", controlplane.ContextVMRequest{
				Event: &nostr.Event{PubKey: key}, RPC: controlplane.ContextVMJSONRPCRequest{Params: payload},
			})
			if !errors.Is(err, readmodel.ErrVirtualizationUnavailable) {
				t.Fatalf("mutation must fail closed: %v", err)
			}
			if _, err := v.Query.Get(ctx, org, domain.PersistentVMResource, id); !errors.Is(err, readmodel.ErrVirtualizationUnavailable) {
				t.Fatalf("SQL read must fail closed: %v", err)
			}
			if err := v.Run(ctx); !errors.Is(err, readmodel.ErrVirtualizationUnavailable) {
				t.Fatalf("SQL recovery must not run: %v", err)
			}
			bus.Publish(ctx, events.Event{Type: events.EventVirtualizationResourceChanged,
				Data: events.VirtualizationChange{OrgID: org.String(), ResourceID: id.String()}})
			if admission.calls != 0 || publisher.calls != 0 || repo.listCalls != 0 {
				t.Fatalf("SQL admission=%d publications=%d journal reads=%d", admission.calls, publisher.calls, repo.listCalls)
			}
		})
	}
}

func TestVirtualizationSuspensionIsVisibleButDoesNotGateCoreReadiness(t *testing.T) {
	provider := NewHealthProvider(nil, nil)
	registerVirtualizationSuspendedHealth(provider)
	snapshot := provider.Readiness()
	if !snapshot.Ready || snapshot.Status != SnapshotStatusDegraded {
		t.Fatalf("suspension readiness=%v status=%s", snapshot.Ready, snapshot.Status)
	}
	found := false
	for _, check := range snapshot.Checks {
		if check.Name == "virtualization_canonical_recovery" {
			found = true
			if check.Status != HealthStatusWarn || !strings.Contains(check.Message, "signed-intent recovery") {
				t.Fatalf("unexpected virtualization check: %#v", check)
			}
		}
	}
	if !found {
		t.Fatal("missing virtualization suspension warning")
	}
}

func TestVirtualizationSuspensionUsesConfigurationWhenPostgresUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       config.Config
		available bool
		want      bool
	}{
		{name: "postgres configured but absent", cfg: config.Config{DB: config.DBConfig{Host: "db.internal", Name: "bahia"}}, want: true},
		{name: "virtualization configured without postgres", cfg: config.Config{Virtualization: config.VirtualizationConfig{PersistentVM: config.PersistentVMConfig{Enabled: true}}}, want: true},
		{name: "postgres available", available: true, want: true},
		{name: "no postgres or virtualization", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := virtualizationSuspensionRelevant(&tc.cfg, tc.available); got != tc.want {
				t.Fatalf("warning relevant = %t, want %t", got, tc.want)
			}
		})
	}
}

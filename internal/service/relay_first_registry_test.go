package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

func TestRelayFirstRegistryCreateServiceFailsWhenRelayPublishFails(t *testing.T) {
	ctx := context.Background()
	calls := []string{}
	serviceRepo := &relayFirstServiceRepo{calls: &calls}
	delegate := NewRegistryService(serviceRepo, nil, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	publisher := &relayFirstCapturePublisher{err: errors.New("relay rejected event"), calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, zap.NewNop())

	err := registry.CreateService(ctx, &domain.Service{ID: uuid.New(), Name: "api"})
	if err == nil {
		t.Fatal("expected relay publish failure")
	}
	if len(serviceRepo.services) != 0 {
		t.Fatalf("service repo was written despite relay failure: got %d writes", len(serviceRepo.services))
	}
	if len(calls) != 1 || calls[0] != "publish" {
		t.Fatalf("unexpected call order: %v", calls)
	}
}

func TestRelayFirstRegistryCreateServicePublishesBeforeDatabaseWrite(t *testing.T) {
	ctx := context.Background()
	calls := []string{}
	serviceRepo := &relayFirstServiceRepo{calls: &calls}
	delegate := NewRegistryService(serviceRepo, nil, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	publisher := &relayFirstCapturePublisher{calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, zap.NewNop())
	svc := &domain.Service{ID: uuid.New(), Name: "api", RepoURL: " https://git.example/acme/api.git "}

	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("CreateService returned error: %v", err)
	}
	if _, ok := serviceRepo.services[svc.ID]; !ok {
		t.Fatal("service repo was not written after relay publish")
	}
	wantOrder := []string{"publish", "service.create"}
	if fmt.Sprint(calls) != fmt.Sprint(wantOrder) {
		t.Fatalf("unexpected call order: got %v want %v", calls, wantOrder)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected one published record, got %d", len(publisher.events))
	}
	published := publisher.events[0]
	if published.service == nil || published.id() != svc.ID || published.deleted {
		t.Fatalf("published record = %+v, want live service %s", published, svc.ID)
	}
	if published.service.RuntimeType != domain.RuntimeTypeDocker || published.service.DefaultBranch != "main" {
		t.Fatalf("service defaults were not applied before publish: runtime=%q branch=%q", published.service.RuntimeType, published.service.DefaultBranch)
	}
	// The record carries the service as readers (and so the projector) see
	// the cached row, not the raw write intent (bahia-irsry.41).
	if repo := published.service.Repository; repo == nil || repo.Source != "manual" || repo.CloneURL != "https://git.example/acme/api.git" {
		t.Fatalf("published repository = %+v, want the read-normalized manual repository", repo)
	}
}

func TestRelayFirstRegistryCompleteSetConflictDoesNotPublishCanonicalState(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	if err := envs.Create(ctx, env); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	staleRevision := env.UpdatedAt
	envs.environments[env.ID].UpdatedAt = staleRevision.Add(time.Second)

	delegate := newEnvironmentMutationRegistry(envs, units, &capturePublisher{})
	calls := []string{}
	publisher := &relayFirstCapturePublisher{calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, zap.NewNop())
	requested := []*domain.DeploymentUnit{{
		Key:           domain.DefaultDeploymentUnitKey,
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}}

	err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, env, requested, staleRevision)
	if !errors.Is(err, repository.ErrStaleRevision) {
		t.Fatalf("error = %v, want stale revision", err)
	}
	if len(publisher.events) != 0 || len(calls) != 0 {
		t.Fatalf("stale complete-set update published canonical state: events=%d calls=%v", len(publisher.events), calls)
	}
}

func TestRelayFirstRegistryCompleteSetPublishesPersistedRevision(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	if err := envs.Create(ctx, env); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	delegate := newEnvironmentMutationRegistry(envs, units, &capturePublisher{})
	publisher := &relayFirstCapturePublisher{}
	registry := NewRelayFirstRegistry(delegate, publisher, zap.NewNop())
	requested := []*domain.DeploymentUnit{{
		Key:           domain.DefaultDeploymentUnitKey,
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}}

	if err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, env, requested, env.UpdatedAt); err != nil {
		t.Fatalf("UpdateEnvironmentWithDeploymentUnits: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
	publishedRevision := publisher.events[0].environment.UpdatedAt
	persisted, err := envs.GetByID(ctx, env.ID)
	if err != nil || persisted == nil {
		t.Fatalf("load persisted environment: env=%#v err=%v", persisted, err)
	}
	if !publishedRevision.Equal(persisted.UpdatedAt) {
		t.Fatalf("published revision %s != persisted revision %s", publishedRevision, persisted.UpdatedAt)
	}

	requested[0].ReconcileMode = domain.ReconcileModeAutoApply
	if err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, persisted, requested, publishedRevision); err != nil {
		t.Fatalf("second complete-set update using published revision: %v", err)
	}
}

func TestRelayFirstRegistryCompleteSetPublishFailureRollsBackStagedRevision(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	if err := envs.Create(ctx, env); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	originalRevision := env.UpdatedAt

	delegate := newEnvironmentMutationRegistry(envs, units, &capturePublisher{})
	publisher := &relayFirstCapturePublisher{err: errors.New("relay rejected event")}
	registry := NewRelayFirstRegistry(delegate, publisher, zap.NewNop())
	requested := []*domain.DeploymentUnit{{
		Key:           domain.DefaultDeploymentUnitKey,
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}}

	if err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, env, requested, originalRevision); err == nil {
		t.Fatal("expected relay publication failure")
	}
	persisted, err := envs.GetByID(ctx, env.ID)
	if err != nil || persisted == nil {
		t.Fatalf("load persisted environment: env=%#v err=%v", persisted, err)
	}
	if !persisted.UpdatedAt.Equal(originalRevision) {
		t.Fatalf("revision changed after rollback: got %s want %s", persisted.UpdatedAt, originalRevision)
	}
	if persistedUnits, _ := units.ListByEnvironment(ctx, env.ID); len(persistedUnits) != 0 {
		t.Fatalf("units persisted after publication failure: %#v", persistedUnits)
	}
}

func TestRelayFirstRegistryCreateEnvironmentRequiresRelayAcceptance(t *testing.T) {
	ctx := context.Background()
	calls := []string{}
	envRepo := &relayFirstEnvironmentRepo{calls: &calls}
	delegate := NewRegistryService(nil, envRepo, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	publisher := &relayFirstCapturePublisher{err: errors.New("no relay accepted the event"), calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, zap.NewNop())

	err := registry.CreateEnvironment(ctx, &domain.Environment{ID: uuid.New(), Name: "prod"})
	if err == nil {
		t.Fatal("expected error when no relay accepts the event")
	}
	if len(envRepo.environments) != 0 {
		t.Fatalf("environment repo was written despite no relay acceptance: got %d writes", len(envRepo.environments))
	}
	if len(calls) != 1 || calls[0] != "publish" {
		t.Fatalf("unexpected call order: %v", calls)
	}
}

func TestRelayFirstRegistryUpdateEnvironmentPublishesBeforeDatabaseWrite(t *testing.T) {
	ctx := context.Background()
	calls := []string{}
	envRepo := &relayFirstEnvironmentRepo{calls: &calls}
	delegate := NewRegistryService(nil, envRepo, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	publisher := &relayFirstCapturePublisher{calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, zap.NewNop())
	env := &domain.Environment{ID: uuid.New(), Name: "prod", DeployStrategy: domain.DeployStrategyCanary, Protected: true}

	if err := registry.UpdateEnvironment(ctx, env); err != nil {
		t.Fatalf("UpdateEnvironment returned error: %v", err)
	}
	wantOrder := []string{"publish", "environment.update"}
	if fmt.Sprint(calls) != fmt.Sprint(wantOrder) {
		t.Fatalf("unexpected call order: got %v want %v", calls, wantOrder)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected one published record, got %d", len(publisher.events))
	}
	if published := publisher.events[0]; published.environment == nil || published.id() != env.ID || published.deleted {
		t.Fatalf("published record = %+v, want live environment %s", published, env.ID)
	}
}

// relayFirstPublication is one record the registry handed to its
// RelayFirstStatePublisher. Its wire shape is the projector builder's, which
// internal/adapters/nostr tests against the projector itself.
type relayFirstPublication struct {
	service     *domain.Service
	environment *domain.Environment
	deleted     bool
}

func (p relayFirstPublication) id() uuid.UUID {
	if p.service != nil {
		return p.service.ID
	}
	if p.environment != nil {
		return p.environment.ID
	}
	return uuid.Nil
}

type relayFirstCapturePublisher struct {
	mu     sync.Mutex
	err    error
	events []relayFirstPublication
	calls  *[]string
}

func (p *relayFirstCapturePublisher) record(pub relayFirstPublication) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != nil {
		*p.calls = append(*p.calls, "publish")
	}
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, pub)
	return nil
}

func (p *relayFirstCapturePublisher) PublishServiceRegistry(_ context.Context, svc *domain.Service, deleted bool) error {
	snapshot := *svc
	return p.record(relayFirstPublication{service: &snapshot, deleted: deleted})
}

func (p *relayFirstCapturePublisher) PublishEnvironmentRegistry(_ context.Context, env *domain.Environment, deleted bool) error {
	snapshot := *env
	return p.record(relayFirstPublication{environment: &snapshot, deleted: deleted})
}

type relayFirstServiceRepo struct {
	services map[uuid.UUID]*domain.Service
	calls    *[]string
}

func (r *relayFirstServiceRepo) ensure() {
	if r.services == nil {
		r.services = map[uuid.UUID]*domain.Service{}
	}
}
func (r *relayFirstServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	r.ensure()
	if r.calls != nil {
		*r.calls = append(*r.calls, "service.create")
	}
	copy := *svc
	r.services[svc.ID] = &copy
	return nil
}
func (r *relayFirstServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	r.ensure()
	return r.services[id], nil
}
func (r *relayFirstServiceRepo) GetByName(_ context.Context, name string) (*domain.Service, error) {
	r.ensure()
	for _, svc := range r.services {
		if svc.Name == name {
			return svc, nil
		}
	}
	return nil, nil
}
func (r *relayFirstServiceRepo) List(context.Context) ([]domain.Service, error) {
	r.ensure()
	out := make([]domain.Service, 0, len(r.services))
	for _, svc := range r.services {
		out = append(out, *svc)
	}
	return out, nil
}
func (r *relayFirstServiceRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Service, error) {
	r.ensure()
	out := []domain.Service{}
	for _, svc := range r.services {
		if svc.OrgID == orgID {
			out = append(out, *svc)
		}
	}
	return out, nil
}
func (r *relayFirstServiceRepo) Update(_ context.Context, svc *domain.Service) error {
	r.ensure()
	if r.calls != nil {
		*r.calls = append(*r.calls, "service.update")
	}
	copy := *svc
	r.services[svc.ID] = &copy
	return nil
}
func (r *relayFirstServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.ensure()
	if r.calls != nil {
		*r.calls = append(*r.calls, "service.delete")
	}
	delete(r.services, id)
	return nil
}

type relayFirstEnvironmentRepo struct {
	environments map[uuid.UUID]*domain.Environment
	calls        *[]string
}

func (r *relayFirstEnvironmentRepo) ensure() {
	if r.environments == nil {
		r.environments = map[uuid.UUID]*domain.Environment{}
	}
}
func (r *relayFirstEnvironmentRepo) Create(_ context.Context, env *domain.Environment) error {
	r.ensure()
	if r.calls != nil {
		*r.calls = append(*r.calls, "environment.create")
	}
	copy := *env
	r.environments[env.ID] = &copy
	return nil
}
func (r *relayFirstEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	r.ensure()
	return r.environments[id], nil
}
func (r *relayFirstEnvironmentRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	r.ensure()
	for _, env := range r.environments {
		if env.Name == name {
			return env, nil
		}
	}
	return nil, nil
}
func (r *relayFirstEnvironmentRepo) List(context.Context) ([]domain.Environment, error) {
	r.ensure()
	out := make([]domain.Environment, 0, len(r.environments))
	for _, env := range r.environments {
		out = append(out, *env)
	}
	return out, nil
}
func (r *relayFirstEnvironmentRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	r.ensure()
	out := []domain.Environment{}
	for _, env := range r.environments {
		if env.OrgID == orgID {
			out = append(out, *env)
		}
	}
	return out, nil
}
func (r *relayFirstEnvironmentRepo) Update(_ context.Context, env *domain.Environment) error {
	r.ensure()
	if r.calls != nil {
		*r.calls = append(*r.calls, "environment.update")
	}
	copy := *env
	r.environments[env.ID] = &copy
	return nil
}
func (r *relayFirstEnvironmentRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.ensure()
	if r.calls != nil {
		*r.calls = append(*r.calls, "environment.delete")
	}
	delete(r.environments, id)
	return nil
}

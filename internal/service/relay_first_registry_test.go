package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
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
	registry := NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop())

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
	publisher := &relayFirstCapturePublisher{published: 1, calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop())
	svc := &domain.Service{ID: uuid.New(), Name: "api"}

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
		t.Fatalf("expected one published event, got %d", len(publisher.events))
	}
	ev := publisher.events[0]
	if ev.Kind != gonostr.Kind(relayFirstCanonicalStateKind) {
		t.Fatalf("published kind = %d, want %d", ev.Kind, relayFirstCanonicalStateKind)
	}
	assertRelayFirstTag(t, ev.Tags, "domain", "service")
	assertRelayFirstTag(t, ev.Tags, "entity", "registry")
	assertRelayFirstTag(t, ev.Tags, "schema", relayFirstStateSchema)
	if ev.ID == (gonostr.ID{}) || ev.Sig == ([64]byte{}) || ev.PubKey == (gonostr.PubKey{}) {
		t.Fatalf("published event was not signed: id=%q sig=%q pubkey=%q", ev.ID.Hex(), gonostr.HexEncodeToString(ev.Sig[:]), ev.PubKey.Hex())
	}
	if svc.RuntimeType != domain.RuntimeTypeDocker || svc.DefaultBranch != "main" {
		t.Fatalf("service defaults were not applied before publish: runtime=%q branch=%q", svc.RuntimeType, svc.DefaultBranch)
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
	publisher := &relayFirstCapturePublisher{published: 1, calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop())
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

func TestRelayFirstRegistryCreateEnvironmentRequiresRelayAcceptance(t *testing.T) {
	ctx := context.Background()
	calls := []string{}
	envRepo := &relayFirstEnvironmentRepo{calls: &calls}
	delegate := NewRegistryService(nil, envRepo, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	publisher := &relayFirstCapturePublisher{published: 0, calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop())

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
	publisher := &relayFirstCapturePublisher{published: 1, calls: &calls}
	registry := NewRelayFirstRegistry(delegate, publisher, relayFirstTestSigner(t), zap.NewNop())
	env := &domain.Environment{ID: uuid.New(), Name: "prod", DeployStrategy: domain.DeployStrategyCanary, Protected: true}

	if err := registry.UpdateEnvironment(ctx, env); err != nil {
		t.Fatalf("UpdateEnvironment returned error: %v", err)
	}
	wantOrder := []string{"publish", "environment.update"}
	if fmt.Sprint(calls) != fmt.Sprint(wantOrder) {
		t.Fatalf("unexpected call order: got %v want %v", calls, wantOrder)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected one published event, got %d", len(publisher.events))
	}
	if publisher.events[0].Kind != gonostr.Kind(relayFirstCanonicalStateKind) {
		t.Fatalf("published kind = %d, want %d", publisher.events[0].Kind, relayFirstCanonicalStateKind)
	}
	assertRelayFirstTag(t, publisher.events[0].Tags, "domain", "environment")
	assertRelayFirstTag(t, publisher.events[0].Tags, "entity", "registry")
	assertRelayFirstTag(t, publisher.events[0].Tags, "schema", relayFirstStateSchema)
}

func assertRelayFirstTag(t *testing.T, tags gonostr.Tags, name, value string) {
	t.Helper()
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return
		}
	}
	t.Fatalf("missing tag %s=%s in %#v", name, value, tags)
}

func relayFirstTestSigner(t *testing.T) RelayFirstSigner {
	t.Helper()
	return RelayFirstPrivateKeySigner(gonostr.Generate().Hex())
}

type relayFirstCapturePublisher struct {
	published int
	err       error
	events    []gonostr.Event
	calls     *[]string
}

func (p *relayFirstCapturePublisher) Publish(_ context.Context, ev gonostr.Event) (int, error) {
	if p.calls != nil {
		*p.calls = append(*p.calls, "publish")
	}
	p.events = append(p.events, ev)
	return p.published, p.err
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

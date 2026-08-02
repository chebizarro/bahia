package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type environmentMutationEnvRepo struct {
	environments map[uuid.UUID]*domain.Environment
}

func newEnvironmentMutationEnvRepo() *environmentMutationEnvRepo {
	return &environmentMutationEnvRepo{environments: map[uuid.UUID]*domain.Environment{}}
}

func (r *environmentMutationEnvRepo) Create(_ context.Context, env *domain.Environment) error {
	if _, exists := r.environments[env.ID]; exists {
		return repository.ErrConflict
	}
	now := time.Now().UTC()
	if env.CreatedAt.IsZero() {
		env.CreatedAt = now
	}
	if env.UpdatedAt.IsZero() {
		env.UpdatedAt = now
	}
	copy := *env
	r.environments[env.ID] = &copy
	return nil
}

func (r *environmentMutationEnvRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	env := r.environments[id]
	if env == nil {
		return nil, nil
	}
	copy := *env
	return &copy, nil
}

func (r *environmentMutationEnvRepo) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.GetByID(ctx, id)
}

func (r *environmentMutationEnvRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for _, env := range r.environments {
		if env.Name == name {
			copy := *env
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *environmentMutationEnvRepo) List(context.Context) ([]domain.Environment, error) {
	return r.list(uuid.Nil), nil
}

func (r *environmentMutationEnvRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	return r.list(orgID), nil
}

func (r *environmentMutationEnvRepo) list(orgID uuid.UUID) []domain.Environment {
	result := make([]domain.Environment, 0, len(r.environments))
	for _, env := range r.environments {
		if orgID == uuid.Nil || env.OrgID == orgID {
			result = append(result, *env)
		}
	}
	return result
}

func (r *environmentMutationEnvRepo) Update(_ context.Context, env *domain.Environment) error {
	if r.environments[env.ID] == nil {
		return repository.ErrNotFound
	}
	env.UpdatedAt = time.Now().UTC()
	copy := *env
	r.environments[env.ID] = &copy
	return nil
}

func (r *environmentMutationEnvRepo) Delete(_ context.Context, id uuid.UUID) error {
	if r.environments[id] == nil {
		return repository.ErrNotFound
	}
	delete(r.environments, id)
	return nil
}

func cloneEnvironmentMutationEnvRepo(source *environmentMutationEnvRepo) *environmentMutationEnvRepo {
	clone := newEnvironmentMutationEnvRepo()
	for id, env := range source.environments {
		copy := *env
		clone.environments[id] = &copy
	}
	return clone
}

type environmentMutationUnitRepo struct {
	units         map[uuid.UUID]*domain.DeploymentUnit
	referenced    map[uuid.UUID]bool
	failCreateKey string
}

func newEnvironmentMutationUnitRepo() *environmentMutationUnitRepo {
	return &environmentMutationUnitRepo{
		units:      map[uuid.UUID]*domain.DeploymentUnit{},
		referenced: map[uuid.UUID]bool{},
	}
}

func (r *environmentMutationUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	if unit.Key == r.failCreateKey {
		return fmt.Errorf("injected create failure")
	}
	if unit.ID == uuid.Nil {
		unit.ID = uuid.New()
	}
	for _, existing := range r.units {
		if existing.EnvironmentID == unit.EnvironmentID && existing.Key == unit.Key {
			return repository.ErrConflict
		}
	}
	copy := *unit
	copy.Implicit = false
	r.units[unit.ID] = &copy
	return nil
}

func (r *environmentMutationUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	unit := r.units[id]
	if unit == nil {
		return nil, nil
	}
	copy := *unit
	return &copy, nil
}

func (r *environmentMutationUnitRepo) GetByEnvironmentKey(_ context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	for _, unit := range r.units {
		if unit.EnvironmentID == environmentID && unit.Key == key {
			copy := *unit
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *environmentMutationUnitRepo) ListByEnvironment(_ context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	result := make([]domain.DeploymentUnit, 0)
	for _, unit := range r.units {
		if unit.EnvironmentID == environmentID {
			result = append(result, *unit)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func (r *environmentMutationUnitRepo) ListByEnvironmentForUpdate(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	return r.ListByEnvironment(ctx, environmentID)
}

func (r *environmentMutationUnitRepo) ResolveDefault(ctx context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	envCopy := *env
	domain.NormalizeEnvironmentTargeting(&envCopy)
	units, err := r.ListByEnvironment(ctx, envCopy.ID)
	if err != nil {
		return nil, err
	}
	for i := range units {
		if units[i].Key == envCopy.Targeting.DefaultUnitKey {
			unit := units[i]
			return &unit, nil
		}
	}
	if len(units) > 0 || envCopy.Targeting.DefaultUnitKey != domain.DefaultDeploymentUnitKey {
		return nil, repository.ErrConflict
	}
	return domain.NewImplicitDefaultDeploymentUnit(&envCopy)
}

func (r *environmentMutationUnitRepo) Update(_ context.Context, unit *domain.DeploymentUnit) error {
	if r.units[unit.ID] == nil {
		return repository.ErrNotFound
	}
	copy := *unit
	copy.Implicit = false
	r.units[unit.ID] = &copy
	return nil
}

func (r *environmentMutationUnitRepo) DeleteIfUnreferenced(_ context.Context, id uuid.UUID) error {
	if r.units[id] == nil {
		return repository.ErrNotFound
	}
	if r.referenced[id] {
		return repository.ErrConflict
	}
	delete(r.units, id)
	return nil
}

func cloneEnvironmentMutationUnitRepo(source *environmentMutationUnitRepo) *environmentMutationUnitRepo {
	clone := newEnvironmentMutationUnitRepo()
	clone.failCreateKey = source.failCreateKey
	for id, unit := range source.units {
		copy := *unit
		clone.units[id] = &copy
	}
	for id, referenced := range source.referenced {
		clone.referenced[id] = referenced
	}
	return clone
}

type environmentMutationTxExecutor struct {
	environments *environmentMutationEnvRepo
	units        *environmentMutationUnitRepo
}

func (e *environmentMutationTxExecutor) WithinTx(ctx context.Context, fn func(repos repository.TxRepos) error) error {
	txEnvironments := cloneEnvironmentMutationEnvRepo(e.environments)
	txUnits := cloneEnvironmentMutationUnitRepo(e.units)
	if err := fn(repository.TxRepos{Environments: txEnvironments, DeploymentUnits: txUnits}); err != nil {
		return err
	}
	e.environments.environments = txEnvironments.environments
	e.units.units = txUnits.units
	e.units.referenced = txUnits.referenced
	return nil
}

func newEnvironmentMutationRegistry(envs *environmentMutationEnvRepo, units *environmentMutationUnitRepo, publisher *capturePublisher) *RegistryService {
	executor := &environmentMutationTxExecutor{environments: envs, units: units}
	return NewRegistryService(
		nil, envs, nil, nil, nil, nil, nil, nil,
		nil, publisher, zap.NewNop(),
		WithRegistryTxExecutor(executor),
	)
}

func TestRegistryServiceCreateEnvironmentWithDeploymentUnitsCommitsAtomically(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	publisher := &capturePublisher{}
	registry := newEnvironmentMutationRegistry(envs, units, publisher)
	env := &domain.Environment{
		ID:   uuid.New(),
		Name: "max",
		Targeting: domain.EnvironmentTargeting{
			DefaultUnitKey:       "max",
			DefaultReconcileMode: domain.ReconcileModeAutoApply,
		},
	}
	requested := []*domain.DeploymentUnit{{
		Key:           "max",
		RuntimeType:   domain.RuntimeTypeCompose,
		EndpointRef:   "max",
		ComposeDir:    "/srv/bahia/gastown",
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}}

	if err := registry.CreateEnvironmentWithDeploymentUnits(ctx, env, requested); err != nil {
		t.Fatalf("CreateEnvironmentWithDeploymentUnits: %v", err)
	}
	if envs.environments[env.ID] == nil {
		t.Fatalf("environment was not committed")
	}
	persisted, _ := units.ListByEnvironment(ctx, env.ID)
	if len(persisted) != 1 || persisted[0].ID == uuid.Nil || persisted[0].ReconcileMode != domain.ReconcileModeAutoApply {
		t.Fatalf("unexpected persisted deployment units: %#v", persisted)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1 after commit", len(publisher.events))
	}
}

func TestRegistryServiceCreateEnvironmentWithDeploymentUnitsRollsBackOnUnitFailure(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	units.failCreateKey = "broken"
	publisher := &capturePublisher{}
	registry := newEnvironmentMutationRegistry(envs, units, publisher)
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	requested := []*domain.DeploymentUnit{
		{Key: domain.DefaultDeploymentUnitKey, RuntimeType: domain.RuntimeTypeDocker},
		{Key: "broken", RuntimeType: domain.RuntimeTypeCompose},
	}

	if err := registry.CreateEnvironmentWithDeploymentUnits(ctx, env, requested); err == nil {
		t.Fatalf("expected injected unit failure")
	}
	if envs.environments[env.ID] != nil {
		t.Fatalf("environment remained after transaction rollback")
	}
	if len(units.units) != 0 {
		t.Fatalf("deployment units remained after transaction rollback: %#v", units.units)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("published events before commit: %#v", publisher.events)
	}
}

func TestRegistryServiceUpdateEnvironmentWithDeploymentUnitsTransitionsImplicitToExplicit(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	publisher := &capturePublisher{}
	registry := newEnvironmentMutationRegistry(envs, units, publisher)
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	if err := envs.Create(ctx, env); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	resolved, err := units.ResolveDefault(ctx, env)
	if err != nil || !resolved.Implicit {
		t.Fatalf("expected implicit default before update, unit=%#v err=%v", resolved, err)
	}

	requested := []*domain.DeploymentUnit{{Key: domain.DefaultDeploymentUnitKey, RuntimeType: domain.RuntimeTypeCompose}}
	if err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, env, requested, env.UpdatedAt); err != nil {
		t.Fatalf("UpdateEnvironmentWithDeploymentUnits: %v", err)
	}
	persisted, _ := units.ListByEnvironment(ctx, env.ID)
	if len(persisted) != 1 || persisted[0].Implicit || persisted[0].ID == uuid.Nil {
		t.Fatalf("implicit-to-explicit transition did not persist identity: %#v", persisted)
	}
}

func TestRegistryServiceUpdateEnvironmentWithDeploymentUnitsPreservesExistingIdentity(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	publisher := &capturePublisher{}
	registry := newEnvironmentMutationRegistry(envs, units, publisher)
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	if err := envs.Create(ctx, env); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	existingID := uuid.New()
	existing := &domain.DeploymentUnit{
		ID:            existingID,
		EnvironmentID: env.ID,
		Key:           domain.DefaultDeploymentUnitKey,
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	if err := units.Create(ctx, existing); err != nil {
		t.Fatalf("seed deployment unit: %v", err)
	}

	requested := []*domain.DeploymentUnit{{
		Key:           domain.DefaultDeploymentUnitKey,
		RuntimeType:   domain.RuntimeTypeCompose,
		EndpointRef:   "max",
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}}
	if err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, env, requested, env.UpdatedAt); err != nil {
		t.Fatalf("UpdateEnvironmentWithDeploymentUnits: %v", err)
	}
	if requested[0].ID != existingID {
		t.Fatalf("deployment unit identity changed: got %s want %s", requested[0].ID, existingID)
	}
	persisted, _ := units.GetByID(ctx, existingID)
	if persisted == nil || persisted.RuntimeType != domain.RuntimeTypeCompose || persisted.EndpointRef != "max" {
		t.Fatalf("existing deployment unit was not updated in place: %#v", persisted)
	}
}

func TestRegistryServiceUpdateEnvironmentWithDeploymentUnitsRejectsStaleRevisionBeforeMutation(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	publisher := &capturePublisher{}
	registry := newEnvironmentMutationRegistry(envs, units, publisher)
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	if err := envs.Create(ctx, env); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	existing := &domain.DeploymentUnit{
		ID:            uuid.New(),
		EnvironmentID: env.ID,
		Key:           domain.DefaultDeploymentUnitKey,
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	if err := units.Create(ctx, existing); err != nil {
		t.Fatalf("seed unit: %v", err)
	}

	staleRevision := env.UpdatedAt
	envs.environments[env.ID].UpdatedAt = staleRevision.Add(time.Second)
	updated := *env
	updated.Name = "production"
	requested := []*domain.DeploymentUnit{{
		Key:           domain.DefaultDeploymentUnitKey,
		RuntimeType:   domain.RuntimeTypeCompose,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}}

	err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, &updated, requested, staleRevision)
	if !errors.Is(err, repository.ErrConflict) || !errors.Is(err, repository.ErrStaleRevision) {
		t.Fatalf("error = %v, want revision conflict", err)
	}
	if envs.environments[env.ID].Name != "prod" {
		t.Fatalf("stale update mutated environment: %#v", envs.environments[env.ID])
	}
	persisted, _ := units.GetByID(ctx, existing.ID)
	if persisted == nil || persisted.RuntimeType != domain.RuntimeTypeDocker {
		t.Fatalf("stale update mutated units: %#v", persisted)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("stale update published mutation event: %#v", publisher.events)
	}
}

func TestRegistryServiceUpdateEnvironmentWithDeploymentUnitsProtectsReferencedRemovalAndRollsBack(t *testing.T) {
	ctx := context.Background()
	envs := newEnvironmentMutationEnvRepo()
	units := newEnvironmentMutationUnitRepo()
	publisher := &capturePublisher{}
	registry := newEnvironmentMutationRegistry(envs, units, publisher)
	envID := uuid.New()
	original := &domain.Environment{ID: envID, Name: "prod"}
	if err := envs.Create(ctx, original); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	defaultUnit := &domain.DeploymentUnit{ID: uuid.New(), EnvironmentID: envID, Key: "default", RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged}
	legacyUnit := &domain.DeploymentUnit{ID: uuid.New(), EnvironmentID: envID, Key: "legacy", RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged}
	if err := units.Create(ctx, defaultUnit); err != nil {
		t.Fatalf("seed default unit: %v", err)
	}
	if err := units.Create(ctx, legacyUnit); err != nil {
		t.Fatalf("seed legacy unit: %v", err)
	}
	units.referenced[legacyUnit.ID] = true

	updated := *original
	updated.Name = "production"
	requested := []*domain.DeploymentUnit{{Key: "default", RuntimeType: domain.RuntimeTypeCompose}}
	if err := registry.UpdateEnvironmentWithDeploymentUnits(ctx, &updated, requested, original.UpdatedAt); err == nil {
		t.Fatalf("expected referenced unit removal to fail")
	}
	if envs.environments[envID].Name != "prod" {
		t.Fatalf("environment update was not rolled back: %#v", envs.environments[envID])
	}
	persisted, _ := units.ListByEnvironment(ctx, envID)
	if len(persisted) != 2 {
		t.Fatalf("unit reconciliation was not rolled back: %#v", persisted)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("published update before commit: %#v", publisher.events)
	}
}

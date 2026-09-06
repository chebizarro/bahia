// Package service implements the core business logic for the Bahia Deployment Registry.
package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/driftdecision"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// ImageVerifier validates that an artifact image exists in the container registry.
// Implementations include the Harbor adapter (production) and NoopImageVerifier (dev/test).
type ImageVerifier interface {
	// VerifyImage checks that the given image reference exists and returns its digest and scan status.
	// imageRepo is the full repository path (e.g. "myproject/myimage"), reference is a tag or digest.
	VerifyImage(ctx context.Context, imageRepo, reference string) (*ImageVerification, error)
}

// ImageVerification holds the result of an image verification check.
type ImageVerification struct {
	Exists     bool
	Digest     string
	ScanStatus string
}

// NoopImageVerifier skips image verification. Used when no registry is configured.
type NoopImageVerifier struct{}

// VerifyImage always returns a successful verification with no digest or scan info.
func (n *NoopImageVerifier) VerifyImage(_ context.Context, _, _ string) (*ImageVerification, error) {
	return &ImageVerification{Exists: true}, nil
}

// RegistryService orchestrates the core deployment registry operations.
type RegistryService struct {
	services                        repository.ServiceRepository
	environments                    repository.EnvironmentRepository
	builds                          repository.BuildRepository
	artifacts                       repository.ArtifactRepository
	intents                         repository.DeploymentIntentRepository
	runs                            repository.DeploymentRunRepository
	observations                    repository.RuntimeObservationRepository
	state                           repository.EnvironmentServiceStateRepository
	txExecutor                      repository.TxExecutor
	verifier                        ImageVerifier
	allowManualArtifactRegistration bool
	allowLiveArtifactImport         bool
	publisher                       events.Publisher
	logger                          *zap.Logger
}

// RegistryOption configures optional registry capabilities.
type RegistryOption func(*RegistryService)

// WithRegistryTxExecutor enables atomic multi-repository registry mutations.
func WithRegistryTxExecutor(executor repository.TxExecutor) RegistryOption {
	return func(s *RegistryService) {
		s.txExecutor = executor
	}
}

// WithManualArtifactRegistration controls the advanced operator-supplied
// artifact path. Production defaults should pass false; verified build-result
// registration uses RegisterVerifiedArtifact and is unaffected.
func WithManualArtifactRegistration(allowed bool) RegistryOption {
	return func(s *RegistryService) {
		s.allowManualArtifactRegistration = allowed
	}
}

// WithLiveArtifactImport controls the operator path that imports an
// already-running, observation-verified image as governed build/artifact
// lineage. Production defaults should pass false so CI-attested provenance
// stays the norm; it exists so operators never need direct database mutation.
func WithLiveArtifactImport(allowed bool) RegistryOption {
	return func(s *RegistryService) {
		s.allowLiveArtifactImport = allowed
	}
}

// NewRegistryService creates a new RegistryService.
// The verifier parameter is optional, but advanced manual registration rejects NoopImageVerifier because it cannot prove a manifest digest.
func NewRegistryService(
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	builds repository.BuildRepository,
	artifacts repository.ArtifactRepository,
	intents repository.DeploymentIntentRepository,
	runs repository.DeploymentRunRepository,
	observations repository.RuntimeObservationRepository,
	state repository.EnvironmentServiceStateRepository,
	verifier ImageVerifier,
	publisher events.Publisher,
	logger *zap.Logger,
	options ...RegistryOption,
) *RegistryService {
	if verifier == nil {
		verifier = &NoopImageVerifier{}
	}
	registry := &RegistryService{
		services:                        services,
		environments:                    environments,
		builds:                          builds,
		artifacts:                       artifacts,
		intents:                         intents,
		runs:                            runs,
		observations:                    observations,
		state:                           state,
		verifier:                        verifier,
		allowManualArtifactRegistration: false,
		publisher:                       publisher,
		logger:                          logger,
	}
	for _, option := range options {
		if option != nil {
			option(registry)
		}
	}
	return registry
}

// --- Service CRUD ---

func (s *RegistryService) CreateService(ctx context.Context, svc *domain.Service) error {
	if svc.RuntimeType == "" {
		svc.RuntimeType = domain.RuntimeTypeDocker
	}
	if svc.DefaultBranch == "" {
		svc.DefaultBranch = "main"
	}
	normalizeServiceRepositoryForWrite(svc)
	if err := s.services.Create(ctx, svc); err != nil {
		return err
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventServiceCreated,
		EntityID: svc.ID.String(),
		Data:     events.ResourceData{ServiceID: svc.ID.String()},
	})
	return nil
}

func (s *RegistryService) GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	svc, err := s.services.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	normalizeServiceRepositoryForRead(svc)
	return svc, nil
}

func (s *RegistryService) GetServiceByName(ctx context.Context, name string) (*domain.Service, error) {
	svc, err := s.services.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	normalizeServiceRepositoryForRead(svc)
	return svc, nil
}

func (s *RegistryService) ListServices(ctx context.Context) ([]domain.Service, error) {
	svcs, err := s.services.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range svcs {
		normalizeServiceRepositoryForRead(&svcs[i])
	}
	return svcs, nil
}

func (s *RegistryService) ListServicesByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Service, error) {
	svcs, err := s.services.ListByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range svcs {
		normalizeServiceRepositoryForRead(&svcs[i])
	}
	return svcs, nil
}

func (s *RegistryService) UpdateService(ctx context.Context, svc *domain.Service) error {
	normalizeServiceRepositoryForWrite(svc)
	if err := s.services.Update(ctx, svc); err != nil {
		return err
	}
	s.publishServiceUpdated(ctx, svc)
	return nil
}

// UpdateServiceWithExpectedRevision atomically updates a service only when its
// persisted updated_at revision still matches expectedUpdatedAt.
func (s *RegistryService) UpdateServiceWithExpectedRevision(ctx context.Context, svc *domain.Service, expectedUpdatedAt time.Time) error {
	return s.updateServiceWithExpectedRevision(ctx, svc, expectedUpdatedAt, nil)
}

func (s *RegistryService) updateServiceWithExpectedRevision(ctx context.Context, svc *domain.Service, expectedUpdatedAt time.Time, beforeWrite func() error) error {
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	if expectedUpdatedAt.IsZero() {
		return fmt.Errorf("%w: expected_updated_at is required", domain.ErrInvalidValue)
	}
	if s.txExecutor == nil {
		return fmt.Errorf("service revision transaction handling is not configured")
	}
	normalizeServiceRepositoryForWrite(svc)
	if err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Services == nil {
			return fmt.Errorf("service transaction repository is not configured")
		}
		locker, ok := repos.Services.(serviceForUpdateRepository)
		if !ok {
			return fmt.Errorf("service transactional revision locking is not configured")
		}
		current, err := locker.GetByIDForUpdate(ctx, svc.ID)
		if err != nil {
			return fmt.Errorf("locking service for update: %w", err)
		}
		if current == nil {
			return fmt.Errorf("service %s: %w", svc.ID, repository.ErrNotFound)
		}
		if !current.UpdatedAt.Equal(expectedUpdatedAt) {
			return fmt.Errorf(
				"service %s revision conflict (expected %s, actual %s): %w: %w",
				svc.ID,
				expectedUpdatedAt.UTC().Format(time.RFC3339Nano),
				current.UpdatedAt.UTC().Format(time.RFC3339Nano),
				repository.ErrConflict,
				repository.ErrStaleRevision,
			)
		}
		svc.CreatedAt = current.CreatedAt
		if beforeWrite != nil {
			if err := beforeWrite(); err != nil {
				return err
			}
		}
		return repos.Services.Update(ctx, svc)
	}); err != nil {
		return err
	}
	s.publishServiceUpdated(ctx, svc)
	return nil
}

type serviceForUpdateRepository interface {
	GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Service, error)
}

func (s *RegistryService) publishServiceUpdated(ctx context.Context, svc *domain.Service) {
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventServiceUpdated,
		EntityID: svc.ID.String(),
		Data:     events.ResourceData{ServiceID: svc.ID.String()},
	})
}

// DeleteService deletes a service after checking for dependent resources.
// If force is false and dependents exist, returns an error describing what would be cascaded.
func (s *RegistryService) DeleteService(ctx context.Context, id uuid.UUID, force bool) error {
	var affectedStates []domain.EnvironmentServiceState
	if !force {
		if pgRepo, ok := s.services.(*repository.PgServiceRepository); ok {
			builds, artifacts, intents, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			total := builds + artifacts + intents
			if total > 0 {
				return fmt.Errorf(
					"service has dependent resources (%d builds, %d artifacts, %d deployment intents); "+
						"use force=true to cascade delete or remove dependents first",
					builds, artifacts, intents,
				)
			}
		}
	}
	if states, err := s.state.ListByService(ctx, id); err == nil {
		affectedStates = states
	} else {
		s.logger.Warn("failed to list affected service state before delete", zap.String("service_id", id.String()), zap.Error(err))
	}
	if err := s.services.Delete(ctx, id); err != nil {
		return err
	}
	for _, st := range affectedStates {
		s.publisher.Publish(ctx, events.Event{
			Type:     events.EventEnvironmentServiceStateChanged,
			EntityID: st.ServiceID.String() + ":" + st.EnvironmentID.String(),
			Data: events.ResourceData{
				ServiceID:     st.ServiceID.String(),
				EnvironmentID: st.EnvironmentID.String(),
				Deleted:       true,
			},
		})
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventServiceDeleted,
		EntityID: id.String(),
		Data:     events.ResourceData{ServiceID: id.String(), Deleted: true},
	})
	return nil
}

func normalizeServiceRepositoryForWrite(svc *domain.Service) {
	if svc == nil {
		return
	}

	svc.RepoURL = strings.TrimSpace(svc.RepoURL)

	if svc.Repository != nil {
		svc.Repository.CloneURL = strings.TrimSpace(svc.Repository.CloneURL)
		svc.Repository.WebURL = strings.TrimSpace(svc.Repository.WebURL)
		svc.Repository.Source = strings.TrimSpace(svc.Repository.Source)
		svc.Repository.RepoCoordinate = strings.TrimSpace(svc.Repository.RepoCoordinate)

		if svc.Repository.CI != nil {
			svc.Repository.CI.Provider = strings.TrimSpace(svc.Repository.CI.Provider)
			if svc.Repository.CI.Provider == "" {
				svc.Repository.CI.Provider = "hiveci"
			}
		}

		if len(svc.Repository.RelayURLs) > 0 {
			seen := make(map[string]struct{}, len(svc.Repository.RelayURLs))
			relays := make([]string, 0, len(svc.Repository.RelayURLs))
			for _, relay := range svc.Repository.RelayURLs {
				trimmed := strings.TrimSpace(relay)
				if trimmed == "" {
					continue
				}
				if _, ok := seen[trimmed]; ok {
					continue
				}
				seen[trimmed] = struct{}{}
				relays = append(relays, trimmed)
			}
			svc.Repository.RelayURLs = relays
		}

		if svc.Repository.CloneURL != "" {
			svc.RepoURL = svc.Repository.CloneURL
		}

		if svc.Repository.Source == "" {
			if svc.Repository.RepoCoordinate != "" {
				svc.Repository.Source = "nip34"
			} else {
				svc.Repository.Source = "manual"
			}
		}
	}

	if svc.Repository == nil && svc.RepoURL != "" {
		svc.Repository = &domain.RepositoryRef{
			Source:   "manual",
			CloneURL: svc.RepoURL,
		}
	}
}

func normalizeServiceRepositoryForRead(svc *domain.Service) {
	if svc == nil {
		return
	}

	svc.RepoURL = strings.TrimSpace(svc.RepoURL)

	if svc.Repository == nil && svc.RepoURL != "" {
		svc.Repository = &domain.RepositoryRef{
			Source:   "manual",
			CloneURL: svc.RepoURL,
		}
		return
	}

	if svc.Repository == nil {
		return
	}

	svc.Repository.CloneURL = strings.TrimSpace(svc.Repository.CloneURL)
	if svc.RepoURL == "" || svc.RepoURL != svc.Repository.CloneURL {
		svc.RepoURL = svc.Repository.CloneURL
	}
}

// --- Environment CRUD ---

func (s *RegistryService) CreateEnvironment(ctx context.Context, env *domain.Environment) error {
	if err := normalizeAndValidateEnvironmentMutation(env, nil); err != nil {
		return err
	}
	if err := s.environments.Create(ctx, env); err != nil {
		return err
	}
	s.publishEnvironmentMutation(ctx, events.EventEnvironmentCreated, env)
	return nil
}

// CreateEnvironmentWithDeploymentUnits persists an environment and its explicit units in one transaction.
func (s *RegistryService) CreateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error {
	if err := normalizeAndValidateEnvironmentMutation(env, units); err != nil {
		return err
	}
	if s.txExecutor == nil {
		return fmt.Errorf("environment deployment-unit transaction handling is not configured")
	}
	if err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Environments == nil || repos.DeploymentUnits == nil {
			return fmt.Errorf("environment transaction repositories are not configured")
		}
		if err := repos.Environments.Create(ctx, env); err != nil {
			return err
		}
		for _, unit := range units {
			if err := repos.DeploymentUnits.Create(ctx, unit); err != nil {
				return fmt.Errorf("creating deployment unit %q: %w", unit.Key, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.publishEnvironmentMutation(ctx, events.EventEnvironmentCreated, env)
	return nil
}

func (s *RegistryService) GetEnvironment(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return s.environments.GetByID(ctx, id)
}

func (s *RegistryService) GetEnvironmentByName(ctx context.Context, name string) (*domain.Environment, error) {
	return s.environments.GetByName(ctx, name)
}

func (s *RegistryService) ListEnvironments(ctx context.Context) ([]domain.Environment, error) {
	return s.environments.List(ctx)
}

func (s *RegistryService) ListEnvironmentsByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	return s.environments.ListByOrg(ctx, orgID)
}

func (s *RegistryService) UpdateEnvironment(ctx context.Context, env *domain.Environment) error {
	if err := normalizeAndValidateEnvironmentMutation(env, nil); err != nil {
		return err
	}
	if err := s.environments.Update(ctx, env); err != nil {
		return err
	}
	s.publishEnvironmentMutation(ctx, events.EventEnvironmentUpdated, env)
	return nil
}

// UpdateEnvironmentWithDeploymentUnits updates an environment and reconciles its
// complete explicit unit set atomically when expectedUpdatedAt still matches.
func (s *RegistryService) UpdateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, expectedUpdatedAt time.Time) error {
	return s.updateEnvironmentWithDeploymentUnits(ctx, env, units, expectedUpdatedAt, nil)
}

// updateEnvironmentWithDeploymentUnits runs an optional signer-first publication
// after locking and checking the environment revision, but before local writes.
func (s *RegistryService) updateEnvironmentWithDeploymentUnits(
	ctx context.Context,
	env *domain.Environment,
	units []*domain.DeploymentUnit,
	expectedUpdatedAt time.Time,
	beforeWrite func() error,
) error {
	if err := normalizeAndValidateEnvironmentMutation(env, units); err != nil {
		return err
	}
	if expectedUpdatedAt.IsZero() {
		return fmt.Errorf("%w: expected_updated_at is required for complete-set deployment-unit updates", domain.ErrInvalidValue)
	}
	if s.txExecutor == nil {
		return fmt.Errorf("environment deployment-unit transaction handling is not configured")
	}
	if err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Environments == nil || repos.DeploymentUnits == nil {
			return fmt.Errorf("environment transaction repositories are not configured")
		}
		envLocker, ok := repos.Environments.(environmentForUpdateRepository)
		if !ok {
			return fmt.Errorf("environment transactional revision locking is not configured")
		}
		current, err := envLocker.GetByIDForUpdate(ctx, env.ID)
		if err != nil {
			return fmt.Errorf("locking environment for unit reconciliation: %w", err)
		}
		if current == nil {
			return fmt.Errorf("environment %s: %w", env.ID, repository.ErrNotFound)
		}
		if !current.UpdatedAt.Equal(expectedUpdatedAt) {
			return fmt.Errorf(
				"environment %s revision conflict (expected %s, actual %s): %w: %w",
				env.ID,
				expectedUpdatedAt.UTC().Format(time.RFC3339Nano),
				current.UpdatedAt.UTC().Format(time.RFC3339Nano),
				repository.ErrConflict,
				repository.ErrStaleRevision,
			)
		}

		unitWriter, ok := repos.DeploymentUnits.(deploymentUnitMutationRepository)
		if !ok {
			return fmt.Errorf("deployment unit transactional mutation handling is not configured")
		}
		env.CreatedAt = current.CreatedAt
		if beforeWrite != nil {
			if err := beforeWrite(); err != nil {
				return err
			}
		}
		if err := repos.Environments.Update(ctx, env); err != nil {
			return err
		}
		return reconcileExplicitDeploymentUnits(ctx, unitWriter, env.ID, units)
	}); err != nil {
		return err
	}
	s.publishEnvironmentMutation(ctx, events.EventEnvironmentUpdated, env)
	return nil
}

type environmentForUpdateRepository interface {
	GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Environment, error)
}

type deploymentUnitMutationRepository interface {
	repository.DeploymentUnitRepository
	ListByEnvironmentForUpdate(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error)
	Update(ctx context.Context, unit *domain.DeploymentUnit) error
	DeleteIfUnreferenced(ctx context.Context, id uuid.UUID) error
}

func reconcileExplicitDeploymentUnits(ctx context.Context, repo deploymentUnitMutationRepository, environmentID uuid.UUID, requested []*domain.DeploymentUnit) error {
	existing, err := repo.ListByEnvironmentForUpdate(ctx, environmentID)
	if err != nil {
		return fmt.Errorf("listing deployment units for update: %w", err)
	}
	existingByKey := make(map[string]domain.DeploymentUnit, len(existing))
	for _, unit := range existing {
		existingByKey[unit.Key] = unit
	}

	requestedKeys := make(map[string]struct{}, len(requested))
	for _, unit := range requested {
		requestedKeys[unit.Key] = struct{}{}
		if persisted, ok := existingByKey[unit.Key]; ok {
			unit.ID = persisted.ID
			unit.CreatedAt = persisted.CreatedAt
			if err := repo.Update(ctx, unit); err != nil {
				return fmt.Errorf("updating deployment unit %q: %w", unit.Key, err)
			}
			continue
		}
		if err := repo.Create(ctx, unit); err != nil {
			return fmt.Errorf("creating deployment unit %q: %w", unit.Key, err)
		}
	}

	for _, unit := range existing {
		if _, retained := requestedKeys[unit.Key]; retained {
			continue
		}
		if err := repo.DeleteIfUnreferenced(ctx, unit.ID); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				return fmt.Errorf("deployment unit %q cannot be removed because it is referenced by state, runs, intents, or observations: %w", unit.Key, err)
			}
			return fmt.Errorf("deleting deployment unit %q: %w", unit.Key, err)
		}
	}
	return nil
}

func normalizeAndValidateEnvironmentMutation(env *domain.Environment, units []*domain.DeploymentUnit) error {
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	if env.ID == uuid.Nil {
		env.ID = uuid.New()
	}
	env.Name = strings.TrimSpace(env.Name)
	if env.Name == "" {
		return fmt.Errorf("%w: environment name must not be empty", domain.ErrEmptyField)
	}
	if env.DeployStrategy == "" {
		env.DeployStrategy = domain.DeployStrategyReplace
	}
	if err := domain.ValidateDeployStrategy(env.DeployStrategy); err != nil {
		return err
	}
	domain.NormalizeEnvironmentTargeting(env)
	if err := domain.ValidateReconcileMode(env.Targeting.DefaultReconcileMode); err != nil {
		return err
	}
	switch env.Targeting.SecretScopeMode {
	case "", domain.SecretScopeModeService, domain.SecretScopeModeEnvironment, domain.SecretScopeModeUnit:
	default:
		return fmt.Errorf("%w: secret_scope_mode %q is not valid (allowed: service, environment, unit)", domain.ErrInvalidValue, env.Targeting.SecretScopeMode)
	}

	defaultRuntime := domain.RuntimeTypeFromRuntimeConfig(env.RuntimeConfig)
	if defaultRuntime == "" {
		defaultRuntime = domain.RuntimeTypeDocker
	}
	seenKeys := make(map[string]struct{}, len(units))
	for _, unit := range units {
		if unit == nil {
			return fmt.Errorf("%w: deployment unit must not be nil", domain.ErrInvalidValue)
		}
		unit.EnvironmentID = env.ID
		unit.Key = strings.TrimSpace(unit.Key)
		unit.DisplayName = strings.TrimSpace(unit.DisplayName)
		unit.EndpointRef = strings.TrimSpace(unit.EndpointRef)
		unit.ComposeDir = strings.TrimSpace(unit.ComposeDir)
		unit.Namespace = strings.TrimSpace(unit.Namespace)
		if unit.RuntimeType == "" {
			unit.RuntimeType = defaultRuntime
		}
		if unit.ReconcileMode == "" {
			unit.ReconcileMode = env.Targeting.DefaultReconcileMode
		}
		domain.NormalizeDeploymentUnitTargeting(unit)
		if err := domain.ValidateDeploymentUnit(unit); err != nil {
			return fmt.Errorf("invalid deployment unit %q: %w", unit.Key, err)
		}
		if _, duplicate := seenKeys[unit.Key]; duplicate {
			return fmt.Errorf("%w: duplicate deployment unit key %q", domain.ErrInvalidValue, unit.Key)
		}
		seenKeys[unit.Key] = struct{}{}
	}
	if len(units) > 0 {
		if _, ok := seenKeys[env.Targeting.DefaultUnitKey]; !ok {
			return fmt.Errorf("%w: targeting default_unit_key %q does not identify an explicit deployment unit", domain.ErrInvalidValue, env.Targeting.DefaultUnitKey)
		}
	}
	return nil
}

func (s *RegistryService) publishEnvironmentMutation(ctx context.Context, eventType events.EventType, env *domain.Environment) {
	s.publisher.Publish(ctx, events.Event{
		Type:     eventType,
		EntityID: env.ID.String(),
		Data:     events.ResourceData{EnvironmentID: env.ID.String()},
	})
}

// DeleteEnvironment deletes an environment after checking for dependent resources.
// If force is false and dependents exist, returns an error describing what would be cascaded.
func (s *RegistryService) DeleteEnvironment(ctx context.Context, id uuid.UUID, force bool) error {
	var affectedStates []domain.EnvironmentServiceState
	if !force {
		if pgRepo, ok := s.environments.(*repository.PgEnvironmentRepository); ok {
			intents, states, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			total := intents + states
			if total > 0 {
				return fmt.Errorf(
					"environment has dependent resources (%d deployment intents, %d state records); "+
						"use force=true to cascade delete or remove dependents first",
					intents, states,
				)
			}
		}
	}
	if states, err := s.state.ListByEnvironment(ctx, id); err == nil {
		affectedStates = states
	} else {
		s.logger.Warn("failed to list affected environment state before delete", zap.String("environment_id", id.String()), zap.Error(err))
	}
	if err := s.environments.Delete(ctx, id); err != nil {
		return err
	}
	for _, st := range affectedStates {
		s.publisher.Publish(ctx, events.Event{
			Type:     events.EventEnvironmentServiceStateChanged,
			EntityID: st.ServiceID.String() + ":" + st.EnvironmentID.String(),
			Data: events.ResourceData{
				ServiceID:     st.ServiceID.String(),
				EnvironmentID: st.EnvironmentID.String(),
				Deleted:       true,
			},
		})
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentDeleted,
		EntityID: id.String(),
		Data:     events.ResourceData{EnvironmentID: id.String(), Deleted: true},
	})
	return nil
}

// --- Build operations ---

func (s *RegistryService) RegisterBuild(ctx context.Context, b *domain.Build) error {
	if b.Status == "" {
		b.Status = domain.BuildStatusQueued
	}
	if err := s.builds.Create(ctx, b); err != nil {
		return err
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventBuildRegistered,
		EntityID: b.ID.String(),
		Data:     b,
	})

	s.logger.Info("build registered",
		zap.String("build_id", b.ID.String()),
		zap.String("service_id", b.ServiceID.String()),
		zap.String("git_sha", b.GitSHA),
	)
	return nil
}

func (s *RegistryService) GetBuild(ctx context.Context, id uuid.UUID) (*domain.Build, error) {
	return s.builds.GetByID(ctx, id)
}

func (s *RegistryService) ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.builds.ListByService(ctx, serviceID, limit, offset)
}

func (s *RegistryService) UpdateBuildStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error {
	if err := s.builds.UpdateStatus(ctx, id, status); err != nil {
		return err
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventBuildStatusChanged,
		EntityID: id.String(),
		Data:     map[string]string{"status": string(status)},
	})
	return nil
}

// --- Artifact operations ---

var immutableArtifactDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ArtifactVerificationProof is produced only after a build-result registrar has
// resolved the immutable manifest from Bahia's embedded OCI layout or a
// configured external registry.
type ReleaseArtifactVerificationProof struct {
	Release    domain.HiveCIAcceptedRelease
	VerifiedAt time.Time
}

type ArtifactVerificationProof struct {
	Source                 string
	ManifestDigest         string
	TagResolvedDigest      string
	MediaType              string
	SizeBytes              int64
	ScanStatus             string
	Signatures             []string
	SBOMRef                string
	ProvenanceRef          string
	PolicyState            string
	PolicyID               uuid.UUID
	CIPublisher            string
	ReferrerDiscoveryState string
	VerifiedAt             time.Time
	Annotations            map[string]string
}

// RegisterArtifact is the advanced manual path. It is disabled by production
// policy unless explicitly enabled and always requires a registry to return the
// same immutable manifest digest supplied by the operator.
func (s *RegistryService) RegisterArtifact(ctx context.Context, a *domain.Artifact) error {
	if !s.allowManualArtifactRegistration {
		return fmt.Errorf("manual artifact registration is disabled by policy: artifacts are expected to be CI-attested through the Hive CI path (a signed kind 5402 carrying image_repo, image_tag, and image_digest registers a digest-pinned artifact automatically); set hiveci.allow_manual_artifact_registration=true only to deliberately re-enable the operator-reviewed path")
	}
	if err := s.validateArtifactBinding(ctx, a); err != nil {
		return err
	}

	verification, err := s.verifier.VerifyImage(ctx, a.ImageRepo, a.ImageTag)
	if err != nil {
		s.logger.Error("image verification failed",
			zap.String("image_repo", a.ImageRepo),
			zap.String("reference", a.ImageTag),
			zap.Error(err),
		)
		return fmt.Errorf("verifying image immutable manifest in registry: %w", err)
	}
	if verification == nil || !verification.Exists {
		return fmt.Errorf("immutable image %s@%s not found in container registry", a.ImageRepo, a.ImageDigest)
	}
	verifiedDigest := strings.ToLower(strings.TrimSpace(verification.Digest))
	if !immutableArtifactDigest.MatchString(verifiedDigest) {
		return fmt.Errorf("registry did not return a verifiable sha256 manifest digest")
	}
	if verifiedDigest != a.ImageDigest {
		return fmt.Errorf("digest mismatch: artifact claims %s but registry reports %s", a.ImageDigest, verifiedDigest)
	}
	if verification.ScanStatus != "" && a.ScanStatus == domain.ScanStatusUnknown {
		a.ScanStatus = domain.ScanStatus(verification.ScanStatus)
	}
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	for _, reserved := range []string{"verification", "supply_chain", "policy", "evidence", "oci_annotations"} {
		delete(a.Metadata, reserved)
	}
	a.Metadata["registration_mode"] = "advanced_manual"
	a.Metadata["verification"] = map[string]any{
		"source": "registry_manifest", "manifest_digest": verifiedDigest, "tag_resolved_digest": verifiedDigest,
		"tag": a.ImageTag, "state": "verified", "verified_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	return s.persistArtifact(ctx, a)
}

type ReleaseArtifactAuditPreparer func(*domain.Artifact) (*repository.NostrEventRecord, error)

// RegisterReleaseArtifactWithAudit atomically persists the release build,
// digest-only artifact, and signed audit outbox evidence.
func (s *RegistryService) RegisterReleaseArtifactWithAudit(
	ctx context.Context,
	build *domain.Build,
	artifact *domain.Artifact,
	proof ReleaseArtifactVerificationProof,
	prepareAudit ReleaseArtifactAuditPreparer,
) error {
	if s == nil || s.txExecutor == nil || build == nil || artifact == nil || prepareAudit == nil {
		return fmt.Errorf("atomic release registration and audit are not configured")
	}
	buildCreated := false
	if err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Services == nil || repos.Builds == nil || repos.Artifacts == nil || repos.NostrEvents == nil {
			return fmt.Errorf("release registration transaction repositories are not configured")
		}
		existing, err := repos.Builds.GetByCISystemRunID(ctx, build.CISystem, build.CIRunID)
		if err != nil {
			return fmt.Errorf("lookup release build in transaction: %w", err)
		}
		if existing != nil {
			if existing.ServiceID != build.ServiceID || existing.Status != domain.BuildStatusSucceeded ||
				!strings.EqualFold(existing.GitSHA, build.GitSHA) {
				return fmt.Errorf("existing release build conflicts with accepted lineage")
			}
			*build = *existing
		} else if err := repos.Builds.Create(ctx, build); err != nil {
			return fmt.Errorf("register release build in transaction: %w", err)
		} else {
			buildCreated = true
		}
		artifact.BuildID = build.ID
		txRegistry := *s
		txRegistry.services = repos.Services
		txRegistry.builds = repos.Builds
		txRegistry.artifacts = repos.Artifacts
		txRegistry.publisher = &events.NoopPublisher{}
		txRegistry.txExecutor = nil
		if err := txRegistry.RegisterReleaseArtifact(ctx, artifact, proof); err != nil {
			return err
		}
		audit, err := prepareAudit(artifact)
		if err != nil {
			return fmt.Errorf("prepare release registration audit: %w", err)
		}
		inserted, err := repos.NostrEvents.Record(ctx, audit)
		if err != nil {
			return fmt.Errorf("persist release registration audit: %w", err)
		}
		if !inserted {
			return fmt.Errorf("release registration audit event conflicts with existing outbox evidence")
		}
		return nil
	}); err != nil {
		return err
	}
	if buildCreated {
		s.publisher.Publish(ctx, events.Event{
			Type: events.EventBuildRegistered, EntityID: build.ID.String(), Data: build,
		})
	}
	s.publishArtifactRegistered(ctx, artifact)
	return nil
}

// RegisterReleaseArtifact registers an accepted Hive-CI RELEASE strictly by
// its immutable manifest digest. The producer's optional tag is retained only
// inside signed evidence metadata and is never an artifact lookup key.
func (s *RegistryService) RegisterReleaseArtifact(ctx context.Context, a *domain.Artifact, proof ReleaseArtifactVerificationProof) error {
	if err := s.validateArtifactBindingMode(ctx, a, false); err != nil {
		return err
	}
	release := proof.Release
	if a.ImageDigest != release.Result.Manifest.Digest || a.ImageRepo != release.Result.Manifest.Repository {
		return fmt.Errorf("accepted release manifest does not match artifact identity")
	}
	if a.ImageTag != "" {
		return fmt.Errorf("release artifacts must not persist a mutable image tag")
	}
	a.ManifestMediaType = release.Result.Manifest.MediaType
	size := release.Result.Manifest.Size
	a.SizeBytes = &size
	a.SBOMURL = release.Result.SBOM.Repository + "@" + release.Result.SBOM.Digest
	a.SignatureRef = release.ResultEventID
	a.ScanStatus = domain.ScanStatusUnknown
	verifiedAt := proof.VerifiedAt.UTC()
	if proof.VerifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	a.Metadata = map[string]any{
		"registration_mode":          "hiveci_release_digest",
		"promotion_authority":        "required",
		"ci_mutates_desired_state":   false,
		"signed_image_tag":           release.Result.ImageTag,
		"release_identity":           release.Result.ReleaseIdentity,
		"release_result_event_id":    release.ResultEventID,
		"release_attestor":           release.Attestor,
		"release_content_digest":     release.ContentDigest,
		"signed_release_event":       release.SignedEvent,
		"signed_workflow_run_event":  release.WorkflowRunSignedEvent,
		"lineage":                    release.Result.Lineage,
		"execution":                  release.Result.Execution,
		"policy":                     release.Policy,
		"worker_admission":           release.WorkerAdmissionEvidence,
		"rollback_compatibility":     release.RollbackCompatibility,
		"health_readiness_contracts": release.HealthReadinessContracts,
		"verification": map[string]any{
			"source": "oci_digest", "state": "verified", "verified_at": verifiedAt.Format(time.RFC3339Nano),
			"manifest": release.Result.Manifest, "sbom": release.Result.SBOM, "provenance": release.Result.Provenance,
		},
	}
	if err := s.persistArtifact(ctx, a); err != nil {
		return err
	}
	if a.Metadata["release_identity"] != release.Result.ReleaseIdentity ||
		a.Metadata["release_content_digest"] != release.ContentDigest {
		return fmt.Errorf("existing artifact conflicts with accepted release lineage")
	}
	return nil
}

// RegisterVerifiedArtifact persists a build result only when the caller
// supplies a proof produced by an immutable manifest lookup.
func (s *RegistryService) RegisterVerifiedArtifact(ctx context.Context, a *domain.Artifact, proof ArtifactVerificationProof) error {
	if err := s.validateArtifactBinding(ctx, a); err != nil {
		return err
	}
	source := strings.TrimSpace(proof.Source)
	if source != "embedded_oci_layout" && source != "registry_manifest" {
		return fmt.Errorf("artifact verification source must be embedded_oci_layout or registry_manifest")
	}
	proofDigest := strings.ToLower(strings.TrimSpace(proof.ManifestDigest))
	tagResolvedDigest := strings.ToLower(strings.TrimSpace(proof.TagResolvedDigest))
	if !immutableArtifactDigest.MatchString(proofDigest) || proofDigest != a.ImageDigest || tagResolvedDigest != proofDigest {
		return fmt.Errorf("verified tag-resolved manifest digest does not match artifact digest")
	}
	if !supportedOCIManifestMediaType(proof.MediaType) {
		return fmt.Errorf("unsupported OCI manifest media type %q", proof.MediaType)
	}
	a.ManifestMediaType = strings.TrimSpace(proof.MediaType)
	if proof.SizeBytes > 0 {
		size := proof.SizeBytes
		a.SizeBytes = &size
	}
	if proof.ScanStatus != "" {
		a.ScanStatus = domain.ScanStatus(proof.ScanStatus)
	}
	if a.ScanStatus == "" {
		a.ScanStatus = domain.ScanStatusUnknown
	}
	if len(proof.Signatures) > 0 {
		a.SignatureRef = proof.Signatures[0]
	}
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	a.Metadata["registration_mode"] = "verified_build_result"
	verifiedAt := proof.VerifiedAt.UTC()
	if proof.VerifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	a.Metadata["verification"] = map[string]any{
		"source": source, "state": "verified", "manifest_digest": proofDigest,
		"tag": a.ImageTag, "tag_resolved_digest": tagResolvedDigest,
		"media_type": a.ManifestMediaType, "verified_at": verifiedAt.Format(time.RFC3339Nano),
	}
	a.Metadata["policy"] = map[string]any{
		"state": strings.TrimSpace(proof.PolicyState), "policy_id": proof.PolicyID.String(),
		"ci_publisher": strings.TrimSpace(proof.CIPublisher),
	}
	a.Metadata["supply_chain"] = map[string]any{
		"signature_state":          supplyChainState(len(proof.Signatures) > 0),
		"signature_refs":           append([]string(nil), proof.Signatures...),
		"sbom_state":               supplyChainState(strings.TrimSpace(proof.SBOMRef) != ""),
		"sbom_ref":                 strings.TrimSpace(proof.SBOMRef),
		"provenance_ref":           strings.TrimSpace(proof.ProvenanceRef),
		"policy_state":             strings.TrimSpace(proof.PolicyState),
		"referrer_discovery_state": strings.TrimSpace(proof.ReferrerDiscoveryState),
	}
	if len(proof.Annotations) > 0 {
		a.Metadata["oci_annotations"] = proof.Annotations
	}
	return s.persistArtifact(ctx, a)
}

func (s *RegistryService) validateArtifactBinding(ctx context.Context, a *domain.Artifact) error {
	return s.validateArtifactBindingMode(ctx, a, true)
}

func (s *RegistryService) validateArtifactBindingMode(ctx context.Context, a *domain.Artifact, requireTag bool) error {
	if a == nil {
		return fmt.Errorf("artifact is required")
	}
	a.ImageRepo = strings.TrimSpace(a.ImageRepo)
	a.ImageTag = strings.TrimSpace(a.ImageTag)
	a.ImageDigest = strings.ToLower(strings.TrimSpace(a.ImageDigest))
	if a.BuildID == uuid.Nil || a.ServiceID == uuid.Nil {
		return fmt.Errorf("build_id and service_id are required")
	}
	if a.ImageRepo == "" || (requireTag && a.ImageTag == "") {
		return fmt.Errorf("image repository and build-produced tag are required")
	}
	if !immutableArtifactDigest.MatchString(a.ImageDigest) {
		return fmt.Errorf("image_digest must be an immutable sha256 manifest digest")
	}
	build, err := s.builds.GetByID(ctx, a.BuildID)
	if err != nil {
		return fmt.Errorf("loading artifact build: %w", err)
	}
	if build == nil {
		return fmt.Errorf("artifact build %s not found", a.BuildID)
	}
	if build.ServiceID != a.ServiceID {
		return fmt.Errorf("artifact service does not match build service")
	}
	if build.Status != domain.BuildStatusSucceeded {
		return fmt.Errorf("artifacts may only be registered for successful builds")
	}
	svc, err := s.services.GetByID(ctx, a.ServiceID)
	if err != nil {
		return fmt.Errorf("loading artifact service: %w", err)
	}
	if svc == nil {
		return fmt.Errorf("artifact service %s not found", a.ServiceID)
	}
	if strings.TrimSpace(svc.ArtifactRepo) != a.ImageRepo {
		return fmt.Errorf("image repository must match the service artifact repository")
	}
	if a.ScanStatus == "" {
		a.ScanStatus = domain.ScanStatusUnknown
	}
	return nil
}

func (s *RegistryService) persistArtifact(ctx context.Context, a *domain.Artifact) error {
	existing, err := s.artifacts.GetByImageRepoDigest(ctx, a.ImageRepo, a.ImageDigest)
	if err != nil {
		return fmt.Errorf("checking artifact identity: %w", err)
	}
	if existing != nil {
		if err := validateExistingArtifactBinding(existing, a); err != nil {
			return err
		}
		*a = *existing
		s.publishArtifactRegistered(ctx, a)
		return nil
	}
	if err := s.artifacts.Create(ctx, a); err != nil {
		// The database has a unique (image_repo, image_digest) constraint. A
		// concurrent result delivery may win between lookup and insert; reload
		// that row and converge only when every immutable binding agrees.
		concurrent, lookupErr := s.artifacts.GetByImageRepoDigest(ctx, a.ImageRepo, a.ImageDigest)
		if lookupErr == nil && concurrent != nil {
			if bindingErr := validateExistingArtifactBinding(concurrent, a); bindingErr != nil {
				return bindingErr
			}
			*a = *concurrent
			s.publishArtifactRegistered(ctx, a)
			return nil
		}
		return err
	}
	s.publishArtifactRegistered(ctx, a)
	s.logger.Info("artifact registered",
		zap.String("artifact_id", a.ID.String()),
		zap.String("image", a.ImageRepo+"@"+a.ImageDigest),
		zap.String("digest", a.ImageDigest),
	)
	return nil
}

func validateExistingArtifactBinding(existing, requested *domain.Artifact) error {
	if existing.BuildID != requested.BuildID || existing.ServiceID != requested.ServiceID ||
		existing.ImageRepo != requested.ImageRepo || existing.ImageTag != requested.ImageTag ||
		existing.ImageDigest != requested.ImageDigest {
		return fmt.Errorf("immutable artifact is already registered with different build, service, repository, tag, or digest bindings")
	}
	return nil
}

func (s *RegistryService) publishArtifactRegistered(ctx context.Context, artifact *domain.Artifact) {
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventArtifactRegistered,
		EntityID: artifact.ID.String(),
		Data:     artifact,
	})
}

func supportedOCIManifestMediaType(mediaType string) bool {
	switch strings.TrimSpace(strings.Split(mediaType, ";")[0]) {
	case "application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json":
		return true
	default:
		return false
	}
}

func supplyChainState(present bool) string {
	if present {
		return "present"
	}
	return "missing"
}

func (s *RegistryService) GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return s.artifacts.GetByID(ctx, id)
}

func (s *RegistryService) GetArtifactByDigest(ctx context.Context, repo, digest string) (*domain.Artifact, error) {
	return s.artifacts.GetByDigest(ctx, repo, digest)
}

func (s *RegistryService) ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.artifacts.ListByService(ctx, serviceID, limit, offset)
}

func (s *RegistryService) ListArtifactsByBuild(ctx context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	return s.artifacts.ListByBuild(ctx, buildID)
}

// --- Deployment Intent operations ---

type DeploymentDecisionAuditPreparer func() (*repository.NostrEventRecord, error)

// CreateDeploymentIntentWithAudit atomically persists an intent, any authorized
// desired-state transition, and its signed audit outbox evidence.
func (s *RegistryService) CreateDeploymentIntentWithAudit(
	ctx context.Context,
	intent *domain.DeploymentIntent,
	prepareAudit DeploymentDecisionAuditPreparer,
) error {
	if s == nil || s.txExecutor == nil || intent == nil || prepareAudit == nil {
		return fmt.Errorf("atomic deployment intent and audit are not configured")
	}
	if err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Services == nil || repos.Environments == nil || repos.Artifacts == nil ||
			repos.Intents == nil || repos.State == nil || repos.NostrEvents == nil {
			return fmt.Errorf("deployment intent transaction repositories are not configured")
		}
		txRegistry := *s
		txRegistry.services = repos.Services
		txRegistry.environments = repos.Environments
		txRegistry.artifacts = repos.Artifacts
		txRegistry.intents = repos.Intents
		txRegistry.state = repos.State
		txRegistry.publisher = &events.NoopPublisher{}
		txRegistry.txExecutor = nil
		if err := txRegistry.CreateDeploymentIntent(ctx, intent); err != nil {
			return err
		}
		audit, err := prepareAudit()
		if err != nil {
			return fmt.Errorf("prepare deployment decision audit: %w", err)
		}
		inserted, err := repos.NostrEvents.Record(ctx, audit)
		if err != nil {
			return fmt.Errorf("persist deployment decision audit: %w", err)
		}
		if !inserted {
			return fmt.Errorf("deployment decision audit event conflicts with existing outbox evidence")
		}
		return nil
	}); err != nil {
		return err
	}
	s.publisher.Publish(ctx, events.Event{Type: events.EventDeploymentIntentCreated, EntityID: intent.ID.String(), Data: intent})
	if intent.Status == domain.IntentStatusApproved {
		s.publisher.Publish(ctx, events.Event{
			Type:     events.EventEnvironmentServiceStateChanged,
			EntityID: intent.ServiceID.String() + ":" + intent.EnvironmentID.String(),
			Data: events.ResourceData{
				ServiceID: intent.ServiceID.String(), EnvironmentID: intent.EnvironmentID.String(),
				ArtifactID: intent.ArtifactID.String(), IntentID: intent.ID.String(),
			},
		})
		s.publisher.Publish(ctx, events.Event{
			Type: events.EventDeploymentIntentApproved, EntityID: intent.ID.String(),
			Data: events.ResourceData{
				ServiceID: intent.ServiceID.String(), EnvironmentID: intent.EnvironmentID.String(),
				ArtifactID: intent.ArtifactID.String(), IntentID: intent.ID.String(),
			},
		})
	}
	return nil
}

// DecideDeploymentIntentWithAudit atomically applies a protected promotion
// approval/rejection and records the signed decision evidence.
func (s *RegistryService) DecideDeploymentIntentWithAudit(
	ctx context.Context,
	id uuid.UUID,
	approve bool,
	prepareAudit DeploymentDecisionAuditPreparer,
) error {
	if s == nil || s.txExecutor == nil || prepareAudit == nil {
		return fmt.Errorf("atomic deployment decision and audit are not configured")
	}
	var intent *domain.DeploymentIntent
	stateChanged := approve
	if err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Intents == nil || repos.State == nil || repos.NostrEvents == nil {
			return fmt.Errorf("deployment decision transaction repositories are not configured")
		}
		var err error
		intent, err = repos.Intents.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if intent == nil {
			return fmt.Errorf("deployment intent %s not found", id)
		}
		txRegistry := *s
		txRegistry.intents = repos.Intents
		txRegistry.state = repos.State
		txRegistry.publisher = &events.NoopPublisher{}
		txRegistry.txExecutor = nil
		if approve {
			err = txRegistry.ApproveDeploymentIntent(ctx, id)
		} else {
			before, stateErr := repos.State.Get(ctx, intent.ServiceID, intent.EnvironmentID)
			if stateErr != nil {
				return stateErr
			}
			stateChanged = before != nil && before.DesiredIntentID != nil && *before.DesiredIntentID == id
			err = txRegistry.RejectDeploymentIntent(ctx, id)
		}
		if err != nil {
			return err
		}
		audit, err := prepareAudit()
		if err != nil {
			return fmt.Errorf("prepare deployment approval audit: %w", err)
		}
		inserted, err := repos.NostrEvents.Record(ctx, audit)
		if err != nil {
			return fmt.Errorf("persist deployment approval audit: %w", err)
		}
		if !inserted {
			return fmt.Errorf("deployment approval audit event conflicts with existing outbox evidence")
		}
		return nil
	}); err != nil {
		return err
	}
	if stateChanged {
		s.publisher.Publish(ctx, events.Event{
			Type:     events.EventEnvironmentServiceStateChanged,
			EntityID: intent.ServiceID.String() + ":" + intent.EnvironmentID.String(),
			Data: events.ResourceData{
				ServiceID: intent.ServiceID.String(), EnvironmentID: intent.EnvironmentID.String(),
				ArtifactID: intent.ArtifactID.String(), IntentID: id.String(),
			},
		})
	}
	eventType := events.EventDeploymentIntentRejected
	if approve {
		eventType = events.EventDeploymentIntentApproved
	}
	s.publisher.Publish(ctx, events.Event{
		Type: eventType, EntityID: id.String(),
		Data: events.ResourceData{
			ServiceID: intent.ServiceID.String(), EnvironmentID: intent.EnvironmentID.String(),
			ArtifactID: intent.ArtifactID.String(), IntentID: id.String(),
		},
	})
	return nil
}

// CreateDeploymentIntent creates a new deployment intent and updates the environment service state.
func (s *RegistryService) CreateDeploymentIntent(ctx context.Context, di *domain.DeploymentIntent) error {
	// Validate referenced entities exist.
	svc, err := s.services.GetByID(ctx, di.ServiceID)
	if err != nil {
		return fmt.Errorf("looking up service: %w", err)
	}
	if svc == nil {
		return fmt.Errorf("service %s not found", di.ServiceID)
	}

	env, err := s.environments.GetByID(ctx, di.EnvironmentID)
	if err != nil {
		return fmt.Errorf("looking up environment: %w", err)
	}
	if env == nil {
		return fmt.Errorf("environment %s not found", di.EnvironmentID)
	}

	artifact, err := s.artifacts.GetByID(ctx, di.ArtifactID)
	if err != nil {
		return fmt.Errorf("looking up artifact: %w", err)
	}
	if artifact == nil {
		return fmt.Errorf("artifact %s not found", di.ArtifactID)
	}
	if (svc.OrgID != uuid.Nil || env.OrgID != uuid.Nil) && svc.OrgID != env.OrgID {
		return fmt.Errorf("service and environment must belong to the same organization")
	}
	if artifact.ServiceID != di.ServiceID {
		return fmt.Errorf("artifact %s does not belong to service %s", di.ArtifactID, di.ServiceID)
	}

	// Creation never accepts caller-supplied approval for protected environments;
	// approval must be recorded through ApproveDeploymentIntent after the intent exists.
	if env.Protected {
		di.ApprovalStatus = domain.ApprovalStatusPending
		di.Status = domain.IntentStatusPending
	} else {
		di.ApprovalStatus = domain.ApprovalStatusNotRequired
		if di.Status == "" {
			di.Status = domain.IntentStatusApproved
		}
	}

	if err := s.intents.Create(ctx, di); err != nil {
		return err
	}

	// Pending or rejected intents are proposals only. Desired state advances
	// exclusively after the intent has crossed the authorization boundary.
	if di.Status == domain.IntentStatusApproved {
		if err := s.advanceDesiredStateForIntent(ctx, di); err != nil {
			return err
		}
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentIntentCreated,
		EntityID: di.ID.String(),
		Data:     di,
	})
	if di.Status == domain.IntentStatusApproved {
		s.publisher.Publish(ctx, events.Event{
			Type:     events.EventDeploymentIntentApproved,
			EntityID: di.ID.String(),
			Data: events.ResourceData{
				ServiceID:     di.ServiceID.String(),
				EnvironmentID: di.EnvironmentID.String(),
				ArtifactID:    di.ArtifactID.String(),
				IntentID:      di.ID.String(),
			},
		})
	}

	s.logger.Info("deployment intent created",
		zap.String("intent_id", di.ID.String()),
		zap.String("service", svc.Name),
		zap.String("environment", env.Name),
		zap.String("artifact", artifact.ImageDigest),
	)
	return nil
}

func (s *RegistryService) advanceDesiredStateForIntent(ctx context.Context, di *domain.DeploymentIntent) error {
	if di == nil || di.Status != domain.IntentStatusApproved {
		return fmt.Errorf("only approved deployment intents may advance desired state")
	}
	// Preserve observation/run/health fields that the authorized intent does not
	// change; the repository Upsert replaces every column.
	state, err := s.loadOrInitEnvironmentServiceState(ctx, di.ServiceID, di.EnvironmentID)
	if err != nil {
		return fmt.Errorf("load environment service state for authorized deployment: %w", err)
	}
	state.DeploymentUnitID = deploymentUnitIDForRunIntent(nil, di)
	state.DesiredArtifactID = &di.ArtifactID
	state.DesiredIntentID = &di.ID
	state.DesiredRuntimeState = di.DesiredState
	state.DesiredHash = di.DesiredHash
	state.DriftStatus = domain.DriftStatusDeploying
	if err := s.state.Upsert(ctx, state); err != nil {
		return fmt.Errorf("advance authorized deployment desired state: %w", err)
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: di.ServiceID.String() + ":" + di.EnvironmentID.String(),
		Data: events.ResourceData{
			ServiceID: di.ServiceID.String(), EnvironmentID: di.EnvironmentID.String(),
			ArtifactID: di.ArtifactID.String(), IntentID: di.ID.String(),
		},
	})
	return nil
}

func (s *RegistryService) GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return s.intents.GetByID(ctx, id)
}

func (s *RegistryService) UpdateDeploymentIntentDesiredState(ctx context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	return s.intents.UpdateDesiredState(ctx, id, desiredState, desiredHash)
}

type releasePromotionIntentLookup interface {
	GetByReleasePromotionKey(context.Context, uuid.UUID, uuid.UUID, string, string) (*domain.DeploymentIntent, error)
}

func (s *RegistryService) GetDeploymentIntentByReleasePromotionKey(
	ctx context.Context,
	serviceID, environmentID uuid.UUID,
	requester, idempotencyKey string,
) (*domain.DeploymentIntent, error) {
	if lookup, ok := s.intents.(releasePromotionIntentLookup); ok {
		return lookup.GetByReleasePromotionKey(ctx, serviceID, environmentID, requester, idempotencyKey)
	}
	intents, err := s.intents.ListByServiceEnv(ctx, serviceID, environmentID, 10000, 0)
	if err != nil {
		return nil, err
	}
	for index := range intents {
		intent := &intents[index]
		if intent.Metadata["release_promotion"] == true &&
			intent.Metadata["promotion_requester"] == requester &&
			intent.Metadata["promotion_idempotency_key"] == idempotencyKey {
			return intent, nil
		}
	}
	return nil, nil
}

func (s *RegistryService) ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.intents.ListByServiceEnv(ctx, serviceID, envID, limit, offset)
}

// loadOrInitEnvironmentServiceState returns the current environment service
// state row for load-mutate-store updates, or a fresh row when none exists.
// Callers must go through this (or an equivalent Get) before Upsert, because
// the repository Upsert replaces every column and would otherwise erase
// observation/run/health linkage.
func (s *RegistryService) loadOrInitEnvironmentServiceState(ctx context.Context, serviceID, environmentID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	state, err := s.state.Get(ctx, serviceID, environmentID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if state == nil {
		state = &domain.EnvironmentServiceState{
			ServiceID:     serviceID,
			EnvironmentID: environmentID,
			DriftStatus:   domain.DriftStatusUnknown,
		}
	}
	return state, nil
}

// RestoreEnvironmentServiceStateToDeployedIntent repairs the desired-state read
// model after a route-only deployment fails before becoming the deployed intent.
func (s *RegistryService) RestoreEnvironmentServiceStateToDeployedIntent(ctx context.Context, intent *domain.DeploymentIntent) error {
	if intent == nil || intent.Status != domain.IntentStatusDeployed || intent.DesiredState == nil {
		return fmt.Errorf("a deployed intent with desired state is required for state restoration")
	}
	artifactID := intent.ArtifactID
	intentID := intent.ID
	state, err := s.loadOrInitEnvironmentServiceState(ctx, intent.ServiceID, intent.EnvironmentID)
	if err != nil {
		return fmt.Errorf("loading environment service state for restoration to deployed intent %s: %w", intent.ID, err)
	}
	// Restore only the desired-state linkage; preserve observation, run, and
	// reconcile health fields the failed route attach never touched.
	state.DeploymentUnitID = deploymentUnitIDForRunIntent(nil, intent)
	state.DesiredArtifactID = &artifactID
	state.DesiredIntentID = &intentID
	state.DesiredRuntimeState = intent.DesiredState
	state.DesiredHash = intent.DesiredHash
	state.DriftStatus = domain.DriftStatusInSync
	if err := s.state.Upsert(ctx, state); err != nil {
		return fmt.Errorf("restoring environment service state to deployed intent %s: %w", intent.ID, err)
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: intent.ServiceID.String() + ":" + intent.EnvironmentID.String(),
		Data: events.ResourceData{
			ServiceID:     intent.ServiceID.String(),
			EnvironmentID: intent.EnvironmentID.String(),
			ArtifactID:    intent.ArtifactID.String(),
			IntentID:      intent.ID.String(),
		},
	})
	return nil
}

type deploymentIntentDecisionTransitioner interface {
	TransitionDecision(context.Context, uuid.UUID, domain.ApprovalStatus, domain.DeploymentIntentStatus) (bool, error)
}

type approvedDeploymentIntentWithoutRunLister interface {
	ListApprovedWithoutRuns(context.Context) ([]domain.DeploymentIntent, error)
}

func (s *RegistryService) ApproveDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	di, err := s.intents.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if di == nil {
		return fmt.Errorf("deployment intent %s not found", id)
	}

	// Guard: only pending-approval intents can be approved.
	if di.ApprovalStatus != domain.ApprovalStatusPending {
		return fmt.Errorf("cannot approve intent %s: approval status is %s, expected %s",
			id, di.ApprovalStatus, domain.ApprovalStatusPending)
	}
	if di.Status != domain.IntentStatusPending {
		return fmt.Errorf("cannot approve intent %s: status is %s, expected %s",
			id, di.Status, domain.IntentStatusPending)
	}

	if err := s.transitionDeploymentIntentDecision(ctx, id, domain.ApprovalStatusApproved, domain.IntentStatusApproved); err != nil {
		return err
	}
	authorized := *di
	authorized.ApprovalStatus = domain.ApprovalStatusApproved
	authorized.Status = domain.IntentStatusApproved
	if err := s.advanceDesiredStateForIntent(ctx, &authorized); err != nil {
		return err
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentIntentApproved,
		EntityID: id.String(),
		Data:     events.ResourceData{ServiceID: di.ServiceID.String(), EnvironmentID: di.EnvironmentID.String(), ArtifactID: di.ArtifactID.String(), IntentID: id.String()},
	})
	return nil
}

func (s *RegistryService) RejectDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	di, err := s.intents.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if di == nil {
		return fmt.Errorf("deployment intent %s not found", id)
	}

	// Guard: only pending-approval intents can be rejected.
	if di.ApprovalStatus != domain.ApprovalStatusPending {
		return fmt.Errorf("cannot reject intent %s: approval status is %s, expected %s",
			id, di.ApprovalStatus, domain.ApprovalStatusPending)
	}
	if di.Status != domain.IntentStatusPending {
		return fmt.Errorf("cannot reject intent %s: status is %s, expected %s",
			id, di.Status, domain.IntentStatusPending)
	}

	if err := s.transitionDeploymentIntentDecision(ctx, id, domain.ApprovalStatusRejected, domain.IntentStatusRejected); err != nil {
		return err
	}
	if err := s.repairStateAfterRejectedIntent(ctx, di); err != nil {
		return err
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentIntentRejected,
		EntityID: id.String(),
		Data:     events.ResourceData{ServiceID: di.ServiceID.String(), EnvironmentID: di.EnvironmentID.String(), ArtifactID: di.ArtifactID.String(), IntentID: id.String()},
	})
	return nil
}

func (s *RegistryService) transitionDeploymentIntentDecision(
	ctx context.Context,
	id uuid.UUID,
	approval domain.ApprovalStatus,
	status domain.DeploymentIntentStatus,
) error {
	if transitioner, ok := s.intents.(deploymentIntentDecisionTransitioner); ok {
		changed, err := transitioner.TransitionDecision(ctx, id, approval, status)
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("deployment intent %s decision changed concurrently: %w", id, repository.ErrConflict)
		}
		return nil
	}
	if err := s.intents.UpdateApproval(ctx, id, approval); err != nil {
		return err
	}
	return s.intents.UpdateStatus(ctx, id, status)
}

func (s *RegistryService) repairStateAfterRejectedIntent(ctx context.Context, rejected *domain.DeploymentIntent) error {
	state, err := s.state.Get(ctx, rejected.ServiceID, rejected.EnvironmentID)
	if err != nil {
		return fmt.Errorf("getting state for rejected intent repair: %w", err)
	}
	if state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != rejected.ID {
		return nil
	}

	intents, err := s.intents.ListByServiceEnv(ctx, rejected.ServiceID, rejected.EnvironmentID, 50, 0)
	if err != nil {
		return fmt.Errorf("listing intents for rejected intent repair: %w", err)
	}

	state.DesiredArtifactID = nil
	state.DesiredIntentID = nil
	state.DriftStatus = domain.DriftStatusUnknown
	for i := range intents {
		candidate := intents[i]
		if candidate.ID == rejected.ID {
			continue
		}
		switch candidate.Status {
		case domain.IntentStatusDeployed, domain.IntentStatusDeploying, domain.IntentStatusApproved:
			state.DesiredArtifactID = &candidate.ArtifactID
			state.DesiredIntentID = &candidate.ID
			break
		default:
			continue
		}
		if state.DesiredIntentID != nil {
			break
		}
	}

	if err := s.state.Upsert(ctx, state); err != nil {
		return fmt.Errorf("repairing state after rejected intent: %w", err)
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: rejected.ServiceID.String() + ":" + rejected.EnvironmentID.String(),
		Data: events.ResourceData{
			ServiceID:     rejected.ServiceID.String(),
			EnvironmentID: rejected.EnvironmentID.String(),
		},
	})
	return nil
}

// --- Deployment Run operations ---

type nonTerminalDeploymentRunLister interface {
	ListNonTerminal(context.Context) ([]domain.DeploymentRun, error)
}

type deploymentRunApplyMetadataUpdater interface {
	UpdateApplyMetadata(context.Context, uuid.UUID, map[string]any) error
}

func (s *RegistryService) CreateDeploymentRun(ctx context.Context, dr *domain.DeploymentRun) error {
	// Guard: verify the intent exists and is in an approved state.
	intent, err := s.intents.GetByID(ctx, dr.DeploymentIntentID)
	if err != nil {
		return fmt.Errorf("looking up intent for run: %w", err)
	}
	if intent == nil {
		return fmt.Errorf("deployment intent %s not found", dr.DeploymentIntentID)
	}
	if intent.Status != domain.IntentStatusApproved && intent.Status != domain.IntentStatusDeploying {
		return fmt.Errorf("cannot create run for intent %s: status is %s, must be %s or %s",
			dr.DeploymentIntentID, intent.Status, domain.IntentStatusApproved, domain.IntentStatusDeploying)
	}

	if dr.DeploymentUnitID == nil {
		dr.DeploymentUnitID = deploymentUnitIDForRunIntent(nil, intent)
	}
	if dr.Status == "" {
		dr.Status = domain.RunStatusQueued
	}
	if err := s.runs.Create(ctx, dr); err != nil {
		return err
	}

	// Update intent to deploying.
	if err := s.intents.UpdateStatus(ctx, dr.DeploymentIntentID, domain.IntentStatusDeploying); err != nil {
		s.logger.Error("failed to update intent status to deploying",
			zap.String("intent_id", dr.DeploymentIntentID.String()),
			zap.Error(err),
		)
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentRunCreated,
		EntityID: dr.ID.String(),
		Data:     dr,
	})
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentRunStatusChanged,
		EntityID: dr.ID.String(),
		Data:     events.ResourceData{IntentID: dr.DeploymentIntentID.String(), RunID: dr.ID.String()},
	})
	return nil
}

func (s *RegistryService) GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return s.runs.GetByID(ctx, id)
}

func (s *RegistryService) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	return s.runs.ListByIntent(ctx, intentID)
}

// UpdateDeploymentRunApplyMetadata persists non-secret lifecycle progress and
// republishes the canonical run projection.
func (s *RegistryService) UpdateDeploymentRunApplyMetadata(ctx context.Context, id uuid.UUID, metadata map[string]any) error {
	updater, ok := s.runs.(deploymentRunApplyMetadataUpdater)
	if !ok {
		return fmt.Errorf("deployment run repository does not support apply metadata updates")
	}
	if err := updater.UpdateApplyMetadata(ctx, id, metadata); err != nil {
		return err
	}
	run, err := s.runs.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("deployment run %s not found", id)
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentRunStatusChanged,
		EntityID: id.String(),
		Data:     events.ResourceData{IntentID: run.DeploymentIntentID.String(), RunID: id.String()},
	})
	return nil
}

// ListApprovedDeploymentIntentsWithoutRuns returns durable approved intents
// whose execution did not create a run before process interruption.
func (s *RegistryService) ListApprovedDeploymentIntentsWithoutRuns(ctx context.Context) ([]domain.DeploymentIntent, error) {
	if s.intents == nil {
		return nil, nil
	}
	lister, ok := s.intents.(approvedDeploymentIntentWithoutRunLister)
	if !ok {
		return nil, nil
	}
	return lister.ListApprovedWithoutRuns(ctx)
}

// ListNonTerminalDeploymentRuns returns persisted queued/running runs when the
// configured repository supports startup recovery.
func (s *RegistryService) ListNonTerminalDeploymentRuns(ctx context.Context) ([]domain.DeploymentRun, error) {
	if s.runs == nil {
		return nil, nil
	}
	lister, ok := s.runs.(nonTerminalDeploymentRunLister)
	if !ok {
		return nil, fmt.Errorf("deployment run repository does not support non-terminal recovery")
	}
	return lister.ListNonTerminal(ctx)
}

func deploymentUnitIDForRunIntent(run *domain.DeploymentRun, intent *domain.DeploymentIntent) *uuid.UUID {
	candidates := []*uuid.UUID{}
	if run != nil {
		candidates = append(candidates, run.DeploymentUnitID)
	}
	if intent != nil {
		candidates = append(candidates, intent.DeploymentUnitID)
		if intent.DesiredState != nil {
			candidates = append(candidates, intent.DesiredState.DeploymentUnitID)
		}
	}
	for _, candidate := range candidates {
		if candidate != nil && *candidate != uuid.Nil {
			id := *candidate
			return &id
		}
	}
	return nil
}

// CompleteDeploymentRun marks a deployment run as completed and updates related state.
func (s *RegistryService) CompleteDeploymentRun(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	// Validate the status is a terminal status.
	if !isTerminalRunStatus(status) {
		return fmt.Errorf("cannot complete run with non-terminal status: %s", status)
	}

	dr, err := s.runs.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if dr == nil {
		return fmt.Errorf("deployment run %s not found", id)
	}

	// Guard: only running or queued runs can be completed.
	if dr.Status != domain.RunStatusQueued && dr.Status != domain.RunStatusRunning {
		return fmt.Errorf("cannot complete run %s: status is already %s", id, dr.Status)
	}

	if err := s.runs.UpdateStatus(ctx, id, status, exitCode); err != nil {
		return err
	}

	// Update the parent intent status based on run outcome.
	var intentStatus domain.DeploymentIntentStatus
	switch status {
	case domain.RunStatusSucceeded:
		intentStatus = domain.IntentStatusDeployed
	case domain.RunStatusFailed, domain.RunStatusTimeout:
		intentStatus = domain.IntentStatusFailed
	case domain.RunStatusCancelled:
		intentStatus = domain.IntentStatusFailed
	default:
		return nil
	}

	if err := s.intents.UpdateStatus(ctx, dr.DeploymentIntentID, intentStatus); err != nil {
		s.logger.Error("failed to update intent status after run completion",
			zap.String("intent_id", dr.DeploymentIntentID.String()),
			zap.String("intent_status", string(intentStatus)),
			zap.Error(err),
		)
		return fmt.Errorf("updating intent status after run completion: %w", err)
	}

	// If succeeded, update environment service state.
	if status == domain.RunStatusSucceeded {
		intent, err := s.intents.GetByID(ctx, dr.DeploymentIntentID)
		if err != nil {
			s.logger.Error("failed to fetch intent for state update after successful run",
				zap.String("intent_id", dr.DeploymentIntentID.String()),
				zap.Error(err),
			)
			return fmt.Errorf("fetching intent for state update: %w", err)
		}
		if intent != nil {
			now := time.Now().UTC()
			state, stateErr := s.state.Get(ctx, intent.ServiceID, intent.EnvironmentID)
			if stateErr != nil && !errors.Is(stateErr, repository.ErrNotFound) {
				return fmt.Errorf("loading environment state after successful run: %w", stateErr)
			}
			if state == nil || errors.Is(stateErr, repository.ErrNotFound) {
				state = &domain.EnvironmentServiceState{
					ServiceID:     intent.ServiceID,
					EnvironmentID: intent.EnvironmentID,
					DriftStatus:   domain.DriftStatusUnknown,
				}
			}
			routeOnlyCurrentIntent := state.DesiredIntentID != nil && *state.DesiredIntentID == intent.ID
			state.DeploymentUnitID = deploymentUnitIDForRunIntent(dr, intent)
			state.DesiredArtifactID = &intent.ArtifactID
			state.DesiredIntentID = &intent.ID
			state.DesiredRuntimeState = intent.DesiredState
			state.DesiredHash = intent.DesiredHash
			if domain.IsRouteOnlyDeploymentRun(dr) && routeOnlyCurrentIntent {
				// Route changes are not runtime-observable: Compose containers retain
				// the pre-route desired-hash label and Docker observations have no route
				// hash. Mark success immediately; later observations remain converged by
				// accepting the canonical route-free runtime hash.
				state.DriftStatus = domain.DriftStatusInSync
			}
			state.LastSuccessfulRunID = &id
			state.LastReconciledAt = &now
			if err := s.state.Upsert(ctx, state); err != nil {
				s.logger.Error("failed to upsert environment service state after successful run",
					zap.String("service_id", intent.ServiceID.String()),
					zap.String("environment_id", intent.EnvironmentID.String()),
					zap.Error(err),
				)
				return fmt.Errorf("upserting environment state after successful run: %w", err)
			}
			s.publisher.Publish(ctx, events.Event{
				Type:     events.EventEnvironmentServiceStateChanged,
				EntityID: intent.ServiceID.String() + ":" + intent.EnvironmentID.String(),
				Data: events.ResourceData{
					ServiceID:     intent.ServiceID.String(),
					EnvironmentID: intent.EnvironmentID.String(),
					ArtifactID:    intent.ArtifactID.String(),
					IntentID:      intent.ID.String(),
					RunID:         id.String(),
				},
			})
		}
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentRunCompleted,
		EntityID: id.String(),
		Data:     map[string]string{"status": string(status)},
	})
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentRunStatusChanged,
		EntityID: id.String(),
		Data:     events.ResourceData{IntentID: dr.DeploymentIntentID.String(), RunID: id.String()},
	})
	return nil
}

// --- Runtime Observation ---

type orderedObservationStateUpserter interface {
	UpsertObservation(context.Context, *domain.EnvironmentServiceState) (bool, error)
}

func (s *RegistryService) RecordObservation(ctx context.Context, obs *domain.RuntimeObservation) error {
	normalizeRuntimeObservationHash(obs)
	latest, latestErr := s.observations.GetLatest(ctx, obs.ServiceID, obs.EnvironmentID)
	if latestErr != nil {
		return fmt.Errorf("getting latest runtime observation: %w", latestErr)
	}
	if err := s.observations.Create(ctx, obs); err != nil {
		return err
	}
	if observationIsOlder(obs, latest) {
		s.publisher.Publish(ctx, events.Event{
			Type:     events.EventRuntimeObservation,
			EntityID: obs.ID.String(),
			Data:     obs,
		})
		return nil
	}

	// Update environment service state with new observation.
	state, err := s.state.Get(ctx, obs.ServiceID, obs.EnvironmentID)
	if err != nil {
		s.logger.Error("failed to get environment service state for observation",
			zap.String("service_id", obs.ServiceID.String()),
			zap.String("environment_id", obs.EnvironmentID.String()),
			zap.Error(err),
		)
		return fmt.Errorf("getting environment service state: %w", err)
	}
	if state == nil {
		state = &domain.EnvironmentServiceState{
			ServiceID:     obs.ServiceID,
			EnvironmentID: obs.EnvironmentID,
		}
	}
	previousDrift := state.DriftStatus
	state.CurrentObservationID = &obs.ID

	// Determine drift. Desired-state-managed rows compare hashes when both are
	// present and fall back to the desired artifact digest for pre-stamping
	// runtimes that cannot report the desired hash.
	desiredHash := ""
	observedHash := observedStateHash(obs)
	desiredDigest := ""
	observedDigest := domain.NormalizeImageDigest(obs.ObservedImageDigest)
	decisionBranch := "early-out"
	if desiredStateManaged(state) {
		desiredHash = desiredStateHash(state)
		if desiredHash != "" && observedHash != "" {
			decisionBranch = "hash-compare"
			if desiredHash != observedHash && (state.DesiredRuntimeState == nil || !state.DesiredRuntimeState.MatchesRuntimeConvergenceHash(observedHash)) {
				state.DriftStatus = domain.DriftStatusDrifted
			} else if observationHealthOK(obs.HealthStatus) {
				state.DriftStatus = domain.DriftStatusInSync
			} else if obs.HealthStatus == domain.HealthStatusStarting {
				state.DriftStatus = domain.DriftStatusDeploying
			} else {
				state.DriftStatus = domain.DriftStatusDrifted
			}
		} else if observedHash == "" && state.DesiredArtifactID != nil {
			decisionBranch = "digest-fallback"
			desiredDigest = driftdecision.DesiredArtifactDigest(ctx, s.artifacts, state.DesiredArtifactID, s.logger)
			state.DriftStatus = domain.ArtifactDigestDriftStatus(desiredDigest, observedDigest, obs.HealthStatus, domain.DriftStatusDeploying)
		} else {
			state.DriftStatus = domain.DriftStatusUnknown
		}
	} else if state.DesiredArtifactID != nil {
		decisionBranch = "digest-fallback"
		desiredDigest = driftdecision.DesiredArtifactDigest(ctx, s.artifacts, state.DesiredArtifactID, s.logger)
		state.DriftStatus = domain.ArtifactDigestDriftStatus(desiredDigest, observedDigest, obs.HealthStatus, domain.DriftStatusDeploying)
	}

	now := time.Now().UTC()
	state.LastReconciledAt = &now
	advanced := true
	if ordered, ok := s.state.(orderedObservationStateUpserter); ok {
		advanced, err = ordered.UpsertObservation(ctx, state)
	} else {
		err = s.state.Upsert(ctx, state)
	}
	if err != nil {
		return err
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventRuntimeObservation,
		EntityID: obs.ID.String(),
		Data:     obs,
	})
	if !advanced {
		return nil
	}
	if state.DriftStatus != domain.DriftStatusInSync && state.DriftStatus != previousDrift {
		if desiredDigest == "" && state.DesiredArtifactID != nil {
			desiredDigest = driftdecision.DesiredArtifactDigest(ctx, s.artifacts, state.DesiredArtifactID, s.logger)
		}
		driftdecision.Log(s.logger, driftdecision.LogInput{
			Service: state.ServiceID.String(), Environment: state.EnvironmentID.String(),
			ServiceID: state.ServiceID, EnvironmentID: state.EnvironmentID,
			Status: state.DriftStatus, PreviousStatus: previousDrift, Branch: decisionBranch,
			DesiredHash: desiredHash, ObservedHash: observedHash,
			DesiredDigest: desiredDigest, ObservedDigest: observedDigest,
			Health: obs.HealthStatus, ObservationID: obs.ID, Source: obs.Source,
		})
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: obs.ServiceID.String() + ":" + obs.EnvironmentID.String(),
		Data: events.ResourceData{
			ServiceID:     obs.ServiceID.String(),
			EnvironmentID: obs.EnvironmentID.String(),
		},
	})
	return nil
}

func (s *RegistryService) GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.observations.GetLatest(ctx, serviceID, envID)
}

func normalizeRuntimeObservationHash(obs *domain.RuntimeObservation) {
	if obs == nil {
		return
	}
	if obs.NormalizedState != nil && obs.NormalizedState.ObservationHash == "" {
		obs.NormalizedState.ComputeObservationHash()
	}
	if obs.NormalizedHash == "" && obs.NormalizedState != nil {
		obs.NormalizedHash = obs.NormalizedState.ObservationHash
	}
}

func desiredStateManaged(state *domain.EnvironmentServiceState) bool {
	return state != nil && (state.DesiredHash != "" || state.DesiredRuntimeState != nil)
}

func desiredStateHash(state *domain.EnvironmentServiceState) string {
	if state == nil {
		return ""
	}
	if state.DesiredHash != "" {
		return state.DesiredHash
	}
	if state.DesiredRuntimeState == nil {
		return ""
	}
	if state.DesiredRuntimeState.DesiredHash != "" {
		state.DesiredHash = state.DesiredRuntimeState.DesiredHash
		return state.DesiredHash
	}
	state.DesiredHash = state.DesiredRuntimeState.ComputeDesiredHash()
	return state.DesiredHash
}

func observedStateHash(obs *domain.RuntimeObservation) string {
	if obs == nil {
		return ""
	}
	if obs.NormalizedState != nil && obs.NormalizedState.ObservationHash != "" {
		return obs.NormalizedState.ObservationHash
	}
	return obs.NormalizedHash
}

func observationHealthOK(health domain.HealthStatus) bool {
	return health == domain.HealthStatusHealthy
}

func observationIsOlder(incoming, current *domain.RuntimeObservation) bool {
	if incoming == nil || current == nil {
		return false
	}
	if incoming.ObservedAt.Before(current.ObservedAt) {
		return true
	}
	if incoming.ObservedAt.After(current.ObservedAt) {
		return false
	}
	return incoming.ID.String() <= current.ID.String()
}

// --- State queries ---

func (s *RegistryService) GetEnvironmentServiceState(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return s.state.Get(ctx, serviceID, envID)
}

func (s *RegistryService) ListEnvironmentStates(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return s.state.ListByEnvironment(ctx, envID)
}

func (s *RegistryService) ListDriftedStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return s.state.ListDrifted(ctx)
}

func (s *RegistryService) ListAllStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return s.state.ListAll(ctx)
}

// isTerminalRunStatus returns true if the status represents a completed deployment run.
func isTerminalRunStatus(s domain.DeploymentRunStatus) bool {
	switch s {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

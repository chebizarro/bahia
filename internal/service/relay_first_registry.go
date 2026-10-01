package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// RelayFirstStatePublisher publishes the canonical cp-state record of a
// registry entity (or its tombstone) and returns nil only once a relay holds
// it. internal/adapters/nostr.RelayFirstStatePublisher is the implementation:
// it builds the record with the projector's own builders and shares the
// projector's per-coordinate created_at floor and dedupe memory, so the
// relay-first record and the projection of the same state are identical and
// signed once (bahia-irsry.41).
type RelayFirstStatePublisher interface {
	PublishServiceRegistry(ctx context.Context, svc *domain.Service, deleted bool) error
	PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment, deleted bool) error
}

// RelayFirstRegistry wraps RegistryService so canonical relay publication succeeds before local cache writes.
type RelayFirstRegistry struct {
	delegate  *RegistryService
	publisher RelayFirstStatePublisher
	logger    *zap.Logger
	// createLocks serializes check-publish-store for creates of the same id
	// in this process, so two concurrent creates with one id and different
	// content cannot both publish to the coordinate (bahia-irsry.35).
	createLocks [64]sync.Mutex
}

func (r *RelayFirstRegistry) lockCreate(id uuid.UUID) func() {
	lock := &r.createLocks[int(id[15])%len(r.createLocks)]
	lock.Lock()
	return lock.Unlock
}

func NewRelayFirstRegistry(delegate *RegistryService, publisher RelayFirstStatePublisher, logger *zap.Logger) *RelayFirstRegistry {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RelayFirstRegistry{delegate: delegate, publisher: publisher, logger: logger}
}

func (r *RelayFirstRegistry) CreateService(ctx context.Context, svc *domain.Service) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	// The id (client-minted, or minted here when absent) is fixed before
	// publication so the relay coordinate and the cached row agree. An
	// idempotent retry publishes nothing; a conflicting one must not
	// overwrite the existing coordinate (bahia-irsry.35).
	prepareServiceCreate(svc)
	defer r.lockCreate(svc.ID)()
	if replay, err := r.delegate.replayServiceCreate(ctx, svc); err != nil || replay {
		return err
	}
	if err := r.publishServiceRegistry(ctx, svc, false); err != nil {
		return err
	}
	return r.delegate.CreateService(ctx, svc)
}

func (r *RelayFirstRegistry) UpdateService(ctx context.Context, svc *domain.Service) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	normalizeServiceRepositoryForWrite(svc)
	if err := r.publishServiceRegistry(ctx, svc, false); err != nil {
		return err
	}
	return r.delegate.UpdateService(ctx, svc)
}

// ImportObservedArtifact delegates the operator live-import path. It records
// governed build/artifact lineage for an already-running image and makes no
// desired-state change, so there is no relay-first projection to coordinate.
func (r *RelayFirstRegistry) ImportObservedArtifact(ctx context.Context, in ImportObservedArtifactInput) (*ImportObservedArtifactResult, error) {
	if r.delegate == nil {
		return nil, fmt.Errorf("registry delegate is not configured")
	}
	return r.delegate.ImportObservedArtifact(ctx, in)
}

// UpdateServiceWithExpectedRevision checks the persisted revision under lock
// before publishing and committing the signed service update.
func (r *RelayFirstRegistry) UpdateServiceWithExpectedRevision(ctx context.Context, svc *domain.Service, expectedUpdatedAt time.Time) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if svc == nil {
		return fmt.Errorf("service is nil")
	}
	return r.delegate.updateServiceWithExpectedRevision(ctx, svc, expectedUpdatedAt, func() error {
		return r.publishServiceRegistry(ctx, svc, false)
	})
}

func (r *RelayFirstRegistry) DeleteService(ctx context.Context, id uuid.UUID, force bool) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if !force {
		if pgRepo, ok := r.delegate.services.(*repository.PgServiceRepository); ok {
			builds, artifacts, intents, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			if total := builds + artifacts + intents; total > 0 {
				return fmt.Errorf("service has dependent resources (%d builds, %d artifacts, %d deployment intents); use force=true to cascade delete or remove dependents first", builds, artifacts, intents)
			}
		}
	}
	if err := r.publishServiceRegistry(ctx, &domain.Service{ID: id, UpdatedAt: time.Now().UTC()}, true); err != nil {
		return err
	}
	return r.delegate.DeleteService(ctx, id, force)
}

func (r *RelayFirstRegistry) CreateEnvironment(ctx context.Context, env *domain.Environment) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if err := normalizeAndValidateEnvironmentMutation(env, nil); err != nil {
		return err
	}
	defer r.lockCreate(env.ID)()
	if replay, err := r.delegate.replayEnvironmentCreate(ctx, env, nil); err != nil || replay {
		return err
	}
	if err := r.publishEnvironmentRegistry(ctx, env, false); err != nil {
		return err
	}
	return r.delegate.CreateEnvironment(ctx, env)
}

// CreateEnvironmentWithDeploymentUnits publishes the environment's registry
// record before atomically caching the environment and its units. The record
// is the projector's (control_state_contract.go), which does not carry
// explicit units yet.
func (r *RelayFirstRegistry) CreateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if err := normalizeAndValidateEnvironmentMutation(env, units); err != nil {
		return err
	}
	if units == nil {
		units = []*domain.DeploymentUnit{}
	}
	defer r.lockCreate(env.ID)()
	if replay, err := r.delegate.replayEnvironmentCreate(ctx, env, units); err != nil || replay {
		return err
	}
	if err := r.publishEnvironmentRegistry(ctx, env, false); err != nil {
		return err
	}
	return r.delegate.CreateEnvironmentWithDeploymentUnits(ctx, env, units)
}

func (r *RelayFirstRegistry) UpdateEnvironment(ctx context.Context, env *domain.Environment) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	if err := r.publishEnvironmentRegistry(ctx, env, false); err != nil {
		return err
	}
	return r.delegate.UpdateEnvironment(ctx, env)
}

// UpdateEnvironmentWithDeploymentUnits publishes the environment's registry
// record after the revision has been checked under lock and before atomically
// caching it.
func (r *RelayFirstRegistry) UpdateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, expectedUpdatedAt time.Time) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if err := normalizeAndValidateEnvironmentMutation(env, units); err != nil {
		return err
	}
	return r.delegate.updateEnvironmentWithDeploymentUnits(ctx, env, units, expectedUpdatedAt, func() error {
		return r.publishEnvironmentRegistry(ctx, env, false)
	})
}

func (r *RelayFirstRegistry) DeleteEnvironment(ctx context.Context, id uuid.UUID, force bool) error {
	if r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	if !force {
		if pgRepo, ok := r.delegate.environments.(*repository.PgEnvironmentRepository); ok {
			intents, states, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			if total := intents + states; total > 0 {
				return fmt.Errorf("environment has dependent resources (%d deployment intents, %d state records); use force=true to cascade delete or remove dependents first", intents, states)
			}
		}
	}
	if err := r.publishEnvironmentRegistry(ctx, &domain.Environment{ID: id, UpdatedAt: time.Now().UTC()}, true); err != nil {
		return err
	}
	return r.delegate.DeleteEnvironment(ctx, id, force)
}

// publishServiceRegistry publishes svc as readers will see it once cached:
// the read normalization GetService and ListServices apply (and the projector
// therefore publishes) is applied to a copy, so the relay-first record does
// not differ from the projection of the same row.
func (r *RelayFirstRegistry) publishServiceRegistry(ctx context.Context, svc *domain.Service, deleted bool) error {
	if r.publisher == nil {
		return fmt.Errorf("nostr registry publisher is not configured")
	}
	snapshot := *svc
	if svc.Repository != nil {
		repo := *svc.Repository
		snapshot.Repository = &repo
	}
	normalizeServiceRepositoryForRead(&snapshot)
	if err := r.publisher.PublishServiceRegistry(ctx, &snapshot, deleted); err != nil {
		return fmt.Errorf("publish service registry event: %w", err)
	}
	return nil
}

func (r *RelayFirstRegistry) publishEnvironmentRegistry(ctx context.Context, env *domain.Environment, deleted bool) error {
	if r.publisher == nil {
		return fmt.Errorf("nostr registry publisher is not configured")
	}
	if err := r.publisher.PublishEnvironmentRegistry(ctx, env, deleted); err != nil {
		return fmt.Errorf("publish environment registry event: %w", err)
	}
	return nil
}

func (r *RelayFirstRegistry) GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	return r.delegate.GetService(ctx, id)
}
func (r *RelayFirstRegistry) GetServiceByName(ctx context.Context, name string) (*domain.Service, error) {
	return r.delegate.GetServiceByName(ctx, name)
}
func (r *RelayFirstRegistry) ListServices(ctx context.Context) ([]domain.Service, error) {
	return r.delegate.ListServices(ctx)
}
func (r *RelayFirstRegistry) ListServicesByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Service, error) {
	return r.delegate.ListServicesByOrg(ctx, orgID)
}
func (r *RelayFirstRegistry) GetEnvironment(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.delegate.GetEnvironment(ctx, id)
}
func (r *RelayFirstRegistry) GetEnvironmentByName(ctx context.Context, name string) (*domain.Environment, error) {
	return r.delegate.GetEnvironmentByName(ctx, name)
}
func (r *RelayFirstRegistry) ListEnvironments(ctx context.Context) ([]domain.Environment, error) {
	return r.delegate.ListEnvironments(ctx)
}
func (r *RelayFirstRegistry) ListEnvironmentsByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	return r.delegate.ListEnvironmentsByOrg(ctx, orgID)
}
func (r *RelayFirstRegistry) RegisterBuild(ctx context.Context, b *domain.Build) error {
	return r.delegate.RegisterBuild(ctx, b)
}
func (r *RelayFirstRegistry) GetBuild(ctx context.Context, id uuid.UUID) (*domain.Build, error) {
	return r.delegate.GetBuild(ctx, id)
}
func (r *RelayFirstRegistry) ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	return r.delegate.ListBuilds(ctx, serviceID, limit, offset)
}
func (r *RelayFirstRegistry) UpdateBuildStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error {
	return r.delegate.UpdateBuildStatus(ctx, id, status)
}
func (r *RelayFirstRegistry) RegisterArtifact(ctx context.Context, a *domain.Artifact) error {
	return r.delegate.RegisterArtifact(ctx, a)
}
func (r *RelayFirstRegistry) RegisterVerifiedArtifact(ctx context.Context, a *domain.Artifact, proof ArtifactVerificationProof) error {
	return r.delegate.RegisterVerifiedArtifact(ctx, a, proof)
}
func (r *RelayFirstRegistry) RegisterReleaseArtifact(ctx context.Context, a *domain.Artifact, proof ReleaseArtifactVerificationProof) error {
	return r.delegate.RegisterReleaseArtifact(ctx, a, proof)
}

// RegisterReleaseArtifactWithAudit preserves the atomic build/artifact/audit
// boundary used by Hive-CI registration. Release artifacts have no separate
// relay-first canonical state event; their signed decision evidence is written
// to the delegate's durable Nostr outbox in the same transaction.
func (r *RelayFirstRegistry) RegisterReleaseArtifactWithAudit(
	ctx context.Context,
	build *domain.Build,
	artifact *domain.Artifact,
	proof ReleaseArtifactVerificationProof,
	prepareAudit ReleaseArtifactAuditPreparer,
) error {
	if r == nil || r.delegate == nil {
		return fmt.Errorf("registry delegate is not configured")
	}
	return r.delegate.RegisterReleaseArtifactWithAudit(ctx, build, artifact, proof, prepareAudit)
}
func (r *RelayFirstRegistry) GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return r.delegate.GetArtifact(ctx, id)
}
func (r *RelayFirstRegistry) GetArtifactByDigest(ctx context.Context, repo, digest string) (*domain.Artifact, error) {
	return r.delegate.GetArtifactByDigest(ctx, repo, digest)
}
func (r *RelayFirstRegistry) ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	return r.delegate.ListArtifacts(ctx, serviceID, limit, offset)
}
func (r *RelayFirstRegistry) ListArtifactsByBuild(ctx context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	return r.delegate.ListArtifactsByBuild(ctx, buildID)
}
func (r *RelayFirstRegistry) CreateDeploymentIntent(ctx context.Context, di *domain.DeploymentIntent) error {
	return r.delegate.CreateDeploymentIntent(ctx, di)
}
func (r *RelayFirstRegistry) GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.delegate.GetDeploymentIntent(ctx, id)
}
func (r *RelayFirstRegistry) ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	return r.delegate.ListDeploymentIntents(ctx, serviceID, envID, limit, offset)
}
func (r *RelayFirstRegistry) ApproveDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	return r.delegate.ApproveDeploymentIntent(ctx, id)
}
func (r *RelayFirstRegistry) RejectDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	return r.delegate.RejectDeploymentIntent(ctx, id)
}
func (r *RelayFirstRegistry) CreateDeploymentRun(ctx context.Context, dr *domain.DeploymentRun) error {
	return r.delegate.CreateDeploymentRun(ctx, dr)
}
func (r *RelayFirstRegistry) GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return r.delegate.GetDeploymentRun(ctx, id)
}
func (r *RelayFirstRegistry) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	return r.delegate.ListDeploymentRuns(ctx, intentID)
}
func (r *RelayFirstRegistry) CompleteDeploymentRun(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	return r.delegate.CompleteDeploymentRun(ctx, id, status, exitCode)
}
func (r *RelayFirstRegistry) RecordObservation(ctx context.Context, obs *domain.RuntimeObservation) error {
	return r.delegate.RecordObservation(ctx, obs)
}
func (r *RelayFirstRegistry) GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return r.delegate.GetLatestObservation(ctx, serviceID, envID)
}
func (r *RelayFirstRegistry) GetEnvironmentServiceState(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return r.delegate.GetEnvironmentServiceState(ctx, serviceID, envID)
}
func (r *RelayFirstRegistry) ListEnvironmentStates(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return r.delegate.ListEnvironmentStates(ctx, envID)
}
func (r *RelayFirstRegistry) ListDriftedStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.delegate.ListDriftedStates(ctx)
}
func (r *RelayFirstRegistry) ListAllStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.delegate.ListAllStates(ctx)
}

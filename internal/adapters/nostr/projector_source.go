package nostr

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	projectioncache "github.com/openagentsinc/bahia/internal/service"
)

// ProjectorSource abstracts where the projector reads snapshot state from.
// DBProjectorSource preserves the old DB-backed model; CacheProjectorSource reads
// repositories populated by RelayProjectionCache from canonical relay events.
type ProjectorSource interface {
	ListServices(ctx context.Context) ([]ProjectorServiceSnapshot, error)
	ListEnvironments(ctx context.Context) ([]ProjectorEnvironmentSnapshot, error)
	ListStates(ctx context.Context) ([]ProjectorStateSnapshot, error)
	GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error)
	ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]ProjectorBuildSnapshot, error)
	ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]ProjectorArtifactSnapshot, error)
	ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]ProjectorDeploymentIntentSnapshot, error)
	ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]ProjectorDeploymentRunSnapshot, error)
	ListWorkers(ctx context.Context, status string, limit int) ([]ProjectorWorkerSnapshot, error)
}

type ProjectorServiceSnapshot = domain.Service
type ProjectorEnvironmentSnapshot = domain.Environment
type ProjectorStateSnapshot = domain.EnvironmentServiceState
type ProjectorBuildSnapshot = domain.Build
type ProjectorArtifactSnapshot = domain.Artifact
type ProjectorDeploymentIntentSnapshot = domain.DeploymentIntent
type ProjectorDeploymentRunSnapshot = domain.DeploymentRun
type ProjectorWorkerSnapshot = domain.Worker

// ProjectorSourceRepositories are the read repositories needed to assemble a snapshot.
type ProjectorSourceRepositories struct {
	Services     repository.ServiceRepository
	Environments repository.EnvironmentRepository
	States       repository.EnvironmentServiceStateRepository
	Observations repository.RuntimeObservationRepository
	Builds       repository.BuildRepository
	Artifacts    repository.ArtifactRepository
	Intents      repository.DeploymentIntentRepository
	Runs         repository.DeploymentRunRepository
	Workers      repository.WorkerRepository
}

// DBProjectorSource wraps repository interfaces to implement ProjectorSource.
type DBProjectorSource struct {
	ProjectorSourceRepositories
}

func NewDBProjectorSource(repos ProjectorSourceRepositories) *DBProjectorSource {
	return &DBProjectorSource{ProjectorSourceRepositories: repos}
}

func (s *DBProjectorSource) ListServices(ctx context.Context) ([]ProjectorServiceSnapshot, error) {
	if s == nil || s.Services == nil {
		return nil, nil
	}
	return s.Services.List(ctx)
}

func (s *DBProjectorSource) ListEnvironments(ctx context.Context) ([]ProjectorEnvironmentSnapshot, error) {
	if s == nil || s.Environments == nil {
		return nil, nil
	}
	return s.Environments.List(ctx)
}

func (s *DBProjectorSource) ListStates(ctx context.Context) ([]ProjectorStateSnapshot, error) {
	if s == nil || s.States == nil {
		return nil, nil
	}
	return s.States.ListAll(ctx)
}

func (s *DBProjectorSource) GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	if s == nil || s.Observations == nil {
		return nil, nil
	}
	return s.Observations.GetLatest(ctx, serviceID, envID)
}

func (s *DBProjectorSource) ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]ProjectorBuildSnapshot, error) {
	if s == nil || s.Builds == nil {
		return nil, nil
	}
	return s.Builds.ListByService(ctx, serviceID, limit, offset)
}

func (s *DBProjectorSource) ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]ProjectorArtifactSnapshot, error) {
	if s == nil || s.Artifacts == nil {
		return nil, nil
	}
	return s.Artifacts.ListByService(ctx, serviceID, limit, offset)
}

func (s *DBProjectorSource) ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]ProjectorDeploymentIntentSnapshot, error) {
	if s == nil || s.Intents == nil {
		return nil, nil
	}
	return s.Intents.ListByServiceEnv(ctx, serviceID, envID, limit, offset)
}

func (s *DBProjectorSource) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]ProjectorDeploymentRunSnapshot, error) {
	if s == nil || s.Runs == nil {
		return nil, nil
	}
	return s.Runs.ListByIntent(ctx, intentID)
}

func (s *DBProjectorSource) ListWorkers(ctx context.Context, status string, limit int) ([]ProjectorWorkerSnapshot, error) {
	if s == nil || s.Workers == nil {
		return nil, nil
	}
	return s.Workers.List(ctx, status, limit)
}

// CacheProjectorSource reads from repositories maintained by RelayProjectionCache.
type CacheProjectorSource struct {
	cache *projectioncache.RelayProjectionCache
	DBProjectorSource
}

func NewCacheProjectorSource(cache *projectioncache.RelayProjectionCache, repos ProjectorSourceRepositories) *CacheProjectorSource {
	return &CacheProjectorSource{cache: cache, DBProjectorSource: DBProjectorSource{ProjectorSourceRepositories: repos}}
}

func (s *CacheProjectorSource) RelayProjectionCache() *projectioncache.RelayProjectionCache {
	if s == nil {
		return nil
	}
	return s.cache
}

type legacyProjectorSource struct {
	source ProjectionSource
}

func (s legacyProjectorSource) ListServices(ctx context.Context) ([]ProjectorServiceSnapshot, error) {
	return s.source.ListServices(ctx)
}

func (s legacyProjectorSource) ListEnvironments(ctx context.Context) ([]ProjectorEnvironmentSnapshot, error) {
	return s.source.ListEnvironments(ctx)
}

func (s legacyProjectorSource) ListStates(ctx context.Context) ([]ProjectorStateSnapshot, error) {
	return s.source.ListAllStates(ctx)
}

func (s legacyProjectorSource) GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.source.GetLatestObservation(ctx, serviceID, envID)
}

func (s legacyProjectorSource) ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]ProjectorBuildSnapshot, error) {
	return s.source.ListBuilds(ctx, serviceID, limit, offset)
}

func (s legacyProjectorSource) ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]ProjectorArtifactSnapshot, error) {
	return s.source.ListArtifacts(ctx, serviceID, limit, offset)
}

func (s legacyProjectorSource) ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]ProjectorDeploymentIntentSnapshot, error) {
	return s.source.ListDeploymentIntents(ctx, serviceID, envID, limit, offset)
}

func (s legacyProjectorSource) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]ProjectorDeploymentRunSnapshot, error) {
	return s.source.ListDeploymentRuns(ctx, intentID)
}

func (s legacyProjectorSource) ListWorkers(ctx context.Context, status string, limit int) ([]ProjectorWorkerSnapshot, error) {
	return nil, nil
}

func (p *Projector) snapshotSource() ProjectorSource {
	if p.projectorSource != nil {
		return p.projectorSource
	}
	return legacyProjectorSource{source: p.source}
}

func (p *Projector) workerSnapshotSource() ProjectorSource {
	if p.projectorSource != nil {
		return p.projectorSource
	}
	if p.workerSource == nil {
		return nil
	}
	return workerOnlyProjectorSource{source: p.workerSource}
}

type workerOnlyProjectorSource struct {
	source WorkerProjectionSource
}

func (s workerOnlyProjectorSource) ListServices(context.Context) ([]ProjectorServiceSnapshot, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) ListEnvironments(context.Context) ([]ProjectorEnvironmentSnapshot, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) ListStates(context.Context) ([]ProjectorStateSnapshot, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) GetLatestObservation(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) ListBuilds(context.Context, uuid.UUID, int, int) ([]ProjectorBuildSnapshot, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) ListArtifacts(context.Context, uuid.UUID, int, int) ([]ProjectorArtifactSnapshot, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) ListDeploymentIntents(context.Context, uuid.UUID, uuid.UUID, int, int) ([]ProjectorDeploymentIntentSnapshot, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) ListDeploymentRuns(context.Context, uuid.UUID) ([]ProjectorDeploymentRunSnapshot, error) {
	return nil, nil
}

func (s workerOnlyProjectorSource) ListWorkers(ctx context.Context, status string, limit int) ([]ProjectorWorkerSnapshot, error) {
	return s.source.List(ctx, status, limit)
}

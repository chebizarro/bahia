package controlplane

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type RegistryMutationBackend interface {
	CreateService(ctx context.Context, svc *domain.Service) error
	UpdateService(ctx context.Context, svc *domain.Service) error
	UpdateServiceWithExpectedRevision(ctx context.Context, svc *domain.Service, expectedUpdatedAt time.Time) error
	ImportObservedArtifact(ctx context.Context, in service.ImportObservedArtifactInput) (*service.ImportObservedArtifactResult, error)
	DeleteService(ctx context.Context, id uuid.UUID, force bool) error
	CreateEnvironment(ctx context.Context, env *domain.Environment) error
	CreateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error
	GetEnvironment(ctx context.Context, id uuid.UUID) (*domain.Environment, error)
	UpdateEnvironment(ctx context.Context, env *domain.Environment) error
	UpdateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, expectedUpdatedAt time.Time) error
	DeleteEnvironment(ctx context.Context, id uuid.UUID, force bool) error
	RegisterArtifact(ctx context.Context, artifact *domain.Artifact) error
}

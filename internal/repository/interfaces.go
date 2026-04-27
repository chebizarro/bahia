// Package repository defines data access interfaces for the Bahia domain.
package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ServiceRepository manages service records.
type ServiceRepository interface {
	Create(ctx context.Context, svc *domain.Service) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Service, error)
	GetByName(ctx context.Context, name string) (*domain.Service, error)
	List(ctx context.Context) ([]domain.Service, error)
	Update(ctx context.Context, svc *domain.Service) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// EnvironmentRepository manages environment records.
type EnvironmentRepository interface {
	Create(ctx context.Context, env *domain.Environment) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Environment, error)
	GetByName(ctx context.Context, name string) (*domain.Environment, error)
	List(ctx context.Context) ([]domain.Environment, error)
	Update(ctx context.Context, env *domain.Environment) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// BuildRepository manages build records.
type BuildRepository interface {
	Create(ctx context.Context, b *domain.Build) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Build, error)
	ListByService(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error
}

// ArtifactRepository manages artifact records.
type ArtifactRepository interface {
	Create(ctx context.Context, a *domain.Artifact) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	GetByDigest(ctx context.Context, repo, digest string) (*domain.Artifact, error)
	ListByService(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error)
	ListByBuild(ctx context.Context, buildID uuid.UUID) ([]domain.Artifact, error)
}

// DeploymentIntentRepository manages deployment intent records.
type DeploymentIntentRepository interface {
	Create(ctx context.Context, di *domain.DeploymentIntent) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error)
	ListByServiceEnv(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error
	UpdateApproval(ctx context.Context, id uuid.UUID, status domain.ApprovalStatus) error
}

// DeploymentRunRepository manages deployment run records.
type DeploymentRunRepository interface {
	Create(ctx context.Context, dr *domain.DeploymentRun) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error)
	ListByIntent(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error
}

// RuntimeObservationRepository manages runtime observation records.
type RuntimeObservationRepository interface {
	Create(ctx context.Context, obs *domain.RuntimeObservation) error
	GetLatest(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error)
	ListByServiceEnv(ctx context.Context, serviceID, envID uuid.UUID, limit int) ([]domain.RuntimeObservation, error)
}

// DeploymentPolicyRepository manages deployment policy records.
type DeploymentPolicyRepository interface {
	Create(ctx context.Context, p *domain.DeploymentPolicy) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error)
	GetByName(ctx context.Context, name string) (*domain.DeploymentPolicy, error)
	List(ctx context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error)
	ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.DeploymentPolicy, error)
	ListGlobal(ctx context.Context) ([]domain.DeploymentPolicy, error)
	Update(ctx context.Context, p *domain.DeploymentPolicy) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// SBOMRepository manages artifact SBOM records.
type SBOMRepository interface {
	CreateSBOM(ctx context.Context, sbom *domain.ArtifactSBOM) error
	GetSBOMByID(ctx context.Context, id uuid.UUID) (*domain.ArtifactSBOM, error)
	GetSBOMByArtifact(ctx context.Context, artifactID uuid.UUID) (*domain.ArtifactSBOM, error)
	GetSBOMByHash(ctx context.Context, rawHash string) (*domain.ArtifactSBOM, error)
	CreatePackages(ctx context.Context, packages []domain.SBOMPackage) error
	ListPackagesBySBOM(ctx context.Context, sbomID uuid.UUID) ([]domain.SBOMPackage, error)
	SearchPackagesByName(ctx context.Context, name string, limit int) ([]domain.SBOMPackage, error)
}

// PaymentRecordRepository manages Cashu payment records.
type PaymentRecordRepository interface {
	Create(ctx context.Context, rec *domain.PaymentRecord) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.PaymentRecord, error)
	ListByRun(ctx context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error)
	ListByWorker(ctx context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.PaymentStatus, errMsg string) error
	GetByTokenHash(ctx context.Context, tokenHash string) (*domain.PaymentRecord, error)
}

// ArtifactSignatureRepository manages artifact signature records.
type ArtifactSignatureRepository interface {
	Create(ctx context.Context, sig *domain.ArtifactSignature) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.ArtifactSignature, error)
	ListByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error)
	ListVerifiedByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error)
	HasVerifiedSignature(ctx context.Context, artifactID uuid.UUID) (bool, error)
}

// SecretRepository manages service secret records.
type SecretRepository interface {
	Create(ctx context.Context, s *domain.ServiceSecret) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.ServiceSecret, error)
	ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error)
	ListByServiceAndEnv(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error)
	ListEffective(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error)
	Update(ctx context.Context, s *domain.ServiceSecret) error
	Delete(ctx context.Context, id uuid.UUID) error
	DeleteByName(ctx context.Context, serviceID uuid.UUID, envID *uuid.UUID, name string) error
}

// RolloutPlanRepository manages rollout plan and step records.
type RolloutPlanRepository interface {
	CreatePlan(ctx context.Context, plan *domain.RolloutPlan) error
	GetPlanByID(ctx context.Context, id uuid.UUID) (*domain.RolloutPlan, error)
	GetPlanByIntent(ctx context.Context, intentID uuid.UUID) (*domain.RolloutPlan, error)
	UpdatePlan(ctx context.Context, plan *domain.RolloutPlan) error
	ListActivePlans(ctx context.Context) ([]domain.RolloutPlan, error)

	CreateStep(ctx context.Context, step *domain.RolloutStep) error
	CreateSteps(ctx context.Context, steps []domain.RolloutStep) error
	GetStepByID(ctx context.Context, id uuid.UUID) (*domain.RolloutStep, error)
	ListStepsByPlan(ctx context.Context, planID uuid.UUID) ([]domain.RolloutStep, error)
	UpdateStep(ctx context.Context, step *domain.RolloutStep) error
}

// NotificationRepository manages notification channels and log records.
type NotificationRepository interface {
	CreateChannel(ctx context.Context, ch *domain.NotificationChannel) error
	GetChannelByID(ctx context.Context, id uuid.UUID) (*domain.NotificationChannel, error)
	ListChannels(ctx context.Context, enabledOnly bool) ([]domain.NotificationChannel, error)
	UpdateChannel(ctx context.Context, ch *domain.NotificationChannel) error
	DeleteChannel(ctx context.Context, id uuid.UUID) error

	CreateLog(ctx context.Context, log *domain.NotificationLog) error
	UpdateLog(ctx context.Context, log *domain.NotificationLog) error
	ListLogsByChannel(ctx context.Context, channelID uuid.UUID, limit int) ([]domain.NotificationLog, error)
	ListRecentLogs(ctx context.Context, limit int) ([]domain.NotificationLog, error)
	ListRetryable(ctx context.Context, maxAttempts int) ([]domain.NotificationLog, error)
}

// EnvironmentServiceStateRepository manages the denormalized state view.
type EnvironmentServiceStateRepository interface {
	Upsert(ctx context.Context, state *domain.EnvironmentServiceState) error
	Get(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error)
	ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error)
	ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error)
	ListDrifted(ctx context.Context) ([]domain.EnvironmentServiceState, error)
	ListAll(ctx context.Context) ([]domain.EnvironmentServiceState, error)
}

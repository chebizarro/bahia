package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// BackupControlPlaneRepository manages authoritative backup control-plane records.
type BackupControlPlaneRepository interface {
	UpsertBackupRecipe(ctx context.Context, recipe *domain.BackupRecipe) error
	GetBackupRecipe(ctx context.Context, id uuid.UUID) (*domain.BackupRecipe, error)
	GetBackupRecipeByNameVersion(ctx context.Context, name, version string) (*domain.BackupRecipe, error)
	ListBackupRecipes(ctx context.Context, limit, offset int) ([]domain.BackupRecipe, error)

	UpsertBackupPolicy(ctx context.Context, policy *domain.BackupPolicy) error
	GetBackupPolicy(ctx context.Context, id uuid.UUID) (*domain.BackupPolicy, error)
	GetBackupPolicyByName(ctx context.Context, name string) (*domain.BackupPolicy, error)
	ListBackupPolicies(ctx context.Context, limit, offset int) ([]domain.BackupPolicy, error)

	UpsertBackupRepository(ctx context.Context, repo *domain.BackupRepository) error
	GetBackupRepository(ctx context.Context, id uuid.UUID) (*domain.BackupRepository, error)
	GetBackupRepositoryByName(ctx context.Context, name string) (*domain.BackupRepository, error)
	ListBackupRepositories(ctx context.Context, limit, offset int) ([]domain.BackupRepository, error)

	UpsertBackupDefinition(ctx context.Context, definition *domain.BackupDefinition) error
	GetBackupDefinition(ctx context.Context, id uuid.UUID) (*domain.BackupDefinition, error)
	GetBackupDefinitionByName(ctx context.Context, name string) (*domain.BackupDefinition, error)
	ListBackupDefinitions(ctx context.Context, limit, offset int) ([]domain.BackupDefinition, error)
	DeleteBackupDefinition(ctx context.Context, id uuid.UUID) error

	UpsertBackupRun(ctx context.Context, run *domain.BackupRun) error
	GetBackupRun(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error)
	GetBackupRunByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRun, error)
	CreateBackupRunIfAbsent(ctx context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error)
	ClaimNextQueuedBackupRun(ctx context.Context) (*domain.BackupRun, error)
	RequeueStaleBackupRuns(ctx context.Context, olderThan time.Duration) (int, error)
	ListBackupRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRun, error)

	UpsertBackupRestore(ctx context.Context, restore *domain.BackupRestoreRun) error
	GetBackupRestore(ctx context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error)
	GetBackupRestoreByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRestoreRun, error)
	CreateBackupRestoreIfAbsent(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error)
	ClaimNextQueuedBackupRestore(ctx context.Context) (*domain.BackupRestoreRun, error)
	RequeueStaleBackupRestores(ctx context.Context, olderThan time.Duration) (int, error)
	ListBackupRestores(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRestoreRun, error)

	UpsertBackupRetentionRun(ctx context.Context, run *domain.BackupRetentionRun) error
	GetBackupRetentionRun(ctx context.Context, id uuid.UUID) (*domain.BackupRetentionRun, error)
	GetBackupRetentionRunByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRetentionRun, error)
	CreateBackupRetentionRunIfAbsent(ctx context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error)
	ClaimNextQueuedBackupRetentionRun(ctx context.Context) (*domain.BackupRetentionRun, error)
	RequeueStaleBackupRetentionRuns(ctx context.Context, olderThan time.Duration) (int, error)
	ListBackupRetentionRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRetentionRun, error)

	UpsertBackupVerification(ctx context.Context, record *domain.BackupVerificationRecord) error
	GetBackupVerification(ctx context.Context, id uuid.UUID) (*domain.BackupVerificationRecord, error)
	GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error)
}

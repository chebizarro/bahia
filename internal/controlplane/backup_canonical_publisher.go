package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// backupCanonicalPublisherImpl publishes canonical 30900 cp-state records for
// backup entities. It implements service.BackupCanonicalPublisher using the
// shared record builders and BackupConfigPublishFunc.
type backupCanonicalPublisherImpl struct {
	publish     BackupConfigPublishFunc
	runVerifier backupRunVerificationLookup
}

type backupRunVerificationLookup interface {
	GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error)
}

// NewBackupCanonicalPublisher creates a BackupCanonicalPublisher backed by a
// BackupConfigPublishFunc. The verifier is used to enrich run records with
// verification data (may be nil).
func NewBackupCanonicalPublisher(
	publish BackupConfigPublishFunc,
	verifier backupRunVerificationLookup,
) service.BackupCanonicalPublisher {
	return &backupCanonicalPublisherImpl{publish: publish, runVerifier: verifier}
}

func (p *backupCanonicalPublisherImpl) PublishRecipe(ctx context.Context, recipe *domain.BackupRecipe) error {
	tags, content := BackupRecipeRegistryRecord(recipe, false)
	return p.publish(ctx, backupFamilyKindRecipeRegistry, BackupRecipeDTag(recipe.ID), tags, content, "backup_recipe", &recipe.ID, false)
}

func (p *backupCanonicalPublisherImpl) PublishPolicy(ctx context.Context, policy *domain.BackupPolicy) error {
	tags, content := BackupPolicyRegistryRecord(policy, false)
	return p.publish(ctx, backupFamilyKindPolicyRegistry, BackupPolicyDTag(policy.ID), tags, content, "backup_policy", &policy.ID, false)
}

func (p *backupCanonicalPublisherImpl) PublishRepository(ctx context.Context, repo *domain.BackupRepository) error {
	tags, content := BackupRepositoryRegistryRecord(repo, false)
	return p.publish(ctx, backupFamilyKindRepositoryRegistry, BackupRepositoryDTag(repo.ID), tags, content, "backup_repository", &repo.ID, false)
}

func (p *backupCanonicalPublisherImpl) PublishRun(ctx context.Context, run *domain.BackupRun) error {
	var verification *domain.BackupVerificationRecord
	if p.runVerifier != nil {
		verification, _ = p.runVerifier.GetBackupVerificationByRunID(ctx, run.ID)
	}
	tags, content := BackupRunStateRecord(run, verification)
	return p.publish(ctx, backupFamilyKindRunState, BackupRunDTag(run.ID), tags, content, "backup_run", &run.ID, false)
}

func (p *backupCanonicalPublisherImpl) PublishRestore(ctx context.Context, restore *domain.BackupRestoreRun) error {
	tags, content := BackupRestoreStateRecord(restore)
	return p.publish(ctx, backupFamilyKindRestoreState, BackupRestoreDTag(restore.ID), tags, content, "backup_restore", &restore.ID, false)
}

func (p *backupCanonicalPublisherImpl) PublishVerification(ctx context.Context, record *domain.BackupVerificationRecord) error {
	tags, content := BackupVerificationStateRecord(record)
	return p.publish(ctx, backupFamilyKindVerificationState, BackupVerificationDTag(record.BackupRunID), tags, content, "backup_verification", &record.ID, false)
}

func (p *backupCanonicalPublisherImpl) PublishRetention(ctx context.Context, run *domain.BackupRetentionRun) error {
	tags, content := BackupRetentionStateRecord(run)
	return p.publish(ctx, backupFamilyKindRetentionRegistry, BackupRetentionDTag(run.ID), tags, content, "backup_retention", &run.ID, false)
}

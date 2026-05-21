package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
)

const EventBackupRestoreChanged events.EventType = "backup_restore.changed"

// CreateBackupRestoreIfAbsent creates an idempotent restore request from an authoritative restore-eligible backup run.
func (s *BackupRegistryService) CreateBackupRestoreIfAbsent(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error) {
	if restore == nil {
		return nil, false, fmt.Errorf("%w: backup restore run must not be nil", domain.ErrInvalidValue)
	}
	if err := s.prepareRestoreFromSource(ctx, restore); err != nil {
		return nil, false, err
	}
	createdRestore, created, err := s.repo.CreateBackupRestoreIfAbsent(ctx, restore)
	if err != nil {
		return nil, false, err
	}
	if created {
		s.publishRestoreChanged(ctx, createdRestore)
	}
	return createdRestore, created, nil
}

func (s *BackupRegistryService) CreateOrUpdateBackupRestore(ctx context.Context, restore *domain.BackupRestoreRun) error {
	if restore == nil {
		return fmt.Errorf("%w: backup restore run must not be nil", domain.ErrInvalidValue)
	}
	if err := s.ensureRestoreReferences(ctx, restore); err != nil {
		return err
	}
	if err := domain.ValidateBackupRestoreRun(restore); err != nil {
		return err
	}
	if err := s.repo.UpsertBackupRestore(ctx, restore); err != nil {
		return err
	}
	s.publishRestoreChanged(ctx, restore)
	return nil
}

func (s *BackupRegistryService) GetBackupRestore(ctx context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error) {
	return s.repo.GetBackupRestore(ctx, id)
}

func (s *BackupRegistryService) GetBackupRestoreByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRestoreRun, error) {
	return s.repo.GetBackupRestoreByRequestCoordinate(ctx, strings.TrimSpace(pubkey), kind, strings.TrimSpace(dTag))
}

func (s *BackupRegistryService) ListBackupRestores(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRestoreRun, error) {
	if status != "" {
		if err := domain.ValidateDeploymentRunStatus(status); err != nil {
			return nil, err
		}
	}
	return s.repo.ListBackupRestores(ctx, status, limit, offset)
}

func (s *BackupRegistryService) ClaimNextQueuedBackupRestore(ctx context.Context) (*domain.BackupRestoreRun, error) {
	restore, err := s.repo.ClaimNextQueuedBackupRestore(ctx)
	if err != nil || restore == nil {
		return restore, err
	}
	s.publishRestoreChanged(ctx, restore)
	return restore, nil
}

func (s *BackupRegistryService) RequeueStaleBackupRestores(ctx context.Context, olderThan time.Duration) (int, error) {
	return s.repo.RequeueStaleBackupRestores(ctx, olderThan)
}

// ApplyBackupRestoreApproval records a deterministic approval decision. Rejections terminalize the restore before backend execution.
func (s *BackupRegistryService) ApplyBackupRestoreApproval(ctx context.Context, restoreID uuid.UUID, approved bool, approvalEventID, approvedBy, message string) (*domain.BackupRestoreRun, bool, error) {
	restore, err := s.repo.GetBackupRestore(ctx, restoreID)
	if err != nil {
		return nil, false, err
	}
	if restore == nil {
		return nil, false, fmt.Errorf("backup restore %s not found: %w", restoreID, repository.ErrNotFound)
	}
	if restore.ApprovalStatus != domain.BackupApprovalPending || isTerminalRunStatus(restore.Status) {
		return restore, false, nil
	}
	restore.ApprovalEventID = strings.TrimSpace(approvalEventID)
	restore.ApprovalMessage = strings.TrimSpace(message)
	now := time.Now().UTC()
	if approved {
		restore.ApprovalStatus = domain.BackupApprovalApproved
		restore.ApprovedBy = strings.TrimSpace(approvedBy)
		restore.ApprovedAt = &now
		if restore.Status == "" {
			restore.Status = domain.RunStatusQueued
		}
		restore.Error = ""
	} else {
		restore.ApprovalStatus = domain.BackupApprovalRejected
		restore.ApprovedBy = strings.TrimSpace(approvedBy)
		restore.ApprovedAt = &now
		restore.Status = domain.RunStatusFailed
		restore.FinishedAt = &now
		restore.Error = "backup restore rejected"
		if restore.ApprovalMessage != "" {
			restore.Error = restore.Error + ": " + restore.ApprovalMessage
		}
	}
	if err := s.CreateOrUpdateBackupRestore(ctx, restore); err != nil {
		return nil, false, err
	}
	return restore, true, nil
}

// CompleteBackupRestore applies terminal restore state with fail-closed verification when policy requires it.
func (s *BackupRegistryService) CompleteBackupRestore(ctx context.Context, restoreID uuid.UUID, result *BackupRestoreResult, cause error) (*domain.BackupRestoreRun, error) {
	restore, err := s.repo.GetBackupRestore(ctx, restoreID)
	if err != nil {
		return nil, err
	}
	if restore == nil {
		return nil, fmt.Errorf("backup restore %s not found: %w", restoreID, repository.ErrNotFound)
	}
	if isTerminalRunStatus(restore.Status) {
		return restore, nil
	}
	_, _, _, policy, contract, err := s.restoreReferences(ctx, restore)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		contract, err = ParseBackupPolicyRuntimeContract(nil)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	if result != nil {
		restore.Evidence = cloneMap(result.Evidence)
	}
	if cause != nil {
		restore.Status = domain.RunStatusFailed
		restore.Error = strings.TrimSpace(cause.Error())
		if contract.RestoreVerificationMode != domain.BackupVerificationNone {
			if errors.Is(cause, ErrBackupBackendUnsupported) {
				restore.VerificationStatus = domain.BackupVerificationUnsupported
			} else {
				restore.VerificationStatus = domain.BackupVerificationFailed
			}
		} else if restore.VerificationStatus == "" || restore.VerificationStatus == domain.BackupVerificationPending {
			restore.VerificationStatus = restoreVerificationStatus(result, false)
		}
	} else if contract.RestoreVerificationMode != domain.BackupVerificationNone {
		status := restoreVerificationStatus(result, true)
		restore.VerificationStatus = status
		if result == nil || !result.Verified || status != domain.BackupVerificationSucceeded {
			restore.Status = domain.RunStatusFailed
			restore.Error = restoreResultError(result, "backup restore policy requires successful verification")
		} else {
			restore.Status = domain.RunStatusSucceeded
			restore.Error = ""
		}
	} else {
		restore.Status = domain.RunStatusSucceeded
		restore.Error = ""
		status := restoreVerificationStatus(result, false)
		if status == domain.BackupVerificationPending {
			status = domain.BackupVerificationSkipped
		}
		restore.VerificationStatus = status
	}
	if restore.Status == domain.RunStatusFailed && restore.Error == "" {
		restore.Error = "backup restore failed"
	}
	restore.FinishedAt = &now
	if err := s.repo.UpsertBackupRestore(ctx, restore); err != nil {
		return nil, err
	}
	s.publishRestoreChanged(ctx, restore)
	return restore, nil
}

func (s *BackupRegistryService) prepareRestoreFromSource(ctx context.Context, restore *domain.BackupRestoreRun) error {
	_, _, _, _, contract, err := s.restoreReferences(ctx, restore)
	if err != nil {
		return err
	}
	if restore.ApprovalStatus == "" {
		if contract.RestoreApprovalRequired {
			restore.ApprovalStatus = domain.BackupApprovalPending
		} else {
			restore.ApprovalStatus = domain.BackupApprovalNotRequired
		}
	}
	if restore.Status == "" {
		restore.Status = domain.RunStatusQueued
	}
	if restore.VerificationStatus == "" {
		restore.VerificationStatus = domain.BackupVerificationPending
	}
	return domain.ValidateBackupRestoreRun(restore)
}

func (s *BackupRegistryService) ensureRestoreReferences(ctx context.Context, restore *domain.BackupRestoreRun) error {
	_, _, _, _, _, err := s.restoreReferences(ctx, restore)
	return err
}

func (s *BackupRegistryService) restoreReferences(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRun, *domain.BackupRecipe, *domain.BackupRepository, *domain.BackupPolicy, BackupPolicyRuntimeContract, error) {
	contract, _ := ParseBackupPolicyRuntimeContract(nil)
	if restore == nil {
		return nil, nil, nil, nil, contract, fmt.Errorf("%w: backup restore run must not be nil", domain.ErrInvalidValue)
	}
	sourceRun, err := s.repo.GetBackupRun(ctx, restore.BackupRunID)
	if err != nil {
		return nil, nil, nil, nil, contract, err
	}
	if sourceRun == nil {
		return nil, nil, nil, nil, contract, fmt.Errorf("backup run %s not found: %w", restore.BackupRunID, repository.ErrNotFound)
	}
	if !domain.BackupRunRestoreEligible(sourceRun) {
		return nil, nil, nil, nil, contract, fmt.Errorf("%w: backup run %s is not restore-eligible", domain.ErrInvalidValue, sourceRun.ID)
	}
	recipe, err := s.repo.GetBackupRecipe(ctx, sourceRun.RecipeID)
	if err != nil {
		return nil, nil, nil, nil, contract, err
	}
	if recipe == nil {
		return nil, nil, nil, nil, contract, fmt.Errorf("backup recipe %s not found: %w", sourceRun.RecipeID, repository.ErrNotFound)
	}
	repo, err := s.repo.GetBackupRepository(ctx, sourceRun.RepositoryID)
	if err != nil {
		return nil, nil, nil, nil, contract, err
	}
	if repo == nil {
		return nil, nil, nil, nil, contract, fmt.Errorf("backup repository %s not found: %w", sourceRun.RepositoryID, repository.ErrNotFound)
	}
	var policy *domain.BackupPolicy
	if sourceRun.PolicyID != nil {
		policy, err = s.repo.GetBackupPolicy(ctx, *sourceRun.PolicyID)
		if err != nil {
			return nil, nil, nil, nil, contract, err
		}
		if policy == nil {
			return nil, nil, nil, nil, contract, fmt.Errorf("backup policy %s not found: %w", *sourceRun.PolicyID, repository.ErrNotFound)
		}
	}
	contract, err = ParseBackupPolicyRuntimeContract(policy)
	if err != nil {
		return nil, nil, nil, nil, contract, err
	}
	restore.BackupRunID = sourceRun.ID
	restore.RecipeID = sourceRun.RecipeID
	restore.RepositoryID = sourceRun.RepositoryID
	restore.PolicyID = cloneUUIDPtr(sourceRun.PolicyID)
	restore.SnapshotID = sourceRun.SnapshotID
	restore.Backend = sourceRun.Backend
	return sourceRun, recipe, repo, policy, contract, nil
}

func cloneUUIDPtr(in *uuid.UUID) *uuid.UUID {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func restoreVerificationStatus(result *BackupRestoreResult, required bool) domain.BackupVerificationStatus {
	if result == nil {
		if required {
			return domain.BackupVerificationFailed
		}
		return domain.BackupVerificationPending
	}
	if result.VerificationStatus != "" {
		return result.VerificationStatus
	}
	if result.Verified {
		return domain.BackupVerificationSucceeded
	}
	if required {
		return domain.BackupVerificationFailed
	}
	return domain.BackupVerificationSkipped
}

func restoreResultError(result *BackupRestoreResult, fallback string) string {
	if result != nil && strings.TrimSpace(result.Error) != "" {
		return strings.TrimSpace(result.Error)
	}
	return fallback
}

func (s *BackupRegistryService) publishRestoreChanged(ctx context.Context, restore *domain.BackupRestoreRun) {
	if restore != nil {
		s.publish(ctx, EventBackupRestoreChanged, restore.ID.String(), map[string]any{"restore_id": restore.ID.String(), "backup_run_id": restore.BackupRunID.String(), "repository_id": restore.RepositoryID.String(), "approval_status": string(restore.ApprovalStatus), "status": string(restore.Status), "verification_status": string(restore.VerificationStatus)})
	}
}

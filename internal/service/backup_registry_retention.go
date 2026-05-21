package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
)

const EventBackupRetentionChanged events.EventType = "backup_retention.changed"

func (s *BackupRegistryService) CreateBackupRetentionRunIfAbsent(ctx context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error) {
	if err := s.ensureRetentionReferences(ctx, run); err != nil {
		return nil, false, err
	}
	createdRun, created, err := s.repo.CreateBackupRetentionRunIfAbsent(ctx, run)
	if err != nil {
		return nil, false, err
	}
	if created {
		s.publishRetentionChanged(ctx, createdRun)
	}
	return createdRun, created, nil
}

func (s *BackupRegistryService) CreateOrUpdateBackupRetentionRun(ctx context.Context, run *domain.BackupRetentionRun) error {
	if err := s.ensureRetentionReferences(ctx, run); err != nil {
		return err
	}
	if err := s.repo.UpsertBackupRetentionRun(ctx, run); err != nil {
		return err
	}
	s.publishRetentionChanged(ctx, run)
	return nil
}

func (s *BackupRegistryService) GetBackupRetentionRun(ctx context.Context, id uuid.UUID) (*domain.BackupRetentionRun, error) {
	return s.repo.GetBackupRetentionRun(ctx, id)
}

func (s *BackupRegistryService) GetBackupRetentionRunByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRetentionRun, error) {
	return s.repo.GetBackupRetentionRunByRequestCoordinate(ctx, strings.TrimSpace(pubkey), kind, strings.TrimSpace(dTag))
}

func (s *BackupRegistryService) ClaimNextQueuedBackupRetentionRun(ctx context.Context) (*domain.BackupRetentionRun, error) {
	run, err := s.repo.ClaimNextQueuedBackupRetentionRun(ctx)
	if err != nil || run == nil {
		return run, err
	}
	s.publishRetentionChanged(ctx, run)
	return run, nil
}

func (s *BackupRegistryService) RequeueStaleBackupRetentionRuns(ctx context.Context, olderThan time.Duration) (int, error) {
	return s.repo.RequeueStaleBackupRetentionRuns(ctx, olderThan)
}

func (s *BackupRegistryService) ListBackupRetentionRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRetentionRun, error) {
	if status != "" {
		if err := domain.ValidateDeploymentRunStatus(status); err != nil {
			return nil, err
		}
	}
	return s.repo.ListBackupRetentionRuns(ctx, status, limit, offset)
}

func (s *BackupRegistryService) CompleteBackupRetentionRun(ctx context.Context, runID uuid.UUID, evidence map[string]any, cause error) (*domain.BackupRetentionRun, error) {
	run, err := s.repo.GetBackupRetentionRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("backup retention run %s not found: %w", runID, repository.ErrNotFound)
	}
	if run.Backend == "" {
		repo, err := s.repo.GetBackupRepository(ctx, run.RepositoryID)
		if err != nil {
			return nil, err
		}
		if repo != nil {
			run.Backend = repo.Backend
		}
	}
	if err := domain.ValidateBackupRetentionRun(run); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	run.Evidence = cloneMap(evidence)
	if cause != nil {
		run.Status = domain.RunStatusFailed
		run.Error = cause.Error()
	} else {
		run.Status = domain.RunStatusSucceeded
		run.Error = ""
	}
	run.FinishedAt = &now
	if err := s.repo.UpsertBackupRetentionRun(ctx, run); err != nil {
		return nil, err
	}
	s.publishRetentionChanged(ctx, run)
	return run, nil
}

func (s *BackupRegistryService) ensureRetentionReferences(ctx context.Context, run *domain.BackupRetentionRun) error {
	if run == nil {
		return fmt.Errorf("%w: backup retention run must not be nil", domain.ErrInvalidValue)
	}
	repo, err := s.repo.GetBackupRepository(ctx, run.RepositoryID)
	if err != nil {
		return err
	}
	if repo == nil {
		return fmt.Errorf("backup repository %s not found: %w", run.RepositoryID, repository.ErrNotFound)
	}
	run.Backend = repo.Backend
	if run.PolicyID == nil {
		return fmt.Errorf("%w: backup retention policy_id must be set for backend-native retention", ErrBackupBackendConfiguration)
	}
	policy, err := s.repo.GetBackupPolicy(ctx, *run.PolicyID)
	if err != nil {
		return err
	}
	if policy == nil {
		return fmt.Errorf("backup policy %s not found: %w", *run.PolicyID, repository.ErrNotFound)
	}
	contract, err := ParseBackupPolicyRuntimeContract(policy)
	if err != nil {
		return err
	}
	if contract.RetentionMode != BackupRetentionModeBackendNative {
		return fmt.Errorf("%w: backup policy metadata.%s must be %q for retention enforcement", ErrBackupBackendConfiguration, BackupPolicyMetadataRetentionMode, BackupRetentionModeBackendNative)
	}
	if err := domain.ValidateBackupRetentionRun(run); err != nil {
		return err
	}
	backupSetRetentionMetadata(run, map[string]any{
		"retention_mode":            string(contract.RetentionMode),
		"retention_selector":        contract.RetentionSelector,
		"retention_dry_run_default": contract.RetentionDryRunDefault,
	})
	return nil
}

func (s *BackupRegistryService) publishRetentionChanged(ctx context.Context, run *domain.BackupRetentionRun) {
	if run != nil {
		s.publish(ctx, EventBackupRetentionChanged, run.ID.String(), map[string]any{"retention_run_id": run.ID.String(), "repository_id": run.RepositoryID.String(), "status": string(run.Status), "dry_run": run.DryRun})
	}
}

func backupSetRetentionMetadata(run *domain.BackupRetentionRun, values map[string]any) {
	if run.Metadata == nil {
		run.Metadata = map[string]any{}
	}
	for k, v := range values {
		if v != nil {
			run.Metadata[k] = v
		}
	}
}

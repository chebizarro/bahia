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
	"go.uber.org/zap"
)

const (
	EventBackupRecipeChanged       events.EventType = "backup_recipe.changed"
	EventBackupPolicyChanged       events.EventType = "backup_policy.changed"
	EventBackupRepositoryChanged   events.EventType = "backup_repository.changed"
	EventBackupRunChanged          events.EventType = "backup_run.changed"
	EventBackupVerificationChanged events.EventType = "backup_verification.changed"
)

// BackupRegistryService owns authoritative backup registry and run state.
type BackupRegistryService struct {
	repo      repository.BackupControlPlaneRepository
	publisher events.Publisher
	logger    *zap.Logger
}

func NewBackupRegistryService(repo repository.BackupControlPlaneRepository, publisher events.Publisher, logger *zap.Logger) *BackupRegistryService {
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupRegistryService{repo: repo, publisher: publisher, logger: logger}
}

func (s *BackupRegistryService) CreateOrUpdateRecipe(ctx context.Context, recipe *domain.BackupRecipe) error {
	if err := domain.ValidateBackupRecipe(recipe); err != nil {
		return err
	}
	repo, err := s.repo.GetBackupRepository(ctx, recipe.RepositoryID)
	if err != nil {
		return err
	}
	if repo == nil {
		return fmt.Errorf("backup repository %s not found: %w", recipe.RepositoryID, repository.ErrNotFound)
	}
	if recipe.PolicyID != nil {
		policy, err := s.repo.GetBackupPolicy(ctx, *recipe.PolicyID)
		if err != nil {
			return err
		}
		if policy == nil {
			return fmt.Errorf("backup policy %s not found: %w", *recipe.PolicyID, repository.ErrNotFound)
		}
	}
	if err := s.repo.UpsertBackupRecipe(ctx, recipe); err != nil {
		return err
	}
	s.publishRecipeChanged(ctx, recipe)
	return nil
}

func (s *BackupRegistryService) GetRecipe(ctx context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	return s.repo.GetBackupRecipe(ctx, id)
}

func (s *BackupRegistryService) GetRecipeByNameVersion(ctx context.Context, name, version string) (*domain.BackupRecipe, error) {
	return s.repo.GetBackupRecipeByNameVersion(ctx, strings.TrimSpace(name), strings.TrimSpace(version))
}

func (s *BackupRegistryService) ListRecipes(ctx context.Context, limit, offset int) ([]domain.BackupRecipe, error) {
	return s.repo.ListBackupRecipes(ctx, limit, offset)
}

func (s *BackupRegistryService) CreateOrUpdatePolicy(ctx context.Context, policy *domain.BackupPolicy) error {
	if err := domain.ValidateBackupPolicy(policy); err != nil {
		return err
	}
	if err := s.repo.UpsertBackupPolicy(ctx, policy); err != nil {
		return err
	}
	s.publishPolicyChanged(ctx, policy)
	return nil
}

func (s *BackupRegistryService) GetPolicy(ctx context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	return s.repo.GetBackupPolicy(ctx, id)
}

func (s *BackupRegistryService) GetPolicyByName(ctx context.Context, name string) (*domain.BackupPolicy, error) {
	return s.repo.GetBackupPolicyByName(ctx, strings.TrimSpace(name))
}

func (s *BackupRegistryService) ListPolicies(ctx context.Context, limit, offset int) ([]domain.BackupPolicy, error) {
	return s.repo.ListBackupPolicies(ctx, limit, offset)
}

func (s *BackupRegistryService) CreateOrUpdateRepository(ctx context.Context, repo *domain.BackupRepository) error {
	if err := domain.ValidateBackupRepository(repo); err != nil {
		return err
	}
	if err := s.repo.UpsertBackupRepository(ctx, repo); err != nil {
		return err
	}
	s.publishRepositoryChanged(ctx, repo)
	return nil
}

func (s *BackupRegistryService) GetRepository(ctx context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	return s.repo.GetBackupRepository(ctx, id)
}

func (s *BackupRegistryService) GetRepositoryByName(ctx context.Context, name string) (*domain.BackupRepository, error) {
	return s.repo.GetBackupRepositoryByName(ctx, strings.TrimSpace(name))
}

func (s *BackupRegistryService) ListRepositories(ctx context.Context, limit, offset int) ([]domain.BackupRepository, error) {
	return s.repo.ListBackupRepositories(ctx, limit, offset)
}

func (s *BackupRegistryService) CreateBackupRunIfAbsent(ctx context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error) {
	if err := domain.ValidateBackupRun(run); err != nil {
		return nil, false, err
	}
	if err := s.ensureRunReferences(ctx, run); err != nil {
		return nil, false, err
	}
	createdRun, created, err := s.repo.CreateBackupRunIfAbsent(ctx, run)
	if err != nil {
		return nil, false, err
	}
	if created {
		s.publishRunChanged(ctx, createdRun)
	}
	return createdRun, created, nil
}

func (s *BackupRegistryService) CreateOrUpdateBackupRun(ctx context.Context, run *domain.BackupRun) error {
	if err := domain.ValidateBackupRun(run); err != nil {
		return err
	}
	if err := s.ensureRunReferences(ctx, run); err != nil {
		return err
	}
	if err := s.repo.UpsertBackupRun(ctx, run); err != nil {
		return err
	}
	s.publishRunChanged(ctx, run)
	return nil
}

func (s *BackupRegistryService) GetBackupRun(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	return s.repo.GetBackupRun(ctx, id)
}

func (s *BackupRegistryService) GetBackupRunByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRun, error) {
	return s.repo.GetBackupRunByRequestCoordinate(ctx, strings.TrimSpace(pubkey), kind, strings.TrimSpace(dTag))
}

func (s *BackupRegistryService) ClaimNextQueuedBackupRun(ctx context.Context) (*domain.BackupRun, error) {
	run, err := s.repo.ClaimNextQueuedBackupRun(ctx)
	if err != nil || run == nil {
		return run, err
	}
	s.publishRunChanged(ctx, run)
	return run, nil
}

func (s *BackupRegistryService) RequeueStaleBackupRuns(ctx context.Context, olderThan time.Duration) (int, error) {
	return s.repo.RequeueStaleBackupRuns(ctx, olderThan)
}

func (s *BackupRegistryService) ListBackupRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRun, error) {
	if status != "" {
		if err := domain.ValidateDeploymentRunStatus(status); err != nil {
			return nil, err
		}
	}
	return s.repo.ListBackupRuns(ctx, status, limit, offset)
}

func (s *BackupRegistryService) RecordBackupVerification(ctx context.Context, record *domain.BackupVerificationRecord) error {
	if err := domain.ValidateBackupVerificationRecord(record); err != nil {
		return err
	}
	run, err := s.repo.GetBackupRun(ctx, record.BackupRunID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("backup run %s not found: %w", record.BackupRunID, repository.ErrNotFound)
	}
	if err := s.repo.UpsertBackupVerification(ctx, record); err != nil {
		return err
	}
	run.VerificationStatus = record.Status
	if run.VerificationMode == "" || run.VerificationMode == domain.BackupVerificationNone {
		run.VerificationMode = record.Mode
	}
	refreshBackupRunEligibility(run)
	if err := s.repo.UpsertBackupRun(ctx, run); err != nil {
		return err
	}
	s.publishVerificationChanged(ctx, record)
	s.publishRunChanged(ctx, run)
	return nil
}

func (s *BackupRegistryService) GetBackupVerification(ctx context.Context, id uuid.UUID) (*domain.BackupVerificationRecord, error) {
	return s.repo.GetBackupVerification(ctx, id)
}

func (s *BackupRegistryService) GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error) {
	return s.repo.GetBackupVerificationByRunID(ctx, runID)
}

// CompleteBackupRun applies the verification-first terminal state rule for the first backup slice.
func (s *BackupRegistryService) CompleteBackupRun(ctx context.Context, runID uuid.UUID, snapshotID string, verification *domain.BackupVerificationRecord, cause error) (*domain.BackupRun, error) {
	run, err := s.repo.GetBackupRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("backup run %s not found: %w", runID, repository.ErrNotFound)
	}
	if err := s.ensureRunReferences(ctx, run); err != nil {
		return nil, err
	}
	policy, err := s.policyForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	run.SnapshotID = strings.TrimSpace(snapshotID)
	run.SnapshotCreated = run.SnapshotID != ""
	if verification != nil {
		verification.BackupRunID = run.ID
		if err := domain.ValidateBackupVerificationRecord(verification); err != nil {
			return nil, err
		}
		if verification.Status == domain.BackupVerificationSucceeded && verification.VerifiedAt == nil {
			verification.VerifiedAt = &now
		}
		if err := s.repo.UpsertBackupVerification(ctx, verification); err != nil {
			return nil, err
		}
		run.VerificationMode = verification.Mode
		run.VerificationStatus = verification.Status
	}
	if cause != nil {
		run.Status = domain.RunStatusFailed
		run.Error = cause.Error()
		if run.FailureCategory == domain.BackupFailureNone {
			run.FailureCategory = domain.BackupFailureUnknown
		}
	} else if policy != nil && policy.RequireVerification {
		if run.VerificationStatus == domain.BackupVerificationSucceeded {
			run.Status = domain.RunStatusSucceeded
			run.VerificationPolicyFailure = ""
		} else {
			run.Status = domain.RunStatusFailed
			run.FailureCategory = domain.BackupFailurePolicy
			run.RestoreEligibility = domain.RestoreEligibilityPolicyBlocked
			run.VerificationPolicyFailure = "backup policy requires successful verification"
			if run.Error == "" {
				run.Error = run.VerificationPolicyFailure
			}
		}
	} else if run.SnapshotCreated {
		run.Status = domain.RunStatusSucceeded
		if verification == nil {
			run.VerificationStatus = domain.BackupVerificationSkipped
		}
	} else {
		run.Status = domain.RunStatusFailed
		if run.Error == "" {
			run.Error = "backup snapshot was not created"
		}
	}
	if run.RestoreEligibility != domain.RestoreEligibilityPolicyBlocked {
		refreshBackupRunEligibility(run)
	}
	run.FinishedAt = &now
	if err := s.repo.UpsertBackupRun(ctx, run); err != nil {
		return nil, err
	}
	if verification != nil {
		s.publishVerificationChanged(ctx, verification)
	}
	s.publishRunChanged(ctx, run)
	return run, nil
}

func (s *BackupRegistryService) ensureRunReferences(ctx context.Context, run *domain.BackupRun) error {
	recipe, err := s.repo.GetBackupRecipe(ctx, run.RecipeID)
	if err != nil {
		return err
	}
	if recipe == nil {
		return fmt.Errorf("backup recipe %s not found: %w", run.RecipeID, repository.ErrNotFound)
	}
	if run.RepositoryID != recipe.RepositoryID {
		return fmt.Errorf("%w: backup run repository_id %s does not match recipe repository_id %s", domain.ErrInvalidValue, run.RepositoryID, recipe.RepositoryID)
	}
	if run.Backend != recipe.Backend {
		return fmt.Errorf("%w: backup run backend %q does not match recipe backend %q", domain.ErrInvalidValue, run.Backend, recipe.Backend)
	}
	if strings.TrimSpace(run.TargetRef) != strings.TrimSpace(recipe.TargetRef) {
		return fmt.Errorf("%w: backup run target_ref does not match recipe target_ref", domain.ErrInvalidValue)
	}
	if !sameUUIDPtr(run.PolicyID, recipe.PolicyID) {
		return fmt.Errorf("%w: backup run policy_id does not match recipe policy_id", domain.ErrInvalidValue)
	}
	repo, err := s.repo.GetBackupRepository(ctx, run.RepositoryID)
	if err != nil {
		return err
	}
	if repo == nil {
		return fmt.Errorf("backup repository %s not found: %w", run.RepositoryID, repository.ErrNotFound)
	}
	if recipe.PolicyID != nil {
		policy, err := s.repo.GetBackupPolicy(ctx, *recipe.PolicyID)
		if err != nil {
			return err
		}
		if policy == nil {
			return fmt.Errorf("backup policy %s not found: %w", *recipe.PolicyID, repository.ErrNotFound)
		}
	}
	return nil
}

func (s *BackupRegistryService) policyForRun(ctx context.Context, run *domain.BackupRun) (*domain.BackupPolicy, error) {
	policyID := run.PolicyID
	if policyID == nil {
		recipe, err := s.repo.GetBackupRecipe(ctx, run.RecipeID)
		if err != nil {
			return nil, err
		}
		if recipe != nil {
			policyID = recipe.PolicyID
		}
	}
	if policyID == nil {
		return nil, nil
	}
	policy, err := s.repo.GetBackupPolicy(ctx, *policyID)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return nil, fmt.Errorf("backup policy %s not found: %w", *policyID, repository.ErrNotFound)
	}
	return policy, nil
}

func sameUUIDPtr(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *BackupRegistryService) publish(ctx context.Context, typ events.EventType, entityID string, data any) {
	s.publisher.Publish(ctx, events.Event{Type: typ, EntityID: entityID, Data: data})
}

func (s *BackupRegistryService) publishRecipeChanged(ctx context.Context, recipe *domain.BackupRecipe) {
	if recipe != nil {
		s.publish(ctx, EventBackupRecipeChanged, recipe.ID.String(), map[string]any{"recipe_id": recipe.ID.String(), "name": recipe.Name, "version": recipe.Version})
	}
}

func (s *BackupRegistryService) publishPolicyChanged(ctx context.Context, policy *domain.BackupPolicy) {
	if policy != nil {
		s.publish(ctx, EventBackupPolicyChanged, policy.ID.String(), map[string]any{"policy_id": policy.ID.String(), "name": policy.Name, "require_verification": policy.RequireVerification})
	}
}

func (s *BackupRegistryService) publishRepositoryChanged(ctx context.Context, repo *domain.BackupRepository) {
	if repo != nil {
		s.publish(ctx, EventBackupRepositoryChanged, repo.ID.String(), map[string]any{"repository_id": repo.ID.String(), "name": repo.Name, "backend": string(repo.Backend)})
	}
}

func (s *BackupRegistryService) publishRunChanged(ctx context.Context, run *domain.BackupRun) {
	if run != nil {
		s.publish(ctx, EventBackupRunChanged, run.ID.String(), map[string]any{"run_id": run.ID.String(), "recipe_id": run.RecipeID.String(), "repository_id": run.RepositoryID.String(), "status": string(run.Status), "verification_status": string(run.VerificationStatus)})
	}
}

func (s *BackupRegistryService) publishVerificationChanged(ctx context.Context, record *domain.BackupVerificationRecord) {
	if record != nil {
		s.publish(ctx, EventBackupVerificationChanged, record.ID.String(), map[string]any{"verification_id": record.ID.String(), "run_id": record.BackupRunID.String(), "status": string(record.Status), "verified": record.Verified})
	}
}

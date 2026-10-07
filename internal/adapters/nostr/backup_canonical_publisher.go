package nostr

import (
	"context"
	"fmt"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// BackupCanonicalPublisher publishes authoritative backup state records through
// the shared builder and outbox.
//
// Follows the MLCanonicalPublisher pattern: holds a *Projector reference and
// calls publishReplaceableJSON / publishControlState for each entity type,
// going through controlStateEnvelope → the shared signing/outbox pipeline.
// The cpStateFamilies table is the single envelope source.
//
// The BackupRegistryService calls this after each material mutation, so each
// canonical record is published once per change instead of O(fleet) per tick.
type BackupCanonicalPublisher struct {
	projector   *Projector
	runVerifier backupRunVerificationLookup
	logger      *zap.Logger

	// Runtime observation debounce.
	runtimeObsMu       sync.Mutex
	runtimeObsTimer    *time.Timer
	runtimeObsDebounce time.Duration
	runtimeObsSource   BackupRuntimeObservationSource
	staleTimeout       time.Duration
}

// backupRunVerificationLookup looks up verification for a run.
type backupRunVerificationLookup interface {
	GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error)
}

// BackupRuntimeObservationSource provides the data needed to compute the
// backup runtime observation aggregate.
type BackupRuntimeObservationSource interface {
	ListBackupRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRun, error)
	ListBackupRestores(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRestoreRun, error)
	ListBackupRetentionRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRetentionRun, error)
}

// NewBackupCanonicalPublisher creates a publisher that delegates to the
// projector's shared signing and outbox pipeline.
func NewBackupCanonicalPublisher(projector *Projector, logger *zap.Logger) *BackupCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupCanonicalPublisher{
		projector:          projector,
		logger:             logger.Named("backup-canonical"),
		runtimeObsDebounce: 2 * time.Second,
		staleTimeout:       15 * time.Minute,
	}
}

// SetRunVerifier configures the verification lookup used to enrich run records.
func (p *BackupCanonicalPublisher) SetRunVerifier(v backupRunVerificationLookup) {
	p.runVerifier = v
}

// SetRuntimeObservationSource configures the data source for the runtime
// observation aggregate.
func (p *BackupCanonicalPublisher) SetRuntimeObservationSource(src BackupRuntimeObservationSource) {
	p.runtimeObsSource = src
}

// PublishRecipe publishes a canonical backup recipe registry record.
func (p *BackupCanonicalPublisher) PublishRecipe(ctx context.Context, recipe *domain.BackupRecipe) error {
	if p.projector == nil || !p.projector.Enabled() || recipe == nil {
		return nil
	}
	dTag := BackupRecipeDTag(recipe.ID)
	tags, content := BackupRecipeRegistryRecord(recipe, false)
	return p.projector.publishControlState(ctx, KindBackupRecipeRegistry, dTag, false, tags, content, "backup_recipe.projection", &recipe.ID)
}

// PublishPolicy publishes a canonical backup policy registry record.
func (p *BackupCanonicalPublisher) PublishPolicy(ctx context.Context, policy *domain.BackupPolicy) error {
	if p.projector == nil || !p.projector.Enabled() || policy == nil {
		return nil
	}
	dTag := BackupPolicyDTag(policy.ID)
	tags, content := BackupPolicyRegistryRecord(policy, false)
	return p.projector.publishControlState(ctx, KindBackupPolicyRegistry, dTag, false, tags, content, "backup_policy.projection", &policy.ID)
}

// PublishRepository publishes a canonical backup repository registry record.
func (p *BackupCanonicalPublisher) PublishRepository(ctx context.Context, repo *domain.BackupRepository) error {
	if p.projector == nil || !p.projector.Enabled() || repo == nil {
		return nil
	}
	dTag := BackupRepositoryDTag(repo.ID)
	tags, content := BackupRepositoryRegistryRecord(repo, false)
	return p.projector.publishControlState(ctx, KindBackupRepositoryRegistry, dTag, false, tags, content, "backup_repository.projection", &repo.ID)
}

// PublishDefinition publishes a canonical backup definition registry record.
func (p *BackupCanonicalPublisher) PublishDefinition(ctx context.Context, def *domain.BackupDefinition) error {
	if p.projector == nil || !p.projector.Enabled() || def == nil {
		return nil
	}
	dTag := BackupDefinitionDTag(def.ID)
	tags, content := BackupDefinitionRegistryRecord(def, false)
	return p.projector.publishControlState(ctx, KindBackupDefinitionRegistry, dTag, false, tags, content, "backup_definition.projection", &def.ID)
}

// PublishRun publishes a canonical backup run state record, enriched with
// verification data when available.
func (p *BackupCanonicalPublisher) PublishRun(ctx context.Context, run *domain.BackupRun) error {
	if p.projector == nil || !p.projector.Enabled() || run == nil {
		return nil
	}
	var verification *domain.BackupVerificationRecord
	if p.runVerifier != nil {
		v, err := p.runVerifier.GetBackupVerificationByRunID(ctx, run.ID)
		if err != nil {
			p.logger.Warn("GetBackupVerificationByRunID failed, publishing run without verification",
				zap.String("run_id", run.ID.String()), zap.Error(err))
		} else {
			verification = v
		}
	}
	dTag := BackupRunDTag(run.ID)
	tags, content := BackupRunStateRecord(run, verification)
	if err := p.projector.publishControlState(ctx, KindBackupRunState, dTag, false, tags, content, "backup_run.projection", &run.ID); err != nil {
		return err
	}
	p.scheduleRuntimeObservation()
	return nil
}

// PublishRestore publishes a canonical backup restore state record.
func (p *BackupCanonicalPublisher) PublishRestore(ctx context.Context, restore *domain.BackupRestoreRun) error {
	if p.projector == nil || !p.projector.Enabled() || restore == nil {
		return nil
	}
	dTag := BackupRestoreDTag(restore.ID)
	tags, content := BackupRestoreStateRecord(restore)
	if err := p.projector.publishControlState(ctx, KindBackupRestoreState, dTag, false, tags, content, "backup_restore.projection", &restore.ID); err != nil {
		return err
	}
	p.scheduleRuntimeObservation()
	return nil
}

// PublishVerification publishes a canonical backup verification state record.
func (p *BackupCanonicalPublisher) PublishVerification(ctx context.Context, record *domain.BackupVerificationRecord) error {
	if p.projector == nil || !p.projector.Enabled() || record == nil {
		return nil
	}
	dTag := BackupVerificationDTag(record.BackupRunID)
	tags, content := BackupVerificationStateRecord(record)
	return p.projector.publishControlState(ctx, KindBackupVerificationState, dTag, false, tags, content, "backup_verification.projection", &record.ID)
}

// PublishRetention publishes a canonical backup retention state record.
func (p *BackupCanonicalPublisher) PublishRetention(ctx context.Context, run *domain.BackupRetentionRun) error {
	if p.projector == nil || !p.projector.Enabled() || run == nil {
		return nil
	}
	dTag := BackupRetentionDTag(run.ID)
	tags, content := BackupRetentionStateRecord(run)
	if err := p.projector.publishControlState(ctx, KindBackupRetentionRegistry, dTag, false, tags, content, "backup_retention.projection", &run.ID); err != nil {
		return err
	}
	p.scheduleRuntimeObservation()
	return nil
}

// PublishDeleted publishes a tombstone for the given backup entity.
func (p *BackupCanonicalPublisher) PublishDeleted(ctx context.Context, legacyKind int, dTag string, tags gonostr.Tags, content string, entityType string, entityID *uuid.UUID) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	return p.projector.publishControlState(ctx, legacyKind, dTag, true, tags, content, entityType, entityID)
}

// --- Runtime observation (Fix 3) -------------------------------------------

// scheduleRuntimeObservation debounces runtime observation republish.
// Multiple rapid mutations (e.g. a batch of run state changes) collapse into
// one aggregate computation.
func (p *BackupCanonicalPublisher) scheduleRuntimeObservation() {
	p.runtimeObsMu.Lock()
	defer p.runtimeObsMu.Unlock()
	if p.runtimeObsSource == nil {
		return
	}
	if p.runtimeObsTimer != nil {
		p.runtimeObsTimer.Stop()
	}
	p.runtimeObsTimer = time.AfterFunc(p.runtimeObsDebounce, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.publishBackupRuntimeObservation(ctx); err != nil {
			p.logger.Warn("backup runtime observation publish failed", zap.Error(err))
		}
	})
}

// publishBackupRuntimeObservation recomputes and publishes the fleet-wide
// backup runtime observation aggregate. Restored from the pre-B1 projector
// code, now called from the mutation side with debouncing.
func (p *BackupCanonicalPublisher) publishBackupRuntimeObservation(ctx context.Context) error {
	if p.runtimeObsSource == nil || p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	const pageSize = 500
	now := time.Now().UTC()
	staleTimeout := p.staleTimeout
	if staleTimeout <= 0 {
		staleTimeout = 15 * time.Minute
	}
	staleCutoff := now.Add(-staleTimeout)
	counts := map[string]map[string]int{"runs": {}, "restores": {}, "retention": {}}
	failureCategories := map[string]map[string]int{"runs": {}, "restores": {}, "retention": {}}
	backendHealthFailures := make([]map[string]any, 0)
	pendingApprovals := make([]map[string]any, 0)
	last := map[string]any{}

	for offset := 0; ; offset += pageSize {
		runs, err := p.runtimeObsSource.ListBackupRuns(ctx, "", pageSize, offset)
		if err != nil {
			return fmt.Errorf("list backup runs for runtime observation: %w", err)
		}
		for i := range runs {
			run := runs[i]
			backupCountStatus(counts["runs"], run.Status, run.UpdatedAt, staleCutoff)
			backupCountFailureCategory(failureCategories["runs"], run.FailureCategory)
			if run.Status == domain.RunStatusSucceeded {
				backupUpdateLastOutcome(last, "last_successful_run", run.ID.String(), run.FinishedAt, run.CreatedAt, map[string]any{
					"status": string(run.Status), "repository_id": run.RepositoryID.String(),
					"recipe_id": run.RecipeID.String(), "snapshot_id": run.SnapshotID,
					"restore_eligibility": string(run.RestoreEligibility),
				})
			}
			if run.VerificationStatus == domain.BackupVerificationSucceeded {
				backupUpdateLastOutcome(last, "last_verification", run.ID.String(), run.FinishedAt, run.CreatedAt, map[string]any{
					"status": string(run.VerificationStatus), "mode": string(run.VerificationMode),
					"repository_id": run.RepositoryID.String(), "recipe_id": run.RecipeID.String(),
				})
			}
			if run.FailureCategory == domain.BackupFailureBackendHealth && len(backendHealthFailures) < 20 {
				backendHealthFailures = append(backendHealthFailures, map[string]any{
					"type": "run", "id": run.ID.String(), "repository_id": run.RepositoryID.String(),
					"error": run.Error, "updated_at": backupFormatTimeNostr(run.UpdatedAt),
				})
			}
		}
		if len(runs) < pageSize {
			break
		}
	}

	for offset := 0; ; offset += pageSize {
		restores, err := p.runtimeObsSource.ListBackupRestores(ctx, "", pageSize, offset)
		if err != nil {
			return fmt.Errorf("list backup restores for runtime observation: %w", err)
		}
		for i := range restores {
			restore := restores[i]
			backupCountStatus(counts["restores"], restore.Status, restore.UpdatedAt, staleCutoff)
			if restore.ApprovalStatus == domain.BackupApprovalPending {
				counts["restores"]["pending_approval"]++
				if len(pendingApprovals) < 20 {
					pendingApprovals = append(pendingApprovals, map[string]any{
						"restore_id": restore.ID.String(), "backup_run_id": restore.BackupRunID.String(),
						"repository_id": restore.RepositoryID.String(), "requested_by": restore.RequestedBy,
						"created_at":           backupFormatTimeNostr(restore.CreatedAt),
						"approval_requirement": string(restore.ApprovalRequirement),
					})
				}
			}
			backupCountFailureCategory(failureCategories["restores"], restore.FailureCategory)
			if backupTerminalStatus(restore.Status) {
				backupUpdateLastOutcome(last, "last_restore", restore.ID.String(), restore.FinishedAt, restore.CreatedAt, map[string]any{
					"status": string(restore.Status), "approval_status": string(restore.ApprovalStatus),
					"repository_id": restore.RepositoryID.String(), "backup_run_id": restore.BackupRunID.String(),
					"verification_status": string(restore.VerificationStatus),
					"failure_category":    string(restore.FailureCategory),
				})
			}
			if restore.FailureCategory == domain.BackupFailureBackendHealth && len(backendHealthFailures) < 20 {
				backendHealthFailures = append(backendHealthFailures, map[string]any{
					"type": "restore", "id": restore.ID.String(), "repository_id": restore.RepositoryID.String(),
					"error": restore.Error, "updated_at": backupFormatTimeNostr(restore.UpdatedAt),
				})
			}
		}
		if len(restores) < pageSize {
			break
		}
	}

	for offset := 0; ; offset += pageSize {
		retentions, err := p.runtimeObsSource.ListBackupRetentionRuns(ctx, "", pageSize, offset)
		if err != nil {
			return fmt.Errorf("list backup retention runs for runtime observation: %w", err)
		}
		for i := range retentions {
			run := retentions[i]
			backupCountStatus(counts["retention"], run.Status, run.UpdatedAt, staleCutoff)
			backupCountFailureCategory(failureCategories["retention"], run.FailureCategory)
			if backupTerminalStatus(run.Status) {
				backupUpdateLastOutcome(last, "last_retention", run.ID.String(), run.FinishedAt, run.CreatedAt, map[string]any{
					"status": string(run.Status), "repository_id": run.RepositoryID.String(),
					"dry_run": run.DryRun, "failure_category": string(run.FailureCategory),
				})
			}
			if run.FailureCategory == domain.BackupFailureBackendHealth && len(backendHealthFailures) < 20 {
				backendHealthFailures = append(backendHealthFailures, map[string]any{
					"type": "retention", "id": run.ID.String(), "repository_id": run.RepositoryID.String(),
					"error": run.Error, "updated_at": backupFormatTimeNostr(run.UpdatedAt),
				})
			}
		}
		if len(retentions) < pageSize {
			break
		}
	}

	payload := map[string]any{
		"deleted":                   false,
		"scope":                     "fleet",
		"generated_at":              backupFormatTimeNostr(now),
		"stale_after_seconds":       int(staleTimeout.Seconds()),
		"counts":                    counts,
		"last_outcomes":             last,
		"failure_categories":        failureCategories,
		"backend_health_failures":   backendHealthFailures,
		"pending_restore_approvals": pendingApprovals,
	}
	tags := gonostr.Tags{
		{"scope", "fleet"},
		{"status", "summary"},
		{"pending_approval", fmt.Sprintf("%d", counts["restores"]["pending_approval"])},
	}
	return p.projector.publishReplaceableJSON(ctx, KindBackupRuntimeObservationState, "backup-runtime:fleet", tags, payload, "backup.runtime_observation.projection", nil)
}

// --- Runtime observation helpers --------------------------------------------

func backupCountStatus(counts map[string]int, status domain.DeploymentRunStatus, updatedAt time.Time, staleCutoff time.Time) {
	counts[string(status)]++
	if status == domain.RunStatusRunning && updatedAt.Before(staleCutoff) {
		counts["stale"]++
	}
}

func backupCountFailureCategory(counts map[string]int, category domain.BackupFailureCategory) {
	if category != domain.BackupFailureNone {
		counts[string(category)]++
	}
}

func backupTerminalStatus(status domain.DeploymentRunStatus) bool {
	switch status {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

func backupUpdateLastOutcome(last map[string]any, key, id string, finishedAt *time.Time, createdAt time.Time, values map[string]any) {
	candidate := createdAt
	if finishedAt != nil && !finishedAt.IsZero() {
		candidate = *finishedAt
	}
	if existing, ok := last[key].(map[string]any); ok {
		if raw, ok := existing["at"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, raw); err == nil && !candidate.After(parsed) {
				return
			}
		}
	}
	values["id"] = id
	values["at"] = backupFormatTimeNostr(candidate)
	last[key] = values
}

func backupFormatTimeNostr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Ensure BackupCanonicalPublisher satisfies the service-level interface at
// compile time. The import is intentionally avoided to prevent a dependency
// cycle; the assertion uses a local copy of the constraint.
var _ interface {
	PublishRecipe(context.Context, *domain.BackupRecipe) error
	PublishPolicy(context.Context, *domain.BackupPolicy) error
	PublishRepository(context.Context, *domain.BackupRepository) error
	PublishRun(context.Context, *domain.BackupRun) error
	PublishRestore(context.Context, *domain.BackupRestoreRun) error
	PublishVerification(context.Context, *domain.BackupVerificationRecord) error
	PublishRetention(context.Context, *domain.BackupRetentionRun) error
} = (*BackupCanonicalPublisher)(nil)

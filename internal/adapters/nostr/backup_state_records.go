package nostr

import (
	"encoding/json"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// --- Record builders -------------------------------------------------------
// Each function returns the family-specific tags and content JSON for one
// backup entity type. These are shared between the intent handler (via
// BackupConfigPublishFunc) and the warm-start comparison, following the same
// pattern as LLMRouteRegistryRecord / serviceRegistryRecord.

// BackupRecipeRegistryRecord builds canonical cp-state tags and content for a
// backup recipe mutation.
func BackupRecipeRegistryRecord(recipe *domain.BackupRecipe, deleted bool) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted": deleted,
		"id":      recipe.ID.String(),
	}
	if !deleted {
		content["name"] = recipe.Name
		content["version"] = recipe.Version
		content["backend"] = string(recipe.Backend)
		content["repository_id"] = recipe.RepositoryID.String()
		content["policy_id"] = backupUUIDStringPtr(recipe.PolicyID)
		content["target_ref"] = recipe.TargetRef
		content["include"] = recipe.Include
		content["exclude"] = recipe.Exclude
		content["verification_mode"] = string(recipe.VerificationMode)
		content["metadata"] = recipe.Metadata
		content["created_at"] = backupFormatTime(recipe.CreatedAt)
		content["updated_at"] = backupFormatTime(recipe.UpdatedAt)
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"recipe", "backup-recipe:" + recipe.ID.String()},
		{"recipe_id", recipe.ID.String()},
		{"repository_id", recipe.RepositoryID.String()},
		{"backend", string(recipe.Backend)},
		{"target", recipe.TargetRef},
		{"version", recipe.Version},
	}
	if recipe.PolicyID != nil {
		tags = append(tags,
			gonostr.Tag{"policy", recipe.PolicyID.String()},
			gonostr.Tag{"policy_id", recipe.PolicyID.String()},
		)
	}
	return tags, string(contentJSON)
}

// BackupPolicyRegistryRecord builds canonical cp-state tags and content for a
// backup policy mutation.
func BackupPolicyRegistryRecord(policy *domain.BackupPolicy, deleted bool) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted": deleted,
		"id":      policy.ID.String(),
	}
	if !deleted {
		content["name"] = policy.Name
		content["require_verification"] = policy.RequireVerification
		content["verification_mode"] = string(policy.VerificationMode)
		content["metadata"] = policy.Metadata
		content["created_at"] = backupFormatTime(policy.CreatedAt)
		content["updated_at"] = backupFormatTime(policy.UpdatedAt)
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"policy", "backup-policy:" + policy.ID.String()},
		{"policy_id", policy.ID.String()},
		{"name", policy.Name},
		{"require_verification", fmt.Sprintf("%t", policy.RequireVerification)},
		{"verification", string(policy.VerificationMode)},
	}
	return tags, string(contentJSON)
}

// BackupRepositoryRegistryRecord builds canonical cp-state tags and content
// for a backup repository mutation.
func BackupRepositoryRegistryRecord(repo *domain.BackupRepository, deleted bool) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted": deleted,
		"id":      repo.ID.String(),
	}
	if !deleted {
		content["name"] = repo.Name
		content["backend"] = string(repo.Backend)
		content["repository_uri"] = repo.RepositoryURI
		content["credential_profile"] = repo.CredentialProfile
		content["metadata"] = repo.Metadata
		content["created_at"] = backupFormatTime(repo.CreatedAt)
		content["updated_at"] = backupFormatTime(repo.UpdatedAt)
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"repository", "backup-repository:" + repo.ID.String()},
		{"repository_id", repo.ID.String()},
		{"name", repo.Name},
		{"backend", string(repo.Backend)},
	}
	return tags, string(contentJSON)
}

// BackupDefinitionRegistryRecord builds canonical cp-state tags and content
// for a backup definition mutation.
func BackupDefinitionRegistryRecord(def *domain.BackupDefinition, deleted bool) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted": deleted,
		"id":      def.ID.String(),
	}
	if !deleted {
		content["name"] = def.Name
		content["repository_id"] = def.RepositoryID.String()
		content["repository_name"] = def.RepositoryName
		content["policy_id"] = def.PolicyID.String()
		content["policy_name"] = def.PolicyName
		content["recipe_id"] = def.RecipeID.String()
		content["recipe_name"] = def.RecipeName
		content["recipe_version"] = def.RecipeVersion
		content["schedule_expression"] = def.ScheduleExpression
		content["schedule_enabled"] = def.ScheduleEnabled
		content["schedule_jitter_window"] = def.ScheduleJitterWindow
		content["tenant_id"] = backupUUIDStringPtr(def.TenantID)
		content["tenant_name"] = def.TenantName
		content["environment_id"] = backupUUIDStringPtr(def.EnvironmentID)
		content["environment_name"] = def.EnvironmentName
		content["owner_pubkey"] = def.OwnerPubkey
		content["requires_approval"] = def.RequiresApproval
		content["approval_policy"] = def.ApprovalPolicy
		content["restore_target_rules"] = def.RestoreTargetRules
		content["executor_labels"] = def.ExecutorLabels
		content["capability_requirements"] = def.CapabilityRequirements
		content["labels"] = def.Labels
		content["group"] = def.Group
		content["metadata"] = def.Metadata
		content["created_at"] = backupFormatTime(def.CreatedAt)
		content["updated_at"] = backupFormatTime(def.UpdatedAt)
		content["created_by"] = def.CreatedBy
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"definition", "backup-definition:" + def.ID.String()},
		{"definition_id", def.ID.String()},
		{"name", def.Name},
		{"repository_id", def.RepositoryID.String()},
		{"policy_id", def.PolicyID.String()},
		{"recipe_id", def.RecipeID.String()},
		{"schedule_enabled", fmt.Sprintf("%t", def.ScheduleEnabled)},
	}
	if def.Group != "" {
		tags = append(tags, gonostr.Tag{"group", def.Group})
	}
	return tags, string(contentJSON)
}

// BackupRunStateRecord builds canonical cp-state tags and content for a
// backup run state event published by the daemon.
func BackupRunStateRecord(run *domain.BackupRun, verification *domain.BackupVerificationRecord) (gonostr.Tags, string) {
	restoreEligible := domain.BackupRunRestoreEligible(run)
	content := map[string]any{
		"deleted":                     false,
		"id":                          run.ID.String(),
		"recipe_id":                   run.RecipeID.String(),
		"execution_snapshot":          run.ExecutionSnapshot,
		"repository_id":               run.RepositoryID.String(),
		"policy_id":                   backupUUIDStringPtr(run.PolicyID),
		"requested_by":                run.RequestedBy,
		"request_event_id":            run.RequestEventID,
		"request_kind":                run.RequestKind,
		"request_d_tag":               run.RequestDTag,
		"status":                      string(run.Status),
		"backend":                     string(run.Backend),
		"target_ref":                  run.TargetRef,
		"snapshot_created":            run.SnapshotCreated,
		"snapshot_id":                 run.SnapshotID,
		"verification_mode":           string(run.VerificationMode),
		"verification_status":         string(run.VerificationStatus),
		"restore_eligible":            restoreEligible,
		"restore_eligibility":         string(run.RestoreEligibility),
		"restore_eligibility_reason":  run.RestoreEligibilityReason,
		"verification_policy_failure": run.VerificationPolicyFailure,
		"failure_category":            string(run.FailureCategory),
		"publish_summary":             run.PublishSummary,
		"error":                       run.Error,
		"metadata":                    run.Metadata,
		"started_at":                  run.StartedAt,
		"finished_at":                 run.FinishedAt,
		"created_at":                  backupFormatTime(run.CreatedAt),
		"updated_at":                  backupFormatTime(run.UpdatedAt),
	}
	if verification != nil {
		content["verification_id"] = verification.ID.String()
		content["verified"] = verification.Verified
		content["verification_mode"] = string(verification.Mode)
		content["verification_error"] = verification.Error
		content["verification"] = map[string]any{
			"id":               verification.ID.String(),
			"mode":             string(verification.Mode),
			"status":           string(verification.Status),
			"verified":         verification.Verified,
			"evidence":         verification.Evidence,
			"evidence_details": verification.EvidenceDetails,
			"error":            verification.Error,
		}
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"run", run.ID.String()},
		{"recipe_id", run.RecipeID.String()},
		{"repository_id", run.RepositoryID.String()},
		{"status", string(run.Status)},
		{"backend", string(run.Backend)},
		{"verification", string(run.VerificationStatus)},
		{"restore_eligible", fmt.Sprintf("%t", restoreEligible)},
		{"restore_eligibility", string(run.RestoreEligibility)},
		{"failure_category", string(run.FailureCategory)},
	}
	if run.PolicyID != nil {
		tags = append(tags,
			gonostr.Tag{"policy", run.PolicyID.String()},
			gonostr.Tag{"policy_id", run.PolicyID.String()},
		)
	}
	return tags, string(contentJSON)
}

// BackupRestoreStateRecord builds canonical cp-state tags and content for a
// backup restore state event published by the daemon.
func BackupRestoreStateRecord(restore *domain.BackupRestoreRun) (gonostr.Tags, string) {
	pendingApproval := restore.ApprovalStatus == domain.BackupApprovalPending
	content := map[string]any{
		"deleted":                     false,
		"id":                          restore.ID.String(),
		"backup_run_id":               restore.BackupRunID.String(),
		"recipe_id":                   restore.RecipeID.String(),
		"repository_id":               restore.RepositoryID.String(),
		"policy_id":                   backupUUIDStringPtr(restore.PolicyID),
		"snapshot_id":                 restore.SnapshotID,
		"restore_target_ref":          restore.RestoreTargetRef,
		"requested_by":                restore.RequestedBy,
		"request_event_id":            restore.RequestEventID,
		"request_kind":                restore.RequestKind,
		"request_d_tag":               restore.RequestDTag,
		"approval_status":             string(restore.ApprovalStatus),
		"approval_required":           restore.ApprovalRequired,
		"approval_requirement":        string(restore.ApprovalRequirement),
		"pending_approval":            pendingApproval,
		"approval_event_id":           restore.ApprovalEventID,
		"approved_by":                 restore.ApprovedBy,
		"approved_at":                 restore.ApprovedAt,
		"approval_message":            restore.ApprovalMessage,
		"approval_reason_code":        restore.ApprovalReasonCode,
		"approval_reason":             restore.ApprovalReason,
		"status":                      string(restore.Status),
		"backend":                     string(restore.Backend),
		"verification_status":         string(restore.VerificationStatus),
		"evidence":                    restore.Evidence,
		"publish_summary":             restore.PublishSummary,
		"error":                       restore.Error,
		"verification_policy_failure": restore.VerificationPolicyFailure,
		"failure_category":            string(restore.FailureCategory),
		"metadata":                    restore.Metadata,
		"started_at":                  restore.StartedAt,
		"finished_at":                 restore.FinishedAt,
		"created_at":                  backupFormatTime(restore.CreatedAt),
		"updated_at":                  backupFormatTime(restore.UpdatedAt),
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"restore", restore.ID.String()},
		{"restore_id", restore.ID.String()},
		{"run", restore.BackupRunID.String()},
		{"backup_run_id", restore.BackupRunID.String()},
		{"recipe_id", restore.RecipeID.String()},
		{"repository_id", restore.RepositoryID.String()},
		{"status", string(restore.Status)},
		{"approval", string(restore.ApprovalStatus)},
		{"approval_required", fmt.Sprintf("%t", restore.ApprovalRequired)},
		{"approval_requirement", string(restore.ApprovalRequirement)},
		{"pending_approval", fmt.Sprintf("%t", pendingApproval)},
		{"verification", string(restore.VerificationStatus)},
		{"backend", string(restore.Backend)},
		{"failure_category", string(restore.FailureCategory)},
	}
	if restore.PolicyID != nil {
		tags = append(tags,
			gonostr.Tag{"policy", restore.PolicyID.String()},
			gonostr.Tag{"policy_id", restore.PolicyID.String()},
		)
	}
	return tags, string(contentJSON)
}

// BackupVerificationStateRecord builds canonical cp-state tags and content
// for a backup verification state event published by the daemon.
func BackupVerificationStateRecord(record *domain.BackupVerificationRecord) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted":          false,
		"id":               record.ID.String(),
		"backup_run_id":    record.BackupRunID.String(),
		"mode":             string(record.Mode),
		"status":           string(record.Status),
		"verified":         record.Verified,
		"evidence":         record.Evidence,
		"evidence_details": record.EvidenceDetails,
		"error":            record.Error,
		"publish_summary":  record.PublishSummary,
		"verified_at":      record.VerifiedAt,
		"created_at":       backupFormatTime(record.CreatedAt),
		"updated_at":       backupFormatTime(record.UpdatedAt),
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"run", record.BackupRunID.String()},
		{"verification_id", record.ID.String()},
		{"verification", string(record.Status)},
		{"status", string(record.Status)},
		{"mode", string(record.Mode)},
		{"verified", fmt.Sprintf("%t", record.Verified)},
	}
	return tags, string(contentJSON)
}

// BackupRetentionStateRecord builds canonical cp-state tags and content
// for a backup retention state event published by the daemon.
func BackupRetentionStateRecord(run *domain.BackupRetentionRun) (gonostr.Tags, string) {
	content := map[string]any{
		"deleted":          false,
		"id":               run.ID.String(),
		"repository_id":    run.RepositoryID.String(),
		"policy_id":        backupUUIDStringPtr(run.PolicyID),
		"requested_by":     run.RequestedBy,
		"request_event_id": run.RequestEventID,
		"request_kind":     run.RequestKind,
		"request_d_tag":    run.RequestDTag,
		"status":           string(run.Status),
		"backend":          string(run.Backend),
		"dry_run":          run.DryRun,
		"evidence":         run.Evidence,
		"publish_summary":  run.PublishSummary,
		"error":            run.Error,
		"failure_category": string(run.FailureCategory),
		"metadata":         run.Metadata,
		"started_at":       run.StartedAt,
		"finished_at":      run.FinishedAt,
		"created_at":       backupFormatTime(run.CreatedAt),
		"updated_at":       backupFormatTime(run.UpdatedAt),
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"retention", run.ID.String()},
		{"retention_run_id", run.ID.String()},
		{"repository_id", run.RepositoryID.String()},
		{"status", string(run.Status)},
		{"backend", string(run.Backend)},
		{"dry_run", fmt.Sprintf("%t", run.DryRun)},
		{"failure_category", string(run.FailureCategory)},
	}
	if run.PolicyID != nil {
		tags = append(tags,
			gonostr.Tag{"policy", run.PolicyID.String()},
			gonostr.Tag{"policy_id", run.PolicyID.String()},
		)
	}
	return tags, string(contentJSON)
}

// --- D-tag coordinate helpers ------------------------------------------------

// BackupRecipeDTag returns the coordinate for a backup recipe record.
func BackupRecipeDTag(id uuid.UUID) string { return "backup-recipe:" + id.String() }

// BackupPolicyDTag returns the coordinate for a backup policy record.
func BackupPolicyDTag(id uuid.UUID) string { return "backup-policy:" + id.String() }

// BackupRepositoryDTag returns the coordinate for a backup repository record.
func BackupRepositoryDTag(id uuid.UUID) string { return "backup-repository:" + id.String() }

// BackupDefinitionDTag returns the coordinate for a backup definition record.
func BackupDefinitionDTag(id uuid.UUID) string { return "backup-definition:" + id.String() }

// BackupRunDTag returns the coordinate for a backup run state record.
func BackupRunDTag(id uuid.UUID) string { return "backup-run:" + id.String() }

// BackupRestoreDTag returns the coordinate for a backup restore state record.
func BackupRestoreDTag(id uuid.UUID) string { return "backup-restore:" + id.String() }

// BackupVerificationDTag returns the coordinate for a backup verification record.
func BackupVerificationDTag(runID uuid.UUID) string { return "backup-verification:" + runID.String() }

// BackupRetentionDTag returns the coordinate for a backup retention state record.
func BackupRetentionDTag(id uuid.UUID) string { return "backup-retention:" + id.String() }

// --- helpers ---------------------------------------------------------------

func backupUUIDStringPtr(id *uuid.UUID) interface{} {
	if id == nil {
		return nil
	}
	return id.String()
}

func backupFormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

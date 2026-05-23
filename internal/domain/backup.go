package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BackupBackendKind identifies the concrete backup backend family.
type BackupBackendKind string

const (
	BackupBackendKopia  BackupBackendKind = "kopia"
	BackupBackendVelero BackupBackendKind = "velero"
)

// BackupVerificationMode identifies the verification procedure required after a snapshot.
type BackupVerificationMode string

const (
	BackupVerificationNone                BackupVerificationMode = "none"
	BackupVerificationKopiaSnapshotVerify BackupVerificationMode = "kopia_snapshot_verify"
)

// BackupVerificationStatus records verification outcome for backup restore eligibility.
type BackupVerificationStatus string

const (
	BackupVerificationPending     BackupVerificationStatus = "pending"
	BackupVerificationSucceeded   BackupVerificationStatus = "succeeded"
	BackupVerificationFailed      BackupVerificationStatus = "failed"
	BackupVerificationSkipped     BackupVerificationStatus = "skipped"
	BackupVerificationUnsupported BackupVerificationStatus = "unsupported"
)

// BackupApprovalStatus records restore approval state before execution.
type BackupApprovalStatus string

const (
	BackupApprovalPending     BackupApprovalStatus = "pending"
	BackupApprovalApproved    BackupApprovalStatus = "approved"
	BackupApprovalRejected    BackupApprovalStatus = "rejected"
	BackupApprovalNotRequired BackupApprovalStatus = "not_required"
)

// RestoreEligibility is the operator-facing restore eligibility state for a backup snapshot.
type RestoreEligibility string

const (
	RestoreEligibilityUnknown                 RestoreEligibility = "unknown"
	RestoreEligibilityEligible                RestoreEligibility = "eligible"
	RestoreEligibilityRunNotSucceeded         RestoreEligibility = "run_not_succeeded"
	RestoreEligibilitySnapshotMissing         RestoreEligibility = "snapshot_missing"
	RestoreEligibilityVerificationPending     RestoreEligibility = "verification_pending"
	RestoreEligibilityVerificationFailed      RestoreEligibility = "verification_failed"
	RestoreEligibilityVerificationSkipped     RestoreEligibility = "verification_skipped"
	RestoreEligibilityVerificationUnsupported RestoreEligibility = "verification_unsupported"
	RestoreEligibilityPolicyBlocked           RestoreEligibility = "policy_blocked"
)

// BackupFailureCategory standardizes backup failure buckets for operator observability.
type BackupFailureCategory string

const (
	BackupFailureNone               BackupFailureCategory = ""
	BackupFailureUnknown            BackupFailureCategory = "unknown"
	BackupFailureLoadInputs         BackupFailureCategory = "load_inputs"
	BackupFailureBackendResolve     BackupFailureCategory = "backend_resolve"
	BackupFailureBackendHealth      BackupFailureCategory = "backend_health"
	BackupFailureSnapshot           BackupFailureCategory = "snapshot"
	BackupFailureVerification       BackupFailureCategory = "verification"
	BackupFailureRestoreExecution   BackupFailureCategory = "restore_execution"
	BackupFailureRetentionExecution BackupFailureCategory = "retention_execution"
	BackupFailurePolicy             BackupFailureCategory = "policy"
	BackupFailureApprovalRejected   BackupFailureCategory = "approval_rejected"
	BackupFailureCancelled          BackupFailureCategory = "cancelled"
	BackupFailureTimeout            BackupFailureCategory = "timeout"
)

// BackupApprovalRequirement records why a restore approval decision is or is not required.
type BackupApprovalRequirement string

const (
	BackupApprovalRequirementNone   BackupApprovalRequirement = "none"
	BackupApprovalRequirementPolicy BackupApprovalRequirement = "policy"
)

// BackupRecipe is an authoritative backup recipe definition.
type BackupRecipe struct {
	ID               uuid.UUID              `json:"id"`
	Name             string                 `json:"name"`
	Version          string                 `json:"version"`
	Backend          BackupBackendKind      `json:"backend"`
	RepositoryID     uuid.UUID              `json:"repository_id"`
	PolicyID         *uuid.UUID             `json:"policy_id,omitempty"`
	TargetRef        string                 `json:"target_ref"`
	Include          []string               `json:"include,omitempty"`
	Exclude          []string               `json:"exclude,omitempty"`
	VerificationMode BackupVerificationMode `json:"verification_mode"`
	Metadata         map[string]any         `json:"metadata,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

// BackupPolicy captures verification requirements and backend policy metadata.
type BackupPolicy struct {
	ID                  uuid.UUID              `json:"id"`
	Name                string                 `json:"name"`
	RequireVerification bool                   `json:"require_verification"`
	VerificationMode    BackupVerificationMode `json:"verification_mode"`
	Metadata            map[string]any         `json:"metadata,omitempty"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// BackupRepository records a configured backup repository target.
type BackupRepository struct {
	ID                uuid.UUID         `json:"id"`
	Name              string            `json:"name"`
	Backend           BackupBackendKind `json:"backend"`
	RepositoryURI     string            `json:"repository_uri"`
	CredentialProfile string            `json:"credential_profile,omitempty"`
	Metadata          map[string]any    `json:"metadata,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// BackupDefinition is the canonical operator-facing backup registry object.
// It composes repository, policy, recipe, schedule, scope, approval, restore,
// executor targeting, and grouping intent without executing backup work itself.
type BackupDefinition struct {
	ID                     uuid.UUID      `json:"id"`
	Name                   string         `json:"name"`
	RepositoryID           uuid.UUID      `json:"repository_id"`
	RepositoryName         string         `json:"repository_name"`
	PolicyID               uuid.UUID      `json:"policy_id"`
	PolicyName             string         `json:"policy_name"`
	RecipeID               uuid.UUID      `json:"recipe_id"`
	RecipeName             string         `json:"recipe_name"`
	RecipeVersion          string         `json:"recipe_version"`
	ScheduleExpression     string         `json:"schedule_expression,omitempty"`
	ScheduleEnabled        bool           `json:"schedule_enabled"`
	ScheduleJitterWindow   string         `json:"schedule_jitter_window,omitempty"`
	TenantID               *uuid.UUID     `json:"tenant_id,omitempty"`
	TenantName             string         `json:"tenant_name,omitempty"`
	EnvironmentID          *uuid.UUID     `json:"environment_id,omitempty"`
	EnvironmentName        string         `json:"environment_name,omitempty"`
	OwnerPubkey            string         `json:"owner_pubkey,omitempty"`
	RequiresApproval       bool           `json:"requires_approval"`
	ApprovalPolicy         string         `json:"approval_policy,omitempty"`
	RestoreTargetRules     map[string]any `json:"restore_target_rules,omitempty"`
	ExecutorLabels         []string       `json:"executor_labels,omitempty"`
	CapabilityRequirements []string       `json:"capability_requirements,omitempty"`
	Labels                 map[string]any `json:"labels,omitempty"`
	Group                  string         `json:"group,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
	CreatedBy              string         `json:"created_by"`
}

// BackupRun is the durable control-plane record for a backup request.
type BackupRun struct {
	ID                 uuid.UUID                `json:"id"`
	RecipeID           uuid.UUID                `json:"recipe_id"`
	RepositoryID       uuid.UUID                `json:"repository_id"`
	PolicyID           *uuid.UUID               `json:"policy_id,omitempty"`
	RequestedBy        string                   `json:"requested_by"`
	RequestEventID     string                   `json:"request_event_id"`
	RequestKind        int                      `json:"request_kind"`
	RequestDTag        string                   `json:"request_d_tag"`
	Status             DeploymentRunStatus      `json:"status"`
	Backend            BackupBackendKind        `json:"backend"`
	TargetRef          string                   `json:"target_ref"`
	SnapshotCreated           bool                     `json:"snapshot_created"`
	SnapshotID                string                   `json:"snapshot_id,omitempty"`
	VerificationMode          BackupVerificationMode   `json:"verification_mode"`
	VerificationStatus        BackupVerificationStatus `json:"verification_status"`
	RestoreEligibility        RestoreEligibility       `json:"restore_eligibility"`
	RestoreEligibilityReason  string                   `json:"restore_eligibility_reason,omitempty"`
	VerificationPolicyFailure string                   `json:"verification_policy_failure,omitempty"`
	FailureCategory           BackupFailureCategory    `json:"failure_category,omitempty"`
	PublishSummary            map[string]any           `json:"publish_summary,omitempty"`
	Error                     string                   `json:"error,omitempty"`
	Metadata                  map[string]any           `json:"metadata,omitempty"`
	StartedAt          *time.Time               `json:"started_at,omitempty"`
	FinishedAt         *time.Time               `json:"finished_at,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

// BackupVerificationRecord stores verification evidence for a backup run.
type BackupVerificationRecord struct {
	ID             uuid.UUID                `json:"id"`
	BackupRunID    uuid.UUID                `json:"backup_run_id"`
	Mode           BackupVerificationMode   `json:"mode"`
	Status         BackupVerificationStatus `json:"status"`
	Verified       bool                     `json:"verified"`
	Evidence        map[string]any       `json:"evidence,omitempty"`
	EvidenceDetails map[string]any       `json:"evidence_details,omitempty"`
	Error           string               `json:"error,omitempty"`
	PublishSummary map[string]any           `json:"publish_summary,omitempty"`
	VerifiedAt     *time.Time               `json:"verified_at,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

// BackupRestoreRun is the durable control-plane record for a restore request.
type BackupRestoreRun struct {
	ID                 uuid.UUID                `json:"id"`
	BackupRunID        uuid.UUID                `json:"backup_run_id"`
	RecipeID           uuid.UUID                `json:"recipe_id"`
	RepositoryID       uuid.UUID                `json:"repository_id"`
	PolicyID           *uuid.UUID               `json:"policy_id,omitempty"`
	SnapshotID         string                   `json:"snapshot_id"`
	RestoreTargetRef   string                   `json:"restore_target_ref"`
	RequestedBy        string                   `json:"requested_by"`
	RequestEventID     string                   `json:"request_event_id"`
	RequestKind        int                      `json:"request_kind"`
	RequestDTag        string                   `json:"request_d_tag"`
	ApprovalStatus      BackupApprovalStatus      `json:"approval_status"`
	ApprovalRequired    bool                      `json:"approval_required"`
	ApprovalRequirement BackupApprovalRequirement `json:"approval_requirement"`
	ApprovalEventID     string                    `json:"approval_event_id,omitempty"`
	ApprovedBy          string                    `json:"approved_by,omitempty"`
	ApprovedAt          *time.Time                `json:"approved_at,omitempty"`
	ApprovalMessage     string                    `json:"approval_message,omitempty"`
	ApprovalReasonCode  string                    `json:"approval_reason_code,omitempty"`
	ApprovalReason      map[string]any            `json:"approval_reason,omitempty"`
	Status              DeploymentRunStatus       `json:"status"`
	Backend            BackupBackendKind        `json:"backend"`
	VerificationStatus BackupVerificationStatus `json:"verification_status"`
	Evidence                    map[string]any           `json:"evidence,omitempty"`
	PublishSummary              map[string]any           `json:"publish_summary,omitempty"`
	Error                       string                   `json:"error,omitempty"`
	VerificationPolicyFailure   string                   `json:"verification_policy_failure,omitempty"`
	FailureCategory             BackupFailureCategory    `json:"failure_category,omitempty"`
	Metadata                    map[string]any           `json:"metadata,omitempty"`
	StartedAt          *time.Time               `json:"started_at,omitempty"`
	FinishedAt         *time.Time               `json:"finished_at,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

// BackupRetentionRun is the durable control-plane record for backend-native retention enforcement.
type BackupRetentionRun struct {
	ID             uuid.UUID           `json:"id"`
	RepositoryID   uuid.UUID           `json:"repository_id"`
	PolicyID       *uuid.UUID          `json:"policy_id,omitempty"`
	RequestedBy    string              `json:"requested_by"`
	RequestEventID string              `json:"request_event_id"`
	RequestKind    int                 `json:"request_kind"`
	RequestDTag    string              `json:"request_d_tag"`
	Status         DeploymentRunStatus `json:"status"`
	Backend        BackupBackendKind   `json:"backend"`
	DryRun         bool                `json:"dry_run"`
	Evidence        map[string]any        `json:"evidence,omitempty"`
	PublishSummary  map[string]any        `json:"publish_summary,omitempty"`
	Error           string                `json:"error,omitempty"`
	FailureCategory BackupFailureCategory `json:"failure_category,omitempty"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
	StartedAt      *time.Time          `json:"started_at,omitempty"`
	FinishedAt     *time.Time          `json:"finished_at,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

func (k BackupBackendKind) IsValid() bool {
	switch k {
	case BackupBackendKopia, BackupBackendVelero:
		return true
	default:
		return false
	}
}

func (m BackupVerificationMode) IsValid() bool {
	switch m {
	case BackupVerificationNone, BackupVerificationKopiaSnapshotVerify:
		return true
	default:
		return false
	}
}

func (s BackupVerificationStatus) IsValid() bool {
	switch s {
	case BackupVerificationPending, BackupVerificationSucceeded, BackupVerificationFailed, BackupVerificationSkipped, BackupVerificationUnsupported:
		return true
	default:
		return false
	}
}

func (s BackupApprovalStatus) IsValid() bool {
	switch s {
	case BackupApprovalPending, BackupApprovalApproved, BackupApprovalRejected, BackupApprovalNotRequired:
		return true
	default:
		return false
	}
}

func (s RestoreEligibility) IsValid() bool {
	switch s {
	case RestoreEligibilityUnknown, RestoreEligibilityEligible, RestoreEligibilityRunNotSucceeded, RestoreEligibilitySnapshotMissing, RestoreEligibilityVerificationPending, RestoreEligibilityVerificationFailed, RestoreEligibilityVerificationSkipped, RestoreEligibilityVerificationUnsupported, RestoreEligibilityPolicyBlocked:
		return true
	default:
		return false
	}
}

func (s BackupFailureCategory) IsValid() bool {
	switch s {
	case BackupFailureNone, BackupFailureUnknown, BackupFailureLoadInputs, BackupFailureBackendResolve, BackupFailureBackendHealth, BackupFailureSnapshot, BackupFailureVerification, BackupFailureRestoreExecution, BackupFailureRetentionExecution, BackupFailurePolicy, BackupFailureApprovalRejected, BackupFailureCancelled, BackupFailureTimeout:
		return true
	default:
		return false
	}
}

func (s BackupApprovalRequirement) IsValid() bool {
	switch s {
	case BackupApprovalRequirementNone, BackupApprovalRequirementPolicy:
		return true
	default:
		return false
	}
}

func ValidateBackupRecipe(recipe *BackupRecipe) error {
	if recipe == nil {
		return fmt.Errorf("%w: backup recipe must not be nil", ErrInvalidValue)
	}
	recipe.Name = strings.TrimSpace(recipe.Name)
	recipe.Version = strings.TrimSpace(recipe.Version)
	recipe.TargetRef = strings.TrimSpace(recipe.TargetRef)
	if err := ValidateRequiredString(recipe.Name, "name"); err != nil {
		return err
	}
	if err := ValidateRequiredString(recipe.Version, "version"); err != nil {
		return err
	}
	if !recipe.Backend.IsValid() {
		return fmt.Errorf("%w: backup backend %q is not valid", ErrInvalidValue, recipe.Backend)
	}
	if recipe.Backend != BackupBackendKopia {
		return fmt.Errorf("%w: backup recipes currently support backend %q only; backend %q is valid for non-snapshot capabilities but cannot create backup runs", ErrInvalidValue, BackupBackendKopia, recipe.Backend)
	}
	if err := ValidateRequiredUUID(recipe.RepositoryID, "repository_id"); err != nil {
		return err
	}
	if recipe.PolicyID != nil {
		if err := ValidateRequiredUUID(*recipe.PolicyID, "policy_id"); err != nil {
			return err
		}
	}
	if err := ValidateRequiredString(recipe.TargetRef, "target_ref"); err != nil {
		return err
	}
	if recipe.VerificationMode == "" {
		recipe.VerificationMode = BackupVerificationNone
	}
	if !recipe.VerificationMode.IsValid() {
		return fmt.Errorf("%w: backup verification mode %q is not valid", ErrInvalidValue, recipe.VerificationMode)
	}
	return nil
}

func ValidateBackupPolicy(policy *BackupPolicy) error {
	if policy == nil {
		return fmt.Errorf("%w: backup policy must not be nil", ErrInvalidValue)
	}
	policy.Name = strings.TrimSpace(policy.Name)
	if err := ValidateRequiredString(policy.Name, "name"); err != nil {
		return err
	}
	if policy.VerificationMode == "" {
		policy.VerificationMode = BackupVerificationNone
	}
	if !policy.VerificationMode.IsValid() {
		return fmt.Errorf("%w: backup verification mode %q is not valid", ErrInvalidValue, policy.VerificationMode)
	}
	if policy.RequireVerification && policy.VerificationMode == BackupVerificationNone {
		return fmt.Errorf("%w: verification mode must not be none when verification is required", ErrInvalidValue)
	}
	return nil
}

func ValidateBackupRepository(repo *BackupRepository) error {
	if repo == nil {
		return fmt.Errorf("%w: backup repository must not be nil", ErrInvalidValue)
	}
	repo.Name = strings.TrimSpace(repo.Name)
	repo.RepositoryURI = strings.TrimSpace(repo.RepositoryURI)
	repo.CredentialProfile = strings.TrimSpace(repo.CredentialProfile)
	if err := ValidateRequiredString(repo.Name, "name"); err != nil {
		return err
	}
	if !repo.Backend.IsValid() {
		return fmt.Errorf("%w: backup backend %q is not valid", ErrInvalidValue, repo.Backend)
	}
	if err := ValidateRequiredString(repo.RepositoryURI, "repository_uri"); err != nil {
		return err
	}
	return nil
}

func ValidateBackupDefinition(definition *BackupDefinition) error {
	if definition == nil {
		return fmt.Errorf("%w: backup definition must not be nil", ErrInvalidValue)
	}
	definition.Name = strings.TrimSpace(definition.Name)
	definition.RepositoryName = strings.TrimSpace(definition.RepositoryName)
	definition.PolicyName = strings.TrimSpace(definition.PolicyName)
	definition.RecipeName = strings.TrimSpace(definition.RecipeName)
	definition.RecipeVersion = strings.TrimSpace(definition.RecipeVersion)
	definition.ScheduleExpression = strings.TrimSpace(definition.ScheduleExpression)
	definition.ScheduleJitterWindow = strings.TrimSpace(definition.ScheduleJitterWindow)
	definition.TenantName = strings.TrimSpace(definition.TenantName)
	definition.EnvironmentName = strings.TrimSpace(definition.EnvironmentName)
	definition.OwnerPubkey = strings.TrimSpace(definition.OwnerPubkey)
	definition.ApprovalPolicy = strings.TrimSpace(definition.ApprovalPolicy)
	definition.Group = strings.TrimSpace(definition.Group)
	definition.CreatedBy = strings.TrimSpace(definition.CreatedBy)
	if err := ValidateRequiredString(definition.Name, "name"); err != nil {
		return err
	}
	if err := ValidateRequiredUUID(definition.RepositoryID, "repository_id"); err != nil {
		return err
	}
	if err := ValidateRequiredString(definition.RepositoryName, "repository_name"); err != nil {
		return err
	}
	if err := ValidateRequiredUUID(definition.PolicyID, "policy_id"); err != nil {
		return err
	}
	if err := ValidateRequiredString(definition.PolicyName, "policy_name"); err != nil {
		return err
	}
	if err := ValidateRequiredUUID(definition.RecipeID, "recipe_id"); err != nil {
		return err
	}
	if err := ValidateRequiredString(definition.RecipeName, "recipe_name"); err != nil {
		return err
	}
	if err := ValidateRequiredString(definition.RecipeVersion, "recipe_version"); err != nil {
		return err
	}
	if definition.TenantID != nil {
		if err := ValidateRequiredUUID(*definition.TenantID, "tenant_id"); err != nil {
			return err
		}
	}
	if definition.EnvironmentID != nil {
		if err := ValidateRequiredUUID(*definition.EnvironmentID, "environment_id"); err != nil {
			return err
		}
	}
	if definition.ScheduleEnabled && definition.ScheduleExpression == "" {
		return fmt.Errorf("%w: schedule_expression must not be empty when schedule is enabled", ErrEmptyField)
	}
	if definition.RequiresApproval && definition.ApprovalPolicy == "" {
		return fmt.Errorf("%w: approval_policy must not be empty when approval is required", ErrEmptyField)
	}
	if err := ValidateRequiredString(definition.CreatedBy, "created_by"); err != nil {
		return err
	}
	return nil
}

func ValidateBackupRun(run *BackupRun) error {
	if run == nil {
		return fmt.Errorf("%w: backup run must not be nil", ErrInvalidValue)
	}
	run.RequestedBy = strings.TrimSpace(run.RequestedBy)
	run.RequestEventID = strings.TrimSpace(run.RequestEventID)
	run.RequestDTag = strings.TrimSpace(run.RequestDTag)
	run.TargetRef = strings.TrimSpace(run.TargetRef)
	run.SnapshotID = strings.TrimSpace(run.SnapshotID)
	if err := ValidateRequiredUUID(run.RecipeID, "recipe_id"); err != nil {
		return err
	}
	if err := ValidateRequiredUUID(run.RepositoryID, "repository_id"); err != nil {
		return err
	}
	if run.PolicyID != nil {
		if err := ValidateRequiredUUID(*run.PolicyID, "policy_id"); err != nil {
			return err
		}
	}
	if err := ValidateRequiredString(run.RequestedBy, "requested_by"); err != nil {
		return err
	}
	if err := ValidateRequiredString(run.RequestEventID, "request_event_id"); err != nil {
		return err
	}
	if run.RequestKind == 0 {
		return fmt.Errorf("%w: request_kind must not be zero", ErrInvalidValue)
	}
	if err := ValidateRequiredString(run.RequestDTag, "request_d_tag"); err != nil {
		return err
	}
	if run.Status == "" {
		run.Status = RunStatusQueued
	}
	if err := ValidateDeploymentRunStatus(run.Status); err != nil {
		return err
	}
	if !run.Backend.IsValid() {
		return fmt.Errorf("%w: backup backend %q is not valid", ErrInvalidValue, run.Backend)
	}
	if run.Backend != BackupBackendKopia {
		return fmt.Errorf("%w: backup runs currently support backend %q only; backend %q is valid for non-snapshot capabilities but cannot create backup runs", ErrInvalidValue, BackupBackendKopia, run.Backend)
	}
	if err := ValidateRequiredString(run.TargetRef, "target_ref"); err != nil {
		return err
	}
	if run.VerificationStatus == "" {
		run.VerificationStatus = BackupVerificationPending
	}
	if !run.VerificationStatus.IsValid() {
		return fmt.Errorf("%w: backup verification status %q is not valid", ErrInvalidValue, run.VerificationStatus)
	}
	return nil
}

func ValidateBackupVerificationRecord(record *BackupVerificationRecord) error {
	if record == nil {
		return fmt.Errorf("%w: backup verification record must not be nil", ErrInvalidValue)
	}
	if err := ValidateRequiredUUID(record.BackupRunID, "backup_run_id"); err != nil {
		return err
	}
	if record.Mode == "" {
		record.Mode = BackupVerificationNone
	}
	if !record.Mode.IsValid() {
		return fmt.Errorf("%w: backup verification mode %q is not valid", ErrInvalidValue, record.Mode)
	}
	if record.Status == "" {
		record.Status = BackupVerificationPending
	}
	if !record.Status.IsValid() {
		return fmt.Errorf("%w: backup verification status %q is not valid", ErrInvalidValue, record.Status)
	}
	if record.Verified && record.Status != BackupVerificationSucceeded {
		return fmt.Errorf("%w: verified backup records must have succeeded status", ErrInvalidValue)
	}
	if record.Status == BackupVerificationSucceeded && !record.Verified {
		return fmt.Errorf("%w: succeeded backup verification records must be verified", ErrInvalidValue)
	}
	return nil
}

func ValidateBackupRestoreRun(run *BackupRestoreRun) error {
	if run == nil {
		return fmt.Errorf("%w: backup restore run must not be nil", ErrInvalidValue)
	}
	run.SnapshotID = strings.TrimSpace(run.SnapshotID)
	run.RestoreTargetRef = strings.TrimSpace(run.RestoreTargetRef)
	run.RequestedBy = strings.TrimSpace(run.RequestedBy)
	run.RequestEventID = strings.TrimSpace(run.RequestEventID)
	run.RequestDTag = strings.TrimSpace(run.RequestDTag)
	run.ApprovalEventID = strings.TrimSpace(run.ApprovalEventID)
	run.ApprovedBy = strings.TrimSpace(run.ApprovedBy)
	run.ApprovalMessage = strings.TrimSpace(run.ApprovalMessage)
	if err := ValidateRequiredUUID(run.BackupRunID, "backup_run_id"); err != nil {
		return err
	}
	if err := ValidateRequiredUUID(run.RecipeID, "recipe_id"); err != nil {
		return err
	}
	if err := ValidateRequiredUUID(run.RepositoryID, "repository_id"); err != nil {
		return err
	}
	if run.PolicyID != nil {
		if err := ValidateRequiredUUID(*run.PolicyID, "policy_id"); err != nil {
			return err
		}
	}
	if err := ValidateRequiredString(run.SnapshotID, "snapshot_id"); err != nil {
		return err
	}
	if err := ValidateRequiredString(run.RestoreTargetRef, "restore_target_ref"); err != nil {
		return err
	}
	if err := ValidateRequiredString(run.RequestedBy, "requested_by"); err != nil {
		return err
	}
	if err := ValidateRequiredString(run.RequestEventID, "request_event_id"); err != nil {
		return err
	}
	if run.RequestKind == 0 {
		return fmt.Errorf("%w: request_kind must not be zero", ErrInvalidValue)
	}
	if err := ValidateRequiredString(run.RequestDTag, "request_d_tag"); err != nil {
		return err
	}
	if run.ApprovalStatus == "" {
		run.ApprovalStatus = BackupApprovalPending
	}
	if !run.ApprovalStatus.IsValid() {
		return fmt.Errorf("%w: backup approval status %q is not valid", ErrInvalidValue, run.ApprovalStatus)
	}
	switch run.ApprovalStatus {
	case BackupApprovalApproved:
		if err := ValidateRequiredString(run.ApprovalEventID, "approval_event_id"); err != nil {
			return err
		}
		if err := ValidateRequiredString(run.ApprovedBy, "approved_by"); err != nil {
			return err
		}
		if run.ApprovedAt == nil {
			return fmt.Errorf("%w: approved_at must not be nil for approved restore runs", ErrEmptyField)
		}
	case BackupApprovalRejected:
		if err := ValidateRequiredString(run.ApprovalEventID, "approval_event_id"); err != nil {
			return err
		}
	}
	if run.Status == "" {
		run.Status = RunStatusQueued
	}
	if err := ValidateDeploymentRunStatus(run.Status); err != nil {
		return err
	}
	if !run.Backend.IsValid() {
		return fmt.Errorf("%w: backup backend %q is not valid", ErrInvalidValue, run.Backend)
	}
	if run.VerificationStatus == "" {
		run.VerificationStatus = BackupVerificationPending
	}
	if !run.VerificationStatus.IsValid() {
		return fmt.Errorf("%w: backup verification status %q is not valid", ErrInvalidValue, run.VerificationStatus)
	}
	return nil
}

func ValidateBackupRetentionRun(run *BackupRetentionRun) error {
	if run == nil {
		return fmt.Errorf("%w: backup retention run must not be nil", ErrInvalidValue)
	}
	run.RequestedBy = strings.TrimSpace(run.RequestedBy)
	run.RequestEventID = strings.TrimSpace(run.RequestEventID)
	run.RequestDTag = strings.TrimSpace(run.RequestDTag)
	if err := ValidateRequiredUUID(run.RepositoryID, "repository_id"); err != nil {
		return err
	}
	if run.PolicyID != nil {
		if err := ValidateRequiredUUID(*run.PolicyID, "policy_id"); err != nil {
			return err
		}
	}
	if err := ValidateRequiredString(run.RequestedBy, "requested_by"); err != nil {
		return err
	}
	if err := ValidateRequiredString(run.RequestEventID, "request_event_id"); err != nil {
		return err
	}
	if run.RequestKind == 0 {
		return fmt.Errorf("%w: request_kind must not be zero", ErrInvalidValue)
	}
	if err := ValidateRequiredString(run.RequestDTag, "request_d_tag"); err != nil {
		return err
	}
	if run.Status == "" {
		run.Status = RunStatusQueued
	}
	if err := ValidateDeploymentRunStatus(run.Status); err != nil {
		return err
	}
	if !run.Backend.IsValid() {
		return fmt.Errorf("%w: backup backend %q is not valid", ErrInvalidValue, run.Backend)
	}
	return nil
}

func BackupRunRestoreEligible(run *BackupRun) bool {
	return run != nil && run.Status == RunStatusSucceeded && run.VerificationStatus == BackupVerificationSucceeded
}

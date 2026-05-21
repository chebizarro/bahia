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
	BackupBackendKopia BackupBackendKind = "kopia"
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
	SnapshotCreated    bool                     `json:"snapshot_created"`
	SnapshotID         string                   `json:"snapshot_id,omitempty"`
	VerificationStatus BackupVerificationStatus `json:"verification_status"`
	PublishSummary     map[string]any           `json:"publish_summary,omitempty"`
	Error              string                   `json:"error,omitempty"`
	Metadata           map[string]any           `json:"metadata,omitempty"`
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
	Evidence       map[string]any           `json:"evidence,omitempty"`
	Error          string                   `json:"error,omitempty"`
	PublishSummary map[string]any           `json:"publish_summary,omitempty"`
	VerifiedAt     *time.Time               `json:"verified_at,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

func (k BackupBackendKind) IsValid() bool {
	switch k {
	case BackupBackendKopia:
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

func BackupRunRestoreEligible(run *BackupRun) bool {
	return run != nil && run.Status == RunStatusSucceeded && run.VerificationStatus == BackupVerificationSucceeded
}

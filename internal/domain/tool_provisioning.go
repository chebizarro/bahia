package domain

import (
	"time"

	"github.com/google/uuid"
)

// ToolProvisionIntent represents a request to provision tools.
type ToolProvisionIntent struct {
	ID                  uuid.UUID           `json:"id"`
	ServiceID           uuid.UUID           `json:"service_id"`
	EnvironmentID       uuid.UUID           `json:"environment_id"`
	RequestedTools      []ToolRequest       `json:"requested_tools"`
	ResolvedTools       []ResolvedTool      `json:"resolved_tools,omitempty"`
	SecurityScanResults *SecurityScanResult `json:"security_scan_results,omitempty"`
	ToolsetHash         string              `json:"toolset_hash,omitempty"`
	Status              ToolProvisionStatus `json:"status"`
	ApprovalRequired    bool                `json:"approval_required"`
	ApprovalFlags       []string            `json:"approval_flags,omitempty"`
	ApprovedBy          string              `json:"approved_by,omitempty"`
	ApprovedAt          *time.Time          `json:"approved_at,omitempty"`
	NostrEventID        string              `json:"nostr_event_id,omitempty"`
	RequesterPubkey     string              `json:"requester_pubkey,omitempty"`
	CreatedAt           time.Time           `json:"created_at"`
}

// ToolProvisionRun represents a build attempt.
type ToolProvisionRun struct {
	ID               uuid.UUID  `json:"id"`
	IntentID         uuid.UUID  `json:"intent_id"`
	BaseImageDigest  string     `json:"base_image_digest"`
	BuiltImageDigest string     `json:"built_image_digest,omitempty"`
	ArtifactID       *uuid.UUID `json:"artifact_id,omitempty"`
	BuildLogURL      string     `json:"build_log_url,omitempty"`
	Status           string     `json:"status"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
}

// ToolProfileState tracks current toolset per service/environment.
type ToolProfileState struct {
	ServiceID           uuid.UUID      `json:"service_id"`
	EnvironmentID       uuid.UUID      `json:"environment_id"`
	CurrentToolsetHash  string         `json:"current_toolset_hash,omitempty"`
	CurrentImageDigest  string         `json:"current_image_digest,omitempty"`
	InstalledTools      []ResolvedTool `json:"installed_tools"`
	PreviousImageDigest string         `json:"previous_image_digest,omitempty"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// ToolDenylistEntry tracks blocked packages.
type ToolDenylistEntry struct {
	PackageName string    `json:"package_name"`
	Manager     string    `json:"manager"`
	Reason      string    `json:"reason"`
	Source      string    `json:"source,omitempty"`
	BlockedAt   time.Time `json:"blocked_at"`
	BlockedBy   string    `json:"blocked_by,omitempty"`
}

// ToolRequest describes a requested tool.
type ToolRequest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Manager string `json:"manager"`
}

// ResolvedTool describes a resolved package source/version.
type ResolvedTool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Manager string `json:"manager"`
	Source  string `json:"source"`
}

// SecurityScanResult summarizes vulnerability scan results.
type SecurityScanResult struct {
	CriticalCount int               `json:"critical_count"`
	HighCount     int               `json:"high_count"`
	MediumCount   int               `json:"medium_count"`
	LowCount      int               `json:"low_count"`
	Findings      []SecurityFinding `json:"findings,omitempty"`
}

// SecurityFinding captures one package vulnerability.
type SecurityFinding struct {
	PackageName string `json:"package_name"`
	CVE         string `json:"cve"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// ToolProvisionStatus describes the lifecycle state of tool provisioning.
type ToolProvisionStatus string

const (
	ToolProvisionStatusPending          ToolProvisionStatus = "pending"
	ToolProvisionStatusValidating       ToolProvisionStatus = "validating"
	ToolProvisionStatusAwaitingApproval ToolProvisionStatus = "awaiting_approval"
	ToolProvisionStatusApproved         ToolProvisionStatus = "approved"
	ToolProvisionStatusRejected         ToolProvisionStatus = "rejected"
	ToolProvisionStatusBuilding         ToolProvisionStatus = "building"
	ToolProvisionStatusDeploying        ToolProvisionStatus = "deploying"
	ToolProvisionStatusObserving        ToolProvisionStatus = "observing"
	ToolProvisionStatusCompleted        ToolProvisionStatus = "completed"
	ToolProvisionStatusFailed           ToolProvisionStatus = "failed"
)

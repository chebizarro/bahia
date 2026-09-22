package domain

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type VMOperationKind string

const (
	VMOperationDefine       VMOperationKind = "define"
	VMOperationAdopt        VMOperationKind = "adopt"
	VMOperationStart        VMOperationKind = "start"
	VMOperationGracefulStop VMOperationKind = "graceful_stop"
	VMOperationReboot       VMOperationKind = "reboot"
	VMOperationCheckpoint   VMOperationKind = "checkpoint"
	VMOperationExport       VMOperationKind = "export"
	VMOperationClone        VMOperationKind = "clone"
	VMOperationRestore      VMOperationKind = "restore"
	VMOperationDelete       VMOperationKind = "delete"
)

type VMDeleteTarget string

const (
	VMDeleteDeployment VMDeleteTarget = "deployment"
	VMDeleteCheckpoint VMDeleteTarget = "checkpoint"
	VMDeleteExport     VMDeleteTarget = "export"
)

type VMOperationPhase string

const (
	VMOperationAccepted         VMOperationPhase = "accepted"
	VMOperationAwaitingApproval VMOperationPhase = "awaiting_approval"
	VMOperationExecuting        VMOperationPhase = "executing"
	VMOperationVerifying        VMOperationPhase = "verifying"
	VMOperationSucceeded        VMOperationPhase = "succeeded"
	VMOperationFailed           VMOperationPhase = "failed"
	VMOperationCancelled        VMOperationPhase = "cancelled"
	VMOperationUnconfirmed      VMOperationPhase = "unconfirmed"
)

func (p VMOperationPhase) Terminal() bool {
	return p == VMOperationSucceeded || p == VMOperationFailed || p == VMOperationCancelled
}

// Unconfirmed deliberately holds the exclusive mutation slot until inspection.
func VMOperationTransitionAllowed(from, to VMOperationPhase) bool {
	switch from {
	case VMOperationAwaitingApproval:
		return to == VMOperationAccepted || to == VMOperationCancelled
	case VMOperationAccepted:
		return to == VMOperationExecuting || to == VMOperationCancelled || to == VMOperationFailed
	case VMOperationExecuting:
		return to == VMOperationVerifying || to == VMOperationFailed || to == VMOperationUnconfirmed
	case VMOperationVerifying:
		return to == VMOperationSucceeded || to == VMOperationFailed || to == VMOperationUnconfirmed
	case VMOperationUnconfirmed:
		return to == VMOperationVerifying
	default:
		return false
	}
}

type VMApprovalTier int

const (
	VMApprovalRead        VMApprovalTier = 0
	VMApprovalOperator    VMApprovalTier = 1
	VMApprovalDestructive VMApprovalTier = 2
)
const VMApprovalMaxAge = 30 * time.Minute

func MinimumVMApprovalTier(kind VMOperationKind) VMApprovalTier {
	switch kind {
	case VMOperationAdopt, VMOperationExport, VMOperationRestore, VMOperationDelete:
		return VMApprovalDestructive
	default:
		return VMApprovalOperator
	}
}

type VMChangeClass string

const (
	VMChangeSafeMutable      VMChangeClass = "safe_mutable"
	VMChangeLifecycle        VMChangeClass = "lifecycle"
	VMChangeRequiresStopped  VMChangeClass = "requires_stopped"
	VMChangeRecreateRequired VMChangeClass = "recreate_required"
)

type VMFieldChange struct {
	Field      string        `json:"field"`
	Class      VMChangeClass `json:"class"`
	ReasonCode string        `json:"reason_code"`
}
type VMChangePlan struct {
	LifecycleClass      VMLifecycleClass `json:"lifecycle_class"`
	ExpectedGeneration  int64            `json:"expected_generation"`
	CurrentConfigDigest string           `json:"current_config_digest"`
	DesiredConfigDigest string           `json:"desired_config_digest"`
	Changes             []VMFieldChange  `json:"changes"`
	RequiredTier        VMApprovalTier   `json:"required_tier"`
	RequiresStopped     bool             `json:"requires_stopped"`
	RecreateRequired    bool             `json:"recreate_required"`
}

type VMErrorCode string

const (
	VMErrorInvalid          VMErrorCode = "invalid"
	VMErrorConflict         VMErrorCode = "conflict"
	VMErrorUnavailable      VMErrorCode = "unavailable"
	VMErrorUnsupported      VMErrorCode = "unsupported"
	VMErrorForeign          VMErrorCode = "foreign_resource"
	VMErrorApprovalRequired VMErrorCode = "approval_required"
	VMErrorQuota            VMErrorCode = "quota_exceeded"
	VMErrorUnconfirmed      VMErrorCode = "completion_unconfirmed"
	VMErrorIntegrity        VMErrorCode = "integrity_failure"
)

// VMDiagnostic contains a closed code and optional content-addressed redacted
// evidence, not arbitrary provider stderr, URLs, credentials or host paths.
type VMDiagnostic struct {
	Code           VMErrorCode `json:"code,omitempty"`
	EvidenceDigest string      `json:"evidence_digest,omitempty"`
}
type VMProviderError struct {
	Code        VMErrorCode
	Retryable   bool
	Unconfirmed bool
	Cause       error
}

func (e *VMProviderError) Error() string { return "VM provider: " + string(e.Code) }
func (e *VMProviderError) Unwrap() error { return e.Cause }

type VMApproval struct {
	SchemaVersion       int              `json:"schema_version"`
	ID                  uuid.UUID        `json:"id"`
	OrgID               uuid.UUID        `json:"org_id"`
	ResourceID          uuid.UUID        `json:"resource_id"`
	LifecycleClass      VMLifecycleClass `json:"lifecycle_class"`
	Generation          int64            `json:"generation"`
	RequestHash         string           `json:"request_hash"`
	ProviderFingerprint string           `json:"provider_fingerprint"`
	Tier                VMApprovalTier   `json:"tier"`
	Requester           string           `json:"requester"`
	Approver            string           `json:"approver"`
	Reason              string           `json:"reason"`
	CreatedAt           time.Time        `json:"created_at"`
	ExpiresAt           time.Time        `json:"expires_at"`
	ConsumedAt          *time.Time       `json:"consumed_at,omitempty"`
}
type VMOperation struct {
	VirtualizationResourceMeta
	LifecycleClass        VMLifecycleClass `json:"lifecycle_class"`
	ResourceID            uuid.UUID        `json:"resource_id"`
	ResourceGeneration    int64            `json:"resource_generation"`
	ExpectedGeneration    int64            `json:"expected_generation"`
	IdempotencyKey        string           `json:"idempotency_key"`
	RequestHash           string           `json:"request_hash"`
	Actor                 string           `json:"actor"`
	Reason                string           `json:"reason"`
	Kind                  VMOperationKind  `json:"kind"`
	Phase                 VMOperationPhase `json:"phase"`
	RequiredTier          VMApprovalTier   `json:"required_tier"`
	ApprovalID            *uuid.UUID       `json:"approval_id,omitempty"`
	ProviderFingerprint   string           `json:"provider_fingerprint"`
	ProviderCorrelationID uuid.UUID        `json:"provider_correlation_id"`
	Deadline              time.Time        `json:"deadline"`
	// Prepared storage object IDs are durable before side effects. Never store paths.
	PreparedStorageRefs []uuid.UUID    `json:"prepared_storage_refs"`
	CheckpointID        *uuid.UUID     `json:"checkpoint_id,omitempty"`
	ExportID            *uuid.UUID     `json:"export_id,omitempty"`
	CloneTargetID       *uuid.UUID     `json:"clone_target_id,omitempty"`
	Plan                *VMChangePlan  `json:"plan,omitempty"`
	AllowForceStop      bool           `json:"allow_force_stop"`
	DeleteTarget        VMDeleteTarget `json:"delete_target,omitempty"`
	Outcome             VMDiagnostic   `json:"outcome"`
	CompletedAt         *time.Time     `json:"completed_at,omitempty"`
}

// PersistentVMProvider is Item B's capability; it does not replace Hypervisor.
// Execute accepts only durably admitted operations, enforces exact ownership and
// the earlier caller/policy deadline, and never authorizes or selects a fallback.
type PersistentVMProvider interface {
	Inspect(context.Context, VMResourceIdentity) (*VMObservation, error)
	Inventory(context.Context, VirtualizationHost) ([]VMInventoryEntry, error)
	PlanChange(context.Context, VMChangeRequest) (*VMChangePlan, error)
	Execute(context.Context, VMProviderOperation) (*VMProviderResult, error)
}
type VMInventoryEntry struct {
	LifecycleClass     VMLifecycleClass   `json:"lifecycle_class"`
	ProviderResourceID uuid.UUID          `json:"provider_resource_id"`
	Ownership          VMOwnershipClass   `json:"ownership"`
	Marker             *VMOwnershipMarker `json:"marker,omitempty"`
	Observation        VMObservation      `json:"observation"`
}
type VMChangeRequest struct {
	Host        VirtualizationHost
	Current     PersistentVMDeployment
	Desired     PersistentVMDeployment
	Image       VMImage
	Observation VMObservation
}

// VMProviderOperation is intentionally non-serializable: it may hold a private,
// operation-scoped bootstrap delivery capability. Durable/public shapes are above.
type VMProviderOperation struct {
	Host        VirtualizationHost
	Deployment  PersistentVMDeployment
	Image       VMImage
	Operation   VMOperation
	Plan        VMChangePlan
	Checkpoint  *VMCheckpoint
	Export      *VMExport
	CloneTarget *PersistentVMDeployment
	Bootstrap   VMBootstrapDelivery
}

func (VMProviderOperation) MarshalJSON() ([]byte, error) { return nil, ErrInvalidValue }

// VMBootstrapDelivery is implemented by C using audited secret resolution; B
// invokes it only over a supported guest channel, never argv/XML/disk metadata.
type VMBootstrapDelivery interface {
	Deliver(context.Context, VMResourceIdentity, VMBootstrapGuest) error
}
type VMBootstrapGuest interface {
	ApplyBootstrap(context.Context, string, []byte) error
}
type VMProviderResult struct {
	LifecycleClass      VMLifecycleClass `json:"lifecycle_class"`
	OperationID         uuid.UUID        `json:"operation_id"`
	Confirmed           bool             `json:"confirmed"`
	Observation         *VMObservation   `json:"observation,omitempty"`
	Checkpoint          *VMCheckpoint    `json:"checkpoint,omitempty"`
	Export              *VMExport        `json:"export,omitempty"`
	RetainedStorageRefs []uuid.UUID      `json:"retained_storage_refs"`
	Diagnostic          VMDiagnostic     `json:"diagnostic"`
}

package repository

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"time"
)

// VirtualizationResources is tenant-scoped. Get returns ErrNotFound, not nil.
// Create requires generation 1; Update requires value.Generation == expected+1.
// Every successful mutation appends a change in the same transaction. Image
// updates are forbidden; checkpoint/export manifests freeze once ready.
// Observations supplied in Create/Update are rejected: use observation methods.
type VirtualizationResources[T any] interface {
	Create(context.Context, *T) error
	Get(context.Context, uuid.UUID, uuid.UUID) (*T, error)
	List(context.Context, uuid.UUID, int, int) ([]T, error)
	Update(context.Context, *T, int64) error
}
type VirtualizationHostRepository = VirtualizationResources[domain.VirtualizationHost]
type VMImageRepository = VirtualizationResources[domain.VMImage]
type PersistentVMDeploymentRepository = VirtualizationResources[domain.PersistentVMDeployment]
type ExecutionPlaneDeploymentRepository = VirtualizationResources[domain.ExecutionPlaneDeployment]
type VMCheckpointRepository = VirtualizationResources[domain.VMCheckpoint]
type VMExportRepository = VirtualizationResources[domain.VMExport]

type VirtualizationResourceRef struct {
	OrgID uuid.UUID
	Kind  domain.VirtualizationResourceKind
	ID    uuid.UUID
}

// VMOperationAdmission is all-or-nothing: optional desired revision and capacity
// reservation, expected generation, approval consumption, operation, and journal.
// For initial define, Desired is required at generation 1 and ExpectedGeneration=0.
// Otherwise Desired, when present, must be expected+1. ResourceGeneration always
// names the resulting revision. Checkpoint/export storage reservations target
// an already-created artifact. Replays return the original operation unchanged.
type VMOperationAdmission struct {
	Operation          domain.VMOperation
	ExpectedGeneration int64
	Desired            *domain.PersistentVMDeployment
	Reservations       []domain.VMCapacityReservation
}
type VMOperationAdmissionResult struct {
	Operation domain.VMOperation
	Created   bool
}
type VMOperationTransition struct {
	OrgID               uuid.UUID
	OperationID         uuid.UUID
	ExpectedPhase       domain.VMOperationPhase
	ExpectedRevision    int64
	Phase               domain.VMOperationPhase
	Outcome             domain.VMDiagnostic
	PreparedStorageRefs []uuid.UUID
	// ApprovalID is consumed atomically for awaiting_approval -> accepted.
	ApprovalID *uuid.UUID
}
type VirtualizationResourceChange struct {
	Sequence         int64                             `json:"sequence"`
	SchemaVersion    int                               `json:"schema_version"`
	OrgID            uuid.UUID                         `json:"org_id"`
	ResourceKind     domain.VirtualizationResourceKind `json:"resource_kind"`
	ResourceID       uuid.UUID                         `json:"resource_id"`
	Generation       int64                             `json:"generation"`
	LifecycleClasses []domain.VMLifecycleClass         `json:"lifecycle_classes"`
	ChangeType       string                            `json:"change_type"`
	Document         json.RawMessage                   `json:"document"`
	Observation      json.RawMessage                   `json:"observation,omitempty"`
	ApprovalID       *uuid.UUID                        `json:"approval_id,omitempty"`
	OccurredAt       time.Time                         `json:"occurred_at"`
}

// VirtualizationRepository is Item A's composition root. Operations never run
// provider effects. C performs authorization and live image/fingerprint checks;
// these methods additionally enforce persistence invariants and tenant fencing.
type VirtualizationRepository interface {
	Hosts() VirtualizationHostRepository
	Images() VMImageRepository
	Deployments() PersistentVMDeploymentRepository
	ExecutionPlanes() ExecutionPlaneDeploymentRepository
	Checkpoints() VMCheckpointRepository
	Exports() VMExportRepository
	ReserveCapacity(context.Context, domain.VMCapacityReservation) error
	ReleaseCapacity(context.Context, uuid.UUID, uuid.UUID) error
	ListReservations(context.Context, uuid.UUID, uuid.UUID) ([]domain.VMCapacityReservation, error)
	// CAS session rotation is a trusted orchestration action, not observation data.
	RotateObservationSession(context.Context, VirtualizationResourceRef, int64, uuid.UUID, uuid.UUID) error
	AcceptHostObservation(context.Context, uuid.UUID, uuid.UUID, domain.VirtualizationHostObservation) error
	AcceptVMObservation(context.Context, uuid.UUID, uuid.UUID, domain.VMObservation) error
	AcceptPlaneObservation(context.Context, uuid.UUID, uuid.UUID, domain.ExecutionPlaneObservation) error
	CreateApproval(context.Context, *domain.VMApproval) error
	GetApproval(context.Context, uuid.UUID, uuid.UUID) (*domain.VMApproval, error)
	AdmitOperation(context.Context, VMOperationAdmission) (*VMOperationAdmissionResult, error)
	GetOperation(context.Context, uuid.UUID, uuid.UUID) (*domain.VMOperation, error)
	ListOperations(context.Context, uuid.UUID, uuid.UUID, int, int) ([]domain.VMOperation, error)
	TransitionOperation(context.Context, VMOperationTransition) (*domain.VMOperation, error)
	// WithOperationLock holds a session advisory lock on the exact deployment for
	// provider execution. It is not external fencing; connection loss is unconfirmed.
	WithOperationLock(context.Context, uuid.UUID, uuid.UUID, func(context.Context) error) error
	ListChanges(context.Context, uuid.UUID, int64, int) ([]VirtualizationResourceChange, error)
}

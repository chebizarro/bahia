package domain

import "github.com/google/uuid"

// BackupExecutorSelectionPolicy controls how eligible backup executor workers are selected.
type BackupExecutorSelectionPolicy string

const (
	BackupExecutorSelectionLeastQueued   BackupExecutorSelectionPolicy = "least_queued"
	BackupExecutorSelectionFirstEligible BackupExecutorSelectionPolicy = "first_eligible"
	BackupExecutorSelectionAllEligible   BackupExecutorSelectionPolicy = "all_eligible"
)

func (p BackupExecutorSelectionPolicy) IsValid() bool {
	switch p {
	case BackupExecutorSelectionLeastQueued, BackupExecutorSelectionFirstEligible, BackupExecutorSelectionAllEligible:
		return true
	default:
		return false
	}
}

// BackupPlacementReasonCode identifies a structured placement explanation bucket.
type BackupPlacementReasonCode string

const (
	BackupPlacementReasonPlaceable          BackupPlacementReasonCode = "placeable"
	BackupPlacementReasonInvalidDefinition  BackupPlacementReasonCode = "invalid_definition"
	BackupPlacementReasonBackendUnsupported BackupPlacementReasonCode = "backend_unsupported"
	BackupPlacementReasonNoWorkers          BackupPlacementReasonCode = "no_workers"
	BackupPlacementReasonWorkerStatus       BackupPlacementReasonCode = "worker_status"
	BackupPlacementReasonWorkerScheduling   BackupPlacementReasonCode = "worker_scheduling"
	BackupPlacementReasonLabelMismatch      BackupPlacementReasonCode = "executor_label_mismatch"
	BackupPlacementReasonCapabilityMismatch BackupPlacementReasonCode = "capability_mismatch"
)

// BackupPlacementReason is an operator-facing explanation for one placement decision or candidate.
type BackupPlacementReason struct {
	Code                BackupPlacementReasonCode `json:"code"`
	Message             string                    `json:"message"`
	WorkerPubKey        string                    `json:"worker_pubkey,omitempty"`
	WorkerName          string                    `json:"worker_name,omitempty"`
	MissingLabels       []string                  `json:"missing_labels,omitempty"`
	MissingCapabilities []string                  `json:"missing_capabilities,omitempty"`
	Backend             BackupBackendKind         `json:"backend,omitempty"`
}

// BackupPlacementCandidate records one worker's backup executor eligibility.
type BackupPlacementCandidate struct {
	WorkerPubKey string                  `json:"worker_pubkey"`
	WorkerName   string                  `json:"worker_name"`
	Eligible     bool                    `json:"eligible"`
	Score        float64                 `json:"score"`
	Reasons      []BackupPlacementReason `json:"reasons,omitempty"`
}

// BackupPlacementDecision explains whether and where a backup definition can run.
type BackupPlacementDecision struct {
	DefinitionID          uuid.UUID                     `json:"definition_id"`
	Placeable             bool                          `json:"placeable"`
	SelectionPolicy       BackupExecutorSelectionPolicy `json:"selection_policy"`
	SelectedWorkerPubKeys []string                      `json:"selected_worker_pubkeys,omitempty"`
	Candidates            []BackupPlacementCandidate    `json:"candidates,omitempty"`
	Reasons               []BackupPlacementReason       `json:"reasons,omitempty"`
}

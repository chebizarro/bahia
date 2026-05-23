package domain

import "time"

// WorkerAssignmentType identifies the worker-backed workload family.
type WorkerAssignmentType string

const (
	WorkerAssignmentService   WorkerAssignmentType = "service"
	WorkerAssignmentInference WorkerAssignmentType = "inference"
	WorkerAssignmentCI        WorkerAssignmentType = "ci"
	WorkerAssignmentRecipe    WorkerAssignmentType = "recipe"
)

// WorkerAssignment describes one workload currently attached to a worker.
type WorkerAssignment struct {
	Type       WorkerAssignmentType `json:"type"`
	WorkloadID string               `json:"workload_id"`
	Status     string               `json:"status"`
	Pinned     bool                 `json:"pinned"`
	Movable    bool                 `json:"movable"`
	StartedAt  *time.Time           `json:"started_at,omitempty"`
	UpdatedAt  time.Time            `json:"updated_at"`
	Metadata   map[string]any       `json:"metadata,omitempty"`
}

// WorkerAssignmentState is the operator read model for current worker work.
type WorkerAssignmentState struct {
	WorkerPubKey      string             `json:"worker_pubkey"`
	ActiveAssignments []WorkerAssignment `json:"active_assignments"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// WorkerDrainStatus is the operator read model for a worker drain lifecycle.
type WorkerDrainStatus struct {
	WorkerPubKey               string                `json:"worker_pubkey"`
	DrainStartedAt             *time.Time            `json:"drain_started_at,omitempty"`
	Reason                     string                `json:"reason,omitempty"`
	RemainingAssignments       []WorkerAssignment    `json:"remaining_assignments"`
	PinnedBlockers             []WorkerAssignment    `json:"pinned_blockers"`
	LastMigrationAttemptAt     *time.Time            `json:"last_migration_attempt_at,omitempty"`
	LastMigrationAttemptReason string                `json:"last_migration_attempt_reason,omitempty"`
	SafeToEnterMaintenance     bool                  `json:"safe_to_enter_maintenance"`
	SafeToDisable              bool                  `json:"safe_to_disable"`
	SchedulingState            WorkerSchedulingState `json:"scheduling_state"`
	UpdatedAt                  time.Time             `json:"updated_at"`
}

// WorkerEligibilityCandidate explains one worker's preview ranking result.
type WorkerEligibilityCandidate struct {
	WorkerPubKey string  `json:"worker_pubkey"`
	WorkerName   string  `json:"worker_name,omitempty"`
	Eligible     bool    `json:"eligible"`
	Score        float64 `json:"score"`
	Reason       string  `json:"reason"`
}

// WorkerEligibilityPreview is the operator read model for a proposed placement.
type WorkerEligibilityPreview struct {
	PreviewID       string                       `json:"preview_id"`
	WorkloadType    string                       `json:"workload_type"`
	Policy          map[string]any               `json:"policy,omitempty"`
	EligibleWorkers []WorkerEligibilityCandidate `json:"eligible_workers"`
	RejectedWorkers []WorkerEligibilityCandidate `json:"rejected_workers"`
	RankingScores   []WorkerEligibilityCandidate `json:"ranking_scores"`
	SelectedWinner  *WorkerEligibilityCandidate  `json:"selected_winner,omitempty"`
	UpdatedAt       time.Time                    `json:"updated_at"`
}

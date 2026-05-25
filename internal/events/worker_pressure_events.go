package events

import (
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// WorkerTelemetryObserved carries the canonical worker snapshot after a current
// Kind 10100 advertisement has been ingested and pressure has been assessed.
type WorkerTelemetryObserved struct {
	Worker domain.Worker `json:"worker"`
}

// WorkerPressureChanged carries the previous and current Bahia pressure
// assessments for a worker capacity-class transition.
type WorkerPressureChanged struct {
	WorkerPubKey string                           `json:"worker_pubkey"`
	Previous     *domain.WorkerPressureAssessment `json:"previous,omitempty"`
	Current      *domain.WorkerPressureAssessment `json:"current,omitempty"`
	ChangedAt    time.Time                        `json:"changed_at"`
}

// WorkerCleanupEvent reports cleanup orchestration lifecycle for a worker.
type WorkerCleanupEvent struct {
	WorkerPubKey     string     `json:"worker_pubkey"`
	CleanupMode      string     `json:"cleanup_mode"`
	Reason           string     `json:"reason,omitempty"`
	LoomJobID        string     `json:"loom_job_id,omitempty"`
	ProtectedRefs    []string   `json:"protected_refs,omitempty"`
	TargetFreeGB     int        `json:"target_free_gb"`
	Status           string     `json:"status"`
	CapacityRejected bool       `json:"capacity_rejected,omitempty"`
	Error            string     `json:"error,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}

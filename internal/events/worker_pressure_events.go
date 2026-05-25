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

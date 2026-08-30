package runtime

import (
	"context"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// HealthObserver observes one concrete managed runtime instance without applying recovery policy.
type HealthObserver interface {
	ObserveInstance(ctx context.Context, key domain.ManagedInstanceKey) (*InstanceObservation, error)
}

// ManagedInstanceController controls exactly one resolved managed runtime instance.
type ManagedInstanceController interface {
	RestartInstance(ctx context.Context, key domain.ManagedInstanceKey) error
	StopInstance(ctx context.Context, key domain.ManagedInstanceKey) error
}

// InstanceObservation is a detailed point-in-time view of one managed runtime instance.
type InstanceObservation struct {
	Key                domain.ManagedInstanceKey   `json:"key"`
	Status             domain.InstanceHealthStatus `json:"status"`
	RawStatus          string                      `json:"raw_status,omitempty"`
	HealthStatus       string                      `json:"health_status,omitempty"`
	OOMKilled          bool                        `json:"oom_killed"`
	ExitCode           int                         `json:"exit_code"`
	RestartCount       int                         `json:"restart_count"`
	StartedAt          *time.Time                  `json:"started_at,omitempty"`
	FinishedAt         *time.Time                  `json:"finished_at,omitempty"`
	MemoryCurrentBytes int64                       `json:"memory_current_bytes,omitempty"`
	MemoryPeakBytes    int64                       `json:"memory_peak_bytes,omitempty"`
	MemoryLimitBytes   int64                       `json:"memory_limit_bytes,omitempty"`
	Detail             string                      `json:"detail,omitempty"`
	ProbeResult        *ProbeResult                `json:"probe_result,omitempty"`
	ObservedAt         time.Time                   `json:"observed_at"`
}

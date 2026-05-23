package domain

import "time"

const (
	PowerThermalStateNormal   = "normal"
	PowerThermalStateElevated = "elevated"
	PowerThermalStateCritical = "critical"
)

// PowerObservation records worker power and environmental state observed by the control plane.
type PowerObservation struct {
	Source         string        `json:"source"`
	WorkerPubKey   string        `json:"worker_pubkey"`
	UPSRuntime     time.Duration `json:"ups_runtime"`
	BatteryPercent float64       `json:"battery_percent"`
	ThermalState   string        `json:"thermal_state"`
	ObservedAt     time.Time     `json:"observed_at"`
}

// PowerRecommendation is an advisory continuity-mode change derived from power state.
type PowerRecommendation struct {
	ServiceKey      string         `json:"service_key"`
	RecommendedMode ContinuityMode `json:"recommended_mode"`
	Reason          string         `json:"reason"`
	AutoExecute     bool           `json:"auto_execute"`
}

package domain

import "time"

// ReplicationPolicy defines service state replication targets for continuity modes.
type ReplicationPolicy struct {
	ServiceKey    string              `json:"service_key"`
	Targets       []ReplicationTarget `json:"targets"`
	UpdatedAt     time.Time           `json:"updated_at"`
	SourceEventID string              `json:"source_event_id,omitempty"`
}

// ReplicationTarget identifies one worker replication destination and its continuity requirements.
type ReplicationTarget struct {
	WorkerPubKey     string           `json:"worker_pubkey"`
	Strategy         string           `json:"strategy"`
	MaxStaleness     time.Duration    `json:"max_staleness"`
	RequiredForModes []ContinuityMode `json:"required_for_modes,omitempty"`
}

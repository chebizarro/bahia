package domain

import "time"

// HeartbeatObservation records one active worker heartbeat as observed by the control plane.
type HeartbeatObservation struct {
	WorkerPubKey string        `json:"worker_pubkey"`
	ObservedAt   time.Time     `json:"observed_at"`
	Sequence     uint64        `json:"sequence"`
	Interval     time.Duration `json:"interval"`
	ExpiresAfter time.Duration `json:"expires_after"`
}

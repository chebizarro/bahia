package domain

import (
	"testing"
	"time"
)

func TestHeartbeatObservationFields(t *testing.T) {
	observedAt := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	obs := HeartbeatObservation{
		WorkerPubKey: "worker-pubkey",
		ObservedAt:   observedAt,
		Sequence:     42,
		Interval:     10 * time.Second,
		ExpiresAfter: 30 * time.Second,
	}

	if obs.WorkerPubKey != "worker-pubkey" {
		t.Fatalf("WorkerPubKey = %q", obs.WorkerPubKey)
	}
	if !obs.ObservedAt.Equal(observedAt) {
		t.Fatalf("ObservedAt = %s, want %s", obs.ObservedAt, observedAt)
	}
	if obs.Sequence != 42 {
		t.Fatalf("Sequence = %d, want 42", obs.Sequence)
	}
	if obs.Interval != 10*time.Second {
		t.Fatalf("Interval = %s, want 10s", obs.Interval)
	}
	if obs.ExpiresAfter != 30*time.Second {
		t.Fatalf("ExpiresAfter = %s, want 30s", obs.ExpiresAfter)
	}
}

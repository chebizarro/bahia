package domain

import (
	"testing"
	"time"
)

func TestReplicationPolicyCarriesContinuityTargets(t *testing.T) {
	policy := ReplicationPolicy{
		ServiceKey:    "svc.api",
		UpdatedAt:     time.Unix(1710000000, 0).UTC(),
		SourceEventID: "event-31403",
		Targets: []ReplicationTarget{
			{
				WorkerPubKey:     "worker-standby-hot",
				Strategy:         "event_mirror",
				MaxStaleness:     30 * time.Second,
				RequiredForModes: []ContinuityMode{ContinuityModeFull, ContinuityModeDegraded},
			},
			{
				WorkerPubKey:     "worker-secret-backup",
				Strategy:         "secret_backup",
				MaxStaleness:     5 * time.Minute,
				RequiredForModes: []ContinuityMode{ContinuityModeEmergency},
			},
		},
	}

	if policy.ServiceKey != "svc.api" {
		t.Fatalf("ServiceKey = %q", policy.ServiceKey)
	}
	if len(policy.Targets) != 2 {
		t.Fatalf("Targets len = %d", len(policy.Targets))
	}
	if policy.Targets[0].Strategy != "event_mirror" {
		t.Fatalf("Strategy = %q", policy.Targets[0].Strategy)
	}
	if policy.Targets[0].MaxStaleness != 30*time.Second {
		t.Fatalf("MaxStaleness = %s", policy.Targets[0].MaxStaleness)
	}
	if got := policy.Targets[0].RequiredForModes[1]; got != ContinuityModeDegraded {
		t.Fatalf("RequiredForModes[1] = %q", got)
	}
}

func TestReplicationTargetSupportsPlannedStrategiesAsStrings(t *testing.T) {
	strategies := []string{"snapshot", "incremental", "event_mirror", "secret_backup", "scb_sync"}

	for _, strategy := range strategies {
		target := ReplicationTarget{WorkerPubKey: "worker", Strategy: strategy}
		if target.Strategy != strategy {
			t.Fatalf("Strategy = %q, want %q", target.Strategy, strategy)
		}
	}
}

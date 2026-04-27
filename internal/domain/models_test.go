package domain

import "testing"

func TestBuildStatusValues(t *testing.T) {
	statuses := []BuildStatus{
		BuildStatusQueued,
		BuildStatusRunning,
		BuildStatusSucceeded,
		BuildStatusFailed,
		BuildStatusCancelled,
	}

	for _, s := range statuses {
		if s == "" {
			t.Error("empty build status")
		}
	}
}

func TestDeploymentIntentStatusValues(t *testing.T) {
	statuses := []DeploymentIntentStatus{
		IntentStatusPending,
		IntentStatusApproved,
		IntentStatusRejected,
		IntentStatusSuperseded,
		IntentStatusDeploying,
		IntentStatusDeployed,
		IntentStatusFailed,
		IntentStatusRolledBack,
	}

	for _, s := range statuses {
		if s == "" {
			t.Error("empty intent status")
		}
	}

	if len(statuses) != 8 {
		t.Errorf("expected 8 intent statuses, got %d", len(statuses))
	}
}

func TestDriftStatusValues(t *testing.T) {
	statuses := []DriftStatus{
		DriftStatusUnknown,
		DriftStatusInSync,
		DriftStatusDrifted,
		DriftStatusDeploying,
	}

	expected := map[DriftStatus]bool{
		"unknown":   true,
		"in_sync":   true,
		"drifted":   true,
		"deploying": true,
	}

	for _, s := range statuses {
		if !expected[s] {
			t.Errorf("unexpected drift status: %s", s)
		}
	}
}

func TestHealthStatusValues(t *testing.T) {
	if HealthStatusHealthy != "healthy" {
		t.Error("unexpected healthy status value")
	}
	if HealthStatusStopped != "stopped" {
		t.Error("unexpected stopped status value")
	}
}

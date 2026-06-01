package kinds

import (
	"testing"
)

func TestIsRequestKind(t *testing.T) {
	testCases := []struct {
		name     string
		kind     int
		expected bool
	}{
		// DNS requests
		{"DNS zone create", DNSZoneCreateRequest, true},
		{"DNS backend register", DNSBackendRegisterRequest, true},
		// Core requests
		{"Deploy request", DeployRequest, true},
		{"Rollback request", RollbackRequest, true},
		{"Service action", ServiceAction, true},
		{"Policy evaluate", PolicyEvaluate, true},
		// Encrypted request is NOT a regular request
		{"Encrypted request", EncryptedRequest, false},
		// Package requests
		{"Package repository apply", PackageRepositoryApply, true},
		{"Package yank request", PackageYankRequest, true},
		// Worker requests
		{"Worker cordon", WorkerCordonRequest, true},
		{"Worker cleanup", WorkerCleanupRequest, true},
		// ML requests
		{"ML recipe run", MLRecipeRunRequest, true},
		{"ML model import", MLModelImportRequest, true},
		// Backup requests
		{"Backup run request", BackupRunRequest, true},
		{"Backup repository probe", BackupRepositoryProbe, true},
		// Assistant requests
		{"Assistant prompt request", AssistantPromptRequest, true},
		{"Assistant approval", AssistantApproval, true},
		// Continuity requests
		{"Failover request", FailoverRequest, true},
		{"Recovery request", RecoveryRequest, true},
		// Non-request kinds
		{"DNS operation status", DNSOperationStatus, false},
		{"Deployment result", DeploymentResult, false},
		{"Service registry", ServiceRegistry, false},
		{"Worker state", WorkerState, false},
		{"Loom worker ad", LoomWorkerAdvertisement, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsRequestKind(tc.kind)
			if got != tc.expected {
				t.Errorf("IsRequestKind(%d) = %v, want %v", tc.kind, got, tc.expected)
			}
		})
	}
}

func TestIsBahiaProjectionKind(t *testing.T) {
	testCases := []struct {
		name     string
		kind     int
		expected bool
	}{
		// DNS status/results
		{"DNS operation status", DNSOperationStatus, true},
		{"DNS zone create result", DNSZoneCreateResult, true},
		{"DNS backend register result", DNSBackendRegisterResult, true},
		// Core status kinds
		{"Deployment status", DeploymentStatus, true},
		{"Service status", ServiceStatus, true},
		{"Package status", PackageStatus, true},
		{"Worker status", WorkerStatus, true},
		// Core result kinds
		{"Deployment result", DeploymentResult, true},
		{"Action result", ActionResult, true},
		{"Encrypted result", EncryptedResult, true},
		{"Package result", PackageResult, true},
		{"Worker result", WorkerResult, true},
		// Read-model kinds
		{"Service registry", ServiceRegistry, true},
		{"Environment registry", EnvironmentRegistry, true},
		{"DNS zone state", DNSZoneState, true},
		{"DNS backend state", DNSBackendState, true},
		{"Worker state", WorkerState, true},
		{"Worker assignment state", WorkerAssignmentState, true},
		// Backup kinds
		{"Backup run status", BackupRunStatus, true},
		{"Backup run result", BackupRunResult, true},
		{"Backup definition registry", BackupDefinitionRegistry, true},
		// ML kinds
		{"ML model registry", MLModelRegistry, true},
		{"ML recipe run result", MLRecipeRunResult, true},
		// Assistant kinds
		{"Assistant session", AssistantSession, true},
		{"Assistant status", AssistantStatus, true},
		{"Assistant result", AssistantResult, true},
		// Audit kinds
		{"Build registered audit", BuildRegistered, true},
		{"DNS zone synced audit", DNSZoneSyncedAudit, true},
		// Continuity kinds
		{"Continuity profile", ContinuityProfile, true},
		{"Heartbeat observation", HeartbeatObservation, true},
		// Request kinds should NOT be projection kinds
		{"Deploy request", DeployRequest, false},
		{"DNS zone create request", DNSZoneCreateRequest, false},
		{"Backup run request", BackupRunRequest, false},
		// Open interop kinds
		{"Loom worker ad", LoomWorkerAdvertisement, false},
		{"Hive CI workflow run", HiveCIWorkflowRun, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsBahiaProjectionKind(tc.kind)
			if got != tc.expected {
				t.Errorf("IsBahiaProjectionKind(%d) = %v, want %v", tc.kind, got, tc.expected)
			}
		})
	}
}

func TestIsOpenInteropKind(t *testing.T) {
	testCases := []struct {
		name     string
		kind     int
		expected bool
	}{
		{"Loom worker ad", LoomWorkerAdvertisement, true},
		{"Loom job status", LoomJobStatusUpdate, true},
		{"Loom job result", LoomJobResult, true},
		{"Loom job cancellation", LoomJobCancellation, true},
		{"Hive CI workflow run", HiveCIWorkflowRun, true},
		{"Hive CI workflow result", HiveCIWorkflowResult, true},
		// Non-interop kinds
		{"Deploy request", DeployRequest, false},
		{"Service registry", ServiceRegistry, false},
		{"Worker state", WorkerState, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsOpenInteropKind(tc.kind)
			if got != tc.expected {
				t.Errorf("IsOpenInteropKind(%d) = %v, want %v", tc.kind, got, tc.expected)
			}
		})
	}
}

func TestIsReadableKind(t *testing.T) {
	// All projection kinds and interop kinds should be readable
	testCases := []struct {
		name     string
		kind     int
		expected bool
	}{
		// Projection kinds
		{"DNS operation status", DNSOperationStatus, true},
		{"Service registry", ServiceRegistry, true},
		{"Worker state", WorkerState, true},
		{"Encrypted result", EncryptedResult, true},
		// Interop kinds
		{"Loom worker ad", LoomWorkerAdvertisement, true},
		{"Hive CI workflow run", HiveCIWorkflowRun, true},
		// Request kinds are not readable without author scope
		{"Deploy request", DeployRequest, false},
		{"DNS zone create request", DNSZoneCreateRequest, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsReadableKind(tc.kind)
			if got != tc.expected {
				t.Errorf("IsReadableKind(%d) = %v, want %v", tc.kind, got, tc.expected)
			}
		})
	}
}

func TestDNSKindsCompleteness(t *testing.T) {
	// Verify all DNS kinds are covered by the policy functions
	requestKinds := DNSRequestKinds()
	for _, kind := range requestKinds {
		if !IsRequestKind(kind) {
			t.Errorf("DNS request kind %d not recognized as request kind", kind)
		}
	}

	resultKinds := DNSResultKinds()
	for _, kind := range resultKinds {
		if !IsBahiaProjectionKind(kind) {
			t.Errorf("DNS result kind %d not recognized as projection kind", kind)
		}
	}

	readModelKinds := DNSReadModelKinds()
	for _, kind := range readModelKinds {
		if !IsBahiaProjectionKind(kind) {
			t.Errorf("DNS read-model kind %d not recognized as projection kind", kind)
		}
	}

	// DNS operation status
	if !IsBahiaProjectionKind(DNSOperationStatus) {
		t.Errorf("DNS operation status kind %d not recognized as projection kind", DNSOperationStatus)
	}
}

func TestBackupKindsCompleteness(t *testing.T) {
	// Verify all backup request kinds are recognized
	for _, kind := range BackupRequestKinds() {
		if !IsRequestKind(kind) {
			t.Errorf("Backup request kind %d not recognized as request kind", kind)
		}
	}
}

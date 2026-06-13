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
		// ContextVM production request transport
		{"ContextVM message", ContextVMMessage, true},
		{"ContextVM gift wrap", ContextVMGiftWrap, true},
		{"ContextVM ephemeral gift wrap", ContextVMEphemeralGiftWrap, true},
		// Legacy request kind-number commands are migration inventory only.
		{"DNS zone create", DNSZoneCreateRequest, false},
		{"Deploy request", DeployRequest, false},
		{"Service action", ServiceAction, false},
		{"Package repository apply", PackageRepositoryApply, false},
		{"Worker cordon", WorkerCordonRequest, false},
		{"ML recipe run", MLRecipeRunRequest, false},
		{"Backup run request", BackupRunRequest, false},
		{"Assistant prompt request", AssistantPromptRequest, false},
		{"Failover request", FailoverRequest, false},
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
		// Canonical observable projections
		{"CAS control-plane state", CASControlState, true},
		{"CAS audit", CASAudit, true},
		{"NIP-38 status", NIP38Status, true},
		{"ContextVM server announcement", ContextVMServerAnnouncement, true},
		{"ContextVM tools list", ContextVMToolsList, true},
		{"ContextVM resources list", ContextVMResourcesList, true},
		{"ContextVM resource templates list", ContextVMResourceTemplatesList, true},
		{"ContextVM prompts list", ContextVMPromptsList, true},
		{"NIP-51 relay set", RelaySetDiscovery, true},
		{"NIP-65 relay list", NIP65RelayList, true},
		{"NIP-51 DM relay list", NIP51DMRelayList, true},
		{"SBOM attestation", SBOMAttestation, true},
		{"Bahia identity", BahiaIdentityDefinition, true},
		{"Bahia replay checkpoint", BahiaReplayCheckpoint, true},
		{"Bahia readiness status", BahiaReadinessStatus, true},
		// Legacy runtime projections remain migration-only and are not canonical observable projections.
		{"DNS operation status", DNSOperationStatus, false},
		{"DNS zone create result", DNSZoneCreateResult, false},
		{"Deployment status", DeploymentStatus, false},
		{"Deployment result", DeploymentResult, false},
		{"Service registry", ServiceRegistry, false},
		{"Worker state", WorkerState, false},
		{"Build registered audit", BuildRegistered, false},
		// Requests and open interop kinds are not Bahia projections.
		{"Deploy request", DeployRequest, false},
		{"DNS zone create request", DNSZoneCreateRequest, false},
		{"Backup run request", BackupRunRequest, false},
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
		{"NIP-22 comment", NIP22Comment, true},
		{"NIP-34 user grasp list", NIP34UserGraspList, true},
		{"NIP-34 patch", NIP34Patch, true},
		{"NIP-34 pull request", NIP34PullRequest, true},
		{"NIP-34 pull request update", NIP34PullRequestUpdate, true},
		{"NIP-34 issue", NIP34Issue, true},
		{"NIP-34 status open", NIP34StatusOpen, true},
		{"NIP-34 status applied", NIP34StatusAppliedOrMerged, true},
		{"NIP-34 status closed", NIP34StatusClosed, true},
		{"NIP-34 status draft", NIP34StatusDraft, true},
		{"NIP-34 repository announcement", NIP34RepositoryAnnouncement, true},
		{"NIP-34 repository state", NIP34RepositoryState, true},
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
	testCases := []struct {
		name     string
		kind     int
		expected bool
	}{
		// Canonical observable kinds
		{"CAS control-plane state", CASControlState, true},
		{"CAS audit", CASAudit, true},
		{"NIP-38 status", NIP38Status, true},
		{"ContextVM tools list", ContextVMToolsList, true},
		{"NIP-51 relay set", RelaySetDiscovery, true},
		{"NIP-51 DM relay list", NIP51DMRelayList, true},
		// Interop kinds
		{"Loom worker ad", LoomWorkerAdvertisement, true},
		{"Hive CI workflow run", HiveCIWorkflowRun, true},
		{"NIP-22 comment", NIP22Comment, true},
		{"NIP-34 repository announcement", NIP34RepositoryAnnouncement, true},
		{"NIP-34 repository state", NIP34RepositoryState, true},
		{"NIP-34 issue", NIP34Issue, true},
		// Legacy runtime kinds and requests are not readable after the migration boundary.
		{"DNS operation status", DNSOperationStatus, false},
		{"Service registry", ServiceRegistry, false},
		{"Worker state", WorkerState, false},
		{"Encrypted result", EncryptedResult, false},
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
	// Legacy DNS request helpers are inventory data only and must not imply
	// production runtime request acceptance.
	requestKinds := DNSRequestKinds()
	for _, kind := range requestKinds {
		if IsRequestKind(kind) {
			t.Errorf("legacy DNS request kind %d unexpectedly accepted as production request kind", kind)
		}
	}

	// DNS result/read-model helpers should now canonicalize to CAS observable kinds.
	for _, kind := range append(DNSResultKinds(), DNSReadModelKinds()...) {
		if !IsBahiaProjectionKind(kind) {
			t.Errorf("DNS canonical kind %d not recognized as projection kind", kind)
		}
	}

	if IsBahiaProjectionKind(DNSOperationStatus) {
		t.Errorf("legacy DNS operation status kind %d unexpectedly recognized as projection kind", DNSOperationStatus)
	}
}

func TestBackupKindsCompleteness(t *testing.T) {
	// Legacy backup request helpers are inventory data only and must not imply
	// production runtime request acceptance.
	for _, kind := range BackupRequestKinds() {
		if IsRequestKind(kind) {
			t.Errorf("legacy backup request kind %d unexpectedly accepted as production request kind", kind)
		}
	}
}

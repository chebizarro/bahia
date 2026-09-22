package dto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestVirtualizationPublicAllowlist(t *testing.T) {
	meta := domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: uuid.New(), Generation: 1, CreatedBy: "password=secret-sentinel", UpdatedAt: time.Now()}
	storage := uuid.New()
	component := domain.VMComponent{Kind: domain.VMComponentDisk, StorageRef: storage, Digest: "sha256:" + strings.Repeat("a", 64), SizeBytes: 12}
	cls := []domain.VMLifecycleClass{domain.VMLifecyclePersistent}
	v := domain.PersistentVMDeployment{VirtualizationResourceMeta: meta, LifecycleClass: domain.VMLifecyclePersistent, Provider: domain.VMProviderLibvirt, Purpose: domain.VMPurposeDesktop, DesiredPower: domain.VMDesiredRunning, DisplayName: "secret-sentinel", Labels: map[string]string{"token": "secret-sentinel"}, Bootstrap: []domain.VMBootstrapBinding{{TargetKey: "secret-sentinel"}}, Connections: []domain.VMPublicConnection{{Protocol: domain.VMConnectionSSH, Address: "guest.example", Port: 22, ResourceID: meta.ID}, {Protocol: domain.VMConnectionHTTPS, Address: "https://password:secret-sentinel@host/", Port: 443, ResourceID: meta.ID}, {Protocol: domain.VMConnectionSSH, Address: "other.example", Port: 22, ResourceID: uuid.New()}}}
	v.Observation = &domain.VMObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, Sequence: 2, ObservedAt: time.Now()}, Availability: domain.VMObservationUnavailable, Diagnostic: domain.VMDiagnostic{Code: "secret-sentinel"}}
	fixtures := map[domain.VirtualizationResourceKind]any{
		domain.VirtualizationHostResource: domain.VirtualizationHost{VirtualizationResourceMeta: meta, LifecycleClasses: cls, Provider: domain.VMProviderLibvirt, ManagementEndpointRef: storage, Observation: &domain.VirtualizationHostObservation{Availability: domain.VMObservationAvailable, Networks: []string{"secret-sentinel"}, StoragePools: []string{"/private/secret-sentinel"}}},
		domain.VMImageResource:            domain.VMImage{VirtualizationResourceMeta: meta, LifecycleClasses: cls, Format: domain.VMImageQCOW2, OS: domain.VMOSLinux, Components: []domain.VMComponent{component}, DriverContract: "secret-sentinel"},
		domain.PersistentVMResource:       v,
		domain.ExecutionPlaneResource:     domain.ExecutionPlaneDeployment{VirtualizationResourceMeta: meta, ManagementEndpointRef: storage, ManagementAuthor: "secret-sentinel", Desired: domain.ExecutionPlaneDesired{LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}, State: domain.ExecutionPlaneEnabled, Configuration: domain.ExecutionPlaneConfiguration{SecretBindings: []domain.VMBootstrapBinding{{TargetKey: "secret-sentinel"}}}}},
		domain.VMCheckpointResource:       domain.VMCheckpoint{VirtualizationResourceMeta: meta, LifecycleClass: domain.VMLifecyclePersistent, State: domain.VMArtifactDeleted, Components: []domain.VMComponent{component}},
		domain.VMExportResource:           domain.VMExport{VirtualizationResourceMeta: meta, LifecycleClass: domain.VMLifecyclePersistent, State: domain.VMArtifactReady, StorageRef: storage, Components: []domain.VMComponent{component}},
		domain.VMOperationResource:        domain.VMOperation{VirtualizationResourceMeta: meta, LifecycleClass: domain.VMLifecyclePersistent, Kind: domain.VMOperationDelete, Phase: domain.VMOperationUnconfirmed, Reason: "secret-sentinel", Actor: "secret-sentinel", IdempotencyKey: "secret-sentinel", PreparedStorageRefs: []uuid.UUID{storage}},
	}
	for kind, input := range fixtures {
		t.Run(string(kind), func(t *testing.T) {
			view, err := PublicVirtualizationResource(kind, input)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"secret-sentinel", storage.String(), "bootstrap", "storage_ref", "management_endpoint", "idempotency_key"} {
				if strings.Contains(string(data), forbidden) {
					t.Fatalf("leaked %s: %s", forbidden, data)
				}
			}
			if view.Kind != kind || view.ID != meta.ID {
				t.Fatal("lost identity")
			}
			if kind == domain.PersistentVMResource {
				if len(view.VM.Connections) != 1 || view.VM.Observation.RuntimeState != nil || view.VM.Observation.Diagnostic != domain.VMErrorUnavailable {
					t.Fatalf("unsafe or fabricated observation: %+v", view)
				}
			}
			if kind == domain.VMCheckpointResource && !view.Deleted {
				t.Fatal("missing tombstone")
			}
		})
	}
}
func TestVirtualizationRejectsUnknownPublicEnums(t *testing.T) {
	v := domain.VirtualizationHost{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: uuid.New(), Generation: 1}, Provider: "password=secret", LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}}
	if _, err := PublicVirtualizationResource(domain.VirtualizationHostResource, v); err == nil {
		t.Fatal("unknown provider must not enter public content")
	}
	for _, kind := range []domain.VirtualizationResourceKind{domain.VirtualizationHostResource, domain.VMImageResource, domain.PersistentVMResource, domain.ExecutionPlaneResource, domain.VMCheckpointResource, domain.VMExportResource, domain.VMOperationResource} {
		if c, err := VirtualizationCoordinate(kind, v.ID); err != nil || !strings.HasSuffix(c, v.ID.String()) {
			t.Fatalf("bad coordinate %q %v", c, err)
		}
	}
}

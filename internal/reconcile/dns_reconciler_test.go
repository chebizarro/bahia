package reconcile

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestDNSReconcilerNoOpWhenSnapshotsMatch(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	backend := &fakeDNSBackend{records: expected}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCalls != 0 {
		t.Fatalf("sync calls = %d, want 0", backend.syncCalls)
	}
}

func TestDNSReconcilerOverwritesWhenSnapshotsDiffer(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	backend := &fakeDNSBackend{records: []domain.DNSRecord{{Zone: zone.Name, Name: "api", FQDN: "api.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.99", TTL: 120, SourceCoordinate: "endpoint:service:api:prod"}}}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCalls != 1 {
		t.Fatalf("sync calls = %d, want 1", backend.syncCalls)
	}
	if len(backend.synced) != len(expected) || backend.synced[0] != expected[0] {
		t.Fatalf("synced records = %#v, want %#v", backend.synced, expected)
	}
}

func testReconcilerProjector() (*DNSProjector, domain.DNSZone, []domain.DNSRecord) {
	envID := uuid.New()
	serviceID := uuid.New()
	cfg := testDNSConfig()
	projector := NewDNSProjector(
		&fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
		&fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}},
		&fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{dnsTestStateKey(serviceID, envID): {ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "10.0.0.10", HealthStatus: domain.HealthStatusHealthy}}},
		nil,
		nil,
		nil,
		cfg,
		nil,
	)
	zone := domain.DNSZone{Name: "prod.example", Visibility: domain.ZoneVisibilityInternal, BackendRef: "test", TTL: 120}
	expected := []domain.DNSRecord{{Zone: zone.Name, Name: "api", FQDN: "api.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.10", TTL: 120, SourceCoordinate: "endpoint:service:api:prod"}}
	return projector, zone, expected
}

type fakeDNSBackend struct {
	records   []domain.DNSRecord
	synced    []domain.DNSRecord
	syncCalls int
}

func (b *fakeDNSBackend) ListRecords(context.Context, domain.DNSZone) ([]domain.DNSRecord, error) {
	return append([]domain.DNSRecord(nil), b.records...), nil
}

func (b *fakeDNSBackend) SyncZone(_ context.Context, _ domain.DNSZone, records []domain.DNSRecord) error {
	b.syncCalls++
	b.synced = append([]domain.DNSRecord(nil), records...)
	b.records = append([]domain.DNSRecord(nil), records...)
	return nil
}

type fakeDNSResolver struct{ backends map[string]DNSBackend }

func (r *fakeDNSResolver) Resolve(ref string) (DNSBackend, bool) {
	backend, ok := r.backends[ref]
	return backend, ok
}

package reconcile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestDNSReconcilerAddOnlyDiffSyncsZone(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	backend := &fakeDNSBackend{}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCallCount() != 1 {
		t.Fatalf("sync calls = %d, want 1", backend.syncCallCount())
	}
	assertRecordsEqual(t, backend.syncedRecords(), expected)
}

func TestDNSReconcilerDeleteOnlyDiffSyncsZone(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	stale := domain.DNSRecord{Zone: zone.Name, Name: "old", FQDN: "old.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.99", TTL: 120, SourceCoordinate: "endpoint:service:old:prod"}
	backend := &fakeDNSBackend{records: append(append([]domain.DNSRecord(nil), expected...), stale)}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCallCount() != 1 {
		t.Fatalf("sync calls = %d, want 1", backend.syncCallCount())
	}
	assertRecordsEqual(t, backend.syncedRecords(), expected)
}

func TestDNSReconcilerUpdateDiffValueChangedSyncsZone(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	actual := cloneDNSRecords(expected)
	actual[0].Value = "10.0.0.99"
	backend := &fakeDNSBackend{records: actual}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCallCount() != 1 {
		t.Fatalf("sync calls = %d, want 1", backend.syncCallCount())
	}
	assertRecordsEqual(t, backend.syncedRecords(), expected)
}

func TestDNSReconcilerUpdateDiffTTLChangedSyncsZone(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	actual := cloneDNSRecords(expected)
	actual[0].TTL = 60
	backend := &fakeDNSBackend{records: actual}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCallCount() != 1 {
		t.Fatalf("sync calls = %d, want 1", backend.syncCallCount())
	}
	assertRecordsEqual(t, backend.syncedRecords(), expected)
}

func TestDNSReconcilerNoOpWhenSnapshotsMatch(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	backend := &fakeDNSBackend{records: expected}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCallCount() != 0 {
		t.Fatalf("sync calls = %d, want 0", backend.syncCallCount())
	}
}

func TestDNSReconcilerSuppressesNonConvergingAuthoritySyncs(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	zone.Authoritative = true
	backend := &fakeAuthorityBackend{fakeDNSBackend: &fakeDNSBackend{records: expected}}
	core, logs := observer.New(zap.WarnLevel)
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, zap.New(core))

	for i := 0; i < 4; i++ {
		if err := reconciler.ReconcileOnce(ctx); err != nil {
			t.Fatalf("ReconcileOnce %d returned error: %v", i+1, err)
		}
	}
	if got := backend.syncCallCount(); got != 1 {
		t.Fatalf("authority-only sync calls = %d, want exactly 1", got)
	}
	const warning = "zone \"prod.example\" authority not converging — dns agent may predate authoritative-zone support; upgrade the agent"
	if entries := logs.FilterMessage(warning).All(); len(entries) != 1 {
		t.Fatalf("authority convergence warnings = %#v, want exactly one %q", logs.All(), warning)
	}

	backend.mu.Lock()
	backend.records = nil
	backend.mu.Unlock()
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce with record drift returned error: %v", err)
	}
	if got := backend.syncCallCount(); got != 2 {
		t.Fatalf("sync calls after record drift = %d, want 2", got)
	}
}

func TestDNSReconcilerDiffPreservesMultiValueRRsets(t *testing.T) {
	actual := []domain.DNSRecord{
		{Zone: "prod.example", Name: "api", FQDN: "api.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.10", TTL: 120},
	}
	desired := []domain.DNSRecord{
		{Zone: "prod.example", Name: "api", FQDN: "api.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.10", TTL: 120},
		{Zone: "prod.example", Name: "api", FQDN: "api.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.11", TTL: 120},
	}

	diff := diffDNSRecords(actual, desired)

	if len(diff.added) != 1 || diff.added[0].Value != "10.0.0.11" {
		t.Fatalf("added records = %#v, want only 10.0.0.11", diff.added)
	}
	if len(diff.updated) != 0 {
		t.Fatalf("updated records = %#v, want none", diff.updated)
	}
	if len(diff.deleted) != 0 {
		t.Fatalf("deleted records = %#v, want none", diff.deleted)
	}
}

func TestDNSReconcilerDoesNotEmitMutationEventsWhenSyncFails(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	actual := cloneDNSRecords(expected)
	actual[0].Value = "10.0.0.99"
	publisher := &captureDNSPublisher{}
	backend := &fakeDNSBackend{records: actual, syncErr: errors.New("sync failed")}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil, publisher)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}

	assertEventCount(t, publisher.eventsOfType(dnsEventDriftDetected), 1)
	assertEventCount(t, publisher.eventsOfType(dnsEventRecordChanged), 0)
	assertEventCount(t, publisher.eventsOfType(dnsEventEndpointRegistered), 0)
	assertEventCount(t, publisher.eventsOfType(dnsEventEndpointDeregistered), 0)
	assertEventCount(t, publisher.eventsOfType(dnsEventZoneSynced), 0)
}

func TestDNSReconcilerEmitsDNSAuditEvents(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	actual := []domain.DNSRecord{
		{Zone: zone.Name, Name: "api", FQDN: "api.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.99", TTL: 60, SourceCoordinate: "endpoint:service:api:prod"},
		{Zone: zone.Name, Name: "old", FQDN: "old.prod.example", Type: domain.DNSRecordTypeA, Value: "10.0.0.20", TTL: 120, SourceCoordinate: "endpoint:service:old:prod"},
	}
	publisher := &captureDNSPublisher{}
	backend := &fakeDNSBackend{records: actual}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil, publisher)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}

	assertEventCount(t, publisher.eventsOfType(dnsEventDriftDetected), 1)
	assertEventCount(t, publisher.eventsOfType(dnsEventZoneSynced), 1)
	assertEventCount(t, publisher.eventsOfType(dnsEventRecordChanged), 2)
	assertEventCount(t, publisher.eventsOfType(dnsEventEndpointDeregistered), 1)

	drift := eventData(t, publisher.eventsOfType(dnsEventDriftDetected)[0])
	if drift["zone"] != zone.Name || drift["backend_ref"] != zone.BackendRef || drift["deleted_count"] != 1 || drift["updated_count"] != 1 || drift["added_count"] != 0 {
		t.Fatalf("unexpected drift payload: %#v", drift)
	}

	changes := publisher.eventsOfType(dnsEventRecordChanged)
	assertRecordChange(t, changes, "update", expected[0].FQDN, "10.0.0.99", "10.0.0.10", 120)
	assertRecordChange(t, changes, "delete", "old.prod.example", "10.0.0.20", "", 120)

	deregistered := eventData(t, publisher.eventsOfType(dnsEventEndpointDeregistered)[0])
	if deregistered["source_coordinate"] != "endpoint:service:old:prod" {
		t.Fatalf("unexpected deregistered payload: %#v", deregistered)
	}
}

func TestDNSReconcilerDebouncesRapidTriggers(t *testing.T) {
	projector, zone, expected := testReconcilerProjector()
	backend := &fakeDNSBackend{records: expected}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, time.Hour, nil)
	reconciler.debounce = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- reconciler.Run(ctx) }()

	waitForListCalls(t, backend, 1)
	for i := 0; i < 5; i++ {
		reconciler.TriggerReconcile()
	}
	waitForListCalls(t, backend, 2)
	time.Sleep(30 * time.Millisecond)
	if got := backend.listCallCount(); got != 2 {
		t.Fatalf("list calls after rapid triggers = %d, want 2", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func TestDNSReconcilerSyncZoneNotCalledWhenDiffEmpty(t *testing.T) {
	ctx := context.Background()
	projector, zone, expected := testReconcilerProjector()
	backend := &fakeDNSBackend{records: expected}
	publisher := &captureDNSPublisher{}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)
	reconciler.SetPublisher(publisher)

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if backend.syncCallCount() != 0 {
		t.Fatalf("sync calls = %d, want 0", backend.syncCallCount())
	}
	if len(publisher.eventsSnapshot()) != 0 {
		t.Fatalf("events emitted for empty diff: %#v", publisher.eventsSnapshot())
	}
}

func TestDNSOverrideNames(t *testing.T) {
	tests := []struct {
		name       string
		recordName string
		wantName   string
		wantFQDN   string
	}{
		{name: "subdomain", recordName: "api", wantName: "api", wantFQDN: "api.sharegap.net"},
		{name: "fqdn form", recordName: "api.sharegap.net", wantName: "api", wantFQDN: "api.sharegap.net"},
		{name: "apex zone name", recordName: "sharegap.net", wantName: "@", wantFQDN: "sharegap.net"},
		{name: "apex marker", recordName: "@", wantName: "@", wantFQDN: "sharegap.net"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotFQDN := dnsOverrideNames("sharegap.net", tt.recordName)
			if gotName != tt.wantName || gotFQDN != tt.wantFQDN {
				t.Fatalf("dnsOverrideNames(%q) = (%q, %q), want (%q, %q)", tt.recordName, gotName, gotFQDN, tt.wantName, tt.wantFQDN)
			}
		})
	}
}

func TestDNSReconcilerAppliesPersistedRecordOverride(t *testing.T) {
	ctx := context.Background()
	projector, zone, _ := testReconcilerProjector()
	backend := &fakeDNSBackend{}
	reconciler := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, 0, nil)
	override := domain.DNSRecordOverride{ID: uuid.New(), ZoneName: zone.Name, RecordName: "api", RecordType: domain.DNSRecordTypeA, Value: "10.0.0.99", TTL: 30}
	reconciler.SetPersistenceSources(nil, staticDNSOverrideSource{overrides: []domain.DNSRecordOverride{override}})

	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	want := []domain.DNSRecord{{Zone: zone.Name, Name: "api", FQDN: "api.prod.example", Type: domain.DNSRecordTypeA, Value: override.Value, TTL: override.TTL, SourceCoordinate: "manual_override:" + override.ID.String()}}
	assertRecordsEqual(t, backend.syncedRecords(), want)
}

func TestDNSReconcilerSubscriptionsTriggerOnServiceConvergenceEvents(t *testing.T) {
	reconciler := NewDNSReconciler(nil, nil, nil, time.Hour, nil)
	publisher := newSubscriptionDNSPublisher()
	reconciler.SetupSubscriptions(publisher)

	for _, eventType := range []events.EventType{events.EventDeploymentRunCompleted, events.EventEnvironmentServiceStateChanged, events.EventRuntimeObservation} {
		handler := publisher.handlers[eventType]
		if handler == nil {
			t.Fatalf("no DNS reconcile subscription for %s", eventType)
		}
		handler(context.Background(), events.Event{Type: eventType})
		select {
		case <-reconciler.triggerCh:
		default:
			t.Fatalf("event %s did not trigger DNS reconcile", eventType)
		}
	}
}

type staticDNSOverrideSource struct {
	overrides []domain.DNSRecordOverride
}

func (s staticDNSOverrideSource) ListByZone(context.Context, string) ([]domain.DNSRecordOverride, error) {
	return append([]domain.DNSRecordOverride(nil), s.overrides...), nil
}

type subscriptionDNSPublisher struct {
	handlers map[events.EventType]events.Handler
}

func newSubscriptionDNSPublisher() *subscriptionDNSPublisher {
	return &subscriptionDNSPublisher{handlers: map[events.EventType]events.Handler{}}
}

func (p *subscriptionDNSPublisher) Publish(context.Context, events.Event) {}
func (p *subscriptionDNSPublisher) Subscribe(eventType events.EventType, handler events.Handler) {
	p.handlers[eventType] = handler
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

type fakeAuthorityBackend struct {
	*fakeDNSBackend
	authoritative bool
}

func (b *fakeAuthorityBackend) ListZoneState(context.Context, domain.DNSZone) ([]domain.DNSRecord, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listCalls++
	return append([]domain.DNSRecord(nil), b.records...), b.authoritative, nil
}

type fakeDNSBackend struct {
	mu        sync.Mutex
	records   []domain.DNSRecord
	synced    []domain.DNSRecord
	syncErr   error
	syncCalls int
	listCalls int
}

func (b *fakeDNSBackend) ListRecords(context.Context, domain.DNSZone) ([]domain.DNSRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listCalls++
	return append([]domain.DNSRecord(nil), b.records...), nil
}

func (b *fakeDNSBackend) SyncZone(_ context.Context, _ domain.DNSZone, records []domain.DNSRecord) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncCalls++
	if b.syncErr != nil {
		return b.syncErr
	}
	b.synced = append([]domain.DNSRecord(nil), records...)
	b.records = append([]domain.DNSRecord(nil), records...)
	return nil
}

func (b *fakeDNSBackend) syncCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.syncCalls
}

func (b *fakeDNSBackend) listCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.listCalls
}

func (b *fakeDNSBackend) syncedRecords() []domain.DNSRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.DNSRecord(nil), b.synced...)
}

type fakeDNSResolver struct{ backends map[string]DNSBackend }

func (r *fakeDNSResolver) Resolve(ref string) (DNSBackend, bool) {
	backend, ok := r.backends[ref]
	return backend, ok
}

type captureDNSPublisher struct {
	mu     sync.Mutex
	events []events.Event
}

func (p *captureDNSPublisher) Publish(_ context.Context, e events.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *captureDNSPublisher) Subscribe(events.EventType, events.Handler) {}

func (p *captureDNSPublisher) eventsSnapshot() []events.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]events.Event(nil), p.events...)
}

func (p *captureDNSPublisher) eventsOfType(eventType events.EventType) []events.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []events.Event
	for _, event := range p.events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

func assertRecordsEqual(t *testing.T, got, want []domain.DNSRecord) {
	t.Helper()
	sortDNSRecords(got)
	sortDNSRecords(want)
	if len(got) != len(want) {
		t.Fatalf("records length = %d, want %d; got=%#v want=%#v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("record[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func assertEventCount(t *testing.T, got []events.Event, want int) {
	t.Helper()
	if len(got) != want {
		t.Fatalf("event count = %d, want %d; events=%#v", len(got), want, got)
	}
}

func eventData(t *testing.T, event events.Event) map[string]any {
	t.Helper()
	data, ok := event.Data.(map[string]any)
	if !ok {
		t.Fatalf("event data type = %T, want map[string]any", event.Data)
	}
	return data
}

func assertRecordChange(t *testing.T, events []events.Event, operation, fqdn, oldValue, newValue string, ttl int) {
	t.Helper()
	for _, event := range events {
		data := eventData(t, event)
		if data["operation"] == operation && data["fqdn"] == fqdn {
			if data["old_value"] != oldValue || data["new_value"] != newValue || data["ttl"] != ttl {
				t.Fatalf("unexpected record change payload for %s %s: %#v", operation, fqdn, data)
			}
			return
		}
	}
	t.Fatalf("missing record change operation=%s fqdn=%s in %#v", operation, fqdn, events)
}

func waitForListCalls(t *testing.T, backend *fakeDNSBackend, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if backend.listCallCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("list calls = %d, want at least %d", backend.listCallCount(), want)
}

package dns

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestStaticResolverRejectsDuplicateRefs(t *testing.T) {
	backend := NewFilesystemBackend(t.TempDir())
	_, err := NewStaticResolver(
		BackendRegistration{Ref: "primary", Backend: backend},
		BackendRegistration{Ref: "primary", Backend: backend},
	)
	if err == nil {
		t.Fatal("expected duplicate backend ref error")
	}
	if !strings.Contains(err.Error(), "duplicate DNS backend registration") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestStaticResolverResolveAndRefs(t *testing.T) {
	first := NewFilesystemBackend(t.TempDir())
	second := NewFilesystemBackend(t.TempDir())
	resolver, err := NewStaticResolver(
		BackendRegistration{Ref: "z-secondary", Backend: second},
		BackendRegistration{Ref: "a-primary", Backend: first},
	)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	resolved, ok := resolver.Resolve("a-primary")
	if !ok || resolved != first {
		t.Fatalf("expected a-primary to resolve to first backend")
	}
	if _, ok := resolver.Resolve("missing"); ok {
		t.Fatal("expected missing backend ref to fail resolution")
	}
	wantRefs := []string{"a-primary", "z-secondary"}
	if got := resolver.Refs(); !reflect.DeepEqual(got, wantRefs) {
		t.Fatalf("refs mismatch: got %#v want %#v", got, wantRefs)
	}
}

func TestFilesystemBackendHealth(t *testing.T) {
	backend := NewFilesystemBackend(t.TempDir())
	if err := backend.Health(context.Background()); err != nil {
		t.Fatalf("health failed: %v", err)
	}
}

func TestFilesystemBackendHealthRejectsInvalidRoot(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	backend := NewFilesystemBackend(rootFile)
	if err := backend.Health(context.Background()); err == nil {
		t.Fatal("expected health error for file root")
	}
}

func TestFilesystemBackendListRecordsMissingFileReturnsEmptySet(t *testing.T) {
	backend := NewFilesystemBackend(t.TempDir())
	records, err := backend.ListRecords(context.Background(), testZone())
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected empty records for missing zone file, got %#v", records)
	}
}

func TestFilesystemBackendRoundTripsZoneSnapshot(t *testing.T) {
	backend := NewFilesystemBackend(t.TempDir())
	zone := testZone()
	want := []domain.DNSRecord{
		{Zone: zone.Name, Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.8", TTL: 300, SourceCoordinate: "endpoint:service:api:prod"},
		{Zone: zone.Name, Name: "worker", FQDN: "worker.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: "worker.internal", TTL: 300, SourceCoordinate: "endpoint:worker:worker"},
	}
	if err := backend.SyncZone(context.Background(), zone, want); err != nil {
		t.Fatalf("sync zone: %v", err)
	}
	got, err := backend.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records mismatch:\ngot  %#v\nwant %#v", got, want)
	}
}

func TestFilesystemBackendSerializesSRVRecordFields(t *testing.T) {
	rootDir := t.TempDir()
	backend := NewFilesystemBackend(rootDir)
	zone := testZone()
	port := 8080
	priority := 10
	weight := 100
	want := []domain.DNSRecord{{Zone: zone.Name, Name: "_http._tcp.embeddings", FQDN: "_http._tcp.embeddings.prod.cascadia", Type: domain.DNSRecordTypeSRV, Value: "10.0.0.30", TTL: 300, Port: &port, Priority: &priority, Weight: &weight, SourceCoordinate: "endpoint:ml:embeddings:prod"}}
	if err := backend.SyncZone(context.Background(), zone, want); err != nil {
		t.Fatalf("sync SRV zone: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(rootDir, sanitizeZoneFilename(zone.Name)+".json"))
	if err != nil {
		t.Fatalf("read zone snapshot: %v", err)
	}
	content := string(data)
	for _, field := range []string{`"type": "SRV"`, `"priority": 10`, `"weight": 100`, `"port": 8080`} {
		if !strings.Contains(content, field) {
			t.Fatalf("expected SRV field %s in snapshot:\n%s", field, content)
		}
	}
	got, err := backend.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("list SRV zone: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SRV records mismatch:\ngot  %#v\nwant %#v", got, want)
	}
}

func TestFilesystemBackendSyncIsDeterministicAndIdempotent(t *testing.T) {
	rootDir := t.TempDir()
	backend := NewFilesystemBackend(rootDir)
	zone := testZone()
	records := []domain.DNSRecord{
		{Zone: zone.Name, Name: "z", FQDN: "z.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: "z.internal", TTL: 300, SourceCoordinate: "endpoint:service:z:prod"},
		{Zone: zone.Name, Name: "a", FQDN: "a.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "2001:db8::1", TTL: 300, SourceCoordinate: "endpoint:service:a:prod"},
		{Zone: zone.Name, Name: "a", FQDN: "a.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.1", TTL: 300, SourceCoordinate: "endpoint:service:a:prod"},
	}
	if err := backend.SyncZone(context.Background(), zone, records); err != nil {
		t.Fatalf("sync zone: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(rootDir, sanitizeZoneFilename(zone.Name)+".json"))
	if err != nil {
		t.Fatalf("read zone snapshot: %v", err)
	}

	reversed := []domain.DNSRecord{records[2], records[1], records[0]}
	if err := backend.SyncZone(context.Background(), zone, reversed); err != nil {
		t.Fatalf("sync zone again: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(rootDir, sanitizeZoneFilename(zone.Name)+".json"))
	if err != nil {
		t.Fatalf("read zone snapshot again: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("expected idempotent snapshot bytes\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	content := string(second)
	idxAAAA := strings.Index(content, `"type": "AAAA"`)
	idxA := strings.Index(content, `"type": "A"`)
	idxZ := strings.Index(content, `"fqdn": "z.prod.cascadia"`)
	if idxAAAA == -1 || idxA == -1 || idxZ == -1 {
		t.Fatalf("expected sorted records in content:\n%s", content)
	}
	if !(idxA < idxAAAA && idxAAAA < idxZ) {
		t.Fatalf("records not sorted by FQDN, Type, Value:\n%s", content)
	}
}

func TestFilesystemBackendAtomicOverwriteReplacesPriorSnapshot(t *testing.T) {
	backend := NewFilesystemBackend(t.TempDir())
	zone := testZone()
	oldRecords := []domain.DNSRecord{{Zone: zone.Name, Name: "old", FQDN: "old.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.1", TTL: 300, SourceCoordinate: "endpoint:service:old:prod"}}
	newRecords := []domain.DNSRecord{{Zone: zone.Name, Name: "new", FQDN: "new.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.2", TTL: 300, SourceCoordinate: "endpoint:service:new:prod"}}

	if err := backend.SyncZone(context.Background(), zone, oldRecords); err != nil {
		t.Fatalf("sync old records: %v", err)
	}
	if err := backend.SyncZone(context.Background(), zone, newRecords); err != nil {
		t.Fatalf("sync new records: %v", err)
	}
	got, err := backend.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if !reflect.DeepEqual(got, newRecords) {
		t.Fatalf("expected overwrite with new records, got %#v", got)
	}
}

func testZone() domain.DNSZone {
	return domain.DNSZone{
		Name:       "prod.cascadia",
		Visibility: domain.ZoneVisibilityInternal,
		BackendRef: "filesystem-test",
		TTL:        300,
	}
}

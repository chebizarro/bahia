package dns

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

const testNPub = "npub1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"

func TestFIPSBackendSyncZoneWritesHostsFile(t *testing.T) {
	hostsPath := writeTestFIPSHosts(t, "# manual entry\nmanual.fips  npub1manual\n")
	backend := NewFIPSBackend(hostsPath, zap.NewNop())
	zone := testFIPSZone()
	records := []domain.DNSRecord{
		{Zone: zone.Name, Name: "drydock", FQDN: "drydock.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: testNPub, TTL: zone.TTL},
		{Zone: zone.Name, Name: "mesh", FQDN: "mesh.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "fd00::1234", TTL: zone.TTL},
	}

	if err := backend.SyncZone(context.Background(), zone, records); err != nil {
		t.Fatalf("sync zone: %v", err)
	}
	content := readTestFIPSHosts(t, hostsPath)
	for _, want := range []string{
		"# manual entry",
		"manual.fips  npub1manual",
		fipsManagedBegin,
		"# Zone: prod.cascadia",
		"drydock.fips  " + testNPub,
		"mesh.fips  fd00::1234",
		fipsManagedEnd,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in hosts file:\n%s", want, content)
		}
	}
}

func TestFIPSBackendListRecordsParsesHostsFile(t *testing.T) {
	hostsPath := writeTestFIPSHosts(t, strings.Join([]string{
		"# manual comment",
		"drydock.fips  " + testNPub,
		"mesh.fips fd00::abcd # inline comment",
		"not-fips.example npub1ignored",
		"invalid.fips  10.0.0.5",
		"",
	}, "\n"))
	backend := NewFIPSBackend(hostsPath, zap.NewNop())
	zone := testFIPSZone()

	got, err := backend.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	want := []domain.DNSRecord{
		{Zone: zone.Name, Name: "drydock", FQDN: "drydock.fips", Type: domain.DNSRecordTypeCNAME, Value: testNPub, TTL: zone.TTL},
		{Zone: zone.Name, Name: "mesh", FQDN: "mesh.fips", Type: domain.DNSRecordTypeCNAME, Value: "fd00::abcd", TTL: zone.TTL},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records mismatch:\ngot  %#v\nwant %#v", got, want)
	}
}

func TestFIPSBackendManagedSectionPreservedOnRewrite(t *testing.T) {
	hostsPath := writeTestFIPSHosts(t, strings.Join([]string{
		"before.fips  npub1before",
		fipsManagedBegin,
		"old.fips  npub1old",
		fipsManagedEnd,
		"after.fips  npub1after",
		"",
	}, "\n"))
	backend := NewFIPSBackend(hostsPath, zap.NewNop())
	zone := testFIPSZone()

	if err := backend.SyncZone(context.Background(), zone, []domain.DNSRecord{{Zone: zone.Name, Name: "new", FQDN: "new.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: testNPub, TTL: zone.TTL}}); err != nil {
		t.Fatalf("sync zone: %v", err)
	}
	content := readTestFIPSHosts(t, hostsPath)
	if !strings.Contains(content, "before.fips  npub1before") || !strings.Contains(content, "after.fips  npub1after") {
		t.Fatalf("manual entries were not preserved:\n%s", content)
	}
	if strings.Contains(content, "old.fips") {
		t.Fatalf("old managed entry was not replaced:\n%s", content)
	}
	if !strings.Contains(content, "new.fips  "+testNPub) {
		t.Fatalf("new managed entry missing:\n%s", content)
	}
}

func TestFIPSBackendHealth(t *testing.T) {
	hostsPath := writeTestFIPSHosts(t, "")
	backend := NewFIPSBackend(hostsPath, zap.NewNop())
	if err := backend.Health(context.Background()); err != nil {
		t.Fatalf("health failed: %v", err)
	}
}

func TestFIPSBackendHealthFailsForMissingPath(t *testing.T) {
	backend := NewFIPSBackend(filepath.Join(t.TempDir(), "missing-hosts"), zap.NewNop())
	if err := backend.Health(context.Background()); err == nil {
		t.Fatal("expected health to fail for missing hosts path")
	}
}

func TestFIPSBackendSyncZoneRejectsUnsupportedRecordsWithoutWriting(t *testing.T) {
	const original = "# existing configuration\n"
	hostsPath := writeTestFIPSHosts(t, original)
	backend := NewFIPSBackend(hostsPath, zap.NewNop())
	zone := testFIPSZone()
	records := []domain.DNSRecord{
		{Zone: zone.Name, Name: "mesh", FQDN: "mesh.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "fd00::1", TTL: zone.TTL},
		{Zone: zone.Name, Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.5", TTL: zone.TTL},
	}

	err := backend.SyncZone(context.Background(), zone, records)
	if err == nil || !strings.Contains(err.Error(), "unsupported value") {
		t.Fatalf("SyncZone error = %v, want unsupported value error", err)
	}
	if content := readTestFIPSHosts(t, hostsPath); content != original {
		t.Fatalf("hosts file changed after rejected desired records:\n%s", content)
	}
}

func testFIPSZone() domain.DNSZone {
	return domain.DNSZone{Name: "prod.cascadia", Visibility: domain.ZoneVisibilityMesh, BackendRef: "fips-test", TTL: 300}
}

func writeTestFIPSHosts(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write hosts file: %v", err)
	}
	return path
}

func readTestFIPSHosts(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hosts file: %v", err)
	}
	return string(data)
}

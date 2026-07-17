package dns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type mockDnsmasqCommandExecutor struct {
	commands []string
	err      error
	errs     []error
}

func (m *mockDnsmasqCommandExecutor) Run(ctx context.Context, command string) error {
	m.commands = append(m.commands, command)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		return err
	}
	return m.err
}

func TestDnsmasqDirectiveGeneration(t *testing.T) {
	zone := dnsmasqTestZone()
	priority := 10
	weight := 100
	port := 8080
	records := []domain.DNSRecord{
		{Zone: zone.Name, Name: "drydock-review", FQDN: "drydock-review.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.1.44", TTL: 300},
		{Zone: zone.Name, Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "::1", TTL: 300},
		{Zone: zone.Name, Name: "llm", FQDN: "llm.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: "drydock-review.prod.cascadia", TTL: 300},
		{Zone: zone.Name, Name: "_http._tcp.embeddings", FQDN: "_http._tcp.embeddings.prod.cascadia", Type: domain.DNSRecordTypeSRV, Value: "10.0.1.50", TTL: 300, Priority: &priority, Weight: &weight, Port: &port},
	}
	want := []string{
		"address=/drydock-review.prod.cascadia/10.0.1.44",
		"address=/api.prod.cascadia/::1",
		"cname=llm.prod.cascadia,drydock-review.prod.cascadia",
		"srv-host=_http._tcp.embeddings.prod.cascadia,10.0.1.50,8080,10,100",
	}
	for i, record := range records {
		got, err := dnsmasqDirective(zone, record)
		if err != nil {
			t.Fatalf("dnsmasqDirective(%d) returned error: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("directive %d mismatch: got %q want %q", i, got, want[i])
		}
	}
}

func TestDnsmasqBackendSyncZoneWritesConfAndReloads(t *testing.T) {
	rootDir := t.TempDir()
	executor := &mockDnsmasqCommandExecutor{}
	backend := NewDnsmasqBackend(DnsmasqConfig{ConfigDir: rootDir, ReloadCommand: "systemctl reload dnsmasq"})
	backend.commandExecutor = executor
	zone := dnsmasqTestZone()
	priority := 10
	weight := 100
	port := 8080
	records := []domain.DNSRecord{
		{Zone: zone.Name, Name: "drydock-review", FQDN: "drydock-review.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.1.44", TTL: 300},
		{Zone: zone.Name, Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "::1", TTL: 300},
		{Zone: zone.Name, Name: "llm", FQDN: "llm.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: "drydock-review.prod.cascadia", TTL: 300},
		{Zone: zone.Name, Name: "_http._tcp.embeddings", FQDN: "_http._tcp.embeddings.prod.cascadia", Type: domain.DNSRecordTypeSRV, Value: "10.0.1.50", TTL: 300, Priority: &priority, Weight: &weight, Port: &port},
	}
	if err := backend.SyncZone(context.Background(), zone, records); err != nil {
		t.Fatalf("SyncZone returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(rootDir, "bahia-prod-cascadia.conf"))
	if err != nil {
		t.Fatalf("read dnsmasq conf: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"# Managed by Bahia. Manual changes may be replaced.",
		"# Zone: prod.cascadia",
		"address=/api.prod.cascadia/::1",
		"address=/drydock-review.prod.cascadia/10.0.1.44",
		"cname=llm.prod.cascadia,drydock-review.prod.cascadia",
		"srv-host=_http._tcp.embeddings.prod.cascadia,10.0.1.50,8080,10,100",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected conf to contain %q, got:\n%s", want, content)
		}
	}
	if !reflect.DeepEqual(executor.commands, []string{"systemctl reload dnsmasq"}) {
		t.Fatalf("reload commands mismatch: got %#v", executor.commands)
	}
}

func TestDnsmasqBackendListRecordsParsesDirectives(t *testing.T) {
	rootDir := t.TempDir()
	backend := NewDnsmasqBackend(DnsmasqConfig{ConfigDir: rootDir, ReloadCommand: "true"})
	path, err := backend.zonePath("prod.cascadia")
	if err != nil {
		t.Fatalf("zonePath: %v", err)
	}
	content := strings.Join([]string{
		"# Managed by Bahia",
		"address=/drydock-review.prod.cascadia/10.0.1.44",
		"address=/api.prod.cascadia/::1",
		"cname=llm.prod.cascadia,drydock-review.prod.cascadia",
		"srv-host=_http._tcp.embeddings.prod.cascadia,10.0.1.50,8080,10,100",
		"address=/outside.example/192.0.2.1",
		"server=/prod.cascadia/10.0.0.53",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	priority := 10
	weight := 100
	port := 8080
	want := []domain.DNSRecord{
		{Zone: "prod.cascadia", Name: "_http._tcp.embeddings", FQDN: "_http._tcp.embeddings.prod.cascadia", Type: domain.DNSRecordTypeSRV, Value: "10.0.1.50", TTL: 300, Priority: &priority, Weight: &weight, Port: &port},
		{Zone: "prod.cascadia", Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "::1", TTL: 300},
		{Zone: "prod.cascadia", Name: "drydock-review", FQDN: "drydock-review.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.1.44", TTL: 300},
		{Zone: "prod.cascadia", Name: "llm", FQDN: "llm.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: "drydock-review.prod.cascadia", TTL: 300},
	}
	got, err := backend.ListRecords(context.Background(), dnsmasqTestZone())
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records mismatch:\ngot  %#v\nwant %#v", got, want)
	}
}

func TestDnsmasqBackendAtomicFileReplacement(t *testing.T) {
	rootDir := t.TempDir()
	executor := &mockDnsmasqCommandExecutor{}
	backend := NewDnsmasqBackend(DnsmasqConfig{ConfigDir: rootDir, ReloadCommand: "reload"})
	backend.commandExecutor = executor
	zone := dnsmasqTestZone()
	oldRecords := []domain.DNSRecord{{Zone: zone.Name, Name: "old", FQDN: "old.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.1", TTL: 300}}
	newRecords := []domain.DNSRecord{{Zone: zone.Name, Name: "new", FQDN: "new.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.2", TTL: 300}}
	if err := backend.SyncZone(context.Background(), zone, oldRecords); err != nil {
		t.Fatalf("sync old records: %v", err)
	}
	path := filepath.Join(rootDir, "bahia-prod-cascadia.conf")
	oldContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read old conf: %v", err)
	}
	if !strings.Contains(string(oldContent), "address=/old.prod.cascadia/10.0.0.1") {
		t.Fatalf("old conf did not contain old record:\n%s", oldContent)
	}
	if err := backend.SyncZone(context.Background(), zone, newRecords); err != nil {
		t.Fatalf("sync new records: %v", err)
	}
	newContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read new conf: %v", err)
	}
	if strings.Contains(string(newContent), "old.prod.cascadia") || !strings.Contains(string(newContent), "address=/new.prod.cascadia/10.0.0.2") {
		t.Fatalf("expected atomic replacement with only new records:\n%s", newContent)
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read root dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file was left behind: %s", entry.Name())
		}
	}
}

func TestDnsmasqBackendHealthValidAndInvalidDirectories(t *testing.T) {
	valid := NewDnsmasqBackend(DnsmasqConfig{ConfigDir: t.TempDir(), ReloadCommand: "true"})
	if err := valid.Health(context.Background()); err != nil {
		t.Fatalf("Health returned error for valid dir: %v", err)
	}
	missing := NewDnsmasqBackend(DnsmasqConfig{ConfigDir: filepath.Join(t.TempDir(), "missing"), ReloadCommand: "true"})
	if err := missing.Health(context.Background()); err == nil {
		t.Fatal("expected Health error for missing dir")
	}
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fileBackend := NewDnsmasqBackend(DnsmasqConfig{ConfigDir: filePath, ReloadCommand: "true"})
	if err := fileBackend.Health(context.Background()); err == nil {
		t.Fatal("expected Health error for file path")
	}
}

func TestDnsmasqBackendReloadFailureRestoresAndReloadsPreviousConfig(t *testing.T) {
	rootDir := t.TempDir()
	const previous = "# previous config\naddress=/old.prod.cascadia/10.0.0.9\n"
	path := filepath.Join(rootDir, "bahia-prod-cascadia.conf")
	if err := os.WriteFile(path, []byte(previous), 0o640); err != nil {
		t.Fatalf("write previous config: %v", err)
	}
	executor := &mockDnsmasqCommandExecutor{errs: []error{errors.New("reload failed"), nil}}
	backend := NewDnsmasqBackend(DnsmasqConfig{ConfigDir: rootDir, ReloadCommand: "reload"})
	backend.commandExecutor = executor
	zone := dnsmasqTestZone()
	err := backend.SyncZone(context.Background(), zone, []domain.DNSRecord{{Zone: zone.Name, Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.0.1", TTL: 300}})
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("SyncZone error = %v, want initial reload failure", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read restored config: %v", readErr)
	}
	if string(content) != previous {
		t.Fatalf("config after failed reload = %q, want previous %q", content, previous)
	}
	if !reflect.DeepEqual(executor.commands, []string{"reload", "reload"}) {
		t.Fatalf("reload commands = %#v, want initial and rollback reload", executor.commands)
	}
}

func dnsmasqTestZone() domain.DNSZone {
	return domain.DNSZone{Name: "prod.cascadia", Visibility: domain.ZoneVisibilityInternal, BackendRef: "dnsmasq-main", TTL: 300}
}

package engine

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

func TestReloadStrategyDetectionOrder(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"pkill", "killall", "service", "systemctl"} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)

	e := New(Config{IncludeDir: t.TempDir()})
	want := []string{"systemctl", "reload", "dnsmasq"}
	if got := e.SelectedReloadStrategy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("selected strategy = %#v, want %#v", got, want)
	}
}

func TestAutomaticReloadStrategyChainOrder(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "executable")
	if err := os.WriteFile(stub, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write stat stub: %v", err)
	}
	stubInfo, err := os.Stat(stub)
	if err != nil {
		t.Fatalf("stat stub: %v", err)
	}
	tests := []struct {
		name      string
		available map[string]bool
		initD     bool
		want      []string
	}{
		{name: "systemctl first", available: map[string]bool{"systemctl": true, "service": true, "killall": true, "pkill": true}, initD: true, want: []string{"systemctl", "reload", "dnsmasq"}},
		{name: "service second", available: map[string]bool{"service": true, "killall": true, "pkill": true}, initD: true, want: []string{"service", "dnsmasq", "reload"}},
		{name: "init script third", available: map[string]bool{"killall": true, "pkill": true}, initD: true, want: []string{"/etc/init.d/dnsmasq", "reload"}},
		{name: "killall fourth", available: map[string]bool{"killall": true, "pkill": true}, want: []string{"killall", "-HUP", "dnsmasq"}},
		{name: "pkill fifth", available: map[string]bool{"pkill": true}, want: []string{"pkill", "-HUP", "dnsmasq"}},
		{name: "none", available: map[string]bool{}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookPath := func(name string) (string, error) {
				if tt.available[name] {
					return filepath.Join("/fake", name), nil
				}
				return "", os.ErrNotExist
			}
			stat := func(string) (os.FileInfo, error) {
				if tt.initD {
					return stubInfo, nil
				}
				return nil, os.ErrNotExist
			}
			if got := detectAutomaticReloadStrategy(lookPath, stat); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("strategy = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyZoneReloadFailureRestoresPriorBytesAndModeAndReloadsAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bahia-prod-cascadia.conf")
	previous := []byte("# existing manual-compatible bytes\naddress=/old.prod.cascadia/10.0.0.9\n")
	if err := os.WriteFile(path, previous, 0o640); err != nil {
		t.Fatalf("write previous file: %v", err)
	}
	var calls [][]string
	runner := func(_ context.Context, argv []string) error {
		calls = append(calls, append([]string(nil), argv...))
		if len(calls) == 1 {
			return errors.New("reload failed")
		}
		return nil
	}
	e := New(Config{IncludeDir: dir, Runner: runner, Reload: ReloadConfig{ExplicitCommand: "reload dnsmasq"}})

	err := e.ApplyZone(context.Background(), testZone(), testRecords())
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("ApplyZone error = %v, want reload failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read restored file: %v", readErr)
	}
	if !reflect.DeepEqual(got, previous) {
		t.Fatalf("restored bytes = %q, want %q", got, previous)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat restored file: %v", statErr)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o640 {
		t.Fatalf("restored mode = %o, want 640", gotMode)
	}
	wantCalls := [][]string{{"sh", "-c", "reload dnsmasq"}, {"sh", "-c", "reload dnsmasq"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestApplyZoneReloadFailureRemovesNewFile(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	e := New(Config{
		IncludeDir: dir,
		Runner: func(_ context.Context, _ []string) error {
			calls++
			if calls == 1 {
				return errors.New("reload failed")
			}
			return nil
		},
		Reload: ReloadConfig{ExplicitCommand: "reload"},
	})

	if err := e.ApplyZone(context.Background(), testZone(), testRecords()); err == nil {
		t.Fatal("ApplyZone returned nil, want reload failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "bahia-prod-cascadia.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new zone file remains after rollback: %v", err)
	}
	if calls != 2 {
		t.Fatalf("reload calls = %d, want 2", calls)
	}
}

func TestApplyZonePreReloadCheckFailureRestoresWithoutReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bahia-prod-cascadia.conf")
	previous := []byte("# previous\n")
	if err := os.WriteFile(path, previous, 0o644); err != nil {
		t.Fatalf("write previous file: %v", err)
	}
	var calls [][]string
	e := New(Config{
		IncludeDir: dir,
		Runner: func(_ context.Context, argv []string) error {
			calls = append(calls, append([]string(nil), argv...))
			return errors.New("config invalid")
		},
		Reload: ReloadConfig{ExplicitCommand: "reload", PreReloadCheck: []string{"dnsmasq", "--test"}},
	})

	err := e.ApplyZone(context.Background(), testZone(), testRecords())
	if err == nil || !strings.Contains(err.Error(), "config invalid") {
		t.Fatalf("ApplyZone error = %v, want validation failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read restored file: %v", readErr)
	}
	if !reflect.DeepEqual(got, previous) {
		t.Fatalf("restored bytes = %q, want %q", got, previous)
	}
	wantCalls := [][]string{{"dnsmasq", "--test"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want pre-reload check only", calls)
	}
}

func TestApplyZoneLeavesOtherFilesUntouched(t *testing.T) {
	dir := t.TempDir()
	manualPath := filepath.Join(dir, "manual.conf")
	otherPrefixPath := filepath.Join(dir, "other-prod-cascadia.conf")
	manual := []byte("address=/manual.example/192.0.2.1\n")
	other := []byte("address=/other.example/192.0.2.2\n")
	if err := os.WriteFile(manualPath, manual, 0o600); err != nil {
		t.Fatalf("write manual file: %v", err)
	}
	if err := os.WriteFile(otherPrefixPath, other, 0o640); err != nil {
		t.Fatalf("write other-prefix file: %v", err)
	}
	e := New(Config{
		IncludeDir: dir,
		Runner:     func(context.Context, []string) error { return nil },
		Reload:     ReloadConfig{ExplicitCommand: "reload"},
	})

	if err := e.ApplyZone(context.Background(), testZone(), testRecords()); err != nil {
		t.Fatalf("ApplyZone returned error: %v", err)
	}
	assertFileUnchanged(t, manualPath, manual, 0o600)
	assertFileUnchanged(t, otherPrefixPath, other, 0o640)
}

func TestRenderAndParsePreserveEstablishedFormat(t *testing.T) {
	const golden = "# Managed by Bahia. Manual changes may be replaced.\n" +
		"# Zone: prod.cascadia\n" +
		"srv-host=_http._tcp.embeddings.prod.cascadia,10.0.1.50,8080,10,100\n" +
		"address=/api.prod.cascadia/::1\n" +
		"address=/drydock-review.prod.cascadia/10.0.1.44\n" +
		"cname=llm.prod.cascadia,drydock-review.prod.cascadia\n"
	zone := testZone()
	rendered, err := RenderZone(zone, testRecords())
	if err != nil {
		t.Fatalf("RenderZone returned error: %v", err)
	}
	if string(rendered) != golden {
		t.Fatalf("rendered bytes differ from pre-refactor golden:\ngot:\n%s\nwant:\n%s", rendered, golden)
	}
	parsed, err := ParseZoneFile(zone, []byte(golden))
	if err != nil {
		t.Fatalf("ParseZoneFile returned error: %v", err)
	}
	renderedAgain, err := RenderZone(zone, parsed)
	if err != nil {
		t.Fatalf("RenderZone(parsed) returned error: %v", err)
	}
	if string(renderedAgain) != golden {
		t.Fatalf("parse/render round trip changed bytes:\ngot:\n%s\nwant:\n%s", renderedAgain, golden)
	}
}

func assertFileUnchanged(t *testing.T, path string, want []byte, wantMode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s bytes = %q, want %q", path, got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), wantMode)
	}
}

func testZone() domain.DNSZone {
	return domain.DNSZone{Name: "prod.cascadia", Visibility: domain.ZoneVisibilityInternal, BackendRef: "dnsmasq-main", TTL: 300}
}

func testRecords() []domain.DNSRecord {
	priority := 10
	weight := 100
	port := 8080
	return []domain.DNSRecord{
		{Zone: "prod.cascadia", Name: "drydock-review", FQDN: "drydock-review.prod.cascadia", Type: domain.DNSRecordTypeA, Value: "10.0.1.44", TTL: 300},
		{Zone: "prod.cascadia", Name: "api", FQDN: "api.prod.cascadia", Type: domain.DNSRecordTypeAAAA, Value: "::1", TTL: 300},
		{Zone: "prod.cascadia", Name: "llm", FQDN: "llm.prod.cascadia", Type: domain.DNSRecordTypeCNAME, Value: "drydock-review.prod.cascadia", TTL: 300},
		{Zone: "prod.cascadia", Name: "_http._tcp.embeddings", FQDN: "_http._tcp.embeddings.prod.cascadia", Type: domain.DNSRecordTypeSRV, Value: "10.0.1.50", TTL: 300, Priority: &priority, Weight: &weight, Port: &port},
	}
}

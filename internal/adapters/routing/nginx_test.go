package routing

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

type recordingArgvRunner struct {
	path     string
	calls    [][]string
	contents [][]byte
	failAt   map[int]error
}

func (r *recordingArgvRunner) run(_ context.Context, argv []string) error {
	r.calls = append(r.calls, append([]string(nil), argv...))
	data, _ := os.ReadFile(r.path)
	r.contents = append(r.contents, data)
	return r.failAt[len(r.calls)]
}

func nginxBackendFixture(t *testing.T) (*NginxBackend, *domain.DesiredPublicRoutePlan, *recordingArgvRunner, string) {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "fullchain.pem")
	keyFile := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certFile, []byte("certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte("private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := cloudflareTestPlan()
	plan.InternalHTTPS = &domain.DesiredInternalHTTPSPlan{
		SchemaVersion: domain.InternalHTTPSPlanSchemaVersion,
		Hostname:      plan.Hostname,
		Listen:        "443 ssl",
		UpstreamURL:   plan.Tunnel.OriginURL,
		CertFile:      certFile,
		KeyFile:       keyFile,
		ConfigHash:    "sha256:internal",
		Apply:         []domain.DesiredPublicRouteChange{{Order: 1, Resource: "nginx_vhost", Action: "upsert", Summary: "install vhost"}},
		Rollback:      []domain.DesiredPublicRouteChange{{Order: 1, Resource: "nginx_vhost", Action: "restore", Summary: "restore vhost"}},
	}
	path := filepath.Join(dir, "bahia-"+plan.Hostname+".conf")
	runner := &recordingArgvRunner{path: path, failAt: map[int]error{}}
	backend, err := NewNginxBackend(NginxConfig{
		IncludeDir: dir, FilePrefix: "bahia-", TestCommand: []string{"nginx", "-t"}, ReloadCommand: []string{"nginx", "-s", "reload"},
		CertFile: certFile, KeyFile: keyFile, ConfigHash: "sha256:internal", Runner: runner.run,
	})
	if err != nil {
		t.Fatalf("NewNginxBackend: %v", err)
	}
	return backend, plan, runner, path
}

func TestNginxApplyWritesOwnedDeterministicVhostThenTestsAndReloads(t *testing.T) {
	backend, plan, runner, path := nginxBackendFixture(t)
	if err := backend.Apply(context.Background(), plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(runner.calls, [][]string{{"nginx", "-t"}, {"nginx", "-s", "reload"}}) {
		t.Fatalf("commands = %#v", runner.calls)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		nginxOwnershipHeader, "listen 443 ssl;", "server_name arcana.example.com;", "proxy_pass http://edge-01.internal:8080;",
		"proxy_set_header Host $host;", "proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;", "proxy_set_header X-Forwarded-Proto $scheme;",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("vhost missing %q:\n%s", want, data)
		}
	}
	rendered, err := RenderNginxVhost(plan.InternalHTTPS)
	if err != nil || !reflect.DeepEqual(data, rendered) {
		t.Fatalf("render is not deterministic: err=%v\nfile=%s\nrendered=%s", err, data, rendered)
	}
}

func TestNginxTestFailureRestoresBeforeAnyReload(t *testing.T) {
	backend, plan, runner, path := nginxBackendFixture(t)
	previous := []byte(nginxOwnershipHeader + "\n# previous\n")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	runner.failAt[1] = errors.New("nginx test failed")
	err := backend.Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "previous internal route restored") {
		t.Fatalf("Apply error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if !reflect.DeepEqual(got, previous) {
		t.Fatalf("previous vhost not restored: %q", got)
	}
	if len(runner.calls) != 3 || runner.calls[0][1] != "-t" || runner.calls[1][1] != "-t" || runner.calls[2][1] != "-s" {
		t.Fatalf("commands = %#v", runner.calls)
	}
	if reflect.DeepEqual(runner.contents[0], previous) || !reflect.DeepEqual(runner.contents[1], previous) || !reflect.DeepEqual(runner.contents[2], previous) {
		t.Fatalf("reload observed broken config: contents=%q", runner.contents)
	}
}

func TestNginxReloadFailureRestoresRetestsAndReloads(t *testing.T) {
	backend, plan, runner, path := nginxBackendFixture(t)
	previous := []byte(nginxOwnershipHeader + "\n# previous\n")
	if err := os.WriteFile(path, previous, 0o640); err != nil {
		t.Fatal(err)
	}
	runner.failAt[2] = errors.New("reload failed")
	err := backend.Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "previous internal route restored") {
		t.Fatalf("Apply error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if !reflect.DeepEqual(got, previous) {
		t.Fatalf("previous vhost not restored: %q", got)
	}
	if len(runner.calls) != 4 || runner.calls[0][1] != "-t" || runner.calls[1][1] != "-s" || runner.calls[2][1] != "-t" || runner.calls[3][1] != "-s" {
		t.Fatalf("commands = %#v", runner.calls)
	}
	if !reflect.DeepEqual(runner.contents[2], previous) || !reflect.DeepEqual(runner.contents[3], previous) {
		t.Fatalf("restored commands did not observe previous config: %q", runner.contents)
	}
}

func TestNginxRejectsForeignFileCollision(t *testing.T) {
	backend, plan, runner, path := nginxBackendFixture(t)
	foreign := []byte("# operator managed\nserver {}\n")
	if err := os.WriteFile(path, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := backend.Check(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("Check error = %v", err)
	}
	if err := backend.Apply(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("Apply error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if !reflect.DeepEqual(got, foreign) || len(runner.calls) != 0 {
		t.Fatalf("foreign file changed or commands ran: file=%q commands=%#v", got, runner.calls)
	}
}

func TestNginxAbsentInternalPlanRemovesOwnedVhostAndReloads(t *testing.T) {
	backend, plan, runner, path := nginxBackendFixture(t)
	if err := backend.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	plan.InternalHTTPS = nil
	if err := backend.Apply(context.Background(), plan); err != nil {
		t.Fatalf("remove Apply: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned vhost still exists: %v", err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("commands = %#v", runner.calls)
	}
}

func TestNginxHealthCheckRequiresReadableCertificateFiles(t *testing.T) {
	backend, _, _, _ := nginxBackendFixture(t)
	if err := backend.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if err := os.Remove(backend.cfg.CertFile); err != nil {
		t.Fatal(err)
	}
	if err := backend.HealthCheck(context.Background()); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("HealthCheck error = %v", err)
	}
}

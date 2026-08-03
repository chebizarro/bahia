package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestComposeImageEnvKey(t *testing.T) {
	cases := map[string]string{
		"my-service": "MY_SERVICE_IMAGE",
		"agent.api":  "AGENT_API_IMAGE",
		"api_v2":     "API_V2_IMAGE",
		"api/v2":     "API_V2_IMAGE",
	}
	for input, want := range cases {
		if got := composeImageEnvKey(input); got != want {
			t.Errorf("composeImageEnvKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestComposeRuntimeCommandEnvMergesDockerHostAndExtraEnv(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///old.sock")
	t.Setenv("APP_ENV", "old")

	r := &ComposeRuntime{dockerHost: "tcp://docker:2375"}
	env := r.commandEnv([]string{"APP_ENV=new", "MY_SERVICE_IMAGE=registry.example/app:v2"})

	if got := envValue(env, "DOCKER_HOST"); got != "tcp://docker:2375" {
		t.Fatalf("DOCKER_HOST = %q, want tcp://docker:2375", got)
	}
	if got := envValue(env, "APP_ENV"); got != "new" {
		t.Fatalf("APP_ENV = %q, want new", got)
	}
	if got := envValue(env, "MY_SERVICE_IMAGE"); got != "registry.example/app:v2" {
		t.Fatalf("MY_SERVICE_IMAGE = %q, want registry.example/app:v2", got)
	}
}

func TestComposeRuntimeEndpointAddsTLSEnvAndCertPath(t *testing.T) {
	certDir := t.TempDir()
	ca := filepath.Join(certDir, "ca.pem")
	cert := filepath.Join(certDir, "cert.pem")
	key := filepath.Join(certDir, "key.pem")
	for _, path := range []string{ca, cert, key} {
		if err := os.WriteFile(path, []byte("pem"), 0o600); err != nil {
			t.Fatalf("write cert fixture: %v", err)
		}
	}

	r, err := NewComposeRuntimeWithEndpoint("/srv/app", config.RuntimeEndpointConfig{
		DockerHost:     "tcp://docker:2376",
		CACertFile:     ca,
		ClientCertFile: cert,
		ClientKeyFile:  key,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewComposeRuntimeWithEndpoint() error = %v", err)
	}

	env := r.commandEnv(nil)
	if got := envValue(env, "DOCKER_HOST"); got != "tcp://docker:2376" {
		t.Fatalf("DOCKER_HOST = %q", got)
	}
	if got := envValue(env, "DOCKER_TLS_VERIFY"); got != "1" {
		t.Fatalf("DOCKER_TLS_VERIFY = %q, want 1", got)
	}
	certPath := envValue(env, "DOCKER_CERT_PATH")
	if certPath != certDir {
		t.Fatalf("DOCKER_CERT_PATH = %q, want %q", certPath, certDir)
	}
}

func TestComposeRuntimeDeployAppliesImageOverrideToPullAndUp(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "compose-calls.log")
	bin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s|%s|%s\n' "$AGENT_API_IMAGE" "$DOCKER_HOST" "$*" >> "$COMPOSE_CALL_LOG"
`)
	t.Setenv("COMPOSE_CALL_LOG", logPath)

	// Create a Bahia-owned project directory so the ownership gate passes.
	projectDir := t.TempDir()
	createBahiaMarker(t, projectDir)

	r := &ComposeRuntime{
		projectDir: projectDir,
		binary:     bin,
		dockerHost: "tcp://docker:2375",
		logger:     zap.NewNop(),
	}

	if err := r.Deploy(context.Background(), "agent.api", "registry.example/agent:v2", DeployOptions{
		Environment: map[string]string{"AGENT_API_IMAGE": "registry.example/agent:old"},
	}); err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := nonEmptyLines(string(raw))
	if len(lines) != 2 {
		t.Fatalf("expected pull and up calls, got %d: %q", len(lines), raw)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "registry.example/agent:v2|tcp://docker:2375|") {
			t.Fatalf("image override or docker host missing from call line: %q", line)
		}
	}
	if !strings.Contains(lines[0], "pull agent.api") {
		t.Fatalf("first call should pull service, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "up -d --force-recreate --no-deps agent.api") {
		t.Fatalf("second call should up service, got %q", lines[1])
	}
}

func TestComposeRuntimeDeploySkipsBlankImageOverride(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "compose-calls.log")
	bin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s|%s\n' "$MY_SERVICE_IMAGE" "$*" >> "$COMPOSE_CALL_LOG"
`)
	t.Setenv("COMPOSE_CALL_LOG", logPath)

	// Create a Bahia-owned project directory so the ownership gate passes.
	projectDir := t.TempDir()
	createBahiaMarker(t, projectDir)

	r := &ComposeRuntime{projectDir: projectDir, binary: bin, logger: zap.NewNop()}
	if err := r.Deploy(context.Background(), "my-service", "   ", DeployOptions{}); err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, line := range nonEmptyLines(string(raw)) {
		if !strings.HasPrefix(line, "|") {
			t.Fatalf("blank image should not inject MY_SERVICE_IMAGE, got line %q", line)
		}
	}
}

func TestComposeRuntimeObserveResolvesImageDigestFromRunningContainer(t *testing.T) {
	composeBin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s' '{"ID":"running-id","Image":"registry.example/app:v2","State":"running","Status":"Up"}'
`)
	dockerBin := writeFakeNamedBinary(t, "docker", `#!/bin/sh
case "$*" in
	"container inspect running-id")
	printf '%s' '[{"Id":"running-id","Image":"sha256:runningimage","Config":{"Image":"registry.example/app:v2","Labels":{"bahia.desired_hash":"sha256:reviewed"}}}]'
	;;
	"image inspect sha256:runningimage")
	printf '%s' '[{"Id":"sha256:runningimage","RepoDigests":["registry.example/app@sha256:runningdigest"]}]'
	;;
	"image inspect registry.example/app:v2")
	echo "tag inspect fallback should not be used" >&2
	exit 1
	;;
	*)
  echo "unexpected docker args: $*" >&2
  exit 1
	;;
esac
`)
	t.Setenv("PATH", filepath.Dir(dockerBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := &ComposeRuntime{binary: composeBin, dockerHost: "tcp://docker:2375", logger: zap.NewNop()}
	obs, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "app")
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if obs.ObservedImageRepo != "registry.example/app" || obs.ObservedImageDigest != "sha256:runningdigest" {
		t.Fatalf("digest-aware observe mismatch: repo=%q digest=%q", obs.ObservedImageRepo, obs.ObservedImageDigest)
	}
	if obs.NormalizedHash != "sha256:reviewed" {
		t.Fatalf("NormalizedHash = %q, want applied desired hash", obs.NormalizedHash)
	}
}

func TestComposeRuntimeObservePrefersConfiguredRepoDigestWhenImageIDHasMultipleRepos(t *testing.T) {
	composeBin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s' '{"ID":"running-id","Image":"registry.example/app:v2","State":"running","Status":"Up"}'
`)
	dockerBin := writeFakeNamedBinary(t, "docker", `#!/bin/sh
case "$*" in
	"container inspect running-id")
	printf '%s' '[{"Id":"running-id","Image":"sha256:runningimage","Config":{"Image":"registry.example/app:v2","Labels":{"bahia.desired_hash":"sha256:reviewed"}}}]'
	;;
	"image inspect sha256:runningimage")
	printf '%s' '[{"Id":"sha256:runningimage","RepoDigests":["registry.example/old-app@sha256:oldrunningdigest","registry.example/app@sha256:runningdigest"]}]'
	;;
	*)
  echo "unexpected docker args: $*" >&2
  exit 1
	;;
esac
`)
	t.Setenv("PATH", filepath.Dir(dockerBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := &ComposeRuntime{binary: composeBin, dockerHost: "tcp://docker:2375", logger: zap.NewNop()}
	obs, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "app")
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if obs.ObservedImageRepo != "registry.example/app" || obs.ObservedImageDigest != "sha256:runningdigest" {
		t.Fatalf("preferred digest-aware observe mismatch: repo=%q digest=%q", obs.ObservedImageRepo, obs.ObservedImageDigest)
	}
}

func TestComposeRuntimeObserveParsesJSONArrayAndPrefersRunningEntry(t *testing.T) {
	bin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s' '[{"ID":"stopped-id","Image":"registry.example/app:v1","State":"exited","Status":"Exited"},{"ID":"running-id","Image":"registry.example/app:v2","State":"running","Status":"Up"}]'
`)
	r := &ComposeRuntime{binary: bin, logger: zap.NewNop()}

	obs, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "app")
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Fatalf("HealthStatus = %s, want healthy", obs.HealthStatus)
	}
	if obs.ObservedContainerID != "running-id" {
		t.Fatalf("ObservedContainerID = %q, want running-id", obs.ObservedContainerID)
	}
	if obs.ObservedImageRepo != "registry.example/app:v2" {
		t.Fatalf("ObservedImageRepo = %q, want registry.example/app:v2", obs.ObservedImageRepo)
	}
}

func TestComposeRuntimeObserveIgnoresStderrWarnings(t *testing.T) {
	bin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s\n' 'warning: obsolete compose version' >&2
printf '%s' '{"ID":"running-id","Image":"registry.example/app:v2","State":"running","Status":"Up"}'
`)
	r := &ComposeRuntime{binary: bin, logger: zap.NewNop()}

	obs, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "app")
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if obs.ObservedContainerID != "running-id" {
		t.Fatalf("ObservedContainerID = %q, want running-id", obs.ObservedContainerID)
	}
}

func TestComposeRuntimeObserveInheritsDockerHost(t *testing.T) {
	bin := writeFakeComposeBinary(t, `#!/bin/sh
if [ "$DOCKER_HOST" != "tcp://docker:2375" ]; then
  echo "wrong docker host: $DOCKER_HOST" >&2
  exit 1
fi
printf '%s' '{"ID":"running-id","Image":"registry.example/app:v2","State":"running","Status":"Up"}'
`)
	r := &ComposeRuntime{binary: bin, dockerHost: "tcp://docker:2375", logger: zap.NewNop()}

	if _, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "app"); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
}

func TestComposeRuntimeObserveParsesLineDelimitedJSON(t *testing.T) {
	bin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s\n%s\n' '{"ID":"first","Image":"registry.example/app:v1","State":"exited","Status":"Exited"}' '{"ID":"second","Image":"registry.example/app:v2","State":"running","Status":"Up"}'
`)
	r := &ComposeRuntime{binary: bin, logger: zap.NewNop()}

	obs, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "app")
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Fatalf("HealthStatus = %s, want healthy", obs.HealthStatus)
	}
	if obs.ObservedContainerID != "second" {
		t.Fatalf("ObservedContainerID = %q, want second", obs.ObservedContainerID)
	}
}

func TestComposeRuntimeObserveReturnsParseErrorForInvalidJSON(t *testing.T) {
	bin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s' 'not-json'
`)
	r := &ComposeRuntime{binary: bin, logger: zap.NewNop()}

	_, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "app")
	if err == nil {
		t.Fatal("Observe() expected parse error")
	}
	if !strings.Contains(err.Error(), "parse compose ps output") {
		t.Fatalf("Observe() error = %v, want parse compose ps output", err)
	}
}

func writeFakeComposeBinary(t *testing.T, content string) string {
	t.Helper()
	return writeFakeNamedBinary(t, "fake-compose", content)
}

func writeFakeNamedBinary(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake binary %s: %v", name, err)
	}
	return path
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func nonEmptyLines(s string) []string {
	parts := strings.Split(s, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			lines = append(lines, part)
		}
	}
	return lines
}

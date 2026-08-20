package openclawcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

type dockerStateRunner struct {
	mu      sync.Mutex
	calls   []commandCall
	exists  bool
	running bool
	labels  map[string]string
	spec    RuntimeSpec
}

func (r *dockerStateRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, commandCall{name: name, args: append([]string{}, args...)})
	if len(args) > 0 && args[0] == "inspect" {
		if !r.exists {
			return nil, CommandExecutionError{Message: "Error: No such container"}
		}
		labels := r.labels
		if labels == nil {
			labels = runtimeLabels(r.spec)
		}
		record := dockerInspect{ID: "sha256:container-id", Name: "/" + r.spec.ContainerName}
		record.Config.Image = r.spec.ImageDigest
		record.Config.Labels = labels
		record.State.Running = r.running
		record.State.Health = &struct {
			Status string `json:"Status"`
		}{Status: "healthy"}
		return json.Marshal([]dockerInspect{record})
	}
	if containsArgSequence(args, "up", "--detach", "--wait") {
		r.exists = true
		r.running = true
		return []byte(`{"created":true}`), nil
	}
	if len(args) > 0 && args[0] == "restart" {
		r.running = false
		return []byte(`{"restarted":true}`), nil
	}
	if containsArgSequence(args, "down", "--remove-orphans") {
		r.exists = false
		r.running = false
		return []byte(`{"deleted":true}`), nil
	}
	return nil, errors.New("unexpected docker command")
}

func testRuntimeSpec(root, agent, request, run string) RuntimeSpec {
	deployment := deterministicRuntimeName(agent, request, run)
	return RuntimeSpec{
		DeploymentID: deployment, ContainerName: deployment + "-gateway",
		AgentID: agent, SoulID: "soul-" + agent, AccountID: "account-" + agent,
		Model:     "provider/model",
		RequestID: request, RunID: run, SpecHash: "sha256:spec",
		ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		ConfigRevision: "sha256:config", ComposePath: filepath.Join(root, agent, "compose.yaml"),
		ConfigDir: filepath.Join(root, agent, "config"), Workspace: filepath.Join(root, agent, "workspace"),
		AgentDir: filepath.Join(root, agent, "agent"), CPUs: "1.0", Memory: "1g", PIDsLimit: 256,
		User: "1000:1000",
	}
}

func TestRuntimeNamesAndComposeAreDeterministicIsolatedAndSecretSafe(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "provider-token")
	if err := os.WriteFile(secret, []byte("super-secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := testRuntimeSpec(root, "Agent.Alice", "request-one", "run-one")
	spec.SecretFiles = map[string]string{"nip46_client": secret}
	if err := validateSecretFiles(spec.SecretFiles); err != nil {
		t.Fatalf("validate secret: %v", err)
	}
	compose := string(renderCompose(spec))
	var parsedCompose map[string]interface{}
	if err := yaml.Unmarshal([]byte(compose), &parsedCompose); err != nil {
		t.Fatalf("compose is not valid YAML: %v\n%s", err, compose)
	}
	for _, required := range []string{testImageDigest, "user: \"1000:1000\"", "restart: unless-stopped", "healthcheck:", "mem_limit: \"1g\"", "pids_limit: 256", "driver: json-file", "driver: bridge", "read_only: true", "/run/secrets/nip46_client"} {
		if !strings.Contains(compose, required) {
			t.Fatalf("compose missing %q:\n%s", required, compose)
		}
	}
	if strings.Contains(compose, "super-secret-value") {
		t.Fatal("compose leaked secret content")
	}
	executor := newTestExecutor(t, root, true, nil)
	invocation := testProvisionInvocation("Agent.Alice", "sha256:spec")
	invocation.Params["runtime"].(map[string]interface{})["nostr"] = map[string]interface{}{
		"nip46": true, "nip46BunkerUrl": "user@example.com",
	}
	paths := executor.paths(invocation.AgentID)
	for _, directory := range []string{paths.ConfigDir, paths.OpenClawDir, paths.AgentDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := executor.renderRuntimeFiles(invocation, spec, paths); err != nil {
		t.Fatalf("render runtime files: %v", err)
	}
	configBytes, err := os.ReadFile(paths.RuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	config := string(configBytes)
	if strings.Contains(config, "super-secret-value") || strings.Contains(config, `"soulfactory"`) || !strings.Contains(config, `"source": "file"`) || !strings.Contains(config, `"path": "/run/secrets/nip46_client"`) {
		t.Fatalf("runtime config did not contain only file-backed secret references:\n%s", config)
	}
	info, err := os.Stat(paths.RuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime config mode=%v", info.Mode().Perm())
	}
	if spec.DeploymentID != deterministicRuntimeName("Agent.Alice", "request-one", "run-one") {
		t.Fatal("runtime name is not deterministic")
	}
	if spec.DeploymentID == deterministicRuntimeName("Agent.Alice", "request-two", "run-one") {
		t.Fatal("different request ownership collided")
	}
}

func TestRuntimeReconcileAdoptsReplayRecoversRestartRejectsConflictAndDeletes(t *testing.T) {
	spec := testRuntimeSpec(t.TempDir(), "agent-alice", "request-one", "run-one")
	runner := &dockerStateRunner{spec: spec}
	orchestrator := DockerComposeOrchestrator{DockerBin: "docker-test", Runner: runner}

	first, err := orchestrator.Reconcile(t.Context(), spec)
	if err != nil || first.ContainerID != "container-id" || first.Health != "healthy" {
		t.Fatalf("initial reconcile lineage=%+v err=%v", first, err)
	}
	callCount := len(runner.calls)
	second, err := orchestrator.Reconcile(t.Context(), spec)
	if err != nil || second.ContainerID != first.ContainerID || len(runner.calls) != callCount+1 {
		t.Fatalf("exact replay did not adopt deployment: lineage=%+v err=%v calls=%+v", second, err, runner.calls)
	}

	runner.running = false
	if _, err := orchestrator.Reconcile(t.Context(), spec); err != nil || !runner.running {
		t.Fatalf("restart reconciliation failed: %v", err)
	}

	runner.labels = runtimeLabels(spec)
	runner.labels[runtimeSoulLabel] = "foreign-soul"
	beforeConflict := len(runner.calls)
	if _, err := orchestrator.Reconcile(t.Context(), spec); err == nil || !strings.Contains(err.Error(), "ownership/spec conflict") {
		t.Fatalf("conflicting ownership was not rejected: %v", err)
	}
	if len(runner.calls) != beforeConflict+1 {
		t.Fatalf("conflict mutated deployment: %+v", runner.calls[beforeConflict:])
	}
	runner.labels = nil
	if err := orchestrator.Delete(t.Context(), spec); err != nil || runner.exists {
		t.Fatalf("owned delete failed: exists=%v err=%v", runner.exists, err)
	}
}

func TestSecretFilesRequireRestrictiveMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSecretFiles(map[string]string{"provider": path}); err == nil {
		t.Fatal("world-readable secret file was accepted")
	}
}

func TestRuntimeDirectoryRejectsPermissiveOrSymlinkedOwnership(t *testing.T) {
	root := t.TempDir()
	permissive := filepath.Join(root, "permissive")
	if err := os.Mkdir(permissive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(permissive); err == nil {
		t.Fatal("permissive runtime directory was accepted")
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(link); err == nil {
		t.Fatal("symlinked runtime directory was accepted")
	}
}

func TestTwoConcurrentSoulsRenderIndependentRuntimeState(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	outcomes := make(chan string, 2)
	for _, agent := range []string{"agent-one", "agent-two"} {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			executor := newTestExecutor(t, root, true, nil)
			invocation := testProvisionInvocation(agent, "sha256:"+agent)
			invocation.Params["runtime"].(map[string]interface{})["account_id"] = "account-" + agent
			outcome := executor.Execute(t.Context(), invocation)
			if outcome.Status != StatusSuccess {
				outcomes <- "failure:" + agent
				return
			}
			outcomes <- outcome.Result["deployment_id"].(string)
		}()
	}
	wg.Wait()
	close(outcomes)
	seen := map[string]bool{}
	for deployment := range outcomes {
		if strings.HasPrefix(deployment, "failure:") || seen[deployment] {
			t.Fatalf("concurrent soul isolation failed: %q", deployment)
		}
		seen[deployment] = true
	}
	for _, agent := range []string{"agent-one", "agent-two"} {
		state := readStateForTest(t, root, agent)
		expectedWorkspace := filepath.Join(root, "agents", agent, "deployments", state.DeploymentID, "workspace")
		if state.Workspace != expectedWorkspace || state.Container == "" || state.AccountID != "account-"+agent {
			t.Fatalf("shared or missing runtime state for %s: %+v", agent, state)
		}
	}
}

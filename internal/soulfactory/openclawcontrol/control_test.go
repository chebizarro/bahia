package openclawcontrol

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

type recordingRunner struct {
	mu      sync.Mutex
	calls   []commandCall
	err     error
	errAt   int
	outputs [][]byte
}

type commandCall struct {
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, commandCall{name: name, args: append([]string{}, args...)})
	if r.err != nil && (r.errAt == 0 || r.errAt == len(r.calls)) {
		return nil, r.err
	}
	if len(r.outputs) >= len(r.calls) {
		return r.outputs[len(r.calls)-1], nil
	}
	if containsArgSequence(args, "plugins", "list", "--json") {
		return []byte(`{"plugins":[{"id":"nostr","status":"loaded","enabled":true}]}`), nil
	}
	return []byte(`{"ok":true}`), nil
}

type recordingOrchestrator struct {
	mu         sync.Mutex
	reconciles []RuntimeSpec
	deletes    []RuntimeSpec
	err        error
}

func (o *recordingOrchestrator) Inspect(context.Context, RuntimeSpec) (RuntimeInspection, error) {
	return RuntimeInspection{}, o.err
}

func (o *recordingOrchestrator) Reconcile(_ context.Context, spec RuntimeSpec) (RuntimeLineage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reconciles = append(o.reconciles, spec)
	if o.err != nil {
		return RuntimeLineage{}, o.err
	}
	return RuntimeLineage{
		DeploymentID: spec.DeploymentID, ContainerID: "container-" + spec.AgentID,
		ContainerName: spec.ContainerName, ImageDigest: spec.ImageDigest,
		ConfigRevision: spec.ConfigRevision, WorkspaceID: "workspace://" + spec.DeploymentID,
		AgentID: spec.AgentID, AccountID: spec.AccountID, Health: "healthy",
	}, nil
}

func (o *recordingOrchestrator) Delete(_ context.Context, spec RuntimeSpec) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deletes = append(o.deletes, spec)
	return o.err
}

const (
	testImageDigest  = "ghcr.io/openagents/openclaw@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSourceCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestProvisionDryRunCreatesWorkspaceStateAndReplaysIdempotently(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	invocation := testProvisionInvocation("agent-alice", "sha256:spec")

	outcome := executor.Execute(t.Context(), invocation)
	assertSuccess(t, outcome, "running")
	workspace := readStateForTest(t, root, "agent-alice").Workspace
	for _, rel := range []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "MEMORY.md", ".openclaw/soulfactory.json"} {
		if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
			t.Fatalf("expected workspace file %s: %v", rel, err)
		}
	}
	state := readStateForTest(t, root, "agent-alice")
	if state.AgentID != "agent-alice" || state.SpecHash != "sha256:spec" || state.State != "running" || state.RuntimeBinding != "openclaw://agents/agent-alice" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "agent-alice", "last-invocation.json")); err != nil {
		t.Fatalf("last invocation missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "agent-alice", "last-outcome.json")); err != nil {
		t.Fatalf("last outcome missing: %v", err)
	}

	replay := executor.Execute(t.Context(), invocation)
	if !reflect.DeepEqual(outcome, replay) {
		t.Fatalf("replay outcome changed:\nfirst=%+v\nsecond=%+v", outcome, replay)
	}
}

func TestProvisionRejectsDuplicateSpecConflict(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-alice", "sha256:one")), "running")

	conflict := executor.Execute(t.Context(), testProvisionInvocation("agent-alice", "sha256:two"))
	if conflict.Status != StatusRejected || conflict.Error == nil || conflict.Error.Code != ErrorDuplicateConflict {
		t.Fatalf("conflict outcome = %+v, want duplicate_conflict rejection", conflict)
	}
}

func TestProvisionRejectsUnsafeAgentIDAndInlinePrivateSecret(t *testing.T) {
	executor := newTestExecutor(t, t.TempDir(), true, nil)
	unsafeID := testProvisionInvocation("../agent", "sha256:spec")
	if outcome := executor.Execute(t.Context(), unsafeID); outcome.Status != StatusRejected || outcome.Error.Code != ErrorMissingRequired {
		t.Fatalf("unsafe agent id outcome = %+v", outcome)
	}

	secret := testProvisionInvocation("agent-secret", "sha256:spec")
	secret.Params["runtime"].(map[string]interface{})["private_key"] = "nsec1secret"
	if outcome := executor.Execute(t.Context(), secret); outcome.Status != StatusRejected || outcome.Error.Code != ErrorMissingRequired {
		t.Fatalf("inline private secret outcome = %+v", outcome)
	}

	nostrKey := testProvisionInvocation("agent-nostr-secret", "sha256:spec")
	nostrKey.Params["runtime"].(map[string]interface{})["nostr_private_key"] = "config should have used a secret ref"
	if outcome := executor.Execute(t.Context(), nostrKey); outcome.Status != StatusRejected || outcome.Error.Code != ErrorMissingRequired {
		t.Fatalf("nostr private key outcome = %+v", outcome)
	}

	nip46Secret := testProvisionInvocation("agent-nip46-secret", "sha256:spec")
	nip46Secret.Params["runtime"].(map[string]interface{})["nostr"] = map[string]interface{}{
		"nip46Secret": strings.Repeat("a", 64),
	}
	if outcome := executor.Execute(t.Context(), nip46Secret); outcome.Status != StatusRejected || outcome.Error.Code != ErrorMissingRequired {
		t.Fatalf("inline NIP-46 secret outcome = %+v", outcome)
	}

	secretRef := testProvisionInvocation("agent-secret-ref", "sha256:spec")
	secretRef.Params["runtime"].(map[string]interface{})["nostr_private_key_ref"] = "secret://souls/openclaw/nostr-private-key"
	if outcome := executor.Execute(t.Context(), secretRef); outcome.Status != StatusSuccess {
		t.Fatalf("secret reference outcome = %+v, want success", outcome)
	}
}

func TestPersonaUpdateWritesPersonaAndWarning(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-alice", "sha256:spec")), "running")

	persona := testInvocation(soulfactory.RuntimeMethodPersonaUpdate, "agent-alice", "sha256:spec", map[string]interface{}{
		"persona": map[string]interface{}{
			"traits":      []interface{}{"steady"},
			"constraints": []interface{}{"Do not use unapproved relays."},
			"system_prompt_sections": map[string]interface{}{
				"role":       "You are SteadyBot.",
				"guidelines": "Answer with audited local state.",
			},
		},
	})
	outcome := executor.Execute(t.Context(), persona)
	assertSuccess(t, outcome, "running")
	warnings, _ := outcome.Result["warnings"].([]string)
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, "\n"), "hot reload is not confirmed") {
		t.Fatalf("persona warnings = %#v", outcome.Result["warnings"])
	}
	workspace := readStateForTest(t, root, "agent-alice").Workspace
	personaPath := filepath.Join(workspace, ".openclaw", "soulfactory-persona.json")
	data, err := os.ReadFile(personaPath)
	if err != nil {
		t.Fatalf("read persona state: %v", err)
	}
	if !strings.Contains(string(data), "You are SteadyBot.") || !strings.Contains(string(data), "system_prompt_override") {
		t.Fatalf("persona state did not contain mapped prompt:\n%s", data)
	}
	agents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "You are SteadyBot.") {
		t.Fatalf("AGENTS.md did not contain persona prompt:\n%s", agents)
	}
}

func TestRevokeRecordsStateAndHonorsWorkspaceDeletion(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-keep", "sha256:spec")), "running")
	keep := testInvocation(soulfactory.RuntimeMethodRevoke, "agent-keep", "sha256:spec", map[string]interface{}{
		"reason":                     "operator requested revoke",
		"revoke_runtime_credentials": false,
		"delete_workspace":           false,
	})
	assertSuccess(t, executor.Execute(t.Context(), keep), "revoked")
	if _, err := os.Stat(filepath.Join(readStateForTest(t, root, "agent-keep").Workspace, "SOUL.md")); err != nil {
		t.Fatalf("workspace should remain when delete_workspace=false: %v", err)
	}

	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-delete", "sha256:spec")), "running")
	remove := testInvocation(soulfactory.RuntimeMethodRevoke, "agent-delete", "sha256:spec", map[string]interface{}{
		"reason":                     "operator requested deletion",
		"revoke_runtime_credentials": true,
		"delete_workspace":           true,
	})
	outcome := executor.Execute(t.Context(), remove)
	assertSuccess(t, outcome, "revoked")
	deletedWorkspace := readStateForTest(t, root, "agent-delete").Workspace
	if _, err := os.Stat(deletedWorkspace); !os.IsNotExist(err) {
		t.Fatalf("workspace should be removed when delete_workspace=true, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "agent-delete", "state.json")); err != nil {
		t.Fatalf("audit state should remain after workspace deletion: %v", err)
	}
	warnings, _ := outcome.Result["warnings"].([]string)
	if !strings.Contains(strings.Join(warnings, "\n"), "credential revocation") {
		t.Fatalf("credential warning missing: %#v", outcome.Result["warnings"])
	}
}

func TestNonDryRunRevokeUsesPersistedContainerAndReplaysWithoutCommands(t *testing.T) {
	root := t.TempDir()
	setupRunner := &recordingRunner{}
	setup := newTestExecutor(t, root, false, setupRunner)
	assertSuccess(t, setup.Execute(t.Context(), testProvisionInvocation("agent-revoke", "sha256:spec")), "running")

	revokeRunner := &recordingRunner{}
	orchestrator := &recordingOrchestrator{}
	revoker, err := New(Config{
		Root: root, OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest,
		SourceCommit: testSourceCommit, RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"},
		Now:    func() time.Time { return time.Unix(1715700005, 0).UTC() },
		Runner: revokeRunner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatalf("New revoker: %v", err)
	}
	revoke := testInvocation(soulfactory.RuntimeMethodRevoke, "agent-revoke", "sha256:spec", map[string]interface{}{
		"reason":                     "operator requested revoke",
		"revoke_runtime_credentials": false,
		"delete_workspace":           false,
	})
	assertSuccess(t, revoker.Execute(t.Context(), revoke), "revoked")
	assertSuccess(t, revoker.Execute(t.Context(), revoke), "revoked")
	if len(revokeRunner.calls) != 1 {
		t.Fatalf("revoke command calls = %d, want one unbind before replay: %+v", len(revokeRunner.calls), revokeRunner.calls)
	}
	if !containsArgSequence(revokeRunner.calls[0].args, "--container", readStateForTest(t, root, "agent-revoke").Container) || !containsArgSequence(revokeRunner.calls[0].args, "agents", "unbind") {
		t.Fatalf("revoke command did not use persisted container unbind: %+v", revokeRunner.calls[0])
	}
	if len(orchestrator.deletes) != 1 {
		t.Fatalf("dedicated deployment deletes = %d, want one", len(orchestrator.deletes))
	}
}

func TestCommandFailurePersistsFailedAuditState(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{err: CommandExecutionError{Message: "cli rejected add"}}
	executor := newTestExecutor(t, root, false, runner)
	outcome := executor.Execute(t.Context(), testProvisionInvocation("agent-fail", "sha256:spec"))
	if outcome.Status != StatusFailed || outcome.Error == nil || outcome.Error.Code != ErrorExecutionFailed {
		t.Fatalf("failure outcome = %+v", outcome)
	}
	state := readStateForTest(t, root, "agent-fail")
	if state.State != "failed" || !strings.Contains(state.LastReason, "cli rejected add") {
		t.Fatalf("failed audit state = %+v", state)
	}
	replay := executor.Execute(t.Context(), testProvisionInvocation("agent-fail", "sha256:spec"))
	if replay.Status != StatusFailed || len(runner.calls) != 1 {
		t.Fatalf("failed replay outcome=%+v command calls=%d, want cached failure without another command", replay, len(runner.calls))
	}
}

func TestUpdateReplacesResolvedSpecAndRefreshesIdentity(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{}
	executor := newTestExecutor(t, root, false, runner)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-update", "sha256:old")), "running")
	runner.calls = nil

	params := map[string]interface{}{
		"previous_spec_hash": "sha256:old",
		"new_spec_hash":      "sha256:new",
		"update_mode":        "replace",
		"resolved_spec": map[string]interface{}{
			"identity":     map[string]interface{}{"name": "Alice Updated", "purpose": "Operate updated audited tasks"},
			"permissions":  map[string]interface{}{"allowed_kinds": []interface{}{1.0, 30317.0}},
			"relay_policy": map[string]interface{}{"control": []interface{}{"wss://relay.example"}},
			"workspace":    map[string]interface{}{"mode": "generated"},
			"assets":       map[string]interface{}{},
		},
	}
	update := testInvocation(soulfactory.RuntimeMethodUpdate, "agent-update", "sha256:new", params)
	outcome := executor.Execute(t.Context(), update)
	assertSuccess(t, outcome, "running")
	state := readStateForTest(t, root, "agent-update")
	if state.SpecHash != "sha256:new" || state.LastMethod != soulfactory.RuntimeMethodUpdate {
		t.Fatalf("updated state = %+v", state)
	}
	identity, err := os.ReadFile(filepath.Join(readStateForTest(t, root, "agent-update").Workspace, "IDENTITY.md"))
	if err != nil || !strings.Contains(string(identity), "Alice Updated") {
		t.Fatalf("updated identity = %q, err=%v", identity, err)
	}
	if len(runner.calls) != 1 || !containsArgSequence(runner.calls[0].args, "agents", "set-identity") {
		t.Fatalf("update command calls = %+v", runner.calls)
	}
	replay := executor.Execute(t.Context(), update)
	assertSuccess(t, replay, "running")
	if len(runner.calls) != 1 {
		t.Fatalf("replayed update executed another command: %+v", runner.calls)
	}
}

func TestUpdateRejectsStalePreviousSpecHash(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-update", "sha256:current")), "running")
	outcome := executor.Execute(t.Context(), testInvocation(soulfactory.RuntimeMethodUpdate, "agent-update", "sha256:new", map[string]interface{}{
		"previous_spec_hash": "sha256:stale",
		"new_spec_hash":      "sha256:new",
		"update_mode":        "replace",
		"resolved_spec":      map[string]interface{}{"identity": map[string]interface{}{"name": "Updated"}},
	}))
	if outcome.Status != StatusRejected || outcome.Error == nil || outcome.Error.Code != ErrorSpecHashMismatch {
		t.Fatalf("stale update outcome = %+v", outcome)
	}
}

func TestUpdateMergeAppliesPatchToCanonicalPriorSpec(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-merge", "sha256:old")), "running")

	outcome := executor.Execute(t.Context(), testInvocation(soulfactory.RuntimeMethodUpdate, "agent-merge", "sha256:new", map[string]interface{}{
		"previous_spec_hash": "sha256:old",
		"new_spec_hash":      "sha256:new",
		"update_mode":        "merge",
		"patch": map[string]interface{}{
			"identity": map[string]interface{}{"name": "Merged Alice"},
		},
	}))
	assertSuccess(t, outcome, "running")
	workspace := readStateForTest(t, root, "agent-merge").Workspace
	identity, err := os.ReadFile(filepath.Join(workspace, "IDENTITY.md"))
	if err != nil || !strings.Contains(string(identity), "Merged Alice") || !strings.Contains(string(identity), "Operate audited OpenClaw tasks") {
		t.Fatalf("merged identity = %q, err=%v", identity, err)
	}
	provenance, ok, err := readJSONFile[map[string]interface{}](filepath.Join(workspace, ".openclaw", "soulfactory.json"))
	if err != nil || !ok {
		t.Fatalf("read provenance: ok=%v err=%v", ok, err)
	}
	params, _ := provenance["params"].(map[string]interface{})
	mergedIdentity, _ := params["identity"].(map[string]interface{})
	if mergedIdentity["name"] != "Merged Alice" || mergedIdentity["purpose"] != "Operate audited OpenClaw tasks" {
		t.Fatalf("canonical merged params = %+v", params)
	}
}

func TestUpdateRejectsInvalidMode(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-update", "sha256:old")), "running")
	outcome := executor.Execute(t.Context(), testInvocation(soulfactory.RuntimeMethodUpdate, "agent-update", "sha256:new", map[string]interface{}{
		"previous_spec_hash": "sha256:old",
		"new_spec_hash":      "sha256:new",
		"update_mode":        "restart",
		"resolved_spec":      map[string]interface{}{"identity": map[string]interface{}{"name": "Updated"}},
	}))
	if outcome.Status != StatusRejected || outcome.Error == nil || outcome.Error.Code != ErrorMissingRequired {
		t.Fatalf("invalid update mode outcome = %+v", outcome)
	}
}

func TestConfigReloadMergesAllowlistedSectionsAndRerendersRuntimeFiles(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-reload", "sha256:old")), "running")

	reload := testInvocation(soulfactory.RuntimeMethodConfigReload, "agent-reload", "sha256:new", map[string]interface{}{
		"schema":             soulfactory.SoulFactoryConfigReloadSchema,
		"target_fields":      []interface{}{"identity", "relay_policy", "plugins"},
		"previous_spec_hash": "sha256:old",
		"new_spec_hash":      "sha256:new",
		"patch": map[string]interface{}{
			"identity":     map[string]interface{}{"name": "Reloaded Alice"},
			"relay_policy": map[string]interface{}{"control": []interface{}{"wss://reload.example"}},
			"plugins":      map[string]interface{}{"allow": []interface{}{"untrusted"}},
		},
	})
	outcome := executor.Execute(t.Context(), reload)
	assertSuccess(t, outcome, "running")
	if outcome.Result["restart"] != false || !reflect.DeepEqual(outcome.Result["reloaded"], []string{"identity", "relay_policy"}) {
		t.Fatalf("reload result = %+v", outcome.Result)
	}
	state := readStateForTest(t, root, "agent-reload")
	if state.SpecHash != "sha256:new" || state.LastMethod != soulfactory.RuntimeMethodConfigReload {
		t.Fatalf("reloaded state = %+v", state)
	}
	identity, err := os.ReadFile(filepath.Join(state.Workspace, "IDENTITY.md"))
	if err != nil || !strings.Contains(string(identity), "Reloaded Alice") {
		t.Fatalf("reloaded identity = %q, err=%v", identity, err)
	}
	paths := executor.pathsForState(state)
	runtimeConfig, ok, err := readJSONFile[map[string]interface{}](paths.RuntimeConfig)
	if err != nil || !ok {
		t.Fatalf("read runtime config: ok=%v err=%v", ok, err)
	}
	plugins, _ := runtimeConfig["plugins"].(map[string]interface{})
	if !reflect.DeepEqual(plugins["allow"], []interface{}{"nostr"}) {
		t.Fatalf("reload changed wrapper-owned plugins: %+v", plugins)
	}
	channels, _ := runtimeConfig["channels"].(map[string]interface{})
	nostrConfig, _ := channels["nostr"].(map[string]interface{})
	if !reflect.DeepEqual(nostrConfig["relays"], []interface{}{"wss://reload.example"}) {
		t.Fatalf("reload relays = %#v", nostrConfig["relays"])
	}
	runtimeMetadata, ok, err := readJSONFile[map[string]interface{}](paths.RuntimeMetadata)
	if err != nil || !ok {
		t.Fatalf("read runtime metadata: ok=%v err=%v", ok, err)
	}
	reloadedSections, _ := runtimeMetadata["soulfactory"].(map[string]interface{})
	if _, exists := reloadedSections["plugins"]; exists {
		t.Fatalf("runtime metadata included non-allowlisted plugins: %+v", reloadedSections)
	}
	if _, exists := reloadedSections["identity"]; !exists {
		t.Fatalf("runtime metadata omitted reloaded identity: %+v", reloadedSections)
	}
}

func TestConfigReloadAppliesFleetTemplateWithoutChangingCompose(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-fleet-reload", "sha256:spec")), "running")
	state := readStateForTest(t, root, "agent-fleet-reload")
	paths := executor.pathsForState(state)
	composeBefore, err := os.ReadFile(paths.ComposePath)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := soulfactory.FleetConfigSnapshot{
		Coordinate: "31953:operator:soulfactory-fleet-config/v1",
		EventID:    "fleet-revision-new",
		Author:     "operator",
		CreatedAt:  1715600000,
		Document: soulfactory.FleetConfigDocument{
			Schema: soulfactory.SoulFactoryFleetConfigSchema,
			Template: map[string]interface{}{
				"logging": map[string]interface{}{"level": "debug"},
				"agents":  map[string]interface{}{"defaults": map[string]interface{}{"model": map[string]interface{}{"primary": "fleet/model"}}},
			},
		},
	}
	reload := testInvocation(soulfactory.RuntimeMethodConfigReload, "agent-fleet-reload", "sha256:spec", map[string]interface{}{
		"schema":             soulfactory.SoulFactoryConfigReloadSchema,
		"target_fields":      []interface{}{"fleet_config"},
		"previous_spec_hash": "sha256:spec",
		"new_spec_hash":      "sha256:spec",
		"patch":              map[string]interface{}{"fleet_config": snapshot},
	})
	outcome := executor.Execute(t.Context(), reload)
	assertSuccess(t, outcome, "running")
	if outcome.Result["restart"] != false || !reflect.DeepEqual(outcome.Result["reloaded"], []string{"fleet_config"}) {
		t.Fatalf("fleet reload result = %+v", outcome.Result)
	}

	runtimeConfig, ok, err := readJSONFile[map[string]interface{}](paths.RuntimeConfig)
	if err != nil || !ok {
		t.Fatalf("read runtime config: ok=%v err=%v", ok, err)
	}
	logging, _ := runtimeConfig["logging"].(map[string]interface{})
	if logging["level"] != "debug" {
		t.Fatalf("fleet logging config = %#v", logging)
	}
	channels, _ := runtimeConfig["channels"].(map[string]interface{})
	nostrConfig, _ := channels["nostr"].(map[string]interface{})
	if nostrConfig["enabled"] != true || nostrConfig["defaultAccount"] != state.AccountID {
		t.Fatalf("wrapper-owned Nostr config = %#v", nostrConfig)
	}
	metadata, ok, err := readJSONFile[map[string]interface{}](paths.RuntimeMetadata)
	if err != nil || !ok {
		t.Fatalf("read runtime metadata: ok=%v err=%v", ok, err)
	}
	fleetMetadata, _ := metadata["fleetConfig"].(map[string]interface{})
	if fleetMetadata["eventId"] != snapshot.EventID {
		t.Fatalf("fleet metadata = %#v", fleetMetadata)
	}
	composeAfter, err := os.ReadFile(paths.ComposePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(composeAfter, composeBefore) {
		t.Fatal("fleet hot reload changed the compose specification")
	}
}

func TestMemoryReindexReturnsActionableStructuredResult(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-reindex", "sha256:spec")), "running")
	params, err := soulfactory.BuildMemoryReindexRuntimeParams(
		domain.SoulMemorySpec{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small"},
		soulfactory.MemoryReindexModeFull,
		"operator requested",
		"sha256:spec",
		"sha256:spec",
		"",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome := executor.Execute(t.Context(), testInvocation(soulfactory.RuntimeMethodMemoryReindex, "agent-reindex", "sha256:spec", params))
	assertSuccess(t, outcome, "running")
	action, ok := outcome.Result["action_required"].(map[string]interface{})
	if outcome.Result["accepted"] != true || outcome.Result["started"] != false || outcome.Result["mode"] != soulfactory.MemoryReindexModeFull || !ok {
		t.Fatalf("memory reindex result = %+v", outcome.Result)
	}
	if action["type"] != "runtime-native-memory-reindex" || action["agent_id"] != "agent-reindex" {
		t.Fatalf("memory reindex action = %+v", action)
	}
}

func TestUnsupportedMethodRejected(t *testing.T) {
	executor := newTestExecutor(t, t.TempDir(), true, nil)
	outcome := executor.Execute(t.Context(), testInvocation("soulfactory.unsupported", "agent-alice", "sha256:spec", map[string]interface{}{}))
	if outcome.Status != StatusRejected || outcome.Error == nil || outcome.Error.Code != ErrorUnsupportedMethod {
		t.Fatalf("unsupported method outcome = %+v", outcome)
	}
}

func TestNonDryRunUsesContainerizedOpenClawCommands(t *testing.T) {
	runner := &recordingRunner{}
	root := t.TempDir()
	executor := newTestExecutor(t, root, false, runner)
	outcome := executor.Execute(t.Context(), testProvisionInvocation("agent-alice", "sha256:spec"))
	assertSuccess(t, outcome, "running")
	for _, key := range []string{"deployment_id", "container_id", "image_digest", "config_revision", "workspace_path_identifier", "agent_id", "account_id"} {
		if strings.TrimSpace(stringValue(outcome.Result[key])) == "" {
			t.Fatalf("lineage field %s missing from %+v", key, outcome.Result)
		}
	}
	if len(runner.calls) != 5 {
		t.Fatalf("command count = %d, want bootstrap/runtime plugin inspections plus add/set-identity/bind: %+v", len(runner.calls), runner.calls)
	}
	container := readStateForTest(t, root, "agent-alice").Container
	for _, call := range runner.calls {
		joined := strings.Join(append([]string{call.name}, call.args...), " ")
		if strings.Contains(joined, "gateway run") || strings.Contains(joined, "gateway start") || strings.Contains(joined, "go run") || strings.Contains(joined, "npm start") {
			t.Fatalf("forbidden persistent bare-metal command emitted: %s", joined)
		}
		if call.name == "openclaw-test" && !containsArgSequence(call.args, "--container", container) {
			t.Fatalf("command did not target configured container: %+v", call)
		}
	}
	if runner.calls[0].name != "docker-test" || !containsArgSequence(runner.calls[0].args, "compose") || !containsArgSequence(runner.calls[0].args, "run") || !containsArgSequence(runner.calls[1].args, "plugins", "list") || !containsArgSequence(runner.calls[2].args, "agents", "add") || !containsArgSequence(runner.calls[2].args, "--model", "gpt-test") || !containsArgSequence(runner.calls[3].args, "agents", "set-identity") || !containsArgSequence(runner.calls[4].args, "agents", "bind", "--agent", "agent-alice", "--bind", "nostr:account-alice") {
		t.Fatalf("unexpected command sequence: %+v", runner.calls)
	}
}

func TestProvisionAppliesSecretFreeSignetContractToDedicatedRuntime(t *testing.T) {
	root := t.TempDir()
	clientKeyRef := filepath.Join(root, "signet", "agent-signet.nip46-client")
	if err := os.MkdirAll(filepath.Dir(clientKeyRef), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientKeyRef, []byte(strings.Repeat("1", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root: root, OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"}, Runner: runner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := testProvisionInvocation("agent-signet", "sha256:signet")
	contract := soulfactory.OpenClawSignetIdentityContract{
		Schema: soulfactory.OpenClawSignetIdentityContractSchema, AgentID: invocation.AgentID,
		ControllerPubkey: invocation.Envelope.Controller.Pubkey, RuntimePubkey: invocation.Envelope.Target.RuntimePubkey,
		ManagedPubkey: strings.Repeat("a", 64), ProvisionerPubkey: strings.Repeat("b", 64),
		ClientPubkey: strings.Repeat("c", 64), BunkerPubkey: strings.Repeat("d", 64),
		BunkerURL:    "bunker://" + strings.Repeat("d", 64) + "?relay=wss%3A%2F%2Frelay.example",
		ClientKeyRef: clientKeyRef, Relays: []string{"wss://relay.example"},
	}
	invocation.Params["bahia"] = map[string]interface{}{"signet_identity": contract}
	outcome := executor.Execute(t.Context(), invocation)
	assertSuccess(t, outcome, "running")
	if len(orchestrator.reconciles) != 1 || orchestrator.reconciles[0].SecretFiles["nip46_client"] != clientKeyRef {
		t.Fatalf("Signet client key was not mounted into the owned runtime: %+v", orchestrator.reconciles)
	}
	if len(runner.calls) != 6 || !containsArgSequence(runner.calls[2].args, "--container", orchestrator.reconciles[0].ContainerName, "config", "set", "--batch-json") {
		t.Fatalf("command sequence = %+v, want dedicated-container NIP-46 config before add/set-identity/bind", runner.calls)
	}
	joined := strings.Join(runner.calls[2].args, " ")
	if strings.Contains(joined, "secret=") || strings.Contains(joined, clientKeyRef) || !strings.Contains(joined, "/run/secrets/nip46_client") || !strings.Contains(joined, contract.BunkerURL) {
		t.Fatalf("unsafe or incomplete OpenClaw NIP-46 config command: %s", joined)
	}
}

func TestProvisionRejectsMismatchedOrSecretBearingSignetContractBeforeCommands(t *testing.T) {
	for _, mutate := range []func(*soulfactory.OpenClawSignetIdentityContract){
		func(contract *soulfactory.OpenClawSignetIdentityContract) {
			contract.RuntimePubkey = strings.Repeat("f", 64)
		},
		func(contract *soulfactory.OpenClawSignetIdentityContract) {
			contract.BunkerURL += "&secret=must-not-pass"
		},
	} {
		runner := &recordingRunner{}
		executor := newTestExecutor(t, t.TempDir(), false, runner)
		invocation := testProvisionInvocation("agent-signet-reject", "sha256:signet")
		contract := soulfactory.OpenClawSignetIdentityContract{
			Schema: soulfactory.OpenClawSignetIdentityContractSchema, AgentID: invocation.AgentID,
			ControllerPubkey: invocation.Envelope.Controller.Pubkey, RuntimePubkey: invocation.Envelope.Target.RuntimePubkey,
			ManagedPubkey: strings.Repeat("a", 64), ClientPubkey: strings.Repeat("c", 64),
			BunkerURL:    "bunker://" + strings.Repeat("d", 64) + "?relay=wss%3A%2F%2Frelay.example",
			ClientKeyRef: "/run/openclaw/signet/client", Relays: []string{"wss://relay.example"},
		}
		mutate(&contract)
		invocation.Params["bahia"] = map[string]interface{}{"signet_identity": contract}
		outcome := executor.Execute(t.Context(), invocation)
		if outcome.Status != StatusRejected || len(runner.calls) != 0 {
			t.Fatalf("outcome=%+v calls=%+v, want pre-command rejection", outcome, runner.calls)
		}
	}
}

func TestNonDryRunExactReplayAdoptsWithoutRepeatingAgentMutation(t *testing.T) {
	runner := &recordingRunner{}
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root: t.TempDir(), OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"}, Runner: runner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := testProvisionInvocation("agent-replay", "sha256:spec")
	assertSuccess(t, executor.Execute(t.Context(), invocation), "running")
	mutationCalls := countAgentMutationCalls(runner.calls)
	assertSuccess(t, executor.Execute(t.Context(), invocation), "running")
	if countAgentMutationCalls(runner.calls) != mutationCalls || len(orchestrator.reconciles) != 2 {
		t.Fatalf("replay repeated mutations or skipped adoption: commands=%+v reconciles=%d", runner.calls, len(orchestrator.reconciles))
	}
}

func TestNonDryRunRejectsSharedExistingContainerWithoutMutation(t *testing.T) {
	runner := &recordingRunner{}
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root: t.TempDir(), RuntimeMode: RuntimeModeExistingContainer, Container: "marjam-gateway",
		ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"}, Runner: runner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome := executor.Execute(t.Context(), testProvisionInvocation("external-soul", "sha256:spec"))
	if outcome.Status != StatusRejected || outcome.Error == nil || outcome.Error.Code != ErrorUnsupportedMethod {
		t.Fatalf("shared runtime outcome = %+v", outcome)
	}
	if len(runner.calls) != 0 || len(orchestrator.reconciles) != 0 {
		t.Fatalf("shared incumbent was mutated: runner=%+v reconciles=%+v", runner.calls, orchestrator.reconciles)
	}
}

func TestProvisionCommandFailureDeletesOnlyOwnedPartialDeployment(t *testing.T) {
	runner := &recordingRunner{err: CommandExecutionError{Message: "agent creation failed"}, errAt: 3}
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root: t.TempDir(), OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"}, Runner: runner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome := executor.Execute(t.Context(), testProvisionInvocation("agent-partial", "sha256:spec"))
	if outcome.Status != StatusFailed || len(orchestrator.reconciles) != 1 || len(orchestrator.deletes) != 1 {
		t.Fatalf("partial-create cleanup outcome=%+v reconciles=%d deletes=%d", outcome, len(orchestrator.reconciles), len(orchestrator.deletes))
	}
	if orchestrator.deletes[0].DeploymentID != orchestrator.reconciles[0].DeploymentID {
		t.Fatalf("cleanup ownership changed: create=%+v delete=%+v", orchestrator.reconciles[0], orchestrator.deletes[0])
	}
}

func TestProvisionBootstrapsPluginBeforeGatewayAndVerifiesBeforeAgentMutation(t *testing.T) {
	runner := &recordingRunner{outputs: [][]byte{
		[]byte(`{"plugins":[]}`),
		[]byte(`{"installed":true}`),
		[]byte(`{"plugins":[{"id":"nostr","status":"loaded","enabled":true}]}`),
	}}
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root: t.TempDir(), OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"}, Runner: runner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatalf("New executor: %v", err)
	}
	outcome := executor.Execute(t.Context(), testProvisionInvocation("agent-plugin", "sha256:plugin"))
	assertSuccess(t, outcome, "running")
	if len(runner.calls) != 6 || runner.calls[0].name != "docker-test" || !containsArgSequence(runner.calls[0].args, "compose") || !containsArgSequence(runner.calls[0].args, "run") || !containsArgSequence(runner.calls[0].args, "plugins", "list", "--json") || !containsArgSequence(runner.calls[1].args, "plugins", "install", "npm:openclaw-nostr@1.0.0") || !containsArgSequence(runner.calls[2].args, "plugins", "list", "--json") {
		t.Fatalf("unexpected plugin bootstrap calls: %+v", runner.calls)
	}
	if len(orchestrator.reconciles) != 1 {
		t.Fatalf("runtime reconciles = %d, want one gateway create after plugin bootstrap", len(orchestrator.reconciles))
	}
}

func TestProvisionContinuesWhenRequiredRuntimePluginIsLoaded(t *testing.T) {
	runner := &recordingRunner{outputs: [][]byte{
		[]byte(`{"plugins":[{"id":"nostr","status":"loaded","enabled":true}]}`),
	}}
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root: t.TempDir(), OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"}, Runner: runner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatalf("New executor: %v", err)
	}
	assertSuccess(t, executor.Execute(t.Context(), testProvisionInvocation("agent-plugin", "sha256:plugin")), "running")
	if len(runner.calls) != 5 {
		t.Fatalf("calls = %d, want bootstrap/runtime plugin checks plus add/set-identity/bind: %+v", len(runner.calls), runner.calls)
	}
	for _, call := range runner.calls {
		if call.name == "openclaw-test" && !containsArgSequence(call.args, "--container", orchestrator.reconciles[0].ContainerName) {
			t.Fatalf("command did not target configured container: %+v", call)
		}
	}
}

func TestRequiredPluginConfigRejectsAmbiguousInstallSource(t *testing.T) {
	_, err := New(Config{RequiredPlugins: []string{"npm:openclaw-nostr"}})
	if err == nil || !strings.Contains(err.Error(), "plugin-id=install-source") {
		t.Fatalf("New error = %v, want explicit plugin mapping error", err)
	}
}

func TestRuntimeModeDefaultsToDedicatedCompose(t *testing.T) {
	executor, err := FromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if executor.config.RuntimeMode != RuntimeModePerAgentCompose {
		t.Fatalf("runtime mode = %q", executor.config.RuntimeMode)
	}
}

func TestFleetConfigMergePrecedenceAndDefaults(t *testing.T) {
	root := t.TempDir()
	secretPath := filepath.Join(root, "nip46.secret")
	if err := os.WriteFile(secretPath, []byte("file-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor, err := New(Config{
		Root: root, OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		DefaultModel: "env/model", DefaultBindings: []string{"env:binding"},
		RequiredPlugins: []string{"nostr=npm:env-nostr@1.0.0"},
		DryRun:          true, Now: func() time.Time { return time.Unix(1715700005, 0).UTC() },
		Runner: &recordingRunner{}, Orchestrator: &recordingOrchestrator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := testProvisionInvocation("agent-alice", "sha256:fleet")
	invocation.Params["runtime"].(map[string]interface{})["model"] = "agent/model"
	invocation.Params["runtime"].(map[string]interface{})["nostr"] = map[string]interface{}{
		"dmPolicy": "contacts",
	}
	invocation.Params["runtime"].(map[string]interface{})["secret_files"] = map[string]interface{}{
		"nip46_client": secretPath,
	}
	invocation.Params["relay_policy"].(map[string]interface{})["control"] = []interface{}{}
	invocation.Params["memory"] = map[string]interface{}{
		"embedding_provider": "openai",
		"embedding_model":    "text-embedding-3-small",
		"strategy":           "session-aware",
		"auto_index":         true,
		"retention_days":     45,
		"search": map[string]interface{}{
			"top_k": 8, "score_threshold": 0.77, "rerank": true, "rerank_model": "rerank-v3.5",
		},
	}
	invocation.Params["voice"] = map[string]interface{}{
		"provider": "openai-tts", "persona_id": "alloy", "auto_mode": "tagged",
		"providers": map[string]interface{}{
			"openai-tts": map[string]interface{}{"model": "gpt-4o-mini-tts", "voice": "alloy"},
		},
	}
	invocation.Params["fleet_config"] = soulfactory.FleetConfigSnapshot{
		Coordinate: "31953:operator:soulfactory-fleet-config/v1",
		EventID:    "fleet-event", Author: "operator", CreatedAt: 1715600000,
		Document: soulfactory.FleetConfigDocument{
			Schema: soulfactory.SoulFactoryFleetConfigSchema,
			Defaults: soulfactory.FleetConfigDefaults{
				Model: "fleet/model", Bindings: []string{"fleet:binding"},
				RequiredPlugins: []string{"nostr=npm:fleet-nostr@2.0.0"},
			},
			Template: map[string]interface{}{
				"logging": map[string]interface{}{"level": "info"},
				"agents": map[string]interface{}{"defaults": map[string]interface{}{
					"model":          map[string]interface{}{"primary": "template/model"},
					"workspace":      "/fleet/workspace",
					"contextPruning": map[string]interface{}{"mode": "cache-ttl"},
					"memorySearch": map[string]interface{}{
						"provider": "voyage", "model": "fleet-embed", "fallback": "local",
						"query": map[string]interface{}{"maxResults": 3, "customFleetField": true},
					},
				}},
				"messages": map[string]interface{}{
					"ackReaction": "✅",
					"tts": map[string]interface{}{
						"mode": "final", "provider": "elevenlabs",
						"providers": map[string]interface{}{"elevenlabs": map[string]interface{}{"baseUrl": "https://voice.example"}},
					},
				},
				"channels": map[string]interface{}{"nostr": map[string]interface{}{
					"enabled": false, "defaultAccount": "fleet-account", "dmPolicy": "allowlist",
					"relays": []interface{}{"wss://fleet.example"}, "privateKey": "${NOSTR_PRIVATE_KEY}",
					"nip46Secret": "${FLEET_NIP46_SECRET}",
				}},
				"gateway": map[string]interface{}{
					"mode": "remote", "bind": "public", "port": 9999,
					"auth": map[string]interface{}{"mode": "token", "token": "${GATEWAY_AUTH_TOKEN}"},
				},
				"bindings": []interface{}{map[string]interface{}{
					"agentId": "main", "match": map[string]interface{}{"channel": "nostr"},
				}},
				"plugins": map[string]interface{}{
					"allow":   []interface{}{"nostr", "fleet-tool"},
					"entries": map[string]interface{}{"fleet-tool": map[string]interface{}{"enabled": false, "mode": "audit"}},
				},
			},
		},
	}

	spec, err := executor.runtimeSpec(invocation, executor.paths("agent-alice"), nil)
	if err != nil {
		t.Fatalf("runtimeSpec() error = %v", err)
	}
	if spec.Model != "agent/model" || !reflect.DeepEqual(spec.DefaultBindings, []string{"fleet:binding"}) ||
		!reflect.DeepEqual(spec.PluginRequirements, []string{"nostr=npm:fleet-nostr@2.0.0"}) {
		t.Fatalf("fleet defaults = model %q bindings %#v plugins %#v", spec.Model, spec.DefaultBindings, spec.PluginRequirements)
	}

	outcome := executor.Execute(t.Context(), invocation)
	assertSuccess(t, outcome, "running")
	state := readStateForTest(t, root, "agent-alice")
	paths := executor.pathsForState(state)
	var config map[string]interface{}
	data, err := os.ReadFile(paths.RuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["logging"].(map[string]interface{})["level"] != "info" {
		t.Fatalf("fleet logging was not preserved: %#v", config)
	}
	defaults := config["agents"].(map[string]interface{})["defaults"].(map[string]interface{})
	if defaults["workspace"] != containerWorkspace ||
		defaults["model"].(map[string]interface{})["primary"] != "agent/model" ||
		defaults["contextPruning"].(map[string]interface{})["mode"] != "cache-ttl" {
		t.Fatalf("agent/default merge precedence = %#v", defaults)
	}
	memorySearch := defaults["memorySearch"].(map[string]interface{})
	query := memorySearch["query"].(map[string]interface{})
	if memorySearch["provider"] != "openai" || memorySearch["model"] != "text-embedding-3-small" ||
		memorySearch["fallback"] != "local" || query["maxResults"].(float64) != 8 ||
		query["minScore"].(float64) != 0.77 || query["customFleetField"] != true ||
		query["hybrid"].(map[string]interface{})["mmr"].(map[string]interface{})["enabled"] != true {
		t.Fatalf("memory merge precedence = %#v", memorySearch)
	}
	messages := config["messages"].(map[string]interface{})
	tts := messages["tts"].(map[string]interface{})
	if messages["ackReaction"] != "✅" || tts["mode"] != "final" || tts["provider"] != "openai-tts" ||
		tts["auto"] != "tagged" || tts["persona"] != "alloy" {
		t.Fatalf("TTS merge precedence = %#v", messages)
	}
	ttsProviders := tts["providers"].(map[string]interface{})
	if ttsProviders["elevenlabs"].(map[string]interface{})["baseUrl"] != "https://voice.example" ||
		ttsProviders["openai-tts"].(map[string]interface{})["model"] != "gpt-4o-mini-tts" {
		t.Fatalf("TTS provider merge = %#v", ttsProviders)
	}
	if strings.Contains(string(data), "retention_days") || strings.Contains(string(data), "rerank_model") {
		t.Fatalf("portable-only memory fields leaked into OpenClaw config: %s", data)
	}
	metadata, ok, err := readJSONFile[map[string]interface{}](paths.RuntimeMetadata)
	if err != nil || !ok {
		t.Fatalf("read runtime metadata: ok=%v err=%v", ok, err)
	}
	memoryMetadata := metadata["soulfactory"].(map[string]interface{})["memory_config"].(map[string]interface{})
	if memoryMetadata["retention_days"].(float64) != 45 ||
		memoryMetadata["search"].(map[string]interface{})["rerank_model"] != "rerank-v3.5" {
		t.Fatalf("portable memory metadata = %#v", memoryMetadata)
	}
	nostrConfig := config["channels"].(map[string]interface{})["nostr"].(map[string]interface{})
	if nostrConfig["enabled"] != true || nostrConfig["defaultAccount"] != "account-alice" ||
		nostrConfig["dmPolicy"] != "contacts" || !reflect.DeepEqual(stringSlice(nostrConfig["relays"]), []string{"wss://fleet.example"}) {
		t.Fatalf("nostr merge precedence = %#v", nostrConfig)
	}
	if _, exists := nostrConfig["privateKey"]; exists {
		t.Fatalf("fleet private key survived wrapper secret ownership: %#v", nostrConfig)
	}
	secretRef := nostrConfig["nip46Secret"].(map[string]interface{})
	if secretRef["source"] != "file" || secretRef["path"] != "/run/secrets/nip46_client" {
		t.Fatalf("wrapper secret precedence = %#v", secretRef)
	}
	bindings := config["bindings"].([]interface{})
	binding := bindings[0].(map[string]interface{})
	match := binding["match"].(map[string]interface{})
	if binding["agentId"] != "agent-alice" || match["accountId"] != "account-alice" {
		t.Fatalf("wrapper binding precedence = %#v", binding)
	}
	gateway := config["gateway"].(map[string]interface{})
	if gateway["mode"] != "local" || gateway["bind"] != "lan" || gateway["port"].(float64) != 18789 {
		t.Fatalf("wrapper gateway settings did not win: %#v", gateway)
	}
	if _, exists := gateway["auth"]; exists {
		t.Fatalf("fleet gateway secret survived without a wrapper secret file: %#v", gateway)
	}
	entries := config["plugins"].(map[string]interface{})["entries"].(map[string]interface{})
	fleetTool := entries["fleet-tool"].(map[string]interface{})
	if fleetTool["mode"] != "audit" || fleetTool["enabled"] != false {
		t.Fatalf("fleet disabled plugin entry was not preserved: %#v", entries)
	}
	if entries["nostr"].(map[string]interface{})["enabled"] != true {
		t.Fatalf("wrapper-required plugin was not enabled: %#v", entries)
	}
}

func TestProvisionRejectsInvalidDraftMemoryBeforeRuntimeMutation(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{}
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root: root, OpenClawBin: "openclaw-test", DockerBin: "docker-test",
		RuntimeMode: RuntimeModePerAgentCompose, ImageDigest: testImageDigest, SourceCommit: testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"},
		Runner:          runner, Orchestrator: orchestrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := testProvisionInvocation("agent-invalid-memory", "sha256:invalid-memory")
	invocation.Params["memory"] = map[string]interface{}{
		"embedding_provider": "unsupported-provider",
		"strategy":           "session-aware",
		"search":             map[string]interface{}{"top_k": 101},
	}

	outcome := executor.Execute(t.Context(), invocation)
	if outcome.Status != StatusFailed || outcome.Error == nil ||
		!strings.Contains(outcome.Error.Message, "map draft memory config") {
		t.Fatalf("invalid memory outcome = %+v", outcome)
	}
	if len(runner.calls) != 0 || len(orchestrator.reconciles) != 0 {
		t.Fatalf("invalid memory mutated runtime: calls=%#v reconciles=%#v", runner.calls, orchestrator.reconciles)
	}
}

func newTestExecutor(t *testing.T, root string, dryRun bool, runner CommandRunner) *Executor {
	t.Helper()
	orchestrator := &recordingOrchestrator{}
	executor, err := New(Config{
		Root:            root,
		OpenClawBin:     "openclaw-test",
		DockerBin:       "docker-test",
		RuntimeMode:     RuntimeModePerAgentCompose,
		ImageDigest:     testImageDigest,
		SourceCommit:    testSourceCommit,
		RequiredPlugins: []string{"nostr=npm:openclaw-nostr@1.0.0"},
		DryRun:          dryRun,
		Now:             func() time.Time { return time.Unix(1715700005, 0).UTC() },
		Runner:          runner,
		Orchestrator:    orchestrator,
	})
	if err != nil {
		t.Fatalf("New executor: %v", err)
	}
	return executor
}

func testProvisionInvocation(agentID, specHash string) soulfactory.OpenClawControlInvocation {
	return testInvocation(soulfactory.RuntimeMethodProvision, agentID, specHash, provisionParams())
}

func testInvocation(method, agentID, specHash string, params map[string]interface{}) soulfactory.OpenClawControlInvocation {
	return soulfactory.OpenClawControlInvocation{
		Envelope: soulfactory.RuntimeControlEnvelope{
			Schema:         "soulfactory-runtime-control/v1",
			Method:         method,
			IdempotencyKey: "idem-" + method + "-" + agentID,
			RequestedAt:    1715700000,
			Operator:       soulfactory.RuntimeOperatorRef{Pubkey: "operator-pubkey", RequestEvent: "operator-request"},
			Controller:     soulfactory.RuntimeControllerRef{Pubkey: "controller-pubkey"},
			Target:         soulfactory.RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey", AgentID: agentID},
			Soul:           soulfactory.RuntimeSoulRef{ID: "soul-" + agentID, SpecHash: specHash},
			Params:         params,
		},
		Method:   method,
		AgentID:  agentID,
		SoulID:   "soul-" + agentID,
		SpecHash: specHash,
		Params:   params,
	}
}

func provisionParams() map[string]interface{} {
	return map[string]interface{}{
		"identity":     map[string]interface{}{"name": "Alice", "purpose": "Operate audited OpenClaw tasks"},
		"runtime":      map[string]interface{}{"target": "openclaw", "model": "gpt-test", "account_id": "account-alice"},
		"permissions":  map[string]interface{}{"allowed_kinds": []interface{}{domain.KindAgentSoul, domain.KindRuntimeControlResult}},
		"relay_policy": map[string]interface{}{"control": []interface{}{"wss://relay.example"}},
		"workspace":    map[string]interface{}{"mode": "generated"},
		"assets":       map[string]interface{}{},
		"persona":      map[string]interface{}{"traits": []interface{}{"careful"}},
	}
}

func assertSuccess(t *testing.T, outcome *soulfactory.OpenClawControlOutcome, state string) {
	t.Helper()
	if outcome == nil || outcome.Status != StatusSuccess || outcome.Error != nil {
		t.Fatalf("outcome = %+v, want success", outcome)
	}
	if outcome.Result["state"] != state {
		t.Fatalf("state = %v, want %s in outcome %+v", outcome.Result["state"], state, outcome)
	}
}

func readStateForTest(t *testing.T, root, agentID string) State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "agents", agentID, "state.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return state
}

func containsArgSequence(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		matched := true
		for j := range seq {
			if args[i+j] != seq[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func countAgentMutationCalls(calls []commandCall) int {
	count := 0
	for _, call := range calls {
		if containsArgSequence(call.args, "agents", "add") || containsArgSequence(call.args, "agents", "set-identity") || containsArgSequence(call.args, "agents", "bind") {
			count++
		}
	}
	return count
}

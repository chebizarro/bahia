package openclawcontrol

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

type recordingRunner struct {
	calls []commandCall
	err   error
}

type commandCall struct {
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string{}, args...)})
	if r.err != nil {
		return nil, r.err
	}
	return []byte(`{"ok":true}`), nil
}

func TestProvisionDryRunCreatesWorkspaceStateAndReplaysIdempotently(t *testing.T) {
	root := t.TempDir()
	executor := newTestExecutor(t, root, true, nil)
	invocation := testProvisionInvocation("agent-alice", "sha256:spec")

	outcome := executor.Execute(t.Context(), invocation)
	assertSuccess(t, outcome, "running")
	workspace := filepath.Join(root, "agents", "agent-alice", "workspace")
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
	personaPath := filepath.Join(root, "agents", "agent-alice", "workspace", ".openclaw", "soulfactory-persona.json")
	data, err := os.ReadFile(personaPath)
	if err != nil {
		t.Fatalf("read persona state: %v", err)
	}
	if !strings.Contains(string(data), "You are SteadyBot.") || !strings.Contains(string(data), "system_prompt_override") {
		t.Fatalf("persona state did not contain mapped prompt:\n%s", data)
	}
	agents, err := os.ReadFile(filepath.Join(root, "agents", "agent-alice", "workspace", "AGENTS.md"))
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
	if _, err := os.Stat(filepath.Join(root, "agents", "agent-keep", "workspace", "SOUL.md")); err != nil {
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
	if _, err := os.Stat(filepath.Join(root, "agents", "agent-delete", "workspace")); !os.IsNotExist(err) {
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
	revoker, err := New(Config{
		Root:        root,
		OpenClawBin: "openclaw-test",
		RuntimeMode: RuntimeModeExistingContainer,
		DryRun:      false,
		Now:         func() time.Time { return time.Unix(1715700005, 0).UTC() },
		Runner:      revokeRunner,
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
	if !containsArgSequence(revokeRunner.calls[0].args, "--container", "openclaw-gateway") || !containsArgSequence(revokeRunner.calls[0].args, "agents", "unbind") {
		t.Fatalf("revoke command did not use persisted container unbind: %+v", revokeRunner.calls[0])
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

func TestUnsupportedMethodRejected(t *testing.T) {
	executor := newTestExecutor(t, t.TempDir(), true, nil)
	outcome := executor.Execute(t.Context(), testInvocation(soulfactory.RuntimeMethodUpdate, "agent-alice", "sha256:spec", map[string]interface{}{}))
	if outcome.Status != StatusRejected || outcome.Error == nil || outcome.Error.Code != ErrorUnsupportedMethod {
		t.Fatalf("unsupported method outcome = %+v", outcome)
	}
}

func TestNonDryRunUsesContainerizedOpenClawCommands(t *testing.T) {
	runner := &recordingRunner{}
	executor := newTestExecutor(t, t.TempDir(), false, runner)
	outcome := executor.Execute(t.Context(), testProvisionInvocation("agent-alice", "sha256:spec"))
	assertSuccess(t, outcome, "running")
	if len(runner.calls) != 3 {
		t.Fatalf("command count = %d, want add/set-identity/bind: %+v", len(runner.calls), runner.calls)
	}
	for _, call := range runner.calls {
		joined := strings.Join(append([]string{call.name}, call.args...), " ")
		if strings.Contains(joined, "gateway run") || strings.Contains(joined, "gateway start") || strings.Contains(joined, "go run") || strings.Contains(joined, "npm start") {
			t.Fatalf("forbidden persistent bare-metal command emitted: %s", joined)
		}
		if !containsArgSequence(call.args, "--container", "openclaw-gateway") {
			t.Fatalf("command did not target configured container: %+v", call)
		}
	}
	if !containsArgSequence(runner.calls[0].args, "agents", "add") || !containsArgSequence(runner.calls[1].args, "agents", "set-identity") || !containsArgSequence(runner.calls[2].args, "agents", "bind") {
		t.Fatalf("unexpected command sequence: %+v", runner.calls)
	}
}

func newTestExecutor(t *testing.T, root string, dryRun bool, runner CommandRunner) *Executor {
	t.Helper()
	executor, err := New(Config{
		Root:            root,
		OpenClawBin:     "openclaw-test",
		RuntimeMode:     RuntimeModeExistingContainer,
		Container:       "openclaw-gateway",
		DefaultBindings: []string{"nostr:dm"},
		DryRun:          dryRun,
		Now:             func() time.Time { return time.Unix(1715700005, 0).UTC() },
		Runner:          runner,
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
		"runtime":      map[string]interface{}{"target": "openclaw", "model": "gpt-test"},
		"permissions":  map[string]interface{}{"allowed_kinds": []interface{}{30317, 38386}},
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

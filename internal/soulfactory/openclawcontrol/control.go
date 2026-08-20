package openclawcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

const (
	StatusSuccess  = "success"
	StatusRejected = "rejected"
	StatusFailed   = "failed"

	ErrorUnsupportedMethod  = "unsupported_method"
	ErrorMissingRequired    = "missing_required_param"
	ErrorDuplicateConflict  = "duplicate_conflict"
	ErrorSpecHashMismatch   = "spec_hash_mismatch"
	ErrorRuntimeUnavailable = "runtime_unavailable"
	ErrorExecutionFailed    = "execution_failed"

	RuntimeModeExistingContainer = "existing-container"
	RuntimeModePerAgentCompose   = "per-agent-compose"
)

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,126}$`)

type Config struct {
	Root            string
	OpenClawBin     string
	RuntimeMode     string
	Container       string
	DefaultModel    string
	DefaultBindings []string
	RequiredPlugins []string
	DryRun          bool
	Now             func() time.Time
	Runner          CommandRunner
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, RuntimeUnavailableError{Message: fmt.Sprintf("OpenClaw CLI %q was not found", name)}
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = strings.TrimSpace(string(exitErr.Stderr))
			}
			if message == "" {
				message = exitErr.Error()
			}
			return out, CommandExecutionError{Message: message}
		}
		return out, CommandExecutionError{Message: err.Error()}
	}
	return out, nil
}

type RuntimeUnavailableError struct{ Message string }

func (e RuntimeUnavailableError) Error() string { return e.Message }

type CommandExecutionError struct{ Message string }

func (e CommandExecutionError) Error() string { return e.Message }

type State struct {
	AgentID          string   `json:"agent_id"`
	SoulID           string   `json:"soul_id"`
	SpecHash         string   `json:"spec_hash"`
	State            string   `json:"state"`
	RuntimeBinding   string   `json:"runtime_binding"`
	Workspace        string   `json:"workspace"`
	AgentDir         string   `json:"agent_dir"`
	RuntimeMode      string   `json:"runtime_mode"`
	Container        string   `json:"container,omitempty"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
	LastMethod       string   `json:"last_method"`
	LastReason       string   `json:"last_reason,omitempty"`
	Warnings         []string `json:"warnings"`
	OperatorRequest  string   `json:"operator_request,omitempty"`
	ControllerPubkey string   `json:"controller_pubkey,omitempty"`
	RuntimePubkey    string   `json:"runtime_pubkey,omitempty"`
}

type Executor struct {
	config Config
}

func New(config Config) (*Executor, error) {
	resolved, err := resolveConfig(config)
	if err != nil {
		return nil, err
	}
	return &Executor{config: resolved}, nil
}

func FromEnv(getenv func(string) string) (*Executor, error) {
	config, err := ConfigFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return New(config)
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Config{
		Root:            strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_ROOT")),
		OpenClawBin:     strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_OPENCLAW_BIN")),
		RuntimeMode:     strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_RUNTIME_MODE")),
		Container:       strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_CONTAINER")),
		DefaultModel:    strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_DEFAULT_MODEL")),
		DefaultBindings: splitCSV(getenv("OPENCLAW_SOULFACTORY_DEFAULT_BINDINGS")),
		RequiredPlugins: splitCSV(getenv("OPENCLAW_SOULFACTORY_REQUIRED_PLUGINS")),
		DryRun:          truthy(getenv("OPENCLAW_SOULFACTORY_DRY_RUN")),
	}, nil
}

func Execute(ctx context.Context, config Config, invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	executor, err := New(config)
	if err != nil {
		return rejected(ErrorMissingRequired, err.Error(), false, nil)
	}
	return executor.Execute(ctx, invocation)
}

func (e *Executor) Execute(ctx context.Context, invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	invocation.Method = strings.TrimSpace(firstNonEmpty(invocation.Method, invocation.Envelope.Method))
	invocation.AgentID = strings.TrimSpace(firstNonEmpty(invocation.AgentID, invocation.Envelope.Target.AgentID))
	invocation.SoulID = strings.TrimSpace(firstNonEmpty(invocation.SoulID, invocation.Envelope.Soul.ID, invocation.AgentID))
	invocation.SpecHash = strings.TrimSpace(firstNonEmpty(invocation.SpecHash, invocation.Envelope.Soul.SpecHash))
	if invocation.Params == nil {
		invocation.Params = map[string]interface{}{}
	}
	if err := validateInvocationIdentity(invocation); err != nil {
		return rejected(ErrorMissingRequired, err.Error(), false, nil)
	}
	if containsInlinePrivateSecret(invocation.Params) {
		return rejected(ErrorMissingRequired, "resolved params must reference secrets without inline private key material", false, nil)
	}

	switch invocation.Method {
	case soulfactory.RuntimeMethodProvision:
		return e.provision(ctx, invocation)
	case soulfactory.RuntimeMethodUpdate:
		return e.update(ctx, invocation)
	case soulfactory.RuntimeMethodPersonaUpdate:
		return e.personaUpdate(invocation)
	case soulfactory.RuntimeMethodRevoke:
		return e.revoke(ctx, invocation)
	default:
		return rejected(ErrorUnsupportedMethod, "SoulFactory method is not supported by openclaw-soulfactory-control", false, map[string]interface{}{"method": invocation.Method})
	}
}

func (e *Executor) update(ctx context.Context, invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	paths := e.paths(invocation.AgentID)
	state, ok, err := readJSONFile[State](paths.State)
	if err != nil {
		return failed(ErrorExecutionFailed, "read OpenClaw agent state: "+err.Error(), true, nil)
	}
	if !ok || state.State == "revoked" {
		return rejected(ErrorMissingRequired, "update requires existing non-revoked OpenClaw agent state", false, nil)
	}
	if replay, replayed := e.replayOutcome(invocation, paths, state); replayed {
		return replay
	}

	previousSpecHash, _ := invocation.Params["previous_spec_hash"].(string)
	newSpecHash, _ := invocation.Params["new_spec_hash"].(string)
	previousSpecHash = strings.TrimSpace(previousSpecHash)
	newSpecHash = strings.TrimSpace(newSpecHash)
	if previousSpecHash == "" || newSpecHash == "" {
		return rejected(ErrorMissingRequired, "update requires previous_spec_hash and new_spec_hash", false, nil)
	}
	if previousSpecHash != state.SpecHash {
		return rejected(ErrorSpecHashMismatch, "update previous_spec_hash does not match local state", false, map[string]interface{}{
			"existing_spec_hash": state.SpecHash,
			"previous_spec_hash": previousSpecHash,
		})
	}
	if strings.TrimSpace(invocation.SpecHash) != newSpecHash {
		return rejected(ErrorSpecHashMismatch, "update new_spec_hash does not match the requested soul spec_hash", false, map[string]interface{}{
			"requested_spec_hash": invocation.SpecHash,
			"new_spec_hash":       newSpecHash,
		})
	}
	updateMode, _ := invocation.Params["update_mode"].(string)
	updateMode = strings.TrimSpace(updateMode)
	if updateMode != "merge" && updateMode != "replace" {
		return rejected(ErrorMissingRequired, "update_mode must be merge or replace", false, nil)
	}
	provenance, _, err := readJSONFile[map[string]interface{}](paths.Provenance)
	if err != nil {
		return failed(ErrorExecutionFailed, "read provenance: "+err.Error(), true, nil)
	}
	if provenance == nil {
		provenance = map[string]interface{}{}
	}
	resolvedSpec, updateError := resolveUpdateSpec(invocation.Params, updateMode, provenance)
	if updateError != nil {
		return updateError
	}
	identity, ok := resolvedSpec["identity"].(map[string]interface{})
	if !ok {
		return rejected(ErrorMissingRequired, "OpenClaw update resolved_spec requires identity", false, nil)
	}
	persona, hasPersona := resolvedSpec["persona"]
	updatedInvocation := invocation
	updatedInvocation.SpecHash = newSpecHash
	if err := atomicWriteFile(paths.IdentityFile, []byte(renderIdentity(updatedInvocation, identity)), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write updated IDENTITY.md: "+err.Error(), true, nil)
	}
	if err := atomicWriteFile(paths.SoulFile, []byte(renderSoul(updatedInvocation, identity, persona, hasPersona)), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write updated SOUL.md: "+err.Error(), true, nil)
	}
	if err := atomicWriteFile(paths.AgentsFile, []byte(renderAgents(updatedInvocation, "")), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write updated AGENTS.md: "+err.Error(), true, nil)
	}
	provenance["spec_hash"] = newSpecHash
	provenance["params"] = resolvedSpec
	provenance["last_update"] = map[string]interface{}{
		"method":             invocation.Method,
		"previous_spec_hash": previousSpecHash,
		"new_spec_hash":      newSpecHash,
		"update_mode":        updateMode,
		"resolved_spec":      resolvedSpec,
	}
	if err := atomicWriteJSON(paths.Provenance, provenance, 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write provenance: "+err.Error(), true, nil)
	}
	if !e.config.DryRun {
		if outcome := e.runOpenClaw(ctx, e.containerArgs("agents", "set-identity", "--agent", invocation.AgentID, "--identity-file", paths.IdentityFile, "--json")...); outcome != nil {
			state.State = "failed"
			state.LastReason = errorMessage(outcome)
			state.UpdatedAt = e.config.Now().Unix()
			state.LastMethod = invocation.Method
			return e.persistFailure(invocation, outcome, state, paths)
		}
	}
	state.SpecHash = newSpecHash
	state.State = "running"
	state.UpdatedAt = e.config.Now().Unix()
	state.LastMethod = invocation.Method
	state.LastReason = ""
	outcome := success(e.resultFromState(state, state.UpdatedAt))
	if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	return outcome
}

func resolveUpdateSpec(params map[string]interface{}, updateMode string, provenance map[string]interface{}) (map[string]interface{}, *soulfactory.OpenClawControlOutcome) {
	if updateMode == "replace" {
		resolvedSpec, ok := params["resolved_spec"].(map[string]interface{})
		if !ok {
			return nil, rejected(ErrorMissingRequired, "replace update requires resolved_spec", false, nil)
		}
		return cloneObject(resolvedSpec), nil
	}
	patch, ok := params["patch"].(map[string]interface{})
	if !ok {
		return nil, rejected(ErrorMissingRequired, "merge update requires patch", false, nil)
	}
	base, ok := provenance["params"].(map[string]interface{})
	if !ok {
		return nil, rejected(ErrorMissingRequired, "merge update requires canonical prior resolved spec", false, nil)
	}
	return mergeObjects(cloneObject(base), patch), nil
}

func cloneObject(value map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(value))
	for key, item := range value {
		if child, ok := item.(map[string]interface{}); ok {
			cloned[key] = cloneObject(child)
			continue
		}
		cloned[key] = item
	}
	return cloned
}

func mergeObjects(base, patch map[string]interface{}) map[string]interface{} {
	for key, item := range patch {
		patchChild, patchIsObject := item.(map[string]interface{})
		baseChild, baseIsObject := base[key].(map[string]interface{})
		if patchIsObject && baseIsObject {
			base[key] = mergeObjects(cloneObject(baseChild), patchChild)
			continue
		}
		if patchIsObject {
			base[key] = cloneObject(patchChild)
			continue
		}
		base[key] = item
	}
	return base
}

func resolveConfig(config Config) (Config, error) {
	if strings.TrimSpace(config.Root) == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return config, fmt.Errorf("OPENCLAW_SOULFACTORY_ROOT is required when home directory cannot be resolved")
		}
		config.Root = filepath.Join(home, ".openclaw", "soulfactory")
	}
	if strings.TrimSpace(config.OpenClawBin) == "" {
		config.OpenClawBin = "openclaw"
	}
	if strings.TrimSpace(config.RuntimeMode) == "" {
		config.RuntimeMode = RuntimeModeExistingContainer
	}
	if config.RuntimeMode != RuntimeModeExistingContainer && config.RuntimeMode != RuntimeModePerAgentCompose {
		return config, fmt.Errorf("OPENCLAW_SOULFACTORY_RUNTIME_MODE must be existing-container or per-agent-compose")
	}
	config.DefaultBindings = uniqueStrings(config.DefaultBindings)
	config.RequiredPlugins = uniqueStrings(config.RequiredPlugins)
	for _, requirement := range config.RequiredPlugins {
		if _, _, err := parsePluginRequirement(requirement); err != nil {
			return config, err
		}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Runner == nil {
		config.Runner = ExecRunner{}
	}
	return config, nil
}

func validateInvocationIdentity(invocation soulfactory.OpenClawControlInvocation) error {
	if invocation.Method == "" {
		return fmt.Errorf("method is required")
	}
	if !agentIDPattern.MatchString(invocation.AgentID) || strings.Contains(invocation.AgentID, "..") {
		return fmt.Errorf("agent_id must be a safe path segment of letters, numbers, dot, underscore, or dash")
	}
	if strings.TrimSpace(invocation.SoulID) == "" {
		return fmt.Errorf("soul_id is required")
	}
	if strings.TrimSpace(invocation.SpecHash) == "" {
		return fmt.Errorf("spec_hash is required")
	}
	return nil
}

func (e *Executor) provision(ctx context.Context, invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	if err := requireObjectParams(invocation.Params, "identity", "runtime", "permissions", "relay_policy", "workspace", "assets"); err != nil {
		return rejected(ErrorMissingRequired, err.Error(), false, nil)
	}
	if e.config.RuntimeMode == RuntimeModePerAgentCompose && !e.config.DryRun {
		return rejected(ErrorUnsupportedMethod, "per-agent-compose runtime mode requires a container orchestration implementation before non-dry-run use", false, nil)
	}
	if e.config.RuntimeMode == RuntimeModeExistingContainer && !e.config.DryRun && strings.TrimSpace(e.config.Container) == "" {
		return rejected(ErrorMissingRequired, "OPENCLAW_SOULFACTORY_CONTAINER is required for existing-container non-dry-run provisioning", false, nil)
	}
	if !e.config.DryRun {
		if outcome := e.ensureRuntimePlugins(ctx); outcome != nil {
			return outcome
		}
	}

	paths := e.paths(invocation.AgentID)
	if state, ok, err := readJSONFile[State](paths.State); err != nil {
		return failed(ErrorExecutionFailed, "read existing OpenClaw agent state: "+err.Error(), true, nil)
	} else if ok {
		return e.existingProvisionOutcome(invocation, paths, state)
	}

	if err := os.MkdirAll(paths.OpenClawDir, 0o700); err != nil {
		return failed(ErrorExecutionFailed, "create OpenClaw agent workspace: "+err.Error(), true, nil)
	}
	if err := os.MkdirAll(paths.AgentDir, 0o700); err != nil {
		return failed(ErrorExecutionFailed, "create OpenClaw agent directory: "+err.Error(), true, nil)
	}
	if err := e.renderProvisionWorkspace(invocation, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	warnings := e.baseWarnings()
	now := e.config.Now().Unix()
	state := State{
		AgentID:          invocation.AgentID,
		SoulID:           invocation.SoulID,
		SpecHash:         invocation.SpecHash,
		State:            "running",
		RuntimeBinding:   runtimeBinding(invocation.AgentID),
		Workspace:        paths.Workspace,
		AgentDir:         paths.AgentDir,
		RuntimeMode:      e.config.RuntimeMode,
		Container:        e.config.Container,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastMethod:       invocation.Method,
		Warnings:         warnings,
		OperatorRequest:  invocation.Envelope.Operator.RequestEvent,
		ControllerPubkey: invocation.Envelope.Controller.Pubkey,
		RuntimePubkey:    invocation.Envelope.Target.RuntimePubkey,
	}
	if !e.config.DryRun {
		if outcome := e.runProvisionCommands(ctx, invocation, paths); outcome != nil {
			state.State = "failed"
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
	}
	outcome := success(e.resultFromState(state, now))
	if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	return outcome
}

// ensureRuntimePlugins treats plugins as shared runtime prerequisites, not
// per-agent state. A newly installed plugin requires one gateway restart, so
// provisioning stops before creating agent state and succeeds on retry after
// the runtime has restarted and reports the plugin as loaded.
func (e *Executor) ensureRuntimePlugins(ctx context.Context) *soulfactory.OpenClawControlOutcome {
	if len(e.config.RequiredPlugins) == 0 {
		return nil
	}
	out, outcome := e.runOpenClawOutput(ctx, e.containerArgs("plugins", "list", "--json")...)
	if outcome != nil {
		return outcome
	}
	var inventory interface{}
	if err := json.Unmarshal(out, &inventory); err != nil {
		return failed(ErrorExecutionFailed, "parse OpenClaw plugin inventory: "+err.Error(), true, nil)
	}
	for _, requirement := range e.config.RequiredPlugins {
		id, source, _ := parsePluginRequirement(requirement)
		if pluginLoaded(inventory, id) {
			continue
		}
		if _, installOutcome := e.runOpenClawOutput(ctx, e.containerArgs("plugins", "install", source)...); installOutcome != nil {
			return installOutcome
		}
		return failed(ErrorRuntimeUnavailable, fmt.Sprintf("installed required OpenClaw plugin %q from %q; restart the shared gateway, then retry provisioning", id, source), true, map[string]interface{}{
			"plugin_id":        id,
			"restart_required": true,
		})
	}
	return nil
}

func parsePluginRequirement(requirement string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(requirement), "=", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("OPENCLAW_SOULFACTORY_REQUIRED_PLUGINS entries must use plugin-id=install-source")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func pluginLoaded(value interface{}, id string) bool {
	switch typed := value.(type) {
	case []interface{}:
		for _, item := range typed {
			if pluginLoaded(item, id) {
				return true
			}
		}
	case map[string]interface{}:
		pluginID, _ := typed["id"].(string)
		status, _ := typed["status"].(string)
		enabled, hasEnabled := typed["enabled"].(bool)
		if pluginID == id && status == "loaded" && (!hasEnabled || enabled) {
			return true
		}
		for _, item := range typed {
			if pluginLoaded(item, id) {
				return true
			}
		}
	}
	return false
}

func (e *Executor) existingProvisionOutcome(invocation soulfactory.OpenClawControlInvocation, paths localPaths, state State) *soulfactory.OpenClawControlOutcome {
	if state.SpecHash != invocation.SpecHash {
		return rejected(ErrorDuplicateConflict, "agent_id is already bound to a different spec_hash", false, map[string]interface{}{"agent_id": invocation.AgentID, "existing_spec_hash": state.SpecHash, "requested_spec_hash": invocation.SpecHash})
	}
	lastInvocation, exact, err := readJSONFile[soulfactory.OpenClawControlInvocation](paths.LastInvocation)
	if err != nil {
		return failed(ErrorExecutionFailed, "read previous OpenClaw invocation: "+err.Error(), true, nil)
	}
	if exact && invocationFingerprint(lastInvocation) == invocationFingerprint(invocation) {
		if replay, replayed := e.replayOutcome(invocation, paths, state); replayed {
			return replay
		}
	}
	return rejected(ErrorDuplicateConflict, "agent_id is already bound to local state that does not match this provision invocation", false, map[string]interface{}{"agent_id": invocation.AgentID, "spec_hash": invocation.SpecHash})
}

func (e *Executor) renderProvisionWorkspace(invocation soulfactory.OpenClawControlInvocation, paths localPaths) error {
	identity, _ := invocation.Params["identity"].(map[string]interface{})
	persona, hasPersona := invocation.Params["persona"]
	if err := atomicWriteFile(paths.IdentityFile, []byte(renderIdentity(invocation, identity)), 0o600); err != nil {
		return fmt.Errorf("write IDENTITY.md: %w", err)
	}
	if err := atomicWriteFile(paths.SoulFile, []byte(renderSoul(invocation, identity, persona, hasPersona)), 0o600); err != nil {
		return fmt.Errorf("write SOUL.md: %w", err)
	}
	if err := atomicWriteFile(paths.AgentsFile, []byte(renderAgents(invocation, "")), 0o600); err != nil {
		return fmt.Errorf("write AGENTS.md: %w", err)
	}
	if err := atomicWriteFile(paths.MemoryFile, []byte("# Memory\n\nMemory entries are managed by the OpenClaw runtime and SoulFactory-approved tools.\n"), 0o600); err != nil {
		return fmt.Errorf("write MEMORY.md: %w", err)
	}
	provenance := map[string]interface{}{
		"agent_id":          invocation.AgentID,
		"soul_id":           invocation.SoulID,
		"spec_hash":         invocation.SpecHash,
		"method":            invocation.Method,
		"operator_request":  invocation.Envelope.Operator.RequestEvent,
		"operator_pubkey":   invocation.Envelope.Operator.Pubkey,
		"controller_pubkey": invocation.Envelope.Controller.Pubkey,
		"runtime_pubkey":    invocation.Envelope.Target.RuntimePubkey,
		"runtime":           string(domain.RuntimeTargetOpenClaw),
		"params":            invocation.Params,
	}
	return atomicWriteJSON(paths.Provenance, provenance, 0o600)
}

func (e *Executor) runProvisionCommands(ctx context.Context, invocation soulfactory.OpenClawControlInvocation, paths localPaths) *soulfactory.OpenClawControlOutcome {
	contract, outcome := parseSignetIdentityContract(invocation)
	if outcome != nil {
		return outcome
	}
	if contract != nil {
		batch, err := json.Marshal([]map[string]interface{}{
			{"path": "channels.nostr.nip46", "value": true},
			{"path": "channels.nostr.nip46BunkerUrl", "value": contract.BunkerURL},
			{"path": "channels.nostr.nip46Secret", "value": map[string]interface{}{"source": "file", "path": contract.ClientKeyRef}},
			{"path": "channels.nostr.nip46SignerRelays", "value": contract.Relays},
		})
		if err != nil {
			return failed(ErrorExecutionFailed, "marshal OpenClaw NIP-46 config patch: "+err.Error(), false, nil)
		}
		if outcome := e.runOpenClaw(ctx, e.containerArgs("config", "set", "--batch-json", string(batch))...); outcome != nil {
			return outcome
		}
	}
	args := e.containerArgs("agents", "add", invocation.AgentID, "--workspace", paths.Workspace, "--agent-dir", paths.AgentDir, "--non-interactive", "--json")
	model := firstString(invocation.Params, "model")
	if model == "" {
		if runtimeParam, ok := invocation.Params["runtime"].(map[string]interface{}); ok {
			model = firstString(runtimeParam, "model")
		}
	}
	if model == "" {
		model = e.config.DefaultModel
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if outcome := e.runOpenClaw(ctx, args...); outcome != nil {
		return outcome
	}
	if outcome := e.runOpenClaw(ctx, e.containerArgs("agents", "set-identity", "--agent", invocation.AgentID, "--identity-file", paths.IdentityFile, "--json")...); outcome != nil {
		return outcome
	}
	for _, binding := range e.config.DefaultBindings {
		if outcome := e.runOpenClaw(ctx, e.containerArgs("agents", "bind", "--agent", invocation.AgentID, "--bind", binding, "--json")...); outcome != nil {
			return outcome
		}
	}
	return nil
}

func parseSignetIdentityContract(invocation soulfactory.OpenClawControlInvocation) (*soulfactory.OpenClawSignetIdentityContract, *soulfactory.OpenClawControlOutcome) {
	bahia, _ := invocation.Params["bahia"].(map[string]interface{})
	raw, exists := bahia["signet_identity"]
	if !exists {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, rejected(ErrorMissingRequired, "invalid OpenClaw Signet identity contract", false, nil)
	}
	var contract soulfactory.OpenClawSignetIdentityContract
	if err := json.Unmarshal(data, &contract); err != nil {
		return nil, rejected(ErrorMissingRequired, "invalid OpenClaw Signet identity contract", false, nil)
	}
	if contract.Schema != soulfactory.OpenClawSignetIdentityContractSchema || contract.AgentID != invocation.AgentID ||
		contract.ManagedPubkey == "" || contract.ClientPubkey == "" || !filepath.IsAbs(contract.ClientKeyRef) ||
		contract.BunkerURL == "" || strings.Contains(contract.BunkerURL, "secret=") || len(contract.Relays) == 0 {
		return nil, rejected(ErrorMissingRequired, "OpenClaw Signet identity contract is incomplete or contains a one-time secret", false, nil)
	}
	if contract.ControllerPubkey != invocation.Envelope.Controller.Pubkey || contract.RuntimePubkey != invocation.Envelope.Target.RuntimePubkey {
		return nil, rejected(ErrorMissingRequired, "OpenClaw Signet identity contract does not match the addressed controller/runtime", false, nil)
	}
	return &contract, nil
}

func (e *Executor) personaUpdate(invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	paths := e.paths(invocation.AgentID)
	state, ok, err := readJSONFile[State](paths.State)
	if err != nil {
		return failed(ErrorExecutionFailed, "read OpenClaw agent state: "+err.Error(), true, nil)
	}
	if !ok {
		return rejected(ErrorMissingRequired, "persona update requires existing non-revoked OpenClaw agent state", false, nil)
	}
	if replay, replayed := e.replayOutcome(invocation, paths, state); replayed {
		return replay
	}
	if state.State == "revoked" {
		return rejected(ErrorMissingRequired, "persona update requires existing non-revoked OpenClaw agent state", false, nil)
	}
	if state.SpecHash != invocation.SpecHash {
		return rejected(ErrorSpecHashMismatch, "persona update spec_hash does not match local state", false, map[string]interface{}{"existing_spec_hash": state.SpecHash, "requested_spec_hash": invocation.SpecHash})
	}
	mapping, err := soulfactory.ParsePersonaRuntimeParams(invocation.Params)
	if err != nil {
		return rejected(ErrorMissingRequired, err.Error(), false, nil)
	}
	personaDoc := map[string]interface{}{
		"schema":                 soulfactory.PersonalityRuntimeParamsSchema,
		"persona":                mapping.Persona,
		"system_prompt_sections": mapping.Sections,
		"system_prompt_override": mapping.SystemPrompt,
		"agent_defaults_patch":   mapping.RuntimeParams.OpenClaw.AgentDefaultsPatch,
	}
	if err := atomicWriteJSON(paths.PersonaFile, personaDoc, 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write persona state: "+err.Error(), true, nil)
	}
	if err := atomicWriteFile(paths.SoulFile, []byte(renderPersonaSoul(invocation, mapping.SystemPrompt)), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write SOUL.md: "+err.Error(), true, nil)
	}
	if err := atomicWriteFile(paths.AgentsFile, []byte(renderAgents(invocation, mapping.SystemPrompt)), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write AGENTS.md: "+err.Error(), true, nil)
	}
	provenance, _, err := readJSONFile[map[string]interface{}](paths.Provenance)
	if err != nil {
		return failed(ErrorExecutionFailed, "read provenance: "+err.Error(), true, nil)
	}
	if provenance == nil {
		provenance = map[string]interface{}{}
	}
	provenance["agent_defaults_patch"] = mapping.RuntimeParams.OpenClaw.AgentDefaultsPatch
	provenance["last_persona_update"] = invocation.Params
	if err := atomicWriteJSON(paths.Provenance, provenance, 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write provenance: "+err.Error(), true, nil)
	}
	warnings := append([]string{}, state.Warnings...)
	warnings = appendWarning(warnings, "openclaw persona files updated; live runtime hot reload is not confirmed by the current OpenClaw CLI")
	state.UpdatedAt = e.config.Now().Unix()
	state.LastMethod = invocation.Method
	state.Warnings = warnings
	outcome := success(e.resultFromState(state, state.UpdatedAt))
	if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	return outcome
}

func (e *Executor) revoke(ctx context.Context, invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	if err := requireStringParam(invocation.Params, "reason"); err != nil {
		return rejected(ErrorMissingRequired, err.Error(), false, nil)
	}
	if _, ok := invocation.Params["revoke_runtime_credentials"].(bool); !ok {
		return rejected(ErrorMissingRequired, "revoke requires revoke_runtime_credentials boolean", false, nil)
	}
	paths := e.paths(invocation.AgentID)
	state, ok, err := readJSONFile[State](paths.State)
	if err != nil {
		return failed(ErrorExecutionFailed, "read OpenClaw agent state: "+err.Error(), true, nil)
	}
	if !ok {
		return rejected(ErrorMissingRequired, "revoke requires existing OpenClaw agent state", false, nil)
	}
	if replay, replayed := e.replayOutcome(invocation, paths, state); replayed {
		return replay
	}
	if state.SpecHash != invocation.SpecHash {
		return rejected(ErrorSpecHashMismatch, "revoke spec_hash does not match local state", false, map[string]interface{}{"existing_spec_hash": state.SpecHash, "requested_spec_hash": invocation.SpecHash})
	}
	effectiveContainer := firstNonEmpty(e.config.Container, state.Container)
	if !e.config.DryRun && e.config.RuntimeMode == RuntimeModeExistingContainer && effectiveContainer == "" {
		return rejected(ErrorMissingRequired, "OPENCLAW_SOULFACTORY_CONTAINER or persisted state.container is required for existing-container non-dry-run revoke", false, nil)
	}
	if !e.config.DryRun {
		if outcome := e.runOpenClaw(ctx, e.containerArgsFor(effectiveContainer, "agents", "unbind", "--agent", invocation.AgentID, "--all", "--json")...); outcome != nil {
			state.State = "failed"
			state.UpdatedAt = e.config.Now().Unix()
			state.LastMethod = invocation.Method
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
		if boolParam(invocation.Params, "delete_workspace") {
			if outcome := e.runOpenClaw(ctx, e.containerArgsFor(effectiveContainer, "agents", "delete", invocation.AgentID, "--force", "--json")...); outcome != nil {
				state.State = "failed"
				state.UpdatedAt = e.config.Now().Unix()
				state.LastMethod = invocation.Method
				state.LastReason = errorMessage(outcome)
				return e.persistFailure(invocation, outcome, state, paths)
			}
		}
	}
	if boolParam(invocation.Params, "delete_workspace") {
		if err := os.RemoveAll(paths.Workspace); err != nil {
			outcome := failed(ErrorExecutionFailed, "delete OpenClaw workspace: "+err.Error(), true, nil)
			state.State = "failed"
			state.UpdatedAt = e.config.Now().Unix()
			state.LastMethod = invocation.Method
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
	}
	warnings := append([]string{}, state.Warnings...)
	if boolParam(invocation.Params, "revoke_runtime_credentials") {
		warnings = appendWarning(warnings, "runtime credential revocation requested but no configured OpenClaw credential revocation command is available")
	}
	state.State = "revoked"
	state.UpdatedAt = e.config.Now().Unix()
	state.LastMethod = invocation.Method
	state.LastReason = strings.TrimSpace(invocation.Params["reason"].(string))
	state.Warnings = warnings
	outcome := success(e.resultFromState(state, state.UpdatedAt))
	if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	return outcome
}

func (e *Executor) runOpenClaw(ctx context.Context, args ...string) *soulfactory.OpenClawControlOutcome {
	_, outcome := e.runOpenClawOutput(ctx, args...)
	return outcome
}

func (e *Executor) runOpenClawOutput(ctx context.Context, args ...string) ([]byte, *soulfactory.OpenClawControlOutcome) {
	out, err := e.config.Runner.Run(ctx, e.config.OpenClawBin, args...)
	if err == nil {
		return out, nil
	}
	var unavailable RuntimeUnavailableError
	if errors.As(err, &unavailable) {
		return out, failed(ErrorRuntimeUnavailable, unavailable.Error(), true, nil)
	}
	return out, failed(ErrorExecutionFailed, err.Error(), true, nil)
}

func (e *Executor) containerArgs(args ...string) []string {
	return e.containerArgsFor(e.config.Container, args...)
}

func (e *Executor) containerArgsFor(container string, args ...string) []string {
	if e.config.RuntimeMode == RuntimeModeExistingContainer && strings.TrimSpace(container) != "" {
		return append([]string{"--container", strings.TrimSpace(container)}, args...)
	}
	return append([]string{}, args...)
}

func (e *Executor) paths(agentID string) localPaths {
	agentRoot := filepath.Join(e.config.Root, "agents", agentID)
	workspace := filepath.Join(agentRoot, "workspace")
	return localPaths{
		AgentRoot:      agentRoot,
		Workspace:      workspace,
		AgentDir:       filepath.Join(agentRoot, "agent"),
		OpenClawDir:    filepath.Join(workspace, ".openclaw"),
		IdentityFile:   filepath.Join(workspace, "IDENTITY.md"),
		SoulFile:       filepath.Join(workspace, "SOUL.md"),
		AgentsFile:     filepath.Join(workspace, "AGENTS.md"),
		MemoryFile:     filepath.Join(workspace, "MEMORY.md"),
		Provenance:     filepath.Join(workspace, ".openclaw", "soulfactory.json"),
		PersonaFile:    filepath.Join(workspace, ".openclaw", "soulfactory-persona.json"),
		State:          filepath.Join(agentRoot, "state.json"),
		LastInvocation: filepath.Join(agentRoot, "last-invocation.json"),
		LastOutcome:    filepath.Join(agentRoot, "last-outcome.json"),
	}
}

type localPaths struct {
	AgentRoot      string
	Workspace      string
	AgentDir       string
	OpenClawDir    string
	IdentityFile   string
	SoulFile       string
	AgentsFile     string
	MemoryFile     string
	Provenance     string
	PersonaFile    string
	State          string
	LastInvocation string
	LastOutcome    string
}

func (e *Executor) baseWarnings() []string {
	var warnings []string
	if e.config.DryRun {
		warnings = append(warnings, "dry-run mode recorded local state without invoking OpenClaw CLI")
	}
	if e.config.RuntimeMode == RuntimeModePerAgentCompose {
		warnings = append(warnings, "per-agent-compose runtime mode rendered local state only")
	}
	return warnings
}

func (e *Executor) resultFromState(state State, observedAt int64) map[string]interface{} {
	return map[string]interface{}{
		"agent_id":        state.AgentID,
		"runtime":         string(domain.RuntimeTargetOpenClaw),
		"runtime_binding": state.RuntimeBinding,
		"state":           state.State,
		"spec_hash":       state.SpecHash,
		"workspace":       state.Workspace,
		"agent_dir":       state.AgentDir,
		"runtime_mode":    state.RuntimeMode,
		"container":       state.Container,
		"observed_at":     observedAt,
		"warnings":        append([]string{}, state.Warnings...),
	}
}

func (e *Executor) replayOutcome(invocation soulfactory.OpenClawControlInvocation, paths localPaths, state State) (*soulfactory.OpenClawControlOutcome, bool) {
	lastInvocation, exact, err := readJSONFile[soulfactory.OpenClawControlInvocation](paths.LastInvocation)
	if err != nil || !exact || invocationFingerprint(lastInvocation) != invocationFingerprint(invocation) {
		return nil, false
	}
	lastOutcome, ok, err := readJSONFile[soulfactory.OpenClawControlOutcome](paths.LastOutcome)
	if err == nil && ok && lastOutcome.Status != StatusSuccess {
		return &lastOutcome, true
	}
	return success(e.resultFromState(state, state.UpdatedAt)), true
}

func (e *Executor) persistFailure(invocation soulfactory.OpenClawControlInvocation, outcome *soulfactory.OpenClawControlOutcome, state State, paths localPaths) *soulfactory.OpenClawControlOutcome {
	if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
		return failed(ErrorExecutionFailed, errorMessage(outcome)+"; additionally failed to persist OpenClaw audit state: "+err.Error(), true, nil)
	}
	return outcome
}

func (e *Executor) persistInvocationOutcome(invocation soulfactory.OpenClawControlInvocation, outcome *soulfactory.OpenClawControlOutcome, state State, paths localPaths) error {
	if err := atomicWriteJSON(paths.State, state, 0o600); err != nil {
		return fmt.Errorf("write state.json: %w", err)
	}
	if err := atomicWriteJSON(paths.LastInvocation, invocation, 0o600); err != nil {
		return fmt.Errorf("write last-invocation.json: %w", err)
	}
	if err := atomicWriteJSON(paths.LastOutcome, outcome, 0o600); err != nil {
		return fmt.Errorf("write last-outcome.json: %w", err)
	}
	return nil
}

func success(result map[string]interface{}) *soulfactory.OpenClawControlOutcome {
	return &soulfactory.OpenClawControlOutcome{Status: StatusSuccess, Result: result, Error: nil}
}

func rejected(code, message string, retryable bool, details map[string]interface{}) *soulfactory.OpenClawControlOutcome {
	return &soulfactory.OpenClawControlOutcome{Status: StatusRejected, Error: &soulfactory.RuntimeControlError{Code: code, Message: message, Retryable: retryable, Details: details}}
}

func failed(code, message string, retryable bool, details map[string]interface{}) *soulfactory.OpenClawControlOutcome {
	return &soulfactory.OpenClawControlOutcome{Status: StatusFailed, Error: &soulfactory.RuntimeControlError{Code: code, Message: message, Retryable: retryable, Details: details}}
}

func requireObjectParams(params map[string]interface{}, names ...string) error {
	for _, name := range names {
		if _, ok := params[name].(map[string]interface{}); !ok {
			return fmt.Errorf("missing required object param: %s", name)
		}
	}
	return nil
}

func requireStringParam(params map[string]interface{}, name string) error {
	if value, ok := params[name].(string); ok && strings.TrimSpace(value) != "" {
		return nil
	}
	return fmt.Errorf("missing required string param: %s", name)
}

func readJSONFile[T any](path string) (T, bool, error) {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, false, nil
		}
		return zero, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return zero, true, nil
	}
	if err := json.Unmarshal(data, &zero); err != nil {
		return zero, true, err
	}
	return zero, true, nil
}

func atomicWriteJSON(path string, value interface{}, perm os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, perm)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func invocationFingerprint(invocation soulfactory.OpenClawControlInvocation) string {
	invocation.Event = nil
	data, _ := json.Marshal(invocation)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func runtimeBinding(agentID string) string { return "openclaw://agents/" + agentID }

func renderIdentity(invocation soulfactory.OpenClawControlInvocation, identity map[string]interface{}) string {
	lines := []string{"# Identity", "", "Agent ID: " + invocation.AgentID, "Soul ID: " + invocation.SoulID}
	for _, key := range sortedMapKeys(identity) {
		if value := stringValue(identity[key]); value != "" {
			lines = append(lines, title(key)+": "+value)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderSoul(invocation soulfactory.OpenClawControlInvocation, identity map[string]interface{}, persona interface{}, hasPersona bool) string {
	lines := []string{"# Soul", "", "Agent ID: " + invocation.AgentID, "Soul ID: " + invocation.SoulID, "Spec Hash: " + invocation.SpecHash}
	if name := stringValue(identity["name"]); name != "" {
		lines = append(lines, "Name: "+name)
	}
	if purpose := stringValue(identity["purpose"]); purpose != "" {
		lines = append(lines, "", "## Purpose", purpose)
	}
	if hasPersona {
		if data, err := json.MarshalIndent(persona, "", "  "); err == nil {
			lines = append(lines, "", "## Persona", "```json", string(data), "```")
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderPersonaSoul(invocation soulfactory.OpenClawControlInvocation, systemPrompt string) string {
	lines := []string{"# Soul", "", "Agent ID: " + invocation.AgentID, "Soul ID: " + invocation.SoulID, "Spec Hash: " + invocation.SpecHash}
	if strings.TrimSpace(systemPrompt) != "" {
		lines = append(lines, "", "## System Prompt", strings.TrimSpace(systemPrompt))
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderAgents(invocation soulfactory.OpenClawControlInvocation, systemPrompt string) string {
	lines := []string{"# Agent Runtime Instructions", "", "This workspace is managed by Bahia SoulFactory for OpenClaw agent `" + invocation.AgentID + "`.", "Use only operator-approved tools, relay policies, and workspace paths recorded in `.openclaw/soulfactory.json`."}
	if strings.TrimSpace(systemPrompt) != "" {
		lines = append(lines, "", "## Persona System Prompt", strings.TrimSpace(systemPrompt))
	}
	return strings.Join(lines, "\n") + "\n"
}

func appendWarning(warnings []string, warning string) []string {
	for _, existing := range warnings {
		if existing == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}

func containsInlinePrivateSecret(value interface{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, nested := range typed {
			normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			if isInlinePrivateSecretKey(normalized) {
				return true
			}
			if containsInlinePrivateSecret(nested) {
				return true
			}
		}
	case []interface{}:
		for _, nested := range typed {
			if containsInlinePrivateSecret(nested) {
				return true
			}
		}
	case string:
		return looksLikeInlinePrivateSecretValue(typed)
	}
	return false
}

func isInlinePrivateSecretKey(normalized string) bool {
	if strings.HasSuffix(normalized, "ref") || strings.HasSuffix(normalized, "uri") || strings.HasSuffix(normalized, "url") {
		return false
	}
	for _, marker := range []string{"privatekey", "secretkey", "nsec", "seedphrase", "mnemonic"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func looksLikeInlinePrivateSecretValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(normalized, "nsec1") {
		return true
	}
	if strings.Contains(normalized, "-----begin") && strings.Contains(normalized, "private key") {
		return true
	}
	return false
}

func firstString(params map[string]interface{}, key string) string {
	if value, ok := params[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func boolParam(params map[string]interface{}, key string) bool {
	value, _ := params[key].(bool)
	return value
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sortedMapKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func title(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func RunCLI(ctx context.Context, stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	var invocation soulfactory.OpenClawControlInvocation
	decoder := json.NewDecoder(stdin)
	decoder.UseNumber()
	if err := decoder.Decode(&invocation); err != nil {
		writeOutcome(stdout, failed(ErrorExecutionFailed, "decode OpenClaw control invocation: "+err.Error(), false, nil))
		return 0
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		message := "OpenClaw control invocation must contain exactly one JSON document"
		if err != nil {
			message = "decode trailing OpenClaw control invocation data: " + err.Error()
		}
		writeOutcome(stdout, failed(ErrorExecutionFailed, message, false, nil))
		return 0
	}
	executor, err := FromEnv(getenv)
	if err != nil {
		writeOutcome(stdout, rejected(ErrorMissingRequired, err.Error(), false, nil))
		return 0
	}
	writeOutcome(stdout, executor.Execute(ctx, invocation))
	return 0
}

func errorMessage(outcome *soulfactory.OpenClawControlOutcome) string {
	if outcome != nil && outcome.Error != nil {
		return strings.TrimSpace(outcome.Error.Message)
	}
	return "OpenClaw control execution failed"
}

func writeOutcome(w io.Writer, outcome *soulfactory.OpenClawControlOutcome) {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outcome)
}

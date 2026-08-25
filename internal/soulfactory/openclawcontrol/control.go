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
	"strconv"
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
	DockerBin       string
	RuntimeMode     string
	Container       string
	ImageDigest     string
	SourceCommit    string
	CPUs            string
	Memory          string
	PIDsLimit       int
	DefaultModel    string
	DefaultBindings []string
	RequiredPlugins []string
	DryRun          bool
	Now             func() time.Time
	Runner          CommandRunner
	Orchestrator    RuntimeOrchestrator
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
	AccountID        string   `json:"account_id,omitempty"`
	Model            string   `json:"model,omitempty"`
	SpecHash         string   `json:"spec_hash"`
	RuntimeSpecHash  string   `json:"runtime_spec_hash,omitempty"`
	State            string   `json:"state"`
	RuntimeBinding   string   `json:"runtime_binding"`
	Workspace        string   `json:"workspace"`
	WorkspaceID      string   `json:"workspace_id,omitempty"`
	AgentDir         string   `json:"agent_dir"`
	RuntimeMode      string   `json:"runtime_mode"`
	DeploymentID     string   `json:"deployment_id,omitempty"`
	RunID            string   `json:"run_id,omitempty"`
	ContainerID      string   `json:"container_id,omitempty"`
	Container        string   `json:"container,omitempty"`
	ImageDigest      string   `json:"image_digest,omitempty"`
	SourceCommit     string   `json:"source_commit,omitempty"`
	ConfigRevision   string   `json:"config_revision,omitempty"`
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
		DockerBin:       strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_DOCKER_BIN")),
		RuntimeMode:     strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_RUNTIME_MODE")),
		Container:       strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_CONTAINER")),
		ImageDigest:     strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_IMAGE")),
		SourceCommit:    strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_SOURCE_COMMIT")),
		CPUs:            strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_CPUS")),
		Memory:          strings.TrimSpace(getenv("OPENCLAW_SOULFACTORY_MEMORY")),
		PIDsLimit:       intFromString(getenv("OPENCLAW_SOULFACTORY_PIDS_LIMIT")),
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
	case soulfactory.RuntimeMethodConfigReload:
		return e.configReload(invocation)
	case soulfactory.RuntimeMethodMemoryReindex:
		return e.memoryReindex(invocation)
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
	paths = e.pathsForState(state)
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
		if outcome := e.runOpenClaw(ctx, e.containerArgsFor(state.Container, "agents", "set-identity", "--agent", invocation.AgentID, "--identity-file", containerWorkspace+"/IDENTITY.md", "--json")...); outcome != nil {
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

func (e *Executor) configReload(invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	paths := e.paths(invocation.AgentID)
	state, ok, err := readJSONFile[State](paths.State)
	if err != nil {
		return failed(ErrorExecutionFailed, "read OpenClaw agent state: "+err.Error(), true, nil)
	}
	if !ok || state.State == "revoked" {
		return rejected(ErrorMissingRequired, "config reload requires existing non-revoked OpenClaw agent state", false, nil)
	}
	paths = e.pathsForState(state)
	if replay, replayed := e.replayOutcome(invocation, paths, state); replayed {
		return replay
	}
	req, err := soulfactory.ParseConfigReloadRequest(invocation.Params)
	if err != nil {
		return rejected(ErrorMissingRequired, err.Error(), false, nil)
	}
	if req.PreviousSpecHash != "" && req.PreviousSpecHash != state.SpecHash {
		return rejected(ErrorSpecHashMismatch, "config reload previous_spec_hash does not match local state", false, map[string]interface{}{
			"existing_spec_hash": state.SpecHash,
			"previous_spec_hash": req.PreviousSpecHash,
		})
	}
	newSpecHash := strings.TrimSpace(req.NewSpecHash)
	if newSpecHash == "" {
		if invocation.SpecHash != state.SpecHash {
			return rejected(ErrorSpecHashMismatch, "config reload spec_hash does not match local state", false, map[string]interface{}{
				"existing_spec_hash":  state.SpecHash,
				"requested_spec_hash": invocation.SpecHash,
			})
		}
		newSpecHash = state.SpecHash
	} else if invocation.SpecHash != newSpecHash {
		return rejected(ErrorSpecHashMismatch, "config reload new_spec_hash does not match the requested soul spec_hash", false, map[string]interface{}{
			"requested_spec_hash": invocation.SpecHash,
			"new_spec_hash":       newSpecHash,
		})
	}

	provenance, _, err := readJSONFile[map[string]interface{}](paths.Provenance)
	if err != nil {
		return failed(ErrorExecutionFailed, "read provenance: "+err.Error(), true, nil)
	}
	if provenance == nil {
		provenance = map[string]interface{}{}
	}
	canonical, _ := provenance["params"].(map[string]interface{})
	canonical = cloneObject(canonical)
	resolved := map[string]interface{}{}
	if req.ResolvedSpec != nil {
		data, marshalErr := json.Marshal(req.ResolvedSpec)
		if marshalErr != nil {
			return failed(ErrorExecutionFailed, "marshal resolved reload spec: "+marshalErr.Error(), false, nil)
		}
		if unmarshalErr := json.Unmarshal(data, &resolved); unmarshalErr != nil {
			return failed(ErrorExecutionFailed, "decode resolved reload spec: "+unmarshalErr.Error(), false, nil)
		}
	}
	for _, section := range req.TargetFields {
		if value, exists := resolved[section]; exists {
			canonical[section] = cloneValue(value)
		}
		if value, exists := req.Patch[section]; exists {
			if patch, patchOK := value.(map[string]interface{}); patchOK {
				if base, baseOK := canonical[section].(map[string]interface{}); baseOK {
					canonical[section] = mergeObjects(cloneObject(base), patch)
				} else {
					canonical[section] = cloneObject(patch)
				}
			} else {
				canonical[section] = value
			}
		}
	}

	updatedInvocation := invocation
	updatedInvocation.SpecHash = newSpecHash
	updatedInvocation.Params = canonical
	identity, _ := canonical["identity"].(map[string]interface{})
	persona, hasPersona := canonical["persona"]
	if err := atomicWriteFile(paths.IdentityFile, []byte(renderIdentity(updatedInvocation, identity)), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write reloaded IDENTITY.md: "+err.Error(), true, nil)
	}
	if err := atomicWriteFile(paths.SoulFile, []byte(renderSoul(updatedInvocation, identity, persona, hasPersona)), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write reloaded SOUL.md: "+err.Error(), true, nil)
	}
	if err := atomicWriteFile(paths.AgentsFile, []byte(renderAgents(updatedInvocation, "")), 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write reloaded AGENTS.md: "+err.Error(), true, nil)
	}
	if err := e.renderReloadRuntimeFiles(updatedInvocation, state, paths, req.TargetFields); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	provenance["spec_hash"] = newSpecHash
	provenance["params"] = canonical
	provenance["last_config_reload"] = map[string]interface{}{
		"target_fields":      req.TargetFields,
		"previous_spec_hash": req.PreviousSpecHash,
		"new_spec_hash":      newSpecHash,
		"draft_ref":          req.DraftRef,
		"draft_event_id":     req.DraftEventID,
	}
	if err := atomicWriteJSON(paths.Provenance, provenance, 0o600); err != nil {
		return failed(ErrorExecutionFailed, "write reload provenance: "+err.Error(), true, nil)
	}
	state.SpecHash = newSpecHash
	state.State = "running"
	state.UpdatedAt = e.config.Now().Unix()
	state.LastMethod = invocation.Method
	state.LastReason = ""
	result := e.resultFromState(state, state.UpdatedAt)
	result["reloaded"] = append([]string{}, req.TargetFields...)
	result["restart"] = false
	outcome := success(result)
	if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	return outcome
}

func (e *Executor) memoryReindex(invocation soulfactory.OpenClawControlInvocation) *soulfactory.OpenClawControlOutcome {
	paths := e.paths(invocation.AgentID)
	state, ok, err := readJSONFile[State](paths.State)
	if err != nil {
		return failed(ErrorExecutionFailed, "read OpenClaw agent state: "+err.Error(), true, nil)
	}
	if !ok || state.State == "revoked" {
		return rejected(ErrorMissingRequired, "memory reindex requires existing non-revoked OpenClaw agent state", false, nil)
	}
	paths = e.pathsForState(state)
	if replay, replayed := e.replayOutcome(invocation, paths, state); replayed {
		return replay
	}
	data, err := json.Marshal(invocation.Params)
	if err != nil {
		return rejected(ErrorMissingRequired, "marshal memory reindex request: "+err.Error(), false, nil)
	}
	var req soulfactory.MemoryReindexRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return rejected(ErrorMissingRequired, "parse memory reindex request: "+err.Error(), false, nil)
	}
	if req.Schema != soulfactory.SoulFactoryMemoryReindexSchema {
		return rejected(ErrorMissingRequired, "memory reindex requires soulfactory-memory-reindex/v1 schema", false, nil)
	}
	if req.Mode != soulfactory.MemoryReindexModeIncremental && req.Mode != soulfactory.MemoryReindexModeFull {
		return rejected(ErrorMissingRequired, "memory reindex mode must be incremental or full", false, nil)
	}
	if _, ok := invocation.Params["memory_config"].(map[string]interface{}); !ok {
		return rejected(ErrorMissingRequired, "memory reindex requires memory_config object", false, nil)
	}
	if req.PreviousSpecHash != "" && req.PreviousSpecHash != state.SpecHash {
		return rejected(ErrorSpecHashMismatch, "memory reindex previous_spec_hash does not match local state", false, map[string]interface{}{
			"existing_spec_hash": state.SpecHash,
			"previous_spec_hash": req.PreviousSpecHash,
		})
	}
	if req.NewSpecHash != "" && invocation.SpecHash != req.NewSpecHash {
		return rejected(ErrorSpecHashMismatch, "memory reindex new_spec_hash does not match the requested soul spec_hash", false, map[string]interface{}{
			"requested_spec_hash": invocation.SpecHash,
			"new_spec_hash":       req.NewSpecHash,
		})
	}
	if req.NewSpecHash == "" && invocation.SpecHash != state.SpecHash {
		return rejected(ErrorSpecHashMismatch, "memory reindex spec_hash does not match local state", false, map[string]interface{}{
			"existing_spec_hash":  state.SpecHash,
			"requested_spec_hash": invocation.SpecHash,
		})
	}

	state.UpdatedAt = e.config.Now().Unix()
	state.LastMethod = invocation.Method
	state.LastReason = ""
	result := e.resultFromState(state, state.UpdatedAt)
	result["operation"] = "memory.reindex"
	result["mode"] = req.Mode
	result["accepted"] = true
	result["started"] = false
	result["action_required"] = map[string]interface{}{
		"type":     "runtime-native-memory-reindex",
		"agent_id": invocation.AgentID,
		"message":  "Trigger memory indexing through the deployed OpenClaw runtime's native memory surface; this wrapper has no stable reindex CLI command.",
	}
	outcome := success(result)
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
		cloned[key] = cloneValue(item)
	}
	return cloned
}

func cloneValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneObject(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneValue(item)
		}
		return cloned
	default:
		return value
	}
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
	absoluteRoot, err := filepath.Abs(config.Root)
	if err != nil {
		return config, fmt.Errorf("resolve OPENCLAW_SOULFACTORY_ROOT: %w", err)
	}
	config.Root = absoluteRoot
	if strings.TrimSpace(config.OpenClawBin) == "" {
		config.OpenClawBin = "openclaw"
	}
	if strings.TrimSpace(config.DockerBin) == "" {
		config.DockerBin = "docker"
	}
	if strings.TrimSpace(config.RuntimeMode) == "" {
		config.RuntimeMode = RuntimeModePerAgentCompose
	}
	if strings.TrimSpace(config.CPUs) == "" {
		config.CPUs = "1.0"
	}
	if strings.TrimSpace(config.Memory) == "" {
		config.Memory = "1g"
	}
	if config.PIDsLimit == 0 {
		config.PIDsLimit = 256
	}
	if cpus, err := strconv.ParseFloat(config.CPUs, 64); err != nil || cpus <= 0 {
		return config, fmt.Errorf("OPENCLAW_SOULFACTORY_CPUS must be positive")
	}
	if strings.TrimSpace(config.Memory) == "" || config.PIDsLimit <= 0 {
		return config, fmt.Errorf("OPENCLAW_SOULFACTORY_MEMORY and OPENCLAW_SOULFACTORY_PIDS_LIMIT must be positive")
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
	if config.Orchestrator == nil {
		config.Orchestrator = DockerComposeOrchestrator{DockerBin: config.DockerBin, Runner: config.Runner}
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
	if !e.config.DryRun && e.config.RuntimeMode != RuntimeModePerAgentCompose {
		return rejected(ErrorUnsupportedMethod, "externally reachable OpenClaw souls require per-agent-compose; shared existing-container provisioning is disabled", false, nil)
	}
	contract, contractOutcome := parseSignetIdentityContract(invocation)
	if contractOutcome != nil {
		return contractOutcome
	}
	paths := e.paths(invocation.AgentID)
	if e.config.RuntimeMode == RuntimeModePerAgentCompose || !e.config.DryRun {
		deploymentID := deterministicRuntimeName(invocation.AgentID, invocation.Envelope.Operator.RequestEvent, invocation.Envelope.IdempotencyKey)
		paths = e.pathsForDeployment(invocation.AgentID, deploymentID)
	}
	spec, err := e.runtimeSpec(invocation, paths, contract)
	if err != nil {
		return rejected(ErrorMissingRequired, err.Error(), false, nil)
	}
	if state, ok, err := readJSONFile[State](paths.State); err != nil {
		return failed(ErrorExecutionFailed, "read existing OpenClaw agent state: "+err.Error(), true, nil)
	} else if ok {
		outcome := e.existingProvisionOutcome(invocation, paths, state)
		if outcome.Status != StatusSuccess || e.config.DryRun {
			return outcome
		}
		lineage, reconcileErr := e.config.Orchestrator.Reconcile(ctx, spec)
		if reconcileErr != nil {
			return failed(ErrorExecutionFailed, reconcileErr.Error(), true, nil)
		}
		if pluginOutcome := e.ensureRuntimePlugins(ctx, spec); pluginOutcome != nil {
			return pluginOutcome
		}
		applyLineage(&state, lineage)
		state.UpdatedAt = e.config.Now().Unix()
		outcome = success(e.resultFromState(state, state.UpdatedAt))
		if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
			return failed(ErrorExecutionFailed, err.Error(), true, nil)
		}
		return outcome
	}

	for _, directory := range []string{filepath.Dir(paths.AgentRoot), paths.AgentRoot, filepath.Dir(paths.RuntimeRoot), paths.RuntimeRoot, paths.Workspace, paths.OpenClawDir, paths.AgentDir, paths.ConfigDir} {
		if directory == "." || directory == "" {
			continue
		}
		if err := ensurePrivateDirectory(directory); err != nil {
			return failed(ErrorExecutionFailed, "create dedicated OpenClaw runtime directory: "+err.Error(), true, nil)
		}
	}
	if err := e.renderProvisionWorkspace(invocation, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	if err := e.renderRuntimeFiles(invocation, spec, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	warnings := e.baseWarnings()
	now := e.config.Now().Unix()
	state := State{
		AgentID:          invocation.AgentID,
		SoulID:           invocation.SoulID,
		AccountID:        spec.AccountID,
		Model:            spec.Model,
		SpecHash:         invocation.SpecHash,
		RuntimeSpecHash:  invocation.SpecHash,
		State:            "creating",
		RuntimeBinding:   runtimeBinding(invocation.AgentID),
		Workspace:        paths.Workspace,
		WorkspaceID:      "workspace://" + spec.DeploymentID,
		AgentDir:         paths.AgentDir,
		RuntimeMode:      e.config.RuntimeMode,
		DeploymentID:     spec.DeploymentID,
		RunID:            spec.RunID,
		Container:        spec.ContainerName,
		ImageDigest:      spec.ImageDigest,
		SourceCommit:     spec.SourceCommit,
		ConfigRevision:   spec.ConfigRevision,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastMethod:       invocation.Method,
		Warnings:         warnings,
		OperatorRequest:  invocation.Envelope.Operator.RequestEvent,
		ControllerPubkey: invocation.Envelope.Controller.Pubkey,
		RuntimePubkey:    invocation.Envelope.Target.RuntimePubkey,
	}
	if !e.config.DryRun {
		if outcome := e.bootstrapRuntimePlugins(ctx, spec); outcome != nil {
			_ = e.config.Orchestrator.Delete(ctx, spec)
			state.State = "failed"
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
		lineage, reconcileErr := e.config.Orchestrator.Reconcile(ctx, spec)
		if reconcileErr != nil {
			_ = e.config.Orchestrator.Delete(ctx, spec)
			state.State = "failed"
			state.LastReason = reconcileErr.Error()
			return e.persistFailure(invocation, failed(ErrorExecutionFailed, reconcileErr.Error(), true, nil), state, paths)
		}
		applyLineage(&state, lineage)
		if outcome := e.ensureRuntimePlugins(ctx, spec); outcome != nil {
			_ = e.config.Orchestrator.Delete(ctx, spec)
			state.State = "failed"
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
		if outcome := e.runProvisionCommands(ctx, invocation, paths, spec, contract); outcome != nil {
			_ = e.config.Orchestrator.Delete(ctx, spec)
			state.State = "failed"
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
	}
	state.State = "running"
	outcome := success(e.resultFromState(state, now))
	if err := e.persistInvocationOutcome(invocation, outcome, state, paths); err != nil {
		return failed(ErrorExecutionFailed, err.Error(), true, nil)
	}
	return outcome
}

func (e *Executor) bootstrapRuntimePlugins(ctx context.Context, spec RuntimeSpec) *soulfactory.OpenClawControlOutcome {
	prefix := []string{
		"compose", "--project-name", spec.DeploymentID, "--file", spec.ComposePath,
		"run", "--rm", "--no-deps", "--name", spec.DeploymentID + "-bootstrap",
	}
	labels := runtimeLabels(spec)
	labelKeys := make([]string, 0, len(labels))
	for key := range labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		prefix = append(prefix, "--label", key+"="+labels[key])
	}
	prefix = append(prefix, "-e", "OPENCLAW_CONFIG_PATH=/tmp/openclaw-bootstrap.json", "gateway", "node", "dist/index.js")
	out, err := e.config.Runner.Run(ctx, e.config.DockerBin, append(prefix, "plugins", "list", "--json")...)
	if err != nil {
		return failed(ErrorExecutionFailed, "inspect dedicated OpenClaw bootstrap plugins: "+err.Error(), true, nil)
	}
	var inventory interface{}
	if err := json.Unmarshal(out, &inventory); err != nil {
		return failed(ErrorExecutionFailed, "parse dedicated OpenClaw bootstrap plugin inventory: "+err.Error(), true, nil)
	}
	for _, requirement := range e.config.RequiredPlugins {
		id, source, _ := parsePluginRequirement(requirement)
		if pluginLoaded(inventory, id) {
			continue
		}
		if _, err := e.config.Runner.Run(ctx, e.config.DockerBin, append(prefix, "plugins", "install", source)...); err != nil {
			return failed(ErrorExecutionFailed, "install dedicated OpenClaw bootstrap plugin: "+err.Error(), true, nil)
		}
	}
	return nil
}

func (e *Executor) runtimeSpec(invocation soulfactory.OpenClawControlInvocation, paths localPaths, contract *soulfactory.OpenClawSignetIdentityContract) (RuntimeSpec, error) {
	runtimeParams, _ := invocation.Params["runtime"].(map[string]interface{})
	accountID := strings.TrimSpace(firstNonEmpty(firstString(runtimeParams, "account_id"), firstString(runtimeParams, "nostr_account_id")))
	if accountID == "" {
		if bahiaParams, ok := invocation.Params["bahia"].(map[string]interface{}); ok {
			accountID = firstString(bahiaParams, "nostr_pubkey")
		}
	}
	if accountID == "" {
		if !e.config.DryRun {
			return RuntimeSpec{}, fmt.Errorf("runtime.account_id is required for exact Nostr account-to-agent binding")
		}
		accountID = "unassigned"
	}
	requestID := strings.TrimSpace(invocation.Envelope.Operator.RequestEvent)
	runID := strings.TrimSpace(invocation.Envelope.IdempotencyKey)
	if requestID == "" || runID == "" {
		return RuntimeSpec{}, fmt.Errorf("operator request event and idempotency key are required for dedicated runtime ownership")
	}
	secrets, err := secretFilesFromParams(invocation.Params)
	if err != nil {
		return RuntimeSpec{}, err
	}
	if contract != nil {
		clientKeyPath := filepath.Clean(contract.ClientKeyRef)
		if existing, ok := secrets["nip46_client"]; ok && filepath.Clean(existing) != clientKeyPath {
			return RuntimeSpec{}, fmt.Errorf("runtime.secret_files.nip46_client conflicts with the Signet identity contract")
		}
		secrets["nip46_client"] = clientKeyPath
		if err := validateSecretFiles(secrets); err != nil {
			return RuntimeSpec{}, err
		}
	}
	pluginIDs := make([]string, 0, len(e.config.RequiredPlugins))
	for _, requirement := range e.config.RequiredPlugins {
		id, _, err := parsePluginRequirement(requirement)
		if err != nil {
			return RuntimeSpec{}, err
		}
		pluginIDs = append(pluginIDs, id)
	}
	if !e.config.DryRun {
		if !immutableImagePattern.MatchString(e.config.ImageDigest) {
			return RuntimeSpec{}, fmt.Errorf("OPENCLAW_SOULFACTORY_IMAGE must use an immutable OCI digest")
		}
		if !sourceCommitPattern.MatchString(e.config.SourceCommit) {
			return RuntimeSpec{}, fmt.Errorf("OPENCLAW_SOULFACTORY_SOURCE_COMMIT must be a pinned lowercase hexadecimal commit")
		}
		foundNostr := false
		for _, id := range pluginIDs {
			foundNostr = foundNostr || id == "nostr"
		}
		if !foundNostr {
			return RuntimeSpec{}, fmt.Errorf("OPENCLAW_SOULFACTORY_REQUIRED_PLUGINS must explicitly install and allowlist the nostr plugin")
		}
	}
	model := firstString(runtimeParams, "model")
	if model == "" {
		model = e.config.DefaultModel
	}
	if !e.config.DryRun && model == "" {
		return RuntimeSpec{}, fmt.Errorf("runtime.model or OPENCLAW_SOULFACTORY_DEFAULT_MODEL is required")
	}
	revisionInput, err := json.Marshal(map[string]interface{}{
		"invocation": invocation, "image": e.config.ImageDigest, "source_commit": e.config.SourceCommit,
		"plugins": e.config.RequiredPlugins, "cpus": e.config.CPUs, "memory": e.config.Memory,
		"pids_limit": e.config.PIDsLimit, "runtime_user": fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()), "model": model, "secret_files": secrets,
	})
	if err != nil {
		return RuntimeSpec{}, fmt.Errorf("marshal dedicated runtime revision input: %w", err)
	}
	deploymentID := deterministicRuntimeName(invocation.AgentID, requestID, runID)
	containerName := deploymentID + "-gateway"
	if e.config.DryRun && e.config.RuntimeMode == RuntimeModeExistingContainer && strings.TrimSpace(e.config.Container) != "" {
		containerName = strings.TrimSpace(e.config.Container)
	}
	return RuntimeSpec{
		DeploymentID:   deploymentID,
		ContainerName:  containerName,
		AgentID:        invocation.AgentID,
		SoulID:         invocation.SoulID,
		AccountID:      accountID,
		Model:          model,
		RequestID:      requestID,
		RunID:          runID,
		SpecHash:       invocation.SpecHash,
		ImageDigest:    e.config.ImageDigest,
		SourceCommit:   e.config.SourceCommit,
		ConfigRevision: runtimeConfigRevision(revisionInput),
		ComposePath:    paths.ComposePath,
		ConfigDir:      paths.ConfigDir,
		Workspace:      paths.Workspace,
		AgentDir:       paths.AgentDir,
		SecretFiles:    secrets,
		PluginIDs:      uniqueStrings(pluginIDs),
		CPUs:           e.config.CPUs,
		Memory:         e.config.Memory,
		PIDsLimit:      e.config.PIDsLimit,
		User:           fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()),
	}, nil
}

func (e *Executor) renderRuntimeFiles(invocation soulfactory.OpenClawControlInvocation, spec RuntimeSpec, paths localPaths) error {
	pluginEntries := make(map[string]interface{}, len(spec.PluginIDs))
	for _, id := range spec.PluginIDs {
		pluginEntries[id] = map[string]interface{}{"enabled": true}
	}
	runtimeParams, _ := invocation.Params["runtime"].(map[string]interface{})
	nostrConfig, _ := runtimeParams["nostr"].(map[string]interface{})
	nostrConfig = cloneObject(nostrConfig)
	nostrConfig["enabled"] = true
	nostrConfig["defaultAccount"] = spec.AccountID
	if _, ok := nostrConfig["relays"]; !ok {
		if relayPolicy, ok := invocation.Params["relay_policy"].(map[string]interface{}); ok {
			nostrConfig["relays"] = relayPolicy["control"]
		}
	}
	secretRefs := make(map[string]interface{}, len(spec.SecretFiles))
	for name := range spec.SecretFiles {
		ref := map[string]interface{}{"source": "file", "path": "/run/secrets/" + name}
		secretRefs[name] = ref
		switch name {
		case "nip46", "nip46_client", "nip46Secret":
			nostrConfig["nip46Secret"] = ref
		case "nip46_connect", "nip46ConnectSecret":
			nostrConfig["nip46ConnectSecret"] = ref
		}
	}
	runtimeConfig := map[string]interface{}{
		"plugins": map[string]interface{}{
			"allow":   spec.PluginIDs,
			"entries": pluginEntries,
		},
		"channels": map[string]interface{}{"nostr": nostrConfig},
	}
	runtimeMetadata := map[string]interface{}{
		"schema": "bahia-openclaw-runtime/v1",
		"ownership": map[string]interface{}{
			"agentId":        invocation.AgentID,
			"soulId":         invocation.SoulID,
			"accountId":      spec.AccountID,
			"model":          spec.Model,
			"configRevision": spec.ConfigRevision,
			"secretFiles":    secretRefs,
		},
	}
	if err := atomicWriteJSON(paths.RuntimeConfig, runtimeConfig, 0o600); err != nil {
		return fmt.Errorf("write dedicated OpenClaw config: %w", err)
	}
	if err := atomicWriteJSON(paths.RuntimeMetadata, runtimeMetadata, 0o600); err != nil {
		return fmt.Errorf("write dedicated OpenClaw runtime metadata: %w", err)
	}
	if err := atomicWriteFile(paths.ComposePath, renderCompose(spec), 0o600); err != nil {
		return fmt.Errorf("write dedicated OpenClaw compose specification: %w", err)
	}
	return nil
}

func (e *Executor) renderReloadRuntimeFiles(invocation soulfactory.OpenClawControlInvocation, state State, paths localPaths, targets []string) error {
	runtimeConfig, _, err := readJSONFile[map[string]interface{}](paths.RuntimeConfig)
	if err != nil {
		return fmt.Errorf("read dedicated OpenClaw config: %w", err)
	}
	if runtimeConfig == nil {
		runtimeConfig = map[string]interface{}{}
	}
	targeted := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		targeted[target] = struct{}{}
	}
	if _, reloadRuntime := targeted["runtime"]; reloadRuntime {
		channels, _ := runtimeConfig["channels"].(map[string]interface{})
		channels = cloneObject(channels)
		nostrConfig, _ := channels["nostr"].(map[string]interface{})
		nostrConfig = cloneObject(nostrConfig)
		runtimeParams, _ := invocation.Params["runtime"].(map[string]interface{})
		if nostrPatch, ok := runtimeParams["nostr"].(map[string]interface{}); ok {
			nostrConfig = mergeObjects(nostrConfig, nostrPatch)
		}
		nostrConfig["enabled"] = true
		nostrConfig["defaultAccount"] = state.AccountID
		channels["nostr"] = nostrConfig
		runtimeConfig["channels"] = channels
	}
	if _, reloadRelays := targeted["relay_policy"]; reloadRelays {
		channels, _ := runtimeConfig["channels"].(map[string]interface{})
		channels = cloneObject(channels)
		nostrConfig, _ := channels["nostr"].(map[string]interface{})
		nostrConfig = cloneObject(nostrConfig)
		if relayPolicy, ok := invocation.Params["relay_policy"].(map[string]interface{}); ok {
			if control, exists := relayPolicy["control"]; exists {
				nostrConfig["relays"] = cloneValue(control)
			}
		}
		nostrConfig["enabled"] = true
		nostrConfig["defaultAccount"] = state.AccountID
		channels["nostr"] = nostrConfig
		runtimeConfig["channels"] = channels
	}
	if err := atomicWriteJSON(paths.RuntimeConfig, runtimeConfig, 0o600); err != nil {
		return fmt.Errorf("write reloaded OpenClaw config: %w", err)
	}

	runtimeMetadata, _, err := readJSONFile[map[string]interface{}](paths.RuntimeMetadata)
	if err != nil {
		return fmt.Errorf("read dedicated OpenClaw runtime metadata: %w", err)
	}
	if runtimeMetadata == nil {
		runtimeMetadata = map[string]interface{}{"schema": "bahia-openclaw-runtime/v1"}
	}
	soulFactoryConfig, _ := runtimeMetadata["soulfactory"].(map[string]interface{})
	soulFactoryConfig = cloneObject(soulFactoryConfig)
	for _, target := range targets {
		if value, exists := invocation.Params[target]; exists {
			soulFactoryConfig[target] = cloneValue(value)
		}
	}
	runtimeMetadata["soulfactory"] = soulFactoryConfig
	runtimeMetadata["lastConfigReload"] = map[string]interface{}{
		"specHash":     invocation.SpecHash,
		"targetFields": append([]string{}, targets...),
	}
	if err := atomicWriteJSON(paths.RuntimeMetadata, runtimeMetadata, 0o600); err != nil {
		return fmt.Errorf("write reloaded OpenClaw runtime metadata: %w", err)
	}
	return nil
}

func applyLineage(state *State, lineage RuntimeLineage) {
	state.DeploymentID = lineage.DeploymentID
	state.ContainerID = lineage.ContainerID
	state.Container = lineage.ContainerName
	state.ImageDigest = lineage.ImageDigest
	state.ConfigRevision = lineage.ConfigRevision
	state.WorkspaceID = lineage.WorkspaceID
	state.AccountID = lineage.AccountID
}

func (e *Executor) runtimeSpecFromState(state State, paths localPaths) RuntimeSpec {
	return RuntimeSpec{
		DeploymentID:   state.DeploymentID,
		ContainerName:  state.Container,
		AgentID:        state.AgentID,
		SoulID:         state.SoulID,
		AccountID:      state.AccountID,
		Model:          state.Model,
		RequestID:      state.OperatorRequest,
		RunID:          state.RunID,
		SpecHash:       firstNonEmpty(state.RuntimeSpecHash, state.SpecHash),
		ImageDigest:    state.ImageDigest,
		SourceCommit:   state.SourceCommit,
		ConfigRevision: state.ConfigRevision,
		ComposePath:    paths.ComposePath,
		ConfigDir:      paths.ConfigDir,
		Workspace:      paths.Workspace,
		AgentDir:       paths.AgentDir,
		CPUs:           e.config.CPUs,
		Memory:         e.config.Memory,
		PIDsLimit:      e.config.PIDsLimit,
		User:           fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()),
	}
}

func removeRuntimeData(paths localPaths) error {
	for _, path := range []string{paths.Workspace, paths.AgentDir, paths.ConfigDir} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// ensureRuntimePlugins installs requirements only inside the owned dedicated
// gateway and verifies them after a controlled restart.
func (e *Executor) ensureRuntimePlugins(ctx context.Context, spec RuntimeSpec) *soulfactory.OpenClawControlOutcome {
	if len(e.config.RequiredPlugins) == 0 {
		return nil
	}
	out, outcome := e.runOpenClawOutput(ctx, e.containerArgsFor(spec.ContainerName, "plugins", "list", "--json")...)
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
		if _, installOutcome := e.runOpenClawOutput(ctx, e.containerArgsFor(spec.ContainerName, "plugins", "install", source)...); installOutcome != nil {
			return installOutcome
		}
		if _, err := e.config.Runner.Run(ctx, e.config.DockerBin, "restart", spec.ContainerName); err != nil {
			return failed(ErrorExecutionFailed, "restart dedicated OpenClaw gateway after plugin installation: "+err.Error(), true, nil)
		}
		if _, err := e.config.Orchestrator.Reconcile(ctx, spec); err != nil {
			return failed(ErrorRuntimeUnavailable, "dedicated OpenClaw gateway did not recover after plugin installation: "+err.Error(), true, nil)
		}
		out, outcome = e.runOpenClawOutput(ctx, e.containerArgsFor(spec.ContainerName, "plugins", "list", "--json")...)
		if outcome != nil {
			return outcome
		}
		if err := json.Unmarshal(out, &inventory); err != nil || !pluginLoaded(inventory, id) {
			return failed(ErrorRuntimeUnavailable, fmt.Sprintf("required OpenClaw plugin %q was not loaded after dedicated gateway restart", id), true, nil)
		}
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

func (e *Executor) runProvisionCommands(ctx context.Context, invocation soulfactory.OpenClawControlInvocation, paths localPaths, spec RuntimeSpec, contract *soulfactory.OpenClawSignetIdentityContract) *soulfactory.OpenClawControlOutcome {
	if contract != nil {
		batch, err := json.Marshal([]map[string]interface{}{
			{"path": "channels.nostr.nip46", "value": true},
			{"path": "channels.nostr.nip46BunkerUrl", "value": contract.BunkerURL},
			{"path": "channels.nostr.nip46Secret", "value": map[string]interface{}{"source": "file", "path": "/run/secrets/nip46_client"}},
			{"path": "channels.nostr.nip46SignerRelays", "value": contract.Relays},
		})
		if err != nil {
			return failed(ErrorExecutionFailed, "marshal OpenClaw NIP-46 config patch: "+err.Error(), false, nil)
		}
		if outcome := e.runOpenClaw(ctx, e.containerArgsFor(spec.ContainerName, "config", "set", "--batch-json", string(batch))...); outcome != nil {
			return outcome
		}
	}
	args := e.containerArgsFor(spec.ContainerName, "agents", "add", invocation.AgentID, "--workspace", containerWorkspace, "--agent-dir", containerAgentDir, "--non-interactive", "--json")
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if outcome := e.runOpenClaw(ctx, args...); outcome != nil {
		return outcome
	}
	if outcome := e.runOpenClaw(ctx, e.containerArgsFor(spec.ContainerName, "agents", "set-identity", "--agent", invocation.AgentID, "--identity-file", containerWorkspace+"/IDENTITY.md", "--json")...); outcome != nil {
		return outcome
	}
	bindings := append([]string{"nostr:" + spec.AccountID}, e.config.DefaultBindings...)
	for _, binding := range uniqueStrings(bindings) {
		if outcome := e.runOpenClaw(ctx, e.containerArgsFor(spec.ContainerName, "agents", "bind", "--agent", invocation.AgentID, "--bind", binding, "--json")...); outcome != nil {
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
	paths = e.pathsForState(state)
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
	paths = e.pathsForState(state)
	if replay, replayed := e.replayOutcome(invocation, paths, state); replayed {
		return replay
	}
	if state.SpecHash != invocation.SpecHash {
		return rejected(ErrorSpecHashMismatch, "revoke spec_hash does not match local state", false, map[string]interface{}{"existing_spec_hash": state.SpecHash, "requested_spec_hash": invocation.SpecHash})
	}
	if !e.config.DryRun {
		if state.RuntimeMode != RuntimeModePerAgentCompose {
			return rejected(ErrorUnsupportedMethod, "refusing to mutate a shared existing-container deployment during revoke", false, nil)
		}
		spec := e.runtimeSpecFromState(state, paths)
		if outcome := e.runOpenClaw(ctx, e.containerArgsFor(state.Container, "agents", "unbind", "--agent", invocation.AgentID, "--all", "--json")...); outcome != nil {
			state.State = "failed"
			state.UpdatedAt = e.config.Now().Unix()
			state.LastMethod = invocation.Method
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
		if err := e.config.Orchestrator.Delete(ctx, spec); err != nil {
			outcome := failed(ErrorExecutionFailed, err.Error(), true, nil)
			state.State = "failed"
			state.UpdatedAt = e.config.Now().Unix()
			state.LastMethod = invocation.Method
			state.LastReason = errorMessage(outcome)
			return e.persistFailure(invocation, outcome, state, paths)
		}
	}
	if boolParam(invocation.Params, "delete_workspace") {
		if err := removeRuntimeData(paths); err != nil {
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
	if strings.TrimSpace(container) != "" {
		return append([]string{"--container", strings.TrimSpace(container)}, args...)
	}
	return append([]string{}, args...)
}

func (e *Executor) paths(agentID string) localPaths {
	agentRoot := filepath.Join(e.config.Root, "agents", agentID)
	workspace := filepath.Join(agentRoot, "workspace")
	return localPaths{
		AgentRoot:       agentRoot,
		Workspace:       workspace,
		AgentDir:        filepath.Join(agentRoot, "agent"),
		OpenClawDir:     filepath.Join(workspace, ".openclaw"),
		ConfigDir:       filepath.Join(agentRoot, "config"),
		ComposePath:     filepath.Join(agentRoot, "compose.yaml"),
		RuntimeConfig:   filepath.Join(agentRoot, "config", "openclaw.json"),
		RuntimeMetadata: filepath.Join(agentRoot, "config", "bahia-runtime.json"),
		IdentityFile:    filepath.Join(workspace, "IDENTITY.md"),
		SoulFile:        filepath.Join(workspace, "SOUL.md"),
		AgentsFile:      filepath.Join(workspace, "AGENTS.md"),
		MemoryFile:      filepath.Join(workspace, "MEMORY.md"),
		Provenance:      filepath.Join(workspace, ".openclaw", "soulfactory.json"),
		PersonaFile:     filepath.Join(workspace, ".openclaw", "soulfactory-persona.json"),
		State:           filepath.Join(agentRoot, "state.json"),
		LastInvocation:  filepath.Join(agentRoot, "last-invocation.json"),
		LastOutcome:     filepath.Join(agentRoot, "last-outcome.json"),
	}
}

func (e *Executor) pathsForDeployment(agentID, deploymentID string) localPaths {
	paths := e.paths(agentID)
	runtimeRoot := filepath.Join(paths.AgentRoot, "deployments", deploymentID)
	workspace := filepath.Join(runtimeRoot, "workspace")
	paths.RuntimeRoot = runtimeRoot
	paths.Workspace = workspace
	paths.AgentDir = filepath.Join(runtimeRoot, "agent")
	paths.OpenClawDir = filepath.Join(workspace, ".openclaw")
	paths.ConfigDir = filepath.Join(runtimeRoot, "config")
	paths.ComposePath = filepath.Join(runtimeRoot, "compose.yaml")
	paths.RuntimeConfig = filepath.Join(runtimeRoot, "config", "openclaw.json")
	paths.RuntimeMetadata = filepath.Join(runtimeRoot, "config", "bahia-runtime.json")
	paths.IdentityFile = filepath.Join(workspace, "IDENTITY.md")
	paths.SoulFile = filepath.Join(workspace, "SOUL.md")
	paths.AgentsFile = filepath.Join(workspace, "AGENTS.md")
	paths.MemoryFile = filepath.Join(workspace, "MEMORY.md")
	paths.Provenance = filepath.Join(workspace, ".openclaw", "soulfactory.json")
	paths.PersonaFile = filepath.Join(workspace, ".openclaw", "soulfactory-persona.json")
	return paths
}

func (e *Executor) pathsForState(state State) localPaths {
	if state.RuntimeMode == RuntimeModePerAgentCompose && state.DeploymentID != "" {
		return e.pathsForDeployment(state.AgentID, state.DeploymentID)
	}
	return e.paths(state.AgentID)
}

type localPaths struct {
	AgentRoot       string
	RuntimeRoot     string
	Workspace       string
	AgentDir        string
	OpenClawDir     string
	ConfigDir       string
	ComposePath     string
	RuntimeConfig   string
	RuntimeMetadata string
	IdentityFile    string
	SoulFile        string
	AgentsFile      string
	MemoryFile      string
	Provenance      string
	PersonaFile     string
	State           string
	LastInvocation  string
	LastOutcome     string
}

func (e *Executor) baseWarnings() []string {
	var warnings []string
	if e.config.DryRun {
		warnings = append(warnings, "dry-run mode recorded local state without invoking OpenClaw CLI")
	}
	if e.config.DryRun && e.config.RuntimeMode == RuntimeModePerAgentCompose {
		warnings = append(warnings, "dry-run rendered the dedicated runtime specification without creating a container")
	}
	return warnings
}

func (e *Executor) resultFromState(state State, observedAt int64) map[string]interface{} {
	return map[string]interface{}{
		"agent_id":                  state.AgentID,
		"runtime":                   string(domain.RuntimeTargetOpenClaw),
		"runtime_binding":           state.RuntimeBinding,
		"state":                     state.State,
		"spec_hash":                 state.SpecHash,
		"workspace":                 state.Workspace,
		"workspace_path_identifier": state.WorkspaceID,
		"agent_dir":                 state.AgentDir,
		"runtime_mode":              state.RuntimeMode,
		"deployment_id":             state.DeploymentID,
		"container_id":              state.ContainerID,
		"container":                 state.Container,
		"image_digest":              state.ImageDigest,
		"source_commit":             state.SourceCommit,
		"config_revision":           state.ConfigRevision,
		"account_id":                state.AccountID,
		"model":                     state.Model,
		"observed_at":               observedAt,
		"warnings":                  append([]string{}, state.Warnings...),
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
	if strings.HasSuffix(normalized, "ref") || strings.HasSuffix(normalized, "uri") || strings.HasSuffix(normalized, "url") || strings.HasSuffix(normalized, "file") || strings.HasSuffix(normalized, "files") || strings.HasSuffix(normalized, "path") {
		return false
	}
	for _, marker := range []string{"privatekey", "secretkey", "nsec", "seedphrase", "mnemonic", "apikey", "password", "token"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return strings.HasSuffix(normalized, "secret")
}

func looksLikeInlinePrivateSecretValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(normalized, "nsec1") {
		return true
	}
	if strings.Contains(normalized, "-----begin") && strings.Contains(normalized, "private key") {
		return true
	}
	if strings.Contains(normalized, "secret=") || strings.Contains(normalized, "connect_secret=") {
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

func intFromString(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
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

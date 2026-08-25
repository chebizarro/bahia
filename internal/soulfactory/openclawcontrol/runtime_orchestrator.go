package openclawcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	runtimeOwnerLabel          = "ai.openagents.bahia.owner"
	runtimeAgentLabel          = "ai.openagents.bahia.agent-id"
	runtimeSoulLabel           = "ai.openagents.bahia.soul-id"
	runtimeSpecLabel           = "ai.openagents.bahia.spec-hash"
	runtimeRequestLabel        = "ai.openagents.bahia.request-id"
	runtimeRunLabel            = "ai.openagents.bahia.run-id"
	runtimeDeploymentLabel     = "ai.openagents.bahia.deployment-id"
	runtimeImageLabel          = "ai.openagents.bahia.image-digest"
	runtimeSourceCommitLabel   = "ai.openagents.bahia.source-commit"
	runtimeConfigRevisionLabel = "ai.openagents.bahia.config-revision"
	runtimeOwnerValue          = "openclaw-soulfactory"
	containerWorkspace         = "/home/node/.openclaw/workspace"
	containerStateDir          = "/home/node/.openclaw"
	containerAgentDir          = "/home/node/.openclaw/agent"
)

var (
	immutableImagePattern = regexp.MustCompile(`^[^[:space:]@]+@sha256:[a-f0-9]{64}$`)
	sourceCommitPattern   = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
	resourcePartPattern   = regexp.MustCompile(`[^a-z0-9]+`)
)

type RuntimeSpec struct {
	DeploymentID       string
	ContainerName      string
	AgentID            string
	SoulID             string
	AccountID          string
	Model              string
	RequestID          string
	RunID              string
	SpecHash           string
	ImageDigest        string
	SourceCommit       string
	ConfigRevision     string
	ComposePath        string
	ConfigDir          string
	Workspace          string
	AgentDir           string
	SecretFiles        map[string]string
	PluginIDs          []string
	PluginRequirements []string
	DefaultBindings    []string
	CPUs               string
	Memory             string
	PIDsLimit          int
	User               string
}

type RuntimeLineage struct {
	DeploymentID   string
	ContainerID    string
	ContainerName  string
	ImageDigest    string
	ConfigRevision string
	WorkspaceID    string
	AgentID        string
	AccountID      string
	Health         string
}

type RuntimeInspection struct {
	Exists  bool
	Running bool
	Lineage RuntimeLineage
	Labels  map[string]string
}

type RuntimeOrchestrator interface {
	Inspect(context.Context, RuntimeSpec) (RuntimeInspection, error)
	Reconcile(context.Context, RuntimeSpec) (RuntimeLineage, error)
	Delete(context.Context, RuntimeSpec) error
}

type DockerComposeOrchestrator struct {
	DockerBin string
	Runner    CommandRunner
}

type dockerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
		Health  *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

func (o DockerComposeOrchestrator) Inspect(ctx context.Context, spec RuntimeSpec) (RuntimeInspection, error) {
	out, err := o.Runner.Run(ctx, o.DockerBin, "inspect", "--type", "container", spec.ContainerName)
	if err != nil {
		if isDockerNotFound(err) {
			return RuntimeInspection{}, nil
		}
		return RuntimeInspection{}, fmt.Errorf("inspect dedicated OpenClaw container: %w", err)
	}
	var records []dockerInspect
	if err := json.Unmarshal(out, &records); err != nil {
		return RuntimeInspection{}, fmt.Errorf("decode dedicated OpenClaw container inspection: %w", err)
	}
	if len(records) != 1 {
		return RuntimeInspection{}, fmt.Errorf("expected exactly one container inspection, got %d", len(records))
	}
	record := records[0]
	if record.Config.Image != spec.ImageDigest {
		return RuntimeInspection{}, fmt.Errorf("dedicated OpenClaw resource ownership/spec conflict for image reference")
	}
	health := "none"
	if record.State.Health != nil {
		health = record.State.Health.Status
	}
	return RuntimeInspection{
		Exists:  true,
		Running: record.State.Running,
		Labels:  record.Config.Labels,
		Lineage: RuntimeLineage{
			DeploymentID:   record.Config.Labels[runtimeDeploymentLabel],
			ContainerID:    strings.TrimPrefix(record.ID, "sha256:"),
			ContainerName:  strings.TrimPrefix(record.Name, "/"),
			ImageDigest:    record.Config.Labels[runtimeImageLabel],
			ConfigRevision: record.Config.Labels[runtimeConfigRevisionLabel],
			WorkspaceID:    "workspace://" + spec.DeploymentID,
			AgentID:        record.Config.Labels[runtimeAgentLabel],
			AccountID:      spec.AccountID,
			Health:         health,
		},
	}, nil
}

func (o DockerComposeOrchestrator) Reconcile(ctx context.Context, spec RuntimeSpec) (RuntimeLineage, error) {
	if err := validateRuntimeSpec(spec); err != nil {
		return RuntimeLineage{}, err
	}
	inspection, err := o.Inspect(ctx, spec)
	if err != nil {
		return RuntimeLineage{}, err
	}
	if inspection.Exists {
		if err := verifyRuntimeOwnership(spec, inspection.Labels); err != nil {
			return RuntimeLineage{}, err
		}
		if inspection.Running && inspection.Lineage.Health == "healthy" {
			return inspection.Lineage, nil
		}
	}
	if _, err := o.Runner.Run(ctx, o.DockerBin, "compose", "--project-name", spec.DeploymentID, "--file", spec.ComposePath, "up", "--detach", "--wait", "--wait-timeout", "120"); err != nil {
		return RuntimeLineage{}, fmt.Errorf("reconcile dedicated OpenClaw deployment: %w", err)
	}
	inspection, err = o.Inspect(ctx, spec)
	if err != nil {
		return RuntimeLineage{}, err
	}
	if !inspection.Exists || !inspection.Running || inspection.Lineage.Health != "healthy" {
		return RuntimeLineage{}, fmt.Errorf("dedicated OpenClaw deployment did not become healthy")
	}
	if err := verifyRuntimeOwnership(spec, inspection.Labels); err != nil {
		return RuntimeLineage{}, err
	}
	return inspection.Lineage, nil
}

func (o DockerComposeOrchestrator) Delete(ctx context.Context, spec RuntimeSpec) error {
	inspection, err := o.Inspect(ctx, spec)
	if err != nil {
		return err
	}
	if !inspection.Exists {
		return nil
	}
	if err := verifyRuntimeOwnership(spec, inspection.Labels); err != nil {
		return err
	}
	if _, err := o.Runner.Run(ctx, o.DockerBin, "compose", "--project-name", spec.DeploymentID, "--file", spec.ComposePath, "down", "--remove-orphans", "--timeout", "30"); err != nil {
		return fmt.Errorf("delete dedicated OpenClaw deployment: %w", err)
	}
	return nil
}

func validateRuntimeSpec(spec RuntimeSpec) error {
	if !immutableImagePattern.MatchString(spec.ImageDigest) {
		return fmt.Errorf("OpenClaw image must be pinned by immutable OCI digest")
	}
	if !sourceCommitPattern.MatchString(spec.SourceCommit) {
		return fmt.Errorf("OpenClaw source commit must be a 40 to 64 character lowercase hexadecimal commit")
	}
	for name, value := range map[string]string{
		"deployment id": spec.DeploymentID, "container name": spec.ContainerName,
		"agent id": spec.AgentID, "soul id": spec.SoulID, "request id": spec.RequestID,
		"run id": spec.RunID, "spec hash": spec.SpecHash, "config revision": spec.ConfigRevision,
		"model": spec.Model,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required for dedicated OpenClaw runtime", name)
		}
	}
	if spec.CPUs == "" || spec.Memory == "" || spec.PIDsLimit <= 0 || spec.User == "" {
		return fmt.Errorf("positive CPU, memory, and PID limits are required for dedicated OpenClaw runtime")
	}
	return nil
}

func verifyRuntimeOwnership(spec RuntimeSpec, labels map[string]string) error {
	expected := runtimeLabels(spec)
	for key, value := range expected {
		if labels[key] != value {
			return fmt.Errorf("dedicated OpenClaw resource ownership/spec conflict for label %s", key)
		}
	}
	return nil
}

func runtimeLabels(spec RuntimeSpec) map[string]string {
	return map[string]string{
		runtimeOwnerLabel:          runtimeOwnerValue,
		runtimeAgentLabel:          spec.AgentID,
		runtimeSoulLabel:           spec.SoulID,
		runtimeSpecLabel:           spec.SpecHash,
		runtimeRequestLabel:        spec.RequestID,
		runtimeRunLabel:            spec.RunID,
		runtimeDeploymentLabel:     spec.DeploymentID,
		runtimeImageLabel:          spec.ImageDigest,
		runtimeSourceCommitLabel:   spec.SourceCommit,
		runtimeConfigRevisionLabel: spec.ConfigRevision,
	}
}

func isDockerNotFound(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such object") || strings.Contains(message, "no such container")
}

func deterministicRuntimeName(agentID, requestID, runID string) string {
	prefix := strings.Trim(resourcePartPattern.ReplaceAllString(strings.ToLower(agentID), "-"), "-")
	if prefix == "" {
		prefix = "agent"
	}
	if len(prefix) > 32 {
		prefix = prefix[:32]
	}
	sum := sha256.Sum256([]byte(agentID + "\x00" + requestID + "\x00" + runID))
	return "bahia-ocw-" + prefix + "-" + hex.EncodeToString(sum[:8])
}

func renderCompose(spec RuntimeSpec) []byte {
	labels := runtimeLabels(spec)
	labelKeys := make([]string, 0, len(labels))
	for key := range labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	var b strings.Builder
	b.WriteString("services:\n  gateway:\n")
	fmt.Fprintf(&b, "    image: %s\n    container_name: %s\n", strconv.Quote(spec.ImageDigest), strconv.Quote(spec.ContainerName))
	fmt.Fprintf(&b, "    user: %s\n", strconv.Quote(spec.User))
	b.WriteString("    labels:\n")
	for _, key := range labelKeys {
		fmt.Fprintf(&b, "      %s: %s\n", strconv.Quote(key), strconv.Quote(labels[key]))
	}
	b.WriteString("    environment:\n")
	b.WriteString("      HOME: /home/node\n      OPENCLAW_HOME: /home/node\n      OPENCLAW_STATE_DIR: /home/node/.openclaw\n      OPENCLAW_CONFIG_PATH: /home/node/.openclaw/openclaw.json\n      OPENCLAW_WORKSPACE_DIR: /home/node/.openclaw/workspace\n")
	b.WriteString("    volumes:\n")
	writeBindMount(&b, spec.ConfigDir, containerStateDir, false)
	writeBindMount(&b, spec.Workspace, containerWorkspace, false)
	writeBindMount(&b, spec.AgentDir, containerAgentDir, false)
	secretNames := make([]string, 0, len(spec.SecretFiles))
	for name := range spec.SecretFiles {
		secretNames = append(secretNames, name)
	}
	sort.Strings(secretNames)
	for _, name := range secretNames {
		writeBindMount(&b, spec.SecretFiles[name], "/run/secrets/"+name, true)
	}
	fmt.Fprintf(&b, "    cpus: %s\n    mem_limit: %s\n    pids_limit: %d\n", strconv.Quote(spec.CPUs), strconv.Quote(spec.Memory), spec.PIDsLimit)
	b.WriteString("    init: true\n    restart: unless-stopped\n    cap_drop: [NET_RAW, NET_ADMIN]\n    security_opt: [no-new-privileges:true]\n    read_only: true\n    tmpfs:\n      - /tmp:rw,noexec,nosuid,size=64m\n")
	b.WriteString("    logging:\n      driver: json-file\n      options:\n        max-size: 10m\n        max-file: \"3\"\n")
	b.WriteString("    command: [\"node\", \"dist/index.js\", \"gateway\", \"--bind\", \"lan\", \"--port\", \"18789\"]\n")
	b.WriteString("    healthcheck:\n      test: [\"CMD\", \"node\", \"-e\", \"fetch('http://127.0.0.1:18789/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))\"]\n      interval: 10s\n      timeout: 5s\n      retries: 12\n      start_period: 20s\n")
	b.WriteString("    networks: [soul]\nnetworks:\n  soul:\n    driver: bridge\n")
	return []byte(b.String())
}

func writeBindMount(b *strings.Builder, source, target string, readOnly bool) {
	fmt.Fprintf(b, "      - type: bind\n        source: %s\n        target: %s\n", strconv.Quote(source), strconv.Quote(target))
	if readOnly {
		b.WriteString("        read_only: true\n")
	}
}

func validateSecretFiles(files map[string]string) error {
	for name, path := range files {
		if !agentIDPattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("secret file name %q is unsafe", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat secret file %q: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("secret file %q must be regular and mode 0600 or stricter", name)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
			return fmt.Errorf("secret file %q must be owned by the runtime orchestrator user", name)
		}
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not an owned directory", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the runtime orchestrator user", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be mode 0700 or stricter", path)
	}
	return nil
}

func secretFilesFromParams(params map[string]interface{}) (map[string]string, error) {
	runtimeParams, _ := params["runtime"].(map[string]interface{})
	raw, ok := runtimeParams["secret_files"].(map[string]interface{})
	if !ok {
		return map[string]string{}, nil
	}
	files := make(map[string]string, len(raw))
	for name, value := range raw {
		path, ok := value.(string)
		if !ok || !filepath.IsAbs(strings.TrimSpace(path)) {
			return nil, fmt.Errorf("runtime.secret_files.%s must be an absolute file path", name)
		}
		files[name] = filepath.Clean(path)
	}
	if err := validateSecretFiles(files); err != nil {
		return nil, err
	}
	return files, nil
}

func runtimeConfigRevision(revisionInput []byte) string {
	sum := sha256.Sum256(revisionInput)
	return "sha256:" + hex.EncodeToString(sum[:])
}

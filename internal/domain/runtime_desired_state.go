// Package domain defines the core domain types for Bahia.
package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// DesiredStateSchemaVersion is the current schema version for desired-state serialization.
// Bump this when the canonical hash inputs change.
const DesiredStateSchemaVersion = "1"

// ---------------------------------------------------------------------------
// Secret handling types
// ---------------------------------------------------------------------------

// DesiredSecretRef is a safe reference to a secret-backed environment variable
// in the desired state. Plaintext secret values are NEVER stored in persisted
// desired state; only the reference metadata and a redacted placeholder are kept.
type DesiredSecretRef struct {
	// EnvVar is the environment variable name this secret populates.
	EnvVar string `json:"env_var"`
	// Name is the secret's logical name (e.g. "DB_PASSWORD").
	Name string `json:"name"`
	// SecretID is the ID of the ServiceSecret this ref points to.
	SecretID uuid.UUID `json:"secret_id"`
	// RedactedValue is a safe placeholder shown in logs and projections.
	RedactedValue string `json:"redacted_value"`
}

// RedactedPlaceholder returns the canonical redacted placeholder for a secret name.
func RedactedPlaceholder(name string) string {
	return fmt.Sprintf("REDACTED(%s)", name)
}

// ---------------------------------------------------------------------------
// Healthcheck
// ---------------------------------------------------------------------------

// HealthcheckConfig is the portable healthcheck specification shared across
// renderers. Renderer-specific overrides live in the extension types.
type HealthcheckConfig struct {
	Test        []string `json:"test"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	Retries     int      `json:"retries,omitempty"`
	StartPeriod string   `json:"start_period,omitempty"`
}

// ---------------------------------------------------------------------------
// Extension types
// ---------------------------------------------------------------------------

// ComposeExtension carries Compose-specific rendering metadata that is not
// portable across runtimes.
type ComposeExtension struct {
	// DependsOn maps service keys to dependency conditions for Compose depends_on.
	DependsOn map[string]ComposeDependency `json:"depends_on,omitempty"`
	// HealthcheckOverride allows Compose-specific healthcheck tuning.
	HealthcheckOverride *HealthcheckConfig `json:"healthcheck_override,omitempty"`
	// EnvFile records the generated env-file path relative to the compose dir.
	EnvFile string `json:"env_file,omitempty"`
	// ProjectName is the explicit Compose project name.
	ProjectName string `json:"project_name,omitempty"`
	// Networks lists Compose network declarations needed by this service.
	Networks []string `json:"networks,omitempty"`
	// VolumeDeclarations lists named volume declarations for the project.
	VolumeDeclarations []string `json:"volume_declarations,omitempty"`
}

// ComposeDependency describes a single depends_on entry with its condition.
type ComposeDependency struct {
	Condition string `json:"condition,omitempty"` // e.g. "service_healthy", "service_started"
}

// DockerExtension carries Docker Engine-specific configuration that is not
// portable across runtimes.
type DockerExtension struct {
	// HostConfig is raw Docker host-config fields (binds, resources, etc.).
	HostConfig map[string]any `json:"host_config,omitempty"`
	// NetworkingConfig is raw Docker networking config (endpoints, aliases).
	NetworkingConfig map[string]any `json:"networking_config,omitempty"`
	// VolumeOptions carries volume driver/label options for ensured volumes.
	VolumeOptions map[string]any `json:"volume_options,omitempty"`
	// Healthcheck carries Engine-specific healthcheck config that differs from
	// the portable HealthcheckConfig (e.g. []string test format differences).
	Healthcheck map[string]any `json:"healthcheck,omitempty"`
}

// ---------------------------------------------------------------------------
// Network and Volume specs for resource ensure
// ---------------------------------------------------------------------------

// NetworkSpec describes a Docker network that must exist before container
// creation. The ensure helpers inspect existing networks and create missing
// ones, erroring on incompatible existing resources.
type NetworkSpec struct {
	// Name is the Docker network name.
	Name string `json:"name"`
	// Driver is the network driver (e.g. "bridge", "overlay"). Empty means
	// the Docker daemon default (bridge).
	Driver string `json:"driver,omitempty"`
	// Options are driver-specific options (e.g. subnet, gateway).
	Options map[string]string `json:"options,omitempty"`
	// Labels are user-defined metadata applied to the network.
	Labels map[string]string `json:"labels,omitempty"`
}

// VolumeSpec describes a Docker named volume that must exist before container
// creation. The ensure helpers inspect existing volumes and create missing
// ones, erroring on incompatible existing resources.
type VolumeSpec struct {
	// Name is the Docker volume name.
	Name string `json:"name"`
	// Driver is the volume driver (e.g. "local"). Empty means the Docker
	// daemon default (local).
	Driver string `json:"driver,omitempty"`
	// DriverOpts are driver-specific options.
	DriverOpts map[string]string `json:"driver_opts,omitempty"`
	// Labels are user-defined metadata applied to the volume.
	Labels map[string]string `json:"labels,omitempty"`
}

// KubernetesExtension is a placeholder for future Kubernetes renderer data.
type KubernetesExtension struct{}

// PodmanExtension is a placeholder for future Podman renderer data.
// Podman reuses the Docker-compatible path where feasible.
type PodmanExtension struct{}

// ---------------------------------------------------------------------------
// DesiredServiceSpec — service-level desired state
// ---------------------------------------------------------------------------

// DesiredServiceSpec is the typed desired runtime state for a single managed
// service in an environment. It carries enough information to render the
// service for Compose, Docker Engine, and future runtimes.
type DesiredServiceSpec struct {
	// SchemaVersion tracks the serialization format for hash stability.
	SchemaVersion string `json:"schema_version"`

	// Identity
	ServiceID     uuid.UUID `json:"service_id"`
	EnvironmentID uuid.UUID `json:"environment_id"`
	ArtifactID    uuid.UUID `json:"artifact_id"`

	// StableServiceKey is the normalized runtime name derived from
	// Service.RuntimeTargetName(). It is safe for Compose service names,
	// Docker container names, and filesystem paths.
	StableServiceKey string `json:"stable_service_key"`

	// Image
	ImageRef string `json:"image_ref"`

	// Process
	Command    []string `json:"command,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkDir    string   `json:"work_dir,omitempty"`

	// Environment — literal values only; secrets are in SecretRefs.
	Env map[string]string `json:"env,omitempty"`
	// SecretRefs are references to secret-backed env vars. Plaintext is never persisted.
	SecretRefs []DesiredSecretRef `json:"secret_refs,omitempty"`

	// Resources
	Ports   []string          `json:"ports,omitempty"`
	Volumes []string          `json:"volumes,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`

	// Health
	Healthcheck *HealthcheckConfig `json:"healthcheck,omitempty"`

	// Dependencies and policies
	DependsOn     []string `json:"depends_on,omitempty"`
	NetworkMode   string   `json:"network_mode,omitempty"`
	RestartPolicy string   `json:"restart_policy,omitempty"`
	PullPolicy    string   `json:"pull_policy,omitempty"`

	// DesiredHash is the deterministic hash of the canonical service state.
	// It is computed from the hash-relevant fields and used for drift detection.
	DesiredHash string `json:"desired_hash"`

	// Renderer extensions — at most one is populated per runtime target.
	ComposeExtension    *ComposeExtension    `json:"compose_extension,omitempty"`
	DockerExtension     *DockerExtension     `json:"docker_extension,omitempty"`
	KubernetesExtension *KubernetesExtension `json:"kubernetes_extension,omitempty"`
	PodmanExtension     *PodmanExtension     `json:"podman_extension,omitempty"`
}

// ---------------------------------------------------------------------------
// DesiredEnvironmentPlan — environment-level plan
// ---------------------------------------------------------------------------

// DesiredEnvironmentPlan is the environment-scoped desired runtime state
// containing all managed services. Services are sorted deterministically by
// StableServiceKey for hash stability.
type DesiredEnvironmentPlan struct {
	EnvironmentID uuid.UUID            `json:"environment_id"`
	RevisionHash  string               `json:"revision_hash"`
	Services      []DesiredServiceSpec  `json:"services"`
}

// SortServices sorts the plan's services deterministically by StableServiceKey.
func (p *DesiredEnvironmentPlan) SortServices() {
	sort.Slice(p.Services, func(i, j int) bool {
		return p.Services[i].StableServiceKey < p.Services[j].StableServiceKey
	})
}

// ComputeRevisionHash computes the environment revision hash from the sorted
// service desired hashes. Services are sorted before hashing.
func (p *DesiredEnvironmentPlan) ComputeRevisionHash() string {
	p.SortServices()
	h := sha256.New()
	for _, svc := range p.Services {
		h.Write([]byte(svc.DesiredHash))
	}
	p.RevisionHash = fmt.Sprintf("sha256:%x", h.Sum(nil))
	return p.RevisionHash
}

// ---------------------------------------------------------------------------
// Service key normalization
// ---------------------------------------------------------------------------

// serviceKeyRegexp matches characters that are NOT alphanumeric, hyphen, or underscore.
var serviceKeyRegexp = regexp.MustCompile(`[^a-z0-9_-]+`)

// NormalizeServiceKey normalizes a runtime target name into a string safe for
// Compose service names, Docker container names, and filesystem paths.
// It lowercases, replaces unsafe characters with hyphens, collapses runs,
// and trims leading/trailing hyphens.
func NormalizeServiceKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = serviceKeyRegexp.ReplaceAllString(s, "-")
	// Collapse multiple hyphens.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-_")
	if s == "" {
		return "unnamed"
	}
	return s
}

// NormalizeServiceKeyWithSuffix normalizes a service key and appends a short
// ID suffix when the normalized form is lossy (collides with another key) or
// empty before fallback.
func NormalizeServiceKeyWithSuffix(name string, serviceID uuid.UUID) string {
	base := NormalizeServiceKey(name)
	if base == "unnamed" || base == "" {
		// Use short ID fragment for unnamed services.
		return fmt.Sprintf("svc-%s", serviceID.String()[:8])
	}
	return base
}

// ---------------------------------------------------------------------------
// Canonical hashing
// ---------------------------------------------------------------------------

// hashInput is the canonical structure used for deterministic JSON
// serialization. It contains only hash-relevant fields with sorted keys
// (struct field order is stable in encoding/json). Volatile fields like
// DesiredHash itself, extensions, and timestamps are excluded.
type hashInput struct {
	SchemaVersion    string            `json:"schema_version"`
	ServiceID        uuid.UUID         `json:"service_id"`
	EnvironmentID    uuid.UUID         `json:"environment_id"`
	ArtifactID       uuid.UUID         `json:"artifact_id"`
	StableServiceKey string            `json:"stable_service_key"`
	ImageRef         string            `json:"image_ref"`
	Command          []string          `json:"command"`
	Entrypoint       []string          `json:"entrypoint"`
	WorkDir          string            `json:"work_dir"`
	Env              map[string]string `json:"env"`
	SecretRefKeys    []string          `json:"secret_ref_keys"`
	Ports            []string          `json:"ports"`
	Volumes          []string          `json:"volumes"`
	Labels           map[string]string `json:"labels"`
	Healthcheck      *HealthcheckConfig `json:"healthcheck"`
	DependsOn        []string          `json:"depends_on"`
	NetworkMode      string            `json:"network_mode"`
	RestartPolicy    string            `json:"restart_policy"`
	PullPolicy       string            `json:"pull_policy"`
}

// ComputeDesiredHash computes the deterministic hash of a DesiredServiceSpec.
// The hash covers identity, image, process, environment (keys only for secrets),
// resources, health, and policies. Extensions and the hash field itself are excluded.
//
// Map keys are sorted by encoding/json; slice fields should already be in
// canonical order (ports, volumes sorted lexicographically by the caller).
func (s *DesiredServiceSpec) ComputeDesiredHash() string {
	// Collect secret ref env var names (sorted) — only presence matters for hash.
	secretKeys := make([]string, 0, len(s.SecretRefs))
	for _, ref := range s.SecretRefs {
		secretKeys = append(secretKeys, ref.EnvVar)
	}
	sort.Strings(secretKeys)

	// Normalize nil slices to empty for stable JSON.
	cmd := s.Command
	if cmd == nil {
		cmd = []string{}
	}
	ep := s.Entrypoint
	if ep == nil {
		ep = []string{}
	}
	env := s.Env
	if env == nil {
		env = map[string]string{}
	}
	ports := s.Ports
	if ports == nil {
		ports = []string{}
	}
	sort.Strings(ports)
	volumes := s.Volumes
	if volumes == nil {
		volumes = []string{}
	}
	sort.Strings(volumes)
	labels := s.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	deps := s.DependsOn
	if deps == nil {
		deps = []string{}
	}
	sort.Strings(deps)

	input := hashInput{
		SchemaVersion:    s.SchemaVersion,
		ServiceID:        s.ServiceID,
		EnvironmentID:    s.EnvironmentID,
		ArtifactID:       s.ArtifactID,
		StableServiceKey: s.StableServiceKey,
		ImageRef:         s.ImageRef,
		Command:          cmd,
		Entrypoint:       ep,
		WorkDir:          s.WorkDir,
		Env:              env,
		SecretRefKeys:    secretKeys,
		Ports:            ports,
		Volumes:          volumes,
		Labels:           labels,
		Healthcheck:      s.Healthcheck,
		DependsOn:        deps,
		NetworkMode:      s.NetworkMode,
		RestartPolicy:    s.RestartPolicy,
		PullPolicy:       s.PullPolicy,
	}

	data, err := json.Marshal(input)
	if err != nil {
		// hashInput is fully controlled; marshal should never fail.
		panic(fmt.Sprintf("domain: failed to marshal hash input: %v", err))
	}

	sum := sha256.Sum256(data)
	s.DesiredHash = fmt.Sprintf("sha256:%x", sum[:])
	return s.DesiredHash
}

// ContainsPlaintextSecret returns true if any SecretRef contains a value that
// is not a REDACTED(...) placeholder. This is a safety check to prevent
// accidental persistence of plaintext secrets.
func (s *DesiredServiceSpec) ContainsPlaintextSecret() bool {
	for _, ref := range s.SecretRefs {
		if ref.RedactedValue != "" && !strings.HasPrefix(ref.RedactedValue, "REDACTED(") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Normalized observation — comparable runtime state snapshot
// ---------------------------------------------------------------------------

// NormalizedObservation is a renderer-agnostic snapshot of observed runtime
// state, normalized into a comparable subset for drift detection. It uses the
// same canonical serialization approach as desired-state hashing.
//
// Included: image ref/digest, command, entrypoint, working dir, non-secret
// env, secret env key presence, ports, volumes, restart policy, network
// attachments, and Bahia labels.
//
// Excluded: container IDs, timestamps, ephemeral IPs, Compose-generated
// non-semantic labels, and secret plaintext.
type NormalizedObservation struct {
	// SchemaVersion tracks the serialization format for hash stability.
	SchemaVersion string `json:"schema_version"`

	// Image
	ImageRef    string `json:"image_ref"`
	ImageDigest string `json:"image_digest,omitempty"`

	// Process
	Command    []string `json:"command,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkDir    string   `json:"work_dir,omitempty"`

	// Environment — non-secret literal values only.
	Env map[string]string `json:"env,omitempty"`
	// SecretEnvKeys lists env var names known to be secret-backed.
	// Only key presence is tracked; plaintext is never stored.
	SecretEnvKeys []string `json:"secret_env_keys,omitempty"`

	// Resources
	Ports   []string `json:"ports,omitempty"`
	Volumes []string `json:"volumes,omitempty"`

	// Policies
	RestartPolicy string `json:"restart_policy,omitempty"`

	// Network
	NetworkAttachments []string `json:"network_attachments,omitempty"`

	// Labels — only Bahia-managed labels (bahia.* prefix).
	BahiaLabels map[string]string `json:"bahia_labels,omitempty"`

	// ObservationHash is the deterministic hash of the normalized observation.
	ObservationHash string `json:"observation_hash,omitempty"`
}

// observationHashInput is the canonical structure for deterministic observation
// hashing. It mirrors NormalizedObservation but with explicit nil-to-empty
// normalization. The hash excludes the ObservationHash field itself.
type observationHashInput struct {
	SchemaVersion      string            `json:"schema_version"`
	ImageRef           string            `json:"image_ref"`
	ImageDigest        string            `json:"image_digest"`
	Command            []string          `json:"command"`
	Entrypoint         []string          `json:"entrypoint"`
	WorkDir            string            `json:"work_dir"`
	Env                map[string]string `json:"env"`
	SecretEnvKeys      []string          `json:"secret_env_keys"`
	Ports              []string          `json:"ports"`
	Volumes            []string          `json:"volumes"`
	RestartPolicy      string            `json:"restart_policy"`
	NetworkAttachments []string          `json:"network_attachments"`
	BahiaLabels        map[string]string `json:"bahia_labels"`
}

// ComputeObservationHash computes the deterministic hash of a NormalizedObservation.
// It uses the same canonical serialization approach as ComputeDesiredHash:
// sorted map keys (via encoding/json), sorted slices, and nil-to-empty normalization.
func (o *NormalizedObservation) ComputeObservationHash() string {
	cmd := o.Command
	if cmd == nil {
		cmd = []string{}
	}
	ep := o.Entrypoint
	if ep == nil {
		ep = []string{}
	}
	env := o.Env
	if env == nil {
		env = map[string]string{}
	}

	secretKeys := make([]string, len(o.SecretEnvKeys))
	copy(secretKeys, o.SecretEnvKeys)
	sort.Strings(secretKeys)

	ports := make([]string, len(o.Ports))
	copy(ports, o.Ports)
	sort.Strings(ports)

	volumes := make([]string, len(o.Volumes))
	copy(volumes, o.Volumes)
	sort.Strings(volumes)

	networks := make([]string, len(o.NetworkAttachments))
	copy(networks, o.NetworkAttachments)
	sort.Strings(networks)

	labels := o.BahiaLabels
	if labels == nil {
		labels = map[string]string{}
	}

	input := observationHashInput{
		SchemaVersion:      o.SchemaVersion,
		ImageRef:           o.ImageRef,
		ImageDigest:        o.ImageDigest,
		Command:            cmd,
		Entrypoint:         ep,
		WorkDir:            o.WorkDir,
		Env:                env,
		SecretEnvKeys:      secretKeys,
		Ports:              ports,
		Volumes:            volumes,
		RestartPolicy:      o.RestartPolicy,
		NetworkAttachments: networks,
		BahiaLabels:        labels,
	}

	data, err := json.Marshal(input)
	if err != nil {
		// observationHashInput is fully controlled; marshal should never fail.
		panic(fmt.Sprintf("domain: failed to marshal observation hash input: %v", err))
	}

	sum := sha256.Sum256(data)
	o.ObservationHash = fmt.Sprintf("sha256:%x", sum[:])
	return o.ObservationHash
}

// FilterBahiaLabels returns a new map containing only labels with the "bahia."
// prefix. This is used to strip Compose-generated and other non-semantic labels
// from runtime observations before normalization.
func FilterBahiaLabels(labels map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range labels {
		if strings.HasPrefix(k, "bahia.") {
			result[k] = v
		}
	}
	return result
}

// FilterNonSecretEnv returns a new map with only non-secret env vars and a
// sorted slice of secret env var keys. The secretNames set identifies which
// env var names are secret-backed.
func FilterNonSecretEnv(env map[string]string, secretNames map[string]bool) (nonSecret map[string]string, secretKeys []string) {
	nonSecret = make(map[string]string)
	for k, v := range env {
		if secretNames[k] {
			secretKeys = append(secretKeys, k)
		} else {
			nonSecret[k] = v
		}
	}
	sort.Strings(secretKeys)
	return nonSecret, secretKeys
}

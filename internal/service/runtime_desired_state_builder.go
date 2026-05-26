package service

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// DesiredStateBuilder constructs a canonical DesiredServiceSpec from
// service, environment, artifact, runtime config, and resolved secrets.
// It is the single point of truth for building desired service state.
type DesiredStateBuilder struct{}

// NewDesiredStateBuilder creates a new DesiredStateBuilder.
func NewDesiredStateBuilder() *DesiredStateBuilder {
	return &DesiredStateBuilder{}
}

// BuildInput groups all inputs needed to construct a DesiredServiceSpec.
type BuildInput struct {
	Service       *domain.Service
	Environment   *domain.Environment
	Artifact      *domain.Artifact
	RuntimeConfig *domain.ServiceRuntimeConfig
	// Secrets are decrypted secret name→value pairs resolved for this
	// service+environment. The builder will separate them into SecretRefs
	// and will NEVER include plaintext values in the returned spec.
	Secrets []domain.ServiceSecret
}

// Build constructs a canonical DesiredServiceSpec from the provided inputs.
// Secret plaintext is never included in the returned spec — only safe
// DesiredSecretRef entries with redacted placeholders.
func (b *DesiredStateBuilder) Build(input BuildInput) (*domain.DesiredServiceSpec, error) {
	if input.Service == nil {
		return nil, fmt.Errorf("service is required")
	}
	if input.Environment == nil {
		return nil, fmt.Errorf("environment is required")
	}
	if input.Artifact == nil {
		return nil, fmt.Errorf("artifact is required")
	}

	// Resolve stable service key from RuntimeTargetName, normalized for
	// Compose/Docker names.
	stableKey := domain.NormalizeServiceKeyWithSuffix(
		input.Service.RuntimeTargetName(),
		input.Service.ID,
	)

	// Build image reference from artifact.
	imageRef := imageRefForArtifact(input.Artifact)

	// Resolve process fields from adopted runtime config.
	var (
		command    []string
		entrypoint []string
		workDir    string
		restart    string
		networkMode string
		ports      []string
		volumes    []string
		labels     map[string]string
		envLiterals map[string]string
	)

	adopted := adoptedConfig(input.RuntimeConfig)
	if adopted != nil {
		command = copySlice(adopted.Command)
		entrypoint = copySlice(adopted.Entrypoint)
		workDir = adopted.WorkingDir
		restart = adopted.Restart
		networkMode = adopted.NetworkMode
		ports = copySlice(adopted.Ports)
		volumes = copySlice(adopted.Volumes)
		labels = copyStringMap(adopted.Labels)
		envLiterals = copyStringMap(adopted.Environment)
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	if envLiterals == nil {
		envLiterals = make(map[string]string)
	}

	// Split env into literals vs secret refs. Secret names that exist
	// in the environment map are removed from literals.
	secretNames := make(map[string]bool, len(input.Secrets))
	for _, s := range input.Secrets {
		if s.Name != "" {
			secretNames[s.Name] = true
		}
	}

	// Remove secret-backed keys from literals.
	for k := range envLiterals {
		if secretNames[k] {
			delete(envLiterals, k)
		}
	}

	// Build secret refs — never include plaintext.
	secretRefs := make([]domain.DesiredSecretRef, 0, len(input.Secrets))
	for _, s := range input.Secrets {
		if s.Name == "" {
			continue
		}
		secretRefs = append(secretRefs, domain.DesiredSecretRef{
			EnvVar:        s.Name,
			Name:          s.Name,
			SecretID:      s.ID,
			RedactedValue: domain.RedactedPlaceholder(s.Name),
		})
	}
	// Sort secret refs for deterministic output.
	sort.Slice(secretRefs, func(i, j int) bool {
		return secretRefs[i].EnvVar < secretRefs[j].EnvVar
	})

	// Inject Bahia labels — these are always present for managed services.
	labels["bahia.managed"] = "true"
	labels["bahia.service_id"] = input.Service.ID.String()
	labels["bahia.environment_id"] = input.Environment.ID.String()
	labels["bahia.artifact_id"] = input.Artifact.ID.String()
	// bahia.desired_hash is set after hash computation below.

	// Sort ports and volumes for deterministic hashing.
	sort.Strings(ports)
	sort.Strings(volumes)

	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        input.Service.ID,
		EnvironmentID:    input.Environment.ID,
		ArtifactID:       input.Artifact.ID,
		StableServiceKey: stableKey,
		ImageRef:         imageRef,
		Command:          command,
		Entrypoint:       entrypoint,
		WorkDir:          workDir,
		Env:              envLiterals,
		SecretRefs:       secretRefs,
		Ports:            ports,
		Volumes:          volumes,
		Labels:           labels,
		RestartPolicy:    restart,
		NetworkMode:      networkMode,
	}

	// Build renderer extensions based on runtime type.
	buildRendererExtensions(spec, input.Service, adopted)

	// Compute desired hash from canonical fields.
	spec.ComputeDesiredHash()

	// Set the hash label now that we have it.
	spec.Labels["bahia.desired_hash"] = spec.DesiredHash

	return spec, nil
}

// buildRendererExtensions populates the appropriate renderer extension
// based on the service's RuntimeType.
func buildRendererExtensions(spec *domain.DesiredServiceSpec, svc *domain.Service, adopted *domain.AdoptedRuntimeConfig) {
	switch svc.RuntimeType {
	case domain.RuntimeTypeCompose:
		ext := &domain.ComposeExtension{}
		if adopted != nil && adopted.Compose != nil {
			ext.ProjectName = adopted.Compose.ProjectName
		}
		spec.ComposeExtension = ext

	case domain.RuntimeTypeDocker:
		spec.DockerExtension = &domain.DockerExtension{}

	case domain.RuntimeTypePodman:
		spec.PodmanExtension = &domain.PodmanExtension{}

	case domain.RuntimeTypeK8s:
		spec.KubernetesExtension = &domain.KubernetesExtension{}
	}
}

// adoptedConfig safely extracts the AdoptedRuntimeConfig, returning nil if absent.
func adoptedConfig(cfg *domain.ServiceRuntimeConfig) *domain.AdoptedRuntimeConfig {
	if cfg == nil {
		return nil
	}
	return cfg.Adopted
}

// copySlice returns a shallow copy of a string slice (nil-safe).
func copySlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// SecretValueMap builds a name→plaintext map from decrypted secrets.
// This is a convenience for runtime adapters that need resolved values
// in memory during apply. The map should NEVER be persisted.
func SecretValueMap(secrets []domain.ServiceSecret, decrypt func([]byte, domain.EncryptionMethod) (string, error)) (map[string]string, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(secrets))
	for _, s := range secrets {
		if s.Name == "" {
			continue
		}
		val, err := decrypt(s.EncryptedValue, s.EncryptionMethod)
		if err != nil {
			return nil, fmt.Errorf("decrypting secret %q: %w", s.Name, err)
		}
		m[s.Name] = val
	}
	return m, nil
}

// ValidateSpec performs safety checks on a built spec:
// - Ensures no plaintext secrets leaked into the spec
// - Ensures required Bahia labels are present
// - Ensures desired hash is computed
func ValidateSpec(spec *domain.DesiredServiceSpec) error {
	if spec == nil {
		return fmt.Errorf("spec is nil")
	}
	if spec.ContainsPlaintextSecret() {
		return fmt.Errorf("spec contains plaintext secret values — this is a safety violation")
	}
	if spec.DesiredHash == "" {
		return fmt.Errorf("spec has no desired hash")
	}

	requiredLabels := []string{
		"bahia.managed",
		"bahia.service_id",
		"bahia.environment_id",
		"bahia.artifact_id",
		"bahia.desired_hash",
	}
	for _, label := range requiredLabels {
		if v, ok := spec.Labels[label]; !ok || v == "" {
			return fmt.Errorf("required Bahia label %q is missing or empty", label)
		}
	}

	if spec.Labels["bahia.managed"] != "true" {
		return fmt.Errorf("bahia.managed label must be \"true\"")
	}

	// Verify IDs in labels match spec fields.
	if spec.Labels["bahia.service_id"] != spec.ServiceID.String() {
		return fmt.Errorf("bahia.service_id label does not match spec ServiceID")
	}
	if spec.Labels["bahia.environment_id"] != spec.EnvironmentID.String() {
		return fmt.Errorf("bahia.environment_id label does not match spec EnvironmentID")
	}
	if spec.Labels["bahia.artifact_id"] != spec.ArtifactID.String() {
		return fmt.Errorf("bahia.artifact_id label does not match spec ArtifactID")
	}
	if spec.Labels["bahia.desired_hash"] != spec.DesiredHash {
		return fmt.Errorf("bahia.desired_hash label does not match spec DesiredHash")
	}

	return nil
}

// newUUID is a test helper alias; production code uses uuid.New() directly.
var newUUID = uuid.New

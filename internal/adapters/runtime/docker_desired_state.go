package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Container naming
// ---------------------------------------------------------------------------

// BahiaContainerName returns the stable Docker container name for a desired
// service spec. The name is derived from the stable service key with an
// environment-scoped prefix to avoid collisions across environments.
func BahiaContainerName(spec *domain.DesiredServiceSpec) string {
	if spec == nil {
		return ""
	}
	// Use short environment ID prefix (first 8 chars) + stable key.
	envPrefix := spec.EnvironmentID.String()[:8]
	return fmt.Sprintf("bahia-%s-%s", envPrefix, spec.StableServiceKey)
}

// ---------------------------------------------------------------------------
// Managed container lookup
// ---------------------------------------------------------------------------

// FindBahiaManagedContainer locates an existing Bahia-managed container for
// the given service+environment by preferring label-based lookup over name
// matching.
//
// Lookup order:
//  1. Filter by bahia.service_id + bahia.environment_id labels
//  2. Fall back to container name matching via BahiaContainerName
//
// Returns nil (no error) when no managed container is found.
func FindBahiaManagedContainer(ctx context.Context, observer *DockerObserver, spec *domain.DesiredServiceSpec) (*DockerContainer, error) {
	if observer == nil || spec == nil {
		return nil, nil
	}

	// Prefer label-based lookup: bahia.service_id + bahia.environment_id.
	containers, err := listContainersByBahiaLabels(ctx, observer, spec.ServiceID.String(), spec.EnvironmentID.String())
	if err != nil {
		return nil, fmt.Errorf("label-based container lookup: %w", err)
	}
	if len(containers) > 0 {
		return &containers[0], nil
	}

	// Fall back to name-based lookup using the stable container name.
	containerName := BahiaContainerName(spec)
	containers, err = observer.listAllContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("name-based container lookup: %w", err)
	}
	for i := range containers {
		if dockerContainerNameMatches(containers[i].Names, containerName) {
			return &containers[i], nil
		}
	}

	return nil, nil
}

// listContainersByBahiaLabels lists containers matching both bahia.service_id
// and bahia.environment_id labels.
func listContainersByBahiaLabels(ctx context.Context, observer *DockerObserver, serviceID, environmentID string) ([]DockerContainer, error) {
	query := url.Values{}
	query.Set("all", "1")
	// Docker API supports multiple label filters (AND semantics).
	filtersMap := map[string][]string{
		"label": {
			"bahia.service_id=" + serviceID,
			"bahia.environment_id=" + environmentID,
		},
	}
	filtersJSON, err := marshalJSON(filtersMap)
	if err != nil {
		return nil, err
	}
	query.Set("filters", string(filtersJSON))
	return observer.listContainersRaw(ctx, query)
}

// ---------------------------------------------------------------------------
// Config mapping
// ---------------------------------------------------------------------------

// DockerContainerConfigs holds the three Docker Engine API config objects
// needed to create a container.
type DockerContainerConfigs struct {
	// ContainerConfig is the Docker container configuration (image, env, cmd,
	// labels, exposed ports, healthcheck, working dir, entrypoint).
	ContainerConfig map[string]any

	// HostConfig is the Docker host configuration (binds, port bindings,
	// restart policy, network mode).
	HostConfig map[string]any

	// NetworkingConfig is the Docker networking configuration (endpoint
	// settings with aliases).
	NetworkingConfig map[string]any
}

// MapDesiredSpecToContainerConfig deterministically maps a DesiredServiceSpec
// and resolved secrets into Docker Engine container, host, and networking
// configs suitable for the /containers/create API.
//
// Secrets are injected into env at apply time only — they are never persisted
// in the desired-state plan. The secrets map is keyed by environment variable
// name → plaintext value.
//
// The mapping is deterministic: given the same spec and secrets, the output
// is identical. Env vars and labels produce sorted key lists; ports are sorted.
func MapDesiredSpecToContainerConfig(spec *domain.DesiredServiceSpec, secrets map[string]string) (*DockerContainerConfigs, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec is nil")
	}

	// --- Container config ---
	containerConfig := map[string]any{
		"Image":  spec.ImageRef,
		"Labels": buildDockerLabels(spec),
	}

	// Env: merge literals + resolved secrets, sorted for determinism.
	env := buildDockerEnv(spec, secrets)
	if len(env) > 0 {
		containerConfig["Env"] = env
	}

	if len(spec.Command) > 0 {
		containerConfig["Cmd"] = spec.Command
	}
	if len(spec.Entrypoint) > 0 {
		containerConfig["Entrypoint"] = spec.Entrypoint
	}
	if spec.WorkDir != "" {
		containerConfig["WorkingDir"] = spec.WorkDir
	}

	// Exposed ports.
	exposedPorts, portBindings, err := buildDockerPortConfig(spec.Ports)
	if err != nil {
		return nil, fmt.Errorf("building port config: %w", err)
	}
	if len(exposedPorts) > 0 {
		containerConfig["ExposedPorts"] = exposedPorts
	}

	// Healthcheck.
	if hc := buildDockerHealthcheck(spec); hc != nil {
		containerConfig["Healthcheck"] = hc
	}

	// --- Host config ---
	hostConfig := map[string]any{}

	if spec.RestartPolicy != "" {
		hostConfig["RestartPolicy"] = map[string]any{"Name": spec.RestartPolicy}
	}
	if len(portBindings) > 0 {
		hostConfig["PortBindings"] = portBindings
	}
	if binds := cleanDockerBinds(spec.Volumes); len(binds) > 0 {
		hostConfig["Binds"] = binds
	}
	if spec.NetworkMode != "" {
		hostConfig["NetworkMode"] = spec.NetworkMode
	}

	// Merge Docker extension host config overrides.
	if spec.DockerExtension != nil {
		for k, v := range spec.DockerExtension.HostConfig {
			// Extension fields do not overwrite core fields already set above.
			if _, exists := hostConfig[k]; !exists {
				hostConfig[k] = v
			}
		}
	}

	// --- Networking config ---
	networkingConfig := buildDockerNetworkingConfig(spec)

	return &DockerContainerConfigs{
		ContainerConfig:  containerConfig,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
	}, nil
}

// ---------------------------------------------------------------------------
// Label helpers
// ---------------------------------------------------------------------------

// buildDockerLabels returns the full label map for a container. It includes
// all labels from the spec (which already contain Bahia labels injected by
// the builder) plus the bahia.service label used by legacy lookup.
func buildDockerLabels(spec *domain.DesiredServiceSpec) map[string]string {
	labels := make(map[string]string, len(spec.Labels)+1)
	for k, v := range spec.Labels {
		labels[k] = v
	}
	// Ensure legacy lookup label is present.
	if _, ok := labels["bahia.service"]; !ok {
		labels["bahia.service"] = spec.StableServiceKey
	}
	return labels
}

// ---------------------------------------------------------------------------
// Env helpers
// ---------------------------------------------------------------------------

// buildDockerEnv produces a deterministically-sorted []string of KEY=VALUE
// entries from the spec's literal env and resolved secrets. Secret env vars
// are injected at apply time from the secrets map; they are never in the
// persisted spec. Redacted placeholders are never emitted.
func buildDockerEnv(spec *domain.DesiredServiceSpec, secrets map[string]string) []string {
	// Collect all env keys for deterministic ordering.
	envMap := make(map[string]string, len(spec.Env)+len(spec.SecretRefs))

	// Literal env vars.
	for k, v := range spec.Env {
		envMap[k] = v
	}

	// Resolved secret values (apply-time only).
	for _, ref := range spec.SecretRefs {
		if val, ok := secrets[ref.EnvVar]; ok {
			envMap[ref.EnvVar] = val
		}
		// If secret not in map, skip — do NOT emit redacted placeholder.
	}

	if len(envMap) == 0 {
		return nil
	}

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+envMap[k])
	}
	return env
}

// ---------------------------------------------------------------------------
// Healthcheck helpers
// ---------------------------------------------------------------------------

// buildDockerHealthcheck converts the portable HealthcheckConfig into the
// Docker Engine API healthcheck format. Returns nil if no healthcheck is
// configured.
func buildDockerHealthcheck(spec *domain.DesiredServiceSpec) map[string]any {
	hc := spec.Healthcheck

	// Check for Docker extension override first.
	if spec.DockerExtension != nil && len(spec.DockerExtension.Healthcheck) > 0 {
		return spec.DockerExtension.Healthcheck
	}

	if hc == nil || len(hc.Test) == 0 {
		return nil
	}

	result := map[string]any{
		"Test": hc.Test,
	}
	if hc.Interval != "" {
		if ns, err := parseDurationToNanoseconds(hc.Interval); err == nil {
			result["Interval"] = ns
		}
	}
	if hc.Timeout != "" {
		if ns, err := parseDurationToNanoseconds(hc.Timeout); err == nil {
			result["Timeout"] = ns
		}
	}
	if hc.Retries > 0 {
		result["Retries"] = hc.Retries
	}
	if hc.StartPeriod != "" {
		if ns, err := parseDurationToNanoseconds(hc.StartPeriod); err == nil {
			result["StartPeriod"] = ns
		}
	}
	return result
}

// parseDurationToNanoseconds parses a human-friendly duration string (e.g.
// "30s", "5m", "1m30s") into nanoseconds for the Docker API. It supports
// a limited subset: integer seconds ("30s"), minutes ("5m"), and combined
// ("1m30s"). Falls back to treating bare integers as seconds.
func parseDurationToNanoseconds(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	var totalSeconds int64

	// Handle combined format like "1m30s".
	if mIdx := strings.Index(s, "m"); mIdx > 0 {
		remainder := s[mIdx+1:]
		minutesPart := s[:mIdx]
		minutes, err := strconv.ParseInt(minutesPart, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid minutes in %q: %w", s, err)
		}
		totalSeconds += minutes * 60

		if remainder == "" {
			return totalSeconds * 1_000_000_000, nil
		}
		remainder = strings.TrimSuffix(remainder, "s")
		if remainder == "" {
			return totalSeconds * 1_000_000_000, nil
		}
		secs, err := strconv.ParseInt(remainder, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid seconds in %q: %w", s, err)
		}
		totalSeconds += secs
		return totalSeconds * 1_000_000_000, nil
	}

	// Handle "30s" or bare integer.
	s = strings.TrimSuffix(s, "s")
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return secs * 1_000_000_000, nil
}

// ---------------------------------------------------------------------------
// Networking config helpers
// ---------------------------------------------------------------------------

// buildDockerNetworkingConfig builds the Docker networking configuration from
// the desired spec. It sets up endpoint configurations with the container's
// stable service key as a network alias.
func buildDockerNetworkingConfig(spec *domain.DesiredServiceSpec) map[string]any {
	config := map[string]any{}

	// If Docker extension provides explicit networking config, use it.
	if spec.DockerExtension != nil && len(spec.DockerExtension.NetworkingConfig) > 0 {
		return spec.DockerExtension.NetworkingConfig
	}

	// For non-host network mode, add endpoint settings with service alias.
	if spec.NetworkMode != "" && spec.NetworkMode != "host" && spec.NetworkMode != "none" {
		config["EndpointsConfig"] = map[string]any{
			spec.NetworkMode: map[string]any{
				"Aliases": []string{spec.StableServiceKey},
			},
		}
	}

	return config
}

// ---------------------------------------------------------------------------
// Pull policy helpers
// ---------------------------------------------------------------------------

// ShouldPullImage determines whether an image pull is needed based on the
// desired spec's pull policy and the current desired hash comparison.
//
// Pull policies:
//   - "always": always pull
//   - "never": never pull
//   - "if-not-present" (default): pull only when the image might have changed
//     (i.e., hashes differ or no existing container)
func ShouldPullImage(spec *domain.DesiredServiceSpec, existingDesiredHash string) bool {
	policy := strings.ToLower(strings.TrimSpace(spec.PullPolicy))
	switch policy {
	case "always":
		return true
	case "never":
		return false
	default: // "if-not-present" or empty
		// Pull if hashes differ (image might have changed) or no existing container.
		return existingDesiredHash == "" || existingDesiredHash != spec.DesiredHash
	}
}

// ---------------------------------------------------------------------------
// Desired hash comparison for no-op detection
// ---------------------------------------------------------------------------

// ContainerDesiredHashMatches checks whether an existing container's
// bahia.desired_hash label matches the spec's desired hash.
func ContainerDesiredHashMatches(container *DockerContainer, spec *domain.DesiredServiceSpec) bool {
	if container == nil || spec == nil {
		return false
	}
	existing := container.Labels["bahia.desired_hash"]
	return existing != "" && existing == spec.DesiredHash
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// marshalJSON wraps encoding/json.Marshal for internal use.
func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

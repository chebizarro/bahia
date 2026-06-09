// Package runtime provides runtime observation adapters for querying actual deployment state.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Observation normalizer — produces comparable NormalizedObservation from
// Docker or Compose runtime inspection data.
//
// Both Docker and Compose containers are ultimately Docker Engine containers,
// so normalization uses the same Docker inspect response. The difference is
// only in how the container is located (label lookup vs compose ps) and which
// non-semantic labels are filtered.
// ---------------------------------------------------------------------------

// NormalizeDockerContainer produces a NormalizedObservation from a Docker
// Engine container inspect response. Secret env var names are redacted:
// only their key presence is recorded, never their values.
//
// Excluded volatile fields: container ID, timestamps, ephemeral IPs,
// non-Bahia labels, and secret plaintext.
func NormalizeDockerContainer(inspected *dockerContainerInspect, secretNames map[string]bool) *domain.NormalizedObservation {
	if inspected == nil {
		return nil
	}
	return normalizeInspectData(inspected, secretNames)
}

// NormalizeComposeService produces a NormalizedObservation from a Compose
// service's underlying Docker container inspect response. The normalization
// is identical to NormalizeDockerContainer — both produce the same comparable
// output given the same container state — ensuring parity between runtimes.
//
// Compose-generated non-semantic labels (com.docker.compose.*) are excluded
// alongside the standard volatile field exclusions.
func NormalizeComposeService(inspected *dockerContainerInspect, secretNames map[string]bool) *domain.NormalizedObservation {
	if inspected == nil {
		return nil
	}
	return normalizeInspectData(inspected, secretNames)
}

// InspectAndNormalizeDocker performs a Docker container inspect and normalizes
// the result into a NormalizedObservation. This is a convenience method that
// combines the inspect API call with normalization.
func InspectAndNormalizeDocker(ctx context.Context, observer *DockerObserver, containerID string, secretNames map[string]bool) (*domain.NormalizedObservation, error) {
	inspected, err := inspectContainerForNormalization(ctx, observer, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", containerID, err)
	}
	obs := NormalizeDockerContainer(inspected, secretNames)
	if obs == nil {
		return nil, fmt.Errorf("normalize container %s: nil result", containerID)
	}
	obs.ComputeObservationHash()
	return obs, nil
}

// InspectAndNormalizeCompose performs a Docker container inspect (via the
// Docker Engine API, not compose CLI) and normalizes the result for a
// Compose-managed service.
func InspectAndNormalizeCompose(ctx context.Context, observer *DockerObserver, containerID string, secretNames map[string]bool) (*domain.NormalizedObservation, error) {
	inspected, err := inspectContainerForNormalization(ctx, observer, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", containerID, err)
	}
	obs := NormalizeComposeService(inspected, secretNames)
	if obs == nil {
		return nil, fmt.Errorf("normalize container %s: nil result", containerID)
	}
	obs.ComputeObservationHash()
	return obs, nil
}

// ---------------------------------------------------------------------------
// Core normalization logic
// ---------------------------------------------------------------------------

// normalizeInspectData extracts the comparable semantic fields from a Docker
// container inspect response. This is the single normalization path shared by
// both Docker and Compose observations.
func normalizeInspectData(inspected *dockerContainerInspect, secretNames map[string]bool) *domain.NormalizedObservation {
	obs := &domain.NormalizedObservation{
		SchemaVersion: domain.DesiredStateSchemaVersion,
	}

	// Image reference and digest.
	obs.ImageRef = inspected.Config.Image
	obs.ImageDigest = extractDigest(inspected.Image) // Image field is the digest

	// Process: command, entrypoint, working dir.
	obs.Command = normalizeStringSlice(inspected.Config.Cmd)
	obs.Entrypoint = normalizeStringSlice(inspected.Config.Entrypoint)
	obs.WorkDir = inspected.Config.WorkingDir

	// Environment: split into non-secret values and secret key presence.
	obs.Env, obs.SecretEnvKeys = normalizeContainerEnv(inspected.Config.Env, secretNames)

	// Ports: normalize from Docker inspect port map format.
	obs.Ports = normalizeInspectedPorts(inspected.NetworkSettings.Ports)

	// Volumes: normalize from mounts and host config binds.
	obs.Volumes = normalizeInspectedVolumes(inspected.Mounts, inspected.HostConfig.Binds)

	// Restart policy.
	obs.RestartPolicy = inspected.HostConfig.RestartPolicy.Name

	// Network attachments: sorted network names, excluding default bridge.
	obs.NetworkAttachments = normalizeNetworkAttachments(inspected.NetworkSettings.Networks)

	// Labels: only Bahia-managed labels.
	obs.BahiaLabels = domain.FilterBahiaLabels(inspected.Config.Labels)

	return obs
}

// ---------------------------------------------------------------------------
// Environment normalization
// ---------------------------------------------------------------------------

// normalizeContainerEnv parses Docker-style KEY=VALUE env entries, separating
// non-secret values from secret keys. Secret values are never included in
// the observation.
func normalizeContainerEnv(envEntries []string, secretNames map[string]bool) (nonSecret map[string]string, secretKeys []string) {
	if len(envEntries) == 0 {
		return nil, nil
	}

	parsed := make(map[string]string, len(envEntries))
	for _, entry := range envEntries {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		// Skip Docker/system-injected env vars that are not semantically relevant.
		if isSystemEnvVar(key) {
			continue
		}
		parsed[key] = val
	}

	return domain.FilterNonSecretEnv(parsed, secretNames)
}

// isSystemEnvVar returns true for environment variables injected by the Docker
// runtime or OS that are not semantically relevant for drift comparison.
var systemEnvVars = map[string]bool{
	"PATH":     true,
	"HOSTNAME": true,
	"HOME":     true,
}

func isSystemEnvVar(key string) bool {
	return systemEnvVars[key]
}

// ---------------------------------------------------------------------------
// Port normalization
// ---------------------------------------------------------------------------

// normalizeInspectedPorts converts Docker inspect port bindings into sorted
// "hostPort:containerPort/proto" strings. Ephemeral host IPs (0.0.0.0, ::)
// are excluded from the normalized form.
func normalizeInspectedPorts(ports map[string][]dockerPortPublish) []string {
	if len(ports) == 0 {
		return nil
	}

	var normalized []string
	for containerPortProto, bindings := range ports {
		// containerPortProto is like "80/tcp"
		for _, binding := range bindings {
			if binding.HostPort == "" {
				continue
			}
			// Exclude ephemeral host IP — only port matters for semantic comparison.
			normalized = append(normalized, fmt.Sprintf("%s:%s", binding.HostPort, containerPortProto))
		}
		// If no bindings but port is exposed, record just the container port.
		if len(bindings) == 0 {
			normalized = append(normalized, containerPortProto)
		}
	}

	sort.Strings(normalized)
	return normalized
}

// ---------------------------------------------------------------------------
// Volume normalization
// ---------------------------------------------------------------------------

// normalizeInspectedVolumes produces sorted volume mount strings from Docker
// inspect data. It prefers HostConfig.Binds (explicit bind mounts) and falls
// back to Mounts for named volumes. The format is "source:destination[:mode]".
func normalizeInspectedVolumes(mounts []dockerContainerMount, binds []string) []string {
	seen := make(map[string]bool)
	var normalized []string

	// Prefer explicit binds from host config.
	for _, bind := range binds {
		bind = strings.TrimSpace(bind)
		if bind != "" && !seen[bind] {
			normalized = append(normalized, bind)
			seen[bind] = true
		}
	}

	// Add named volumes from mounts that aren't already covered by binds.
	for _, mount := range mounts {
		var entry string
		if mount.Type == "volume" && mount.Name != "" {
			// Named volume: "name:destination[:ro]"
			entry = mount.Name + ":" + mount.Destination
		} else if mount.Type == "bind" && mount.Source != "" {
			// Bind mount: "source:destination[:ro]"
			entry = mount.Source + ":" + mount.Destination
		} else {
			continue
		}
		if !mount.RW {
			entry += ":ro"
		}
		if !seen[entry] {
			normalized = append(normalized, entry)
			seen[entry] = true
		}
	}

	sort.Strings(normalized)
	return normalized
}

// ---------------------------------------------------------------------------
// Network normalization
// ---------------------------------------------------------------------------

// normalizeNetworkAttachments extracts sorted network names from Docker
// inspect network settings, excluding the default bridge network which is
// always present and not semantically configured.
func normalizeNetworkAttachments(networks map[string]dockerContainerAttachment) []string {
	if len(networks) == 0 {
		return nil
	}

	var names []string
	for name := range networks {
		// Skip the default bridge network — it's always present and not semantic.
		if name == "bridge" {
			continue
		}
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// String slice normalization
// ---------------------------------------------------------------------------

// normalizeStringSlice returns nil for empty/nil slices and a copy otherwise.
// This ensures consistent JSON serialization (null vs empty array).
func normalizeStringSlice(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	result := make([]string, len(s))
	copy(result, s)
	return result
}

// ---------------------------------------------------------------------------
// Docker inspect helper
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Kubernetes pod normalization
// ---------------------------------------------------------------------------

// NormalizeKubernetesPod produces a NormalizedObservation from a Kubernetes
// pod structure retrieved via kubectl. Only the first container in the pod
// spec is normalized — bahia maps one service to one primary container.
//
// Secret env var names are redacted: only key presence is recorded, never
// values. Ports are expressed as "containerPort/protocol" strings (no host
// binding since Kubernetes pods use Service resources for that).
//
// The caller MUST ensure pod is non-nil; NormalizeKubernetesPod returns nil
// for a nil pod to match the Docker/Compose normalizer contract.
func NormalizeKubernetesPod(pod *kubePod, secretNames map[string]bool) *domain.NormalizedObservation {
	if pod == nil {
		return nil
	}

	obs := &domain.NormalizedObservation{
		SchemaVersion: domain.DesiredStateSchemaVersion,
	}

	// Primary container: image, command, env, ports.
	if len(pod.Spec.Containers) > 0 {
		c := pod.Spec.Containers[0]

		obs.ImageRef = c.Image

		// Command (equivalent to Docker Cmd/entrypoint merged in K8s).
		obs.Command = normalizeStringSlice(c.Command)

		// Environment: split into non-secret values and secret key presence.
		if len(c.Env) > 0 {
			envMap := make(map[string]string, len(c.Env))
			for _, e := range c.Env {
				envMap[e.Name] = e.Value
			}
			obs.Env, obs.SecretEnvKeys = domain.FilterNonSecretEnv(envMap, secretNames)
		}

		// Ports: "containerPort/protocol" (lowercase protocol).
		if len(c.Ports) > 0 {
			ports := make([]string, 0, len(c.Ports))
			for _, p := range c.Ports {
				proto := strings.ToLower(p.Protocol)
				if proto == "" {
					proto = "tcp"
				}
				ports = append(ports, fmt.Sprintf("%d/%s", p.ContainerPort, proto))
			}
			sort.Strings(ports)
			obs.Ports = ports
		}
	}

	// Image digest from container status (more precise than spec image ref).
	if len(pod.Status.ContainerStatuses) > 0 {
		cs := pod.Status.ContainerStatuses[0]
		obs.ImageDigest = extractDigest(cs.ImageID)
		// Prefer status image over spec image when spec image is empty.
		if obs.ImageRef == "" {
			obs.ImageRef = cs.Image
		}
	}

	// Restart policy from pod spec.
	obs.RestartPolicy = pod.Spec.RestartPolicy

	// Labels — only Bahia-managed labels (bahia.* prefix).
	obs.BahiaLabels = domain.FilterBahiaLabels(pod.Metadata.Labels)

	obs.ComputeObservationHash()
	return obs
}

// inspectContainerForNormalization calls the Docker Engine inspect API and
// returns the parsed response. This reuses the same inspect structures as
// docker_discovery.go.
func inspectContainerForNormalization(ctx context.Context, observer *DockerObserver, containerID string) (*dockerContainerInspect, error) {
	requestURL := fmt.Sprintf("%s/v1.44/containers/%s/json", observer.host, url.PathEscape(containerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := observer.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker container inspect returned %d", resp.StatusCode)
	}
	var inspected dockerContainerInspect
	if err := json.NewDecoder(resp.Body).Decode(&inspected); err != nil {
		return nil, err
	}
	return &inspected, nil
}

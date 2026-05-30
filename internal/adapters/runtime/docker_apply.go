package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Docker desired-state apply
// ---------------------------------------------------------------------------

// ApplyDesiredState converges a single Docker service toward its desired
// runtime state. It implements the DesiredStateApplier interface.
//
// Flow:
//  1. Find existing Bahia-managed container via labels or name
//  2. If found and desired hash matches (and pull policy allows), return no-op
//  3. Otherwise: ensure networks/volumes, pull image, stop/remove old, create new, start
//  4. Partial failures return explicit errors — no silent fallback
func (o *DockerObserver) ApplyDesiredState(ctx context.Context, req DesiredStateApplyRequest) (*DesiredStateApplyResult, error) {
	if req.TargetService == nil {
		return nil, fmt.Errorf("docker apply: target service spec is nil")
	}

	spec := req.TargetService
	containerName := BahiaContainerName(spec)
	logger := o.logger.With(
		zap.String("service_key", spec.StableServiceKey),
		zap.String("container_name", containerName),
		zap.String("desired_hash", spec.DesiredHash),
	)
	control := o.ControlClient()
	executionMode := control.ExecutionMode()

	// Step 1: Find existing managed container.
	existing, err := control.FindManagedContainer(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("docker apply: finding managed container: %w", err)
	}

	// Step 2: Check for no-op (hash match + pull policy allows skip).
	if existing != nil && ContainerDesiredHashMatches(existing, spec) {
		pullPolicy := normalizePullPolicy(req.PullPolicy, spec.PullPolicy)
		if pullPolicy != "always" {
			logger.Info("desired hash matches, no-op",
				zap.String("container_id", existing.ID),
				zap.String("existing_state", existing.State),
			)
			return &DesiredStateApplyResult{
				Renderer:            "docker",
				ExecutionMode:       executionMode,
				DesiredHash:         spec.DesiredHash,
				EnvironmentRevision: environmentRevision(req.EnvironmentPlan),
				ResourceIDs:         []string{existing.ID},
				ResourceNames:       []string{containerName},
				ObservationHints: &ObservationHints{
					ContainerID: existing.ID,
				},
			}, nil
		}
	}

	// Dry run stops here — report what would happen without mutating.
	if req.DryRun {
		action := "create"
		if existing != nil {
			action = "recreate"
		}
		return &DesiredStateApplyResult{
			Renderer:            "docker",
			ExecutionMode:       executionMode,
			DesiredHash:         spec.DesiredHash,
			EnvironmentRevision: environmentRevision(req.EnvironmentPlan),
			Warnings:            []string{fmt.Sprintf("dry-run: would %s container %s", action, containerName)},
		}, nil
	}

	// Step 3: Build container configs before any mutations.
	configs, err := MapDesiredSpecToContainerConfig(spec, req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("docker apply: building container config: %w", err)
	}

	var warnings []string

	// Step 4: Ensure required networks.
	if req.EnvironmentPlan != nil {
		networkSpecs := collectNetworkSpecs(spec)
		if len(networkSpecs) > 0 {
			if err := control.EnsureNetworks(ctx, networkSpecs); err != nil {
				return nil, fmt.Errorf("docker apply: ensuring networks: %w", err)
			}
		}

		volumeSpecs := collectVolumeSpecs(spec)
		if len(volumeSpecs) > 0 {
			if err := control.EnsureVolumes(ctx, volumeSpecs); err != nil {
				return nil, fmt.Errorf("docker apply: ensuring volumes: %w", err)
			}
		}
	}

	// Step 5: Pull image if needed.
	pullPolicy := normalizePullPolicy(req.PullPolicy, spec.PullPolicy)
	existingHash := ""
	if existing != nil {
		existingHash = existing.Labels["bahia.desired_hash"]
	}

	if shouldPull(pullPolicy, existingHash, spec.DesiredHash, existing == nil) {
		logger.Info("pulling image", zap.String("image", spec.ImageRef))
		if err := control.PullImage(ctx, spec.ImageRef); err != nil {
			if pullPolicy == "always" {
				return nil, fmt.Errorf("docker apply: pulling image %s: %w", spec.ImageRef, err)
			}
			// For if-not-present, warn but try to proceed with local image.
			warnings = append(warnings, fmt.Sprintf("image pull failed (proceeding with local): %v", err))
			logger.Warn("image pull failed, proceeding with local image",
				zap.String("image", spec.ImageRef),
				zap.Error(err),
			)
		}
	}

	// Step 6: Stop and remove existing container.
	if existing != nil {
		logger.Info("removing existing container",
			zap.String("container_id", existing.ID),
			zap.String("existing_hash", existingHash),
		)
		if err := control.StopContainer(ctx, existing.ID); err != nil {
			return nil, fmt.Errorf("docker apply: stopping container %s: %w", existing.ID, err)
		}
		if err := control.RemoveContainer(ctx, existing.ID); err != nil {
			return nil, fmt.Errorf("docker apply: removing container %s: %w", existing.ID, err)
		}
	}

	// Step 7: Create new container.
	containerID, err := control.CreateContainer(ctx, containerName, configs)
	if err != nil {
		return nil, fmt.Errorf("docker apply: creating container %s: %w", containerName, err)
	}
	logger.Info("container created", zap.String("container_id", containerID))

	// Step 8: Attach to additional networks (beyond the primary network mode).
	networkWarnings := attachAdditionalNetworks(ctx, control, containerID, spec)
	warnings = append(warnings, networkWarnings...)

	// Step 9: Start the container.
	if err := control.StartContainer(ctx, containerID); err != nil {
		return nil, fmt.Errorf("docker apply: starting container %s: %w", containerID, err)
	}
	logger.Info("container started", zap.String("container_id", containerID))

	return &DesiredStateApplyResult{
		Renderer:            "docker",
		ExecutionMode:       executionMode,
		DesiredHash:         spec.DesiredHash,
		EnvironmentRevision: environmentRevision(req.EnvironmentPlan),
		ResourceIDs:         []string{containerID},
		ResourceNames:       []string{containerName},
		ObservationHints: &ObservationHints{
			ContainerID: containerID,
		},
		Warnings: warnings,
	}, nil
}

// SupportsDesiredState returns true — the Docker adapter supports desired-state
// convergence.
func (o *DockerObserver) SupportsDesiredState() bool { return true }

// ---------------------------------------------------------------------------
// Container lifecycle helpers
// ---------------------------------------------------------------------------

func (o *DockerObserver) stopContainer(ctx context.Context, containerID string) error {
	stopURL := fmt.Sprintf("%s/v1.44/containers/%s/stop?t=10", o.host, containerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stopURL, nil)
	if err != nil {
		return fmt.Errorf("creating stop request: %w", err)
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}
	defer resp.Body.Close()

	// 204 = stopped, 304 = already stopped — both are fine.
	if resp.StatusCode != http.StatusNoContent &&
		resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("docker stop returned %d", resp.StatusCode)
	}
	return nil
}

func (o *DockerObserver) removeContainer(ctx context.Context, containerID string) error {
	rmURL := fmt.Sprintf("%s/v1.44/containers/%s?force=true", o.host, containerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, rmURL, nil)
	if err != nil {
		return fmt.Errorf("creating remove request: %w", err)
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("removing container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker remove returned %d", resp.StatusCode)
	}
	return nil
}

func (o *DockerObserver) createContainer(ctx context.Context, name string, configs *DockerContainerConfigs) (string, error) {
	body := map[string]any{}

	// Merge container config fields.
	for k, v := range configs.ContainerConfig {
		body[k] = v
	}
	body["HostConfig"] = configs.HostConfig
	if len(configs.NetworkingConfig) > 0 {
		body["NetworkingConfig"] = configs.NetworkingConfig
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling container config: %w", err)
	}

	createURL := fmt.Sprintf("%s/v1.44/containers/create?name=%s", o.host, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("docker create returned %d", resp.StatusCode)
	}

	var createResp struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return "", fmt.Errorf("decoding create response: %w", err)
	}
	return createResp.ID, nil
}

func (o *DockerObserver) startContainer(ctx context.Context, containerID string) error {
	startURL := fmt.Sprintf("%s/v1.44/containers/%s/start", o.host, containerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL, nil)
	if err != nil {
		return fmt.Errorf("creating start request: %w", err)
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker start returned %d", resp.StatusCode)
	}
	return nil
}

// attachAdditionalNetworks connects a container to networks listed in the
// spec's DockerExtension that are not the primary network mode. Returns
// warnings for non-fatal attachment failures.
func attachAdditionalNetworks(ctx context.Context, control DockerControlClient, containerID string, spec *domain.DesiredServiceSpec) []string {
	if spec.DockerExtension == nil {
		return nil
	}

	netCfg, ok := spec.DockerExtension.NetworkingConfig["AdditionalNetworks"]
	if !ok {
		return nil
	}

	networks, ok := netCfg.([]string)
	if !ok {
		return nil
	}

	var warnings []string
	for _, network := range networks {
		if network == spec.NetworkMode {
			continue // Already the primary network.
		}
		if err := control.ConnectNetwork(ctx, containerID, network, spec.StableServiceKey); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to attach network %s: %v", network, err))
		}
	}
	return warnings
}

func (o *DockerObserver) connectNetwork(ctx context.Context, containerID, networkName, alias string) error {
	body := map[string]any{
		"Container": containerID,
		"EndpointConfig": map[string]any{
			"Aliases": []string{alias},
		},
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}

	connectURL := fmt.Sprintf("%s/v1.44/networks/%s/connect", o.host, networkName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("docker network connect returned %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// normalizePullPolicy resolves the effective pull policy from request-level
// and spec-level settings. Request-level takes precedence.
func normalizePullPolicy(requestPolicy, specPolicy string) string {
	p := strings.ToLower(strings.TrimSpace(requestPolicy))
	if p == "" {
		p = strings.ToLower(strings.TrimSpace(specPolicy))
	}
	switch p {
	case "always", "never":
		return p
	default:
		return "if-not-present"
	}
}

// shouldPull determines whether an image pull is needed.
func shouldPull(policy, existingHash, desiredHash string, isMissing bool) bool {
	switch policy {
	case "always":
		return true
	case "never":
		return false
	default: // if-not-present
		return isMissing || existingHash == "" || existingHash != desiredHash
	}
}

// environmentRevision extracts the revision hash from the environment plan,
// or returns empty string if no plan is provided.
func environmentRevision(plan *domain.DesiredEnvironmentPlan) string {
	if plan == nil {
		return ""
	}
	return plan.RevisionHash
}

// collectNetworkSpecs derives NetworkSpec entries from a DesiredServiceSpec.
// If the spec has a non-host network mode, that network is included.
func collectNetworkSpecs(spec *domain.DesiredServiceSpec) []domain.NetworkSpec {
	if spec.NetworkMode == "" || spec.NetworkMode == "host" || spec.NetworkMode == "none" || spec.NetworkMode == "bridge" {
		return nil
	}
	return []domain.NetworkSpec{
		{Name: spec.NetworkMode},
	}
}

// collectVolumeSpecs derives VolumeSpec entries from a DesiredServiceSpec.
// Only named volumes (no host path prefix) are included.
func collectVolumeSpecs(spec *domain.DesiredServiceSpec) []domain.VolumeSpec {
	var specs []domain.VolumeSpec
	for _, vol := range spec.Volumes {
		vol = strings.TrimSpace(vol)
		if vol == "" {
			continue
		}
		// Named volumes don't start with / or . — they are "name:/container/path".
		parts := strings.SplitN(vol, ":", 2)
		if len(parts) < 2 {
			continue
		}
		source := parts[0]
		if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "~") {
			continue // Host bind mount, not a named volume.
		}
		specs = append(specs, domain.VolumeSpec{Name: source})
	}
	return specs
}

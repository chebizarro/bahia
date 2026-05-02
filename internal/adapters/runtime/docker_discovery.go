package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// DockerDiscoveryTarget identifies a Docker host to scan for adoptable containers.
type DockerDiscoveryTarget struct {
	Name            string
	DockerHost      string
	Endpoint        config.RuntimeEndpointConfig
	EnvironmentName string
}

// DockerDiscoveryResult contains discovery output for one target host.
type DockerDiscoveryResult struct {
	Target     DockerDiscoveryTarget
	Containers []DiscoveredContainer
	Error      string
}

// DiscoveredContainer is a normalized view of a Docker container suitable for adoption preview/import.
type DiscoveredContainer struct {
	TargetName      string
	EnvironmentName string
	ContainerID     string
	ContainerName   string
	ImageRef        string
	ImageRepo       string
	ImageTag        string
	ImageDigest     string
	SourceRuntime   string
	Labels          map[string]string
	Environment     map[string]string
	Ports           []string
	Volumes         []string
	Restart         string
	Command         []string
	Entrypoint      []string
	WorkingDir      string
	NetworkMode     string
	Compose         *domain.ComposeMetadata
	HealthStatus    domain.HealthStatus
	Warnings        []string
	Adoptable       bool
}

// DockerDiscovery scans a Docker host for containers.
type DockerDiscovery struct {
	observer *DockerObserver
	logger   *zap.Logger
}

// NewDockerDiscovery creates a Docker discovery client using the same host handling as DockerObserver.
func NewDockerDiscovery(dockerHost string, logger *zap.Logger) *DockerDiscovery {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DockerDiscovery{observer: NewDockerObserver(dockerHost, logger), logger: logger}
}

// NewDockerDiscoveryWithEndpoint creates a discovery client using managed endpoint transport settings.
func NewDockerDiscoveryWithEndpoint(endpoint config.RuntimeEndpointConfig, logger *zap.Logger) (*DockerDiscovery, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	observer, err := NewDockerObserverWithEndpoint(endpoint, logger)
	if err != nil {
		return nil, err
	}
	return &DockerDiscovery{observer: observer, logger: logger}, nil
}

// DiscoverDockerTargets scans targets concurrently with bounded host-level parallelism and preserves input order.
func DiscoverDockerTargets(ctx context.Context, targets []DockerDiscoveryTarget, logger *zap.Logger) ([]DockerDiscoveryResult, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	results := make([]DockerDiscoveryResult, len(targets))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup

	for i, target := range targets {
		i, target := i, target
		results[i].Target = target
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i].Error = ctx.Err().Error()
				return
			}

			discovery, err := dockerDiscoveryForTarget(target, logger)
			if err != nil {
				results[i].Error = err.Error()
				return
			}
			containers, err := discovery.Discover(ctx, target)
			if err != nil {
				results[i].Error = err.Error()
				return
			}
			results[i].Containers = containers
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return results, err
	}
	markDuplicateDiscoveredTargets(results)
	return results, nil
}

func dockerDiscoveryForTarget(target DockerDiscoveryTarget, logger *zap.Logger) (*DockerDiscovery, error) {
	if !target.Endpoint.Empty() {
		return NewDockerDiscoveryWithEndpoint(target.Endpoint, logger)
	}
	return NewDockerDiscovery(target.DockerHost, logger), nil
}

// Discover scans all containers on a Docker host and normalizes inspect data.
func (d *DockerDiscovery) Discover(ctx context.Context, target DockerDiscoveryTarget) ([]DiscoveredContainer, error) {
	containers, err := d.observer.listAllContainers(ctx)
	if err != nil {
		return nil, err
	}

	discovered := make([]DiscoveredContainer, 0, len(containers))
	for _, container := range containers {
		inspected, err := d.inspectContainer(ctx, container.ID)
		if err != nil {
			d.logger.Warn("failed to inspect docker container", zap.String("container_id", container.ID), zap.Error(err))
			continue
		}
		image, err := d.inspectImage(ctx, inspected.Image)
		if err != nil {
			d.logger.Debug("failed to inspect docker image", zap.String("image", inspected.Image), zap.Error(err))
		}
		discovered = append(discovered, normalizeDiscoveredContainer(target, inspected, image))
	}
	return discovered, nil
}

func (d *DockerDiscovery) inspectContainer(ctx context.Context, containerID string) (*dockerContainerInspect, error) {
	requestURL := fmt.Sprintf("%s/v1.43/containers/%s/json", d.observer.host, url.PathEscape(containerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.observer.httpClient.Do(req)
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

func (d *DockerDiscovery) inspectImage(ctx context.Context, imageRef string) (*dockerImageInspect, error) {
	if imageRef == "" {
		return nil, nil
	}
	requestURL := fmt.Sprintf("%s/v1.43/images/%s/json", d.observer.host, url.PathEscape(imageRef))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.observer.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker image inspect returned %d", resp.StatusCode)
	}
	var inspected dockerImageInspect
	if err := json.NewDecoder(resp.Body).Decode(&inspected); err != nil {
		return nil, err
	}
	return &inspected, nil
}

type dockerContainerInspect struct {
	ID              string                       `json:"Id"`
	Name            string                       `json:"Name"`
	Image           string                       `json:"Image"`
	Config          dockerContainerConfig        `json:"Config"`
	State           dockerContainerState         `json:"State"`
	HostConfig      dockerContainerHostConfig    `json:"HostConfig"`
	Mounts          []dockerContainerMount       `json:"Mounts"`
	NetworkSettings dockerContainerNetworkConfig `json:"NetworkSettings"`
}

type dockerContainerConfig struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	Cmd        []string          `json:"Cmd"`
	Entrypoint []string          `json:"Entrypoint"`
	WorkingDir string            `json:"WorkingDir"`
}

type dockerContainerState struct {
	Status string                     `json:"Status"`
	Health *dockerContainerHealthInfo `json:"Health"`
}

type dockerContainerHealthInfo struct {
	Status string `json:"Status"`
}

type dockerContainerHostConfig struct {
	Binds         []string                   `json:"Binds"`
	NetworkMode   string                     `json:"NetworkMode"`
	RestartPolicy dockerContainerRestartRule `json:"RestartPolicy"`
}

type dockerContainerRestartRule struct {
	Name string `json:"Name"`
}

type dockerContainerMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
}

type dockerContainerNetworkConfig struct {
	Ports    map[string][]dockerPortPublish       `json:"Ports"`
	Networks map[string]dockerContainerAttachment `json:"Networks"`
}

type dockerPortPublish struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type dockerContainerAttachment struct {
	Aliases []string `json:"Aliases"`
}

func normalizeDiscoveredContainer(target DockerDiscoveryTarget, inspected *dockerContainerInspect, image *dockerImageInspect) DiscoveredContainer {
	labels := copyStringMap(inspected.Config.Labels)
	containerName := strings.TrimPrefix(inspected.Name, "/")
	imageRef := inspected.Config.Image
	imageRepo, imageTag, imageDigest := parseDockerImageReference(imageRef)
	if image != nil {
		if repo, digest := bestRepoDigest(imageRepo, image.RepoDigests); digest != "" {
			imageRepo = repo
			imageDigest = digest
		} else if imageDigest == "" {
			imageDigest = extractDigest(image.ID)
		}
	}
	if imageRepo == "" && imageRef != "" {
		imageRepo = imageRef
	}

	sourceRuntime := "docker"
	compose := composeMetadataFromLabels(labels)
	if compose != nil {
		sourceRuntime = "compose"
	}

	discovered := DiscoveredContainer{
		TargetName:      containerName,
		EnvironmentName: target.EnvironmentName,
		ContainerID:     inspected.ID,
		ContainerName:   containerName,
		ImageRef:        imageRef,
		ImageRepo:       imageRepo,
		ImageTag:        imageTag,
		ImageDigest:     imageDigest,
		SourceRuntime:   sourceRuntime,
		Labels:          labels,
		Environment:     envListToMap(inspected.Config.Env),
		Ports:           normalizeDiscoveredPorts(inspected.NetworkSettings.Ports),
		Volumes:         normalizeDiscoveredVolumes(inspected.HostConfig.Binds, inspected.Mounts),
		Restart:         inspected.HostConfig.RestartPolicy.Name,
		Command:         append([]string(nil), inspected.Config.Cmd...),
		Entrypoint:      append([]string(nil), inspected.Config.Entrypoint...),
		WorkingDir:      inspected.Config.WorkingDir,
		NetworkMode:     inspected.HostConfig.NetworkMode,
		Compose:         compose,
		HealthStatus:    mapDockerInspectHealth(inspected.State),
		Adoptable:       true,
	}
	discovered.Warnings = adoptionWarnings(discovered, inspected)
	if len(discovered.Warnings) > 0 {
		discovered.Adoptable = false
	}
	return discovered
}

func parseDockerImageReference(ref string) (repo, tag, digest string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", ""
	}
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		repo = ref[:at]
		digest = ref[at+1:]
		return repo, tag, digest
	}
	lastSlash := strings.LastIndex(ref, "/")
	lastColon := strings.LastIndex(ref, ":")
	if lastColon > lastSlash {
		return ref[:lastColon], ref[lastColon+1:], ""
	}
	return ref, "", ""
}

func bestRepoDigest(preferredRepo string, repoDigests []string) (string, string) {
	for _, repoDigest := range repoDigests {
		repo, digest := splitRepoDigest(repoDigest)
		if digest == "" {
			continue
		}
		if preferredRepo == "" || repo == preferredRepo {
			return repo, digest
		}
	}
	if len(repoDigests) == 0 {
		return "", ""
	}
	return splitRepoDigest(repoDigests[0])
}

func envListToMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func normalizeDiscoveredPorts(ports map[string][]dockerPortPublish) []string {
	if len(ports) == 0 {
		return nil
	}
	out := make([]string, 0, len(ports))
	keys := make([]string, 0, len(ports))
	for containerPort := range ports {
		keys = append(keys, containerPort)
	}
	sort.Strings(keys)
	for _, containerPort := range keys {
		published := ports[containerPort]
		if len(published) == 0 {
			continue
		}
		portNumber, proto, _ := strings.Cut(containerPort, "/")
		for _, binding := range published {
			if binding.HostPort == "" {
				continue
			}
			mapped := binding.HostPort + ":" + portNumber
			if proto != "" && proto != "tcp" {
				mapped += "/" + proto
			}
			out = append(out, mapped)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeDiscoveredVolumes(binds []string, mounts []dockerContainerMount) []string {
	if cleaned := cleanDockerBinds(binds); len(cleaned) > 0 {
		return cleaned
	}
	out := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		if mount.Destination == "" {
			continue
		}
		source := mount.Source
		if source == "" {
			source = mount.Name
		}
		if source == "" {
			continue
		}
		volume := source + ":" + mount.Destination
		if !mount.RW {
			volume += ":ro"
		} else if mount.Mode != "" {
			volume += ":" + mount.Mode
		}
		out = append(out, volume)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func composeMetadataFromLabels(labels map[string]string) *domain.ComposeMetadata {
	if labels["com.docker.compose.project"] == "" {
		return nil
	}
	metadata := &domain.ComposeMetadata{
		ProjectName: labels["com.docker.compose.project"],
		ServiceName: labels["com.docker.compose.service"],
		WorkingDir:  labels["com.docker.compose.project.working_dir"],
	}
	if rawFiles := labels["com.docker.compose.project.config_files"]; rawFiles != "" {
		metadata.ConfigFiles = strings.Split(rawFiles, ",")
	}
	return metadata
}

func mapDockerInspectHealth(state dockerContainerState) domain.HealthStatus {
	if state.Health != nil && state.Health.Status != "" {
		switch strings.ToLower(state.Health.Status) {
		case "healthy":
			return domain.HealthStatusHealthy
		case "unhealthy":
			return domain.HealthStatusUnhealthy
		case "starting":
			return domain.HealthStatusStarting
		}
	}
	return mapDockerState(state.Status)
}

func adoptionWarnings(discovered DiscoveredContainer, inspected *dockerContainerInspect) []string {
	var warnings []string
	if discovered.TargetName == "" {
		warnings = append(warnings, "missing target container name")
	}
	if discovered.ImageRef == "" || discovered.ImageRepo == "" {
		warnings = append(warnings, "no usable image reference")
	}
	userNetworks := userDefinedNetworks(inspected.NetworkSettings.Networks)
	if len(userNetworks) > 1 {
		warnings = append(warnings, "multiple user-defined networks are not supported")
	}
	if hasUnsupportedNetworkAliases(inspected.NetworkSettings.Networks, discovered.ContainerName, discovered.Labels) {
		warnings = append(warnings, "network aliases cannot be represented in deploy options")
	}
	warnings = append(warnings, unsupportedPortWarnings(inspected.NetworkSettings.Ports)...)
	warnings = append(warnings, unsupportedMountWarnings(inspected.Mounts)...)
	return warnings
}

func userDefinedNetworks(networks map[string]dockerContainerAttachment) []string {
	out := make([]string, 0, len(networks))
	for name := range networks {
		switch name {
		case "", "bridge", "host", "none":
			continue
		default:
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func hasUnsupportedNetworkAliases(networks map[string]dockerContainerAttachment, containerName string, labels map[string]string) bool {
	allowed := map[string]struct{}{}
	if containerName != "" {
		allowed[containerName] = struct{}{}
	}
	if serviceName := labels["com.docker.compose.service"]; serviceName != "" {
		allowed[serviceName] = struct{}{}
	}
	for _, attachment := range networks {
		for _, alias := range attachment.Aliases {
			if alias == "" {
				continue
			}
			if _, ok := allowed[alias]; ok {
				continue
			}
			return true
		}
	}
	return false
}

func unsupportedPortWarnings(ports map[string][]dockerPortPublish) []string {
	var warnings []string
	keys := make([]string, 0, len(ports))
	for containerPort := range ports {
		keys = append(keys, containerPort)
	}
	sort.Strings(keys)
	for _, containerPort := range keys {
		published := ports[containerPort]
		if len(published) > 1 {
			warnings = append(warnings, "multiple host bindings for port "+containerPort+" are not supported")
		}
		for _, binding := range published {
			hostIP := strings.TrimSpace(binding.HostIP)
			if hostIP != "" && hostIP != "0.0.0.0" && hostIP != "::" {
				warnings = append(warnings, "host-specific binding for port "+containerPort+" is not supported")
			}
		}
	}
	return warnings
}

func unsupportedMountWarnings(mounts []dockerContainerMount) []string {
	var warnings []string
	for _, mount := range mounts {
		switch mount.Type {
		case "", "bind", "volume":
			continue
		default:
			warnings = append(warnings, "unsupported mount type "+mount.Type)
		}
	}
	return warnings
}

func markDuplicateDiscoveredTargets(results []DockerDiscoveryResult) {
	counts := map[string]int{}
	for _, result := range results {
		for _, container := range result.Containers {
			key := result.Target.Name + "/" + container.TargetName
			counts[key]++
		}
	}
	for ri := range results {
		for ci := range results[ri].Containers {
			key := results[ri].Target.Name + "/" + results[ri].Containers[ci].TargetName
			if counts[key] > 1 {
				results[ri].Containers[ci].Warnings = append(results[ri].Containers[ci].Warnings, "duplicate target selection within request")
				results[ri].Containers[ci].Adoptable = false
			}
		}
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

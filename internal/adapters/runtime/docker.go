// Package runtime provides runtime observation adapters for querying actual deployment state.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// Observer is the interface for runtime observation.
type Observer interface {
	Observe(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error)
}

// Runtime extends Observer with deployment lifecycle operations.
// Implementations exist for Docker, Docker Compose, and Kubernetes.
type Runtime interface {
	Observer

	// Type returns the runtime type identifier.
	Type() domain.RuntimeType

	// Deploy deploys or updates a service with the given image.
	Deploy(ctx context.Context, serviceName, image string, opts DeployOptions) error

	// Undeploy removes a service deployment.
	Undeploy(ctx context.Context, serviceName string) error

	// StreamLogs streams container logs for a service.
	// The returned channel receives log lines until the context is cancelled.
	StreamLogs(ctx context.Context, serviceName string, opts LogOptions) (<-chan LogEntry, error)
}

// LifecycleRuntime is implemented by runtimes that support direct lifecycle actions.
type LifecycleRuntime interface {
	Runtime
	Restart(ctx context.Context, targetName string) error
	Stop(ctx context.Context, targetName string) error
}

// DeployOptions configures a deployment operation.
type DeployOptions struct {
	Environment map[string]string `json:"environment,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Ports       []string          `json:"ports,omitempty"`   // "8080:80" format
	Volumes     []string          `json:"volumes,omitempty"` // "/host:/container[:ro]" format
	Restart     string            `json:"restart,omitempty"` // always, unless-stopped, on-failure
	Command     []string          `json:"command,omitempty"`
	Entrypoint  []string          `json:"entrypoint,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	NetworkMode string            `json:"network_mode,omitempty"`
	PullAlways  bool              `json:"pull_always,omitempty"`
}

// LogOptions configures log streaming.
type LogOptions struct {
	Tail   int  `json:"tail,omitempty"`   // number of lines from end (0 = all)
	Follow bool `json:"follow,omitempty"` // stream new lines
}

// LogEntry is a single log line from a container.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
	Message   string    `json:"message"`
}

// DockerContainer represents a running Docker container (subset of Docker API response).
type DockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
}

// DockerObserver queries the Docker Engine API for container state.
type DockerObserver struct {
	httpClient *http.Client
	host       string
	logger     *zap.Logger
}

// NewDockerObserver creates a new DockerObserver.
func NewDockerObserver(dockerHost string, logger *zap.Logger) *DockerObserver {
	var client *http.Client

	if strings.HasPrefix(dockerHost, "unix://") {
		socketPath := strings.TrimPrefix(dockerHost, "unix://")
		client = &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
			Timeout: 10 * time.Second,
		}
		dockerHost = "http://localhost"
	} else {
		client = &http.Client{Timeout: 10 * time.Second}
		if !strings.HasPrefix(dockerHost, "http") {
			dockerHost = "http://" + dockerHost
		}
	}

	return &DockerObserver{
		httpClient: client,
		host:       dockerHost,
		logger:     logger,
	}
}

// Observe queries Docker for containers matching the given service name or runtime target name.
func (o *DockerObserver) Observe(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	containers, err := o.listContainers(ctx, serviceName)
	if err != nil {
		return nil, fmt.Errorf("querying docker: %w", err)
	}

	if len(containers) == 0 {
		return &domain.RuntimeObservation{
			ServiceID:           serviceID,
			EnvironmentID:       envID,
			ObservedImageDigest: "",
			HealthStatus:        domain.HealthStatusStopped,
			Source:              "docker",
			ObservedAt:          time.Now().UTC(),
		}, nil
	}

	container := containers[0]
	digest := extractDigest(container.ImageID)
	repo := container.Image
	if resolvedRepo, resolvedDigest, err := o.resolveImageRepoDigest(ctx, container.Image, container.ImageID); err == nil {
		if resolvedRepo != "" {
			repo = resolvedRepo
		}
		if resolvedDigest != "" {
			digest = resolvedDigest
		}
	} else {
		o.logger.Debug("failed to inspect docker image for digest", zap.String("image_id", container.ImageID), zap.Error(err))
	}
	health := mapDockerState(container.State)

	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: digest,
		ObservedImageRepo:   repo,
		ObservedContainerID: container.ID,
		ObservedHost:        o.host,
		HealthStatus:        health,
		Source:              "docker",
		ObservedAt:          time.Now().UTC(),
	}, nil
}

// Type returns the docker runtime type.
func (o *DockerObserver) Type() domain.RuntimeType {
	return domain.RuntimeTypeDocker
}

// Deploy creates or updates a Docker container for the given service.
func (o *DockerObserver) Deploy(ctx context.Context, serviceName, image string, opts DeployOptions) error {
	// Build and validate container config before removing any existing container.
	labels := map[string]string{"bahia.service": serviceName}
	for k, v := range opts.Labels {
		labels[k] = v
	}

	env := make([]string, 0, len(opts.Environment))
	for k, v := range opts.Environment {
		env = append(env, k+"="+v)
	}

	exposedPorts, portBindings, err := buildDockerPortConfig(opts.Ports)
	if err != nil {
		return err
	}

	hostConfig := map[string]any{}
	if opts.Restart != "" {
		hostConfig["RestartPolicy"] = map[string]any{"Name": opts.Restart}
	}
	if len(portBindings) > 0 {
		hostConfig["PortBindings"] = portBindings
	}
	if binds := cleanDockerBinds(opts.Volumes); len(binds) > 0 {
		hostConfig["Binds"] = binds
	}
	if opts.NetworkMode != "" {
		hostConfig["NetworkMode"] = opts.NetworkMode
	}

	body := map[string]any{
		"Image":      image,
		"Labels":     labels,
		"Env":        env,
		"HostConfig": hostConfig,
	}
	if len(opts.Command) > 0 {
		body["Cmd"] = opts.Command
	}
	if len(opts.Entrypoint) > 0 {
		body["Entrypoint"] = opts.Entrypoint
	}
	if opts.WorkingDir != "" {
		body["WorkingDir"] = opts.WorkingDir
	}
	if len(exposedPorts) > 0 {
		body["ExposedPorts"] = exposedPorts
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling container config: %w", err)
	}

	// Stop and remove any existing container only after option validation succeeds.
	_ = o.stopAndRemove(ctx, serviceName)

	// Pull the image if requested.
	if opts.PullAlways {
		if err := o.pullImage(ctx, image); err != nil {
			o.logger.Warn("failed to pull image, trying with local",
				zap.String("image", image), zap.Error(err))
		}
	}

	// Create container.
	createURL := fmt.Sprintf("%s/v1.43/containers/create?name=%s", o.host, serviceName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL,
		strings.NewReader(string(bodyJSON)))
	if err != nil {
		return fmt.Errorf("creating container request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("docker create returned %d", resp.StatusCode)
	}

	var createResp struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return fmt.Errorf("decoding create response: %w", err)
	}

	// Start container.
	startURL := fmt.Sprintf("%s/v1.43/containers/%s/start", o.host, createResp.ID)
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL, nil)
	if err != nil {
		return fmt.Errorf("creating start request: %w", err)
	}

	startResp, err := o.httpClient.Do(startReq)
	if err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	defer startResp.Body.Close()

	if startResp.StatusCode != http.StatusNoContent && startResp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker start returned %d", startResp.StatusCode)
	}

	o.logger.Info("container deployed",
		zap.String("service", serviceName),
		zap.String("image", image),
		zap.String("container_id", createResp.ID),
	)
	return nil
}

// Undeploy stops and removes the container for a service.
func (o *DockerObserver) Undeploy(ctx context.Context, serviceName string) error {
	return o.stopAndRemove(ctx, serviceName)
}

// Restart restarts an existing Docker container by runtime target name.
func (o *DockerObserver) Restart(ctx context.Context, targetName string) error {
	container, err := o.firstContainer(ctx, targetName)
	if err != nil {
		return err
	}
	restartURL := fmt.Sprintf("%s/v1.43/containers/%s/restart?t=10", o.host, container.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, restartURL, nil)
	if err != nil {
		return fmt.Errorf("creating docker restart request: %w", err)
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("restarting container %s: %w", targetName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker restart returned %d", resp.StatusCode)
	}
	return nil
}

// Stop stops an existing Docker container by runtime target name.
func (o *DockerObserver) Stop(ctx context.Context, targetName string) error {
	container, err := o.firstContainer(ctx, targetName)
	if err != nil {
		return err
	}
	stopURL := fmt.Sprintf("%s/v1.43/containers/%s/stop?t=10", o.host, container.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stopURL, nil)
	if err != nil {
		return fmt.Errorf("creating docker stop request: %w", err)
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stopping container %s: %w", targetName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("docker stop returned %d", resp.StatusCode)
	}
	return nil
}

// StreamLogs streams container logs for a service.
func (o *DockerObserver) StreamLogs(ctx context.Context, serviceName string, opts LogOptions) (<-chan LogEntry, error) {
	// Find container by label.
	containers, err := o.listContainers(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("no container found for service %s", serviceName)
	}

	tail := "100"
	if opts.Tail > 0 {
		tail = fmt.Sprintf("%d", opts.Tail)
	}

	follow := "false"
	if opts.Follow {
		follow = "true"
	}

	url := fmt.Sprintf("%s/v1.43/containers/%s/logs?stdout=true&stderr=true&tail=%s&follow=%s&timestamps=true",
		o.host, containers[0].ID, tail, follow)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating logs request: %w", err)
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("streaming logs: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("docker logs returned %d", resp.StatusCode)
	}

	ch := make(chan LogEntry, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)

		buf := make([]byte, 8192)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				// Docker multiplexed log format: 8-byte header + payload.
				// For simplicity, parse as raw text lines.
				lines := strings.Split(string(buf[:n]), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					entry := LogEntry{
						Timestamp: time.Now().UTC(),
						Stream:    "stdout",
						Message:   line,
					}
					select {
					case ch <- entry:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return ch, nil
}

// --- internal helpers ---

type dockerPortBinding struct {
	HostPort string `json:"HostPort"`
}

func buildDockerPortConfig(ports []string) (map[string]struct{}, map[string][]dockerPortBinding, error) {
	exposedPorts := make(map[string]struct{})
	portBindings := make(map[string][]dockerPortBinding)

	for _, raw := range ports {
		port := strings.TrimSpace(raw)
		if port == "" {
			continue
		}

		parts := strings.Split(port, ":")
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("invalid port mapping %q: expected hostPort:containerPort", raw)
		}

		hostPort, containerPort, err := normalizeDockerPortMapping(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), raw)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := portBindings[containerPort]; exists {
			return nil, nil, fmt.Errorf("invalid port mapping %q: duplicate container port %s", raw, containerPort)
		}

		exposedPorts[containerPort] = struct{}{}
		portBindings[containerPort] = []dockerPortBinding{{HostPort: hostPort}}
	}

	return exposedPorts, portBindings, nil
}

func normalizeDockerPortMapping(hostPort, containerPort, raw string) (string, string, error) {
	if hostPort == "" || containerPort == "" {
		return "", "", fmt.Errorf("invalid port mapping %q: host and container ports are required", raw)
	}
	if err := validateDockerPortNumber(hostPort); err != nil {
		return "", "", fmt.Errorf("invalid port mapping %q: invalid host port %q: %w", raw, hostPort, err)
	}

	containerNumber := containerPort
	protocol := "tcp"
	if strings.Contains(containerPort, "/") {
		parts := strings.Split(containerPort, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return "", "", fmt.Errorf("invalid port mapping %q: expected containerPort[/protocol]", raw)
		}
		containerNumber = strings.TrimSpace(parts[0])
		protocol = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	if err := validateDockerPortNumber(containerNumber); err != nil {
		return "", "", fmt.Errorf("invalid port mapping %q: invalid container port %q: %w", raw, containerNumber, err)
	}
	switch protocol {
	case "tcp", "udp", "sctp":
	default:
		return "", "", fmt.Errorf("invalid port mapping %q: unsupported protocol %q", raw, protocol)
	}

	return hostPort, containerNumber + "/" + protocol, nil
}

func validateDockerPortNumber(port string) error {
	parsed, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	if parsed < 1 || parsed > 65535 {
		return fmt.Errorf("must be between 1 and 65535")
	}
	return nil
}

func cleanDockerBinds(volumes []string) []string {
	binds := make([]string, 0, len(volumes))
	for _, volume := range volumes {
		volume = strings.TrimSpace(volume)
		if volume != "" {
			binds = append(binds, volume)
		}
	}
	return binds
}

func (o *DockerObserver) listContainers(ctx context.Context, targetName string) ([]DockerContainer, error) {
	containers, err := o.listContainersByLabel(ctx, targetName)
	if err != nil {
		return nil, err
	}
	if len(containers) > 0 {
		return containers, nil
	}

	containers, err = o.listAllContainers(ctx)
	if err != nil {
		return nil, err
	}
	matched := containers[:0]
	for _, container := range containers {
		if dockerContainerNameMatches(container.Names, targetName) {
			matched = append(matched, container)
		}
	}
	return matched, nil
}

func (o *DockerObserver) listContainersByLabel(ctx context.Context, targetName string) ([]DockerContainer, error) {
	filters, err := json.Marshal(map[string][]string{"label": []string{"bahia.service=" + targetName}})
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("all", "1")
	query.Set("filters", string(filters))
	return o.listContainersRaw(ctx, query)
}

func (o *DockerObserver) listAllContainers(ctx context.Context) ([]DockerContainer, error) {
	query := url.Values{}
	query.Set("all", "1")
	return o.listContainersRaw(ctx, query)
}

func (o *DockerObserver) listContainersRaw(ctx context.Context, query url.Values) ([]DockerContainer, error) {
	requestURL := fmt.Sprintf("%s/v1.43/containers/json", o.host)
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned %d", resp.StatusCode)
	}
	var containers []DockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}
	return containers, nil
}

func (o *DockerObserver) firstContainer(ctx context.Context, targetName string) (*DockerContainer, error) {
	containers, err := o.listContainers(ctx, targetName)
	if err != nil {
		return nil, err
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("no container found for target %s", targetName)
	}
	return &containers[0], nil
}

func dockerContainerNameMatches(names []string, targetName string) bool {
	targetName = strings.TrimPrefix(strings.TrimSpace(targetName), "/")
	if targetName == "" {
		return false
	}
	for _, name := range names {
		if strings.TrimPrefix(name, "/") == targetName {
			return true
		}
	}
	return false
}

func (o *DockerObserver) stopAndRemove(ctx context.Context, serviceName string) error {
	containers, err := o.listContainers(ctx, serviceName)
	if err != nil {
		return err
	}
	for _, c := range containers {
		// Stop.
		stopURL := fmt.Sprintf("%s/v1.43/containers/%s/stop?t=10", o.host, c.ID)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, stopURL, nil)
		resp, err := o.httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}

		// Remove.
		rmURL := fmt.Sprintf("%s/v1.43/containers/%s?force=true", o.host, c.ID)
		req, _ = http.NewRequestWithContext(ctx, http.MethodDelete, rmURL, nil)
		resp, err = o.httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
	return nil
}

func (o *DockerObserver) pullImage(ctx context.Context, image string) error {
	url := fmt.Sprintf("%s/v1.43/images/create?fromImage=%s", o.host, image)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker pull returned %d", resp.StatusCode)
	}
	// Drain response body (pull progress).
	buf := make([]byte, 1024)
	for {
		if _, err := resp.Body.Read(buf); err != nil {
			break
		}
	}
	return nil
}

type dockerImageInspect struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

func (o *DockerObserver) resolveImageRepoDigest(ctx context.Context, imageRef, imageID string) (string, string, error) {
	inspectRef := imageID
	if inspectRef == "" {
		inspectRef = imageRef
	}
	if inspectRef == "" {
		return "", "", nil
	}
	requestURL := fmt.Sprintf("%s/v1.43/images/%s/json", o.host, url.PathEscape(inspectRef))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("docker image inspect returned %d", resp.StatusCode)
	}
	var inspected dockerImageInspect
	if err := json.NewDecoder(resp.Body).Decode(&inspected); err != nil {
		return "", "", err
	}
	for _, repoDigest := range inspected.RepoDigests {
		if repo, digest := splitRepoDigest(repoDigest); digest != "" {
			return repo, digest, nil
		}
	}
	return "", extractDigest(inspected.ID), nil
}

func splitRepoDigest(repoDigest string) (string, string) {
	parts := strings.SplitN(repoDigest, "@", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// Compile-time interface checks.
var (
	_ Observer         = (*DockerObserver)(nil)
	_ Runtime          = (*DockerObserver)(nil)
	_ LifecycleRuntime = (*DockerObserver)(nil)
)

func extractDigest(imageID string) string {
	if idx := strings.Index(imageID, "sha256:"); idx >= 0 {
		return imageID[idx:]
	}
	return imageID
}

func mapDockerState(state string) domain.HealthStatus {
	switch strings.ToLower(state) {
	case "running":
		return domain.HealthStatusHealthy
	case "created", "restarting":
		return domain.HealthStatusStarting
	case "exited", "dead", "removing":
		return domain.HealthStatusStopped
	case "paused":
		return domain.HealthStatusUnhealthy
	default:
		return domain.HealthStatusUnknown
	}
}

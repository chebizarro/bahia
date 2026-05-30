package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// RuntimeExecutionMode identifies how a runtime control client executes
// mutating operations.
type RuntimeExecutionMode string

const (
	ExecutionModeCLI       RuntimeExecutionMode = "cli"
	ExecutionModeEngineAPI RuntimeExecutionMode = "engine_api"
)

// RuntimeControlClient is the narrow common seam for runtime execution
// metadata. Runtime-specific control clients extend it with only the operations
// their appliers need.
type RuntimeControlClient interface {
	ExecutionMode() RuntimeExecutionMode
}

// DockerControlClient owns Docker-compatible Engine API execution for desired
// state apply. It contains runtime control operations only; desired-state
// decisions stay in the applier.
type DockerControlClient interface {
	RuntimeControlClient
	FindManagedContainer(ctx context.Context, spec *domain.DesiredServiceSpec) (*DockerContainer, error)
	EnsureNetworks(ctx context.Context, specs []domain.NetworkSpec) error
	EnsureVolumes(ctx context.Context, specs []domain.VolumeSpec) error
	PullImage(ctx context.Context, image string) error
	StopContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string) error
	CreateContainer(ctx context.Context, name string, configs *DockerContainerConfigs) (string, error)
	StartContainer(ctx context.Context, containerID string) error
	ConnectNetwork(ctx context.Context, containerID, networkName, alias string) error
}

type dockerEngineControlClient struct {
	observer *DockerObserver
}

// NewDockerEngineControlClient creates a Docker-compatible Engine API control
// client for Docker and Podman runtimes.
func NewDockerEngineControlClient(observer *DockerObserver) DockerControlClient {
	return &dockerEngineControlClient{observer: observer}
}

func (c *dockerEngineControlClient) ExecutionMode() RuntimeExecutionMode {
	return ExecutionModeEngineAPI
}

func (c *dockerEngineControlClient) FindManagedContainer(ctx context.Context, spec *domain.DesiredServiceSpec) (*DockerContainer, error) {
	return FindBahiaManagedContainer(ctx, c.observer, spec)
}

func (c *dockerEngineControlClient) EnsureNetworks(ctx context.Context, specs []domain.NetworkSpec) error {
	return EnsureNetworks(ctx, c.observer, specs)
}

func (c *dockerEngineControlClient) EnsureVolumes(ctx context.Context, specs []domain.VolumeSpec) error {
	return EnsureVolumes(ctx, c.observer, specs)
}

func (c *dockerEngineControlClient) PullImage(ctx context.Context, image string) error {
	url := fmt.Sprintf("%s/v1.44/images/create?fromImage=%s", c.observer.host, image)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	if authHeader, ok := c.observer.registryAuthHeader(image); ok {
		req.Header.Set("X-Registry-Auth", authHeader)
	}
	resp, err := c.observer.httpClient.Do(req)
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

func (c *dockerEngineControlClient) StopContainer(ctx context.Context, containerID string) error {
	stopURL := fmt.Sprintf("%s/v1.44/containers/%s/stop?t=10", c.observer.host, containerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stopURL, nil)
	if err != nil {
		return fmt.Errorf("creating stop request: %w", err)
	}
	resp, err := c.observer.httpClient.Do(req)
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

func (c *dockerEngineControlClient) RemoveContainer(ctx context.Context, containerID string) error {
	rmURL := fmt.Sprintf("%s/v1.44/containers/%s?force=true", c.observer.host, containerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, rmURL, nil)
	if err != nil {
		return fmt.Errorf("creating remove request: %w", err)
	}
	resp, err := c.observer.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("removing container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker remove returned %d", resp.StatusCode)
	}
	return nil
}

func (c *dockerEngineControlClient) CreateContainer(ctx context.Context, name string, configs *DockerContainerConfigs) (string, error) {
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

	createURL := fmt.Sprintf("%s/v1.44/containers/create?name=%s", c.observer.host, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.observer.httpClient.Do(req)
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

func (c *dockerEngineControlClient) StartContainer(ctx context.Context, containerID string) error {
	startURL := fmt.Sprintf("%s/v1.44/containers/%s/start", c.observer.host, containerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL, nil)
	if err != nil {
		return fmt.Errorf("creating start request: %w", err)
	}

	resp, err := c.observer.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker start returned %d", resp.StatusCode)
	}
	return nil
}

func (c *dockerEngineControlClient) ConnectNetwork(ctx context.Context, containerID, networkName, alias string) error {
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

	connectURL := fmt.Sprintf("%s/v1.44/networks/%s/connect", c.observer.host, networkName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.observer.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("docker network connect returned %d", resp.StatusCode)
	}
	return nil
}

func (o *DockerObserver) ControlClient() DockerControlClient {
	if o.controlClient != nil {
		return o.controlClient
	}
	return NewDockerEngineControlClient(o)
}

var _ DockerControlClient = (*dockerEngineControlClient)(nil)

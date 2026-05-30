package runtime

import (
	"context"

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
	return c.observer.pullImage(ctx, image)
}

func (c *dockerEngineControlClient) StopContainer(ctx context.Context, containerID string) error {
	return c.observer.stopContainer(ctx, containerID)
}

func (c *dockerEngineControlClient) RemoveContainer(ctx context.Context, containerID string) error {
	return c.observer.removeContainer(ctx, containerID)
}

func (c *dockerEngineControlClient) CreateContainer(ctx context.Context, name string, configs *DockerContainerConfigs) (string, error) {
	return c.observer.createContainer(ctx, name, configs)
}

func (c *dockerEngineControlClient) StartContainer(ctx context.Context, containerID string) error {
	return c.observer.startContainer(ctx, containerID)
}

func (c *dockerEngineControlClient) ConnectNetwork(ctx context.Context, containerID, networkName, alias string) error {
	return c.observer.connectNetwork(ctx, containerID, networkName, alias)
}

func (o *DockerObserver) ControlClient() DockerControlClient {
	return NewDockerEngineControlClient(o)
}

var _ DockerControlClient = (*dockerEngineControlClient)(nil)

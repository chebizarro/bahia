package runtime

import (
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// PodmanObserver wraps DockerObserver since Podman emulates Docker's API.
// This provides explicit Podman support with appropriate defaults while
// reusing all Docker adapter logic.
type PodmanObserver struct {
	*DockerObserver
}

// NewPodmanObserver creates a new PodmanObserver.
// The podmanHost should be a socket path, typically:
//   - Rootless: unix:///run/user/<UID>/podman/podman.sock
//   - Rootful: unix:///run/podman/podman.sock
func NewPodmanObserver(podmanHost string, logger *zap.Logger) *PodmanObserver {
	return &PodmanObserver{
		DockerObserver: NewDockerObserver(podmanHost, logger),
	}
}

// Type returns the podman runtime type.
func (o *PodmanObserver) Type() domain.RuntimeType {
	return domain.RuntimeTypePodman
}

// Compile-time interface checks.
var (
	_ Observer = (*PodmanObserver)(nil)
	_ Runtime  = (*PodmanObserver)(nil)
)

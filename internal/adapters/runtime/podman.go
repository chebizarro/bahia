package runtime

import (
	"context"
	"fmt"
	"strings"

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

// ---------------------------------------------------------------------------
// Desired-state capability — Podman delegates to Docker's implementation
// ---------------------------------------------------------------------------

// SupportsDesiredState returns true — the Podman adapter supports desired-state
// convergence by delegating to the embedded Docker implementation via the
// Docker-compatible API.
func (o *PodmanObserver) SupportsDesiredState() bool { return true }

// ApplyDesiredState converges a single Podman-managed service toward its
// desired runtime state. It validates the spec for Podman compatibility,
// delegates to the Docker implementation, and relabels the result as "podman".
//
// Known Podman compatibility constraints checked here:
//   - Docker Compose extensions are rejected (Podman uses podman-compose or
//     its own Compose compatibility, but this adapter targets the Engine API)
//   - KubernetesExtension is rejected (wrong runtime)
//   - Docker Swarm-style networking (overlay driver) is unsupported
func (o *PodmanObserver) ApplyDesiredState(ctx context.Context, req DesiredStateApplyRequest) (*DesiredStateApplyResult, error) {
	if req.TargetService == nil {
		return nil, fmt.Errorf("podman apply: target service spec is nil")
	}

	// Validate Podman compatibility before delegating.
	if err := validatePodmanCompatibility(req.TargetService); err != nil {
		return nil, fmt.Errorf("podman apply: %w", err)
	}

	// Delegate to the embedded Docker implementation.
	result, err := o.DockerObserver.ApplyDesiredState(ctx, req)
	if err != nil {
		return nil, err
	}

	// Relabel the result renderer as "podman".
	result.Renderer = "podman"
	return result, nil
}

// ---------------------------------------------------------------------------
// Podman compatibility validation
// ---------------------------------------------------------------------------

// ErrPodmanIncompatible is returned when a desired-state spec uses features
// that are not supported by the Podman Docker-compatible API.
var ErrPodmanIncompatible = fmt.Errorf("configuration not compatible with Podman runtime")

// validatePodmanCompatibility checks whether a DesiredServiceSpec uses
// features known to be incompatible with Podman's Docker-compatible API.
// Returns a descriptive error for unsupported configurations.
func validatePodmanCompatibility(spec *domain.DesiredServiceSpec) error {
	if spec == nil {
		return nil
	}

	// Reject Compose extensions — Podman Engine API adapter does not use
	// docker-compose/podman-compose; it manages containers directly.
	if spec.ComposeExtension != nil {
		return fmt.Errorf("%w: compose_extension is set but the Podman adapter "+
			"manages containers directly via the Engine API, not via Compose; "+
			"use a Compose runtime target instead", ErrPodmanIncompatible)
	}

	// Reject Kubernetes extensions — wrong runtime.
	if spec.KubernetesExtension != nil {
		return fmt.Errorf("%w: kubernetes_extension is set but the target runtime "+
			"is Podman; use a Kubernetes runtime target instead", ErrPodmanIncompatible)
	}

	// Check for Docker Swarm overlay networks — not supported by Podman.
	if spec.DockerExtension != nil {
		if netCfg, ok := spec.DockerExtension.NetworkingConfig["Driver"]; ok {
			if driver, ok := netCfg.(string); ok && strings.EqualFold(driver, "overlay") {
				return fmt.Errorf("%w: overlay network driver is not supported by Podman; "+
					"use bridge or macvlan instead", ErrPodmanIncompatible)
			}
		}
	}

	// Check for Docker-specific host config features not available in Podman.
	if spec.DockerExtension != nil {
		for _, unsupported := range podmanUnsupportedHostConfig {
			if _, ok := spec.DockerExtension.HostConfig[unsupported]; ok {
				return fmt.Errorf("%w: Docker host config field %q is not supported "+
					"by Podman; remove it or use a Docker runtime target",
					ErrPodmanIncompatible, unsupported)
			}
		}
	}

	return nil
}

// podmanUnsupportedHostConfig lists Docker HostConfig fields that have no
// Podman equivalent or behave differently enough to warrant explicit rejection.
var podmanUnsupportedHostConfig = []string{
	"Isolation",  // Windows container isolation — not applicable to Podman.
	"CgroupnsMode", // Podman defaults differ; explicit values may conflict.
}

// Compile-time interface checks.
var (
	_ Observer            = (*PodmanObserver)(nil)
	_ Runtime             = (*PodmanObserver)(nil)
	_ DesiredStateApplier = (*PodmanObserver)(nil)
)

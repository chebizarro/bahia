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

// PodmanObserver wraps DockerObserver since Podman emulates Docker's API.
// This provides explicit Podman support with appropriate defaults while
// reusing all Docker adapter logic.
type PodmanObserver struct {
	*DockerObserver
	rootlessInfo PodmanRootlessInfo
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

	// Collect Podman-specific warnings.
	var warnings []string

	// Health check startup probe validation.
	warnings = append(warnings, validatePodmanHealthcheck(req.TargetService)...)

	// Rootless cgroup resource limit validation.
	rootless, err := o.DetectRootless(ctx)
	if err != nil {
		// Non-fatal: log but continue with rootless=false assumption.
		o.DockerObserver.logger.Warn("podman rootless detection failed; assuming rootful",
			zap.Error(err))
	}
	warnings = append(warnings, validatePodmanRootlessResources(req.TargetService, rootless)...)

	// Log any Podman-specific warnings.
	for _, w := range warnings {
		o.DockerObserver.logger.Warn(w)
	}

	// Delegate to the embedded Docker implementation.
	result, err := o.DockerObserver.ApplyDesiredState(ctx, req)
	if err != nil {
		return nil, err
	}

	// Relabel the result renderer as "podman" and attach warnings.
	result.Renderer = "podman"
	result.Warnings = append(result.Warnings, warnings...)
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
	"Isolation",    // Windows container isolation — not applicable to Podman.
	"CgroupnsMode", // Podman defaults differ; explicit values may conflict.
}

// ---------------------------------------------------------------------------
// Health check startup probe validation
// ---------------------------------------------------------------------------

// validatePodmanHealthcheck checks whether a DesiredServiceSpec healthcheck
// uses features that behave differently on Podman vs Docker and returns
// warnings for configurations that may diverge.
//
// Known differences:
//   - Podman does not support Docker's StartInterval field (Docker 25+).
//     The Docker Engine API simply ignores unknown fields, so StartInterval
//     is silently dropped by Podman. We emit a warning.
//   - Podman's StartPeriod implementation is simpler: during the start period,
//     failing health checks do not count toward the failure threshold, but
//     Podman does NOT run probes at a separate startup interval. The health
//     check interval remains constant.
//   - Health check retries and timing may exhibit slight differences due to
//     Podman's conmon-based health check runner vs Docker's built-in checker.
func validatePodmanHealthcheck(spec *domain.DesiredServiceSpec) []string {
	var warnings []string

	// Check portable healthcheck config.
	if spec.Healthcheck != nil {
		if spec.Healthcheck.StartPeriod != "" {
			warnings = append(warnings, "podman healthcheck: StartPeriod is supported but "+
				"Podman does not run probes at a separate startup interval during this period; "+
				"failing probes are simply ignored until the period expires")
		}
	}

	// Check Docker extension healthcheck for StartInterval (Docker 25+ only).
	if spec.DockerExtension != nil && len(spec.DockerExtension.Healthcheck) > 0 {
		if _, hasStartInterval := spec.DockerExtension.Healthcheck["StartInterval"]; hasStartInterval {
			warnings = append(warnings, "podman healthcheck: StartInterval is a Docker 25+ "+
				"feature not supported by Podman; it will be silently ignored")
		}
	}

	return warnings
}

// ---------------------------------------------------------------------------
// Rootless cgroup resource limit validation
// ---------------------------------------------------------------------------

// podmanRootlessResourceFields lists Docker HostConfig resource-limit fields
// that may be silently ignored or behave differently in Podman rootless mode.
var podmanRootlessResourceFields = []string{
	"Memory",
	"MemorySwap",
	"MemoryReservation",
	"CpuShares",
	"NanoCpus",
	"CpuPeriod",
	"CpuQuota",
	"CpusetCpus",
	"CpusetMems",
	"BlkioWeight",
	"PidsLimit",
	"DeviceRequests",
}

// validatePodmanRootlessResources checks whether a DesiredServiceSpec sets
// cgroup resource limits that may not apply in Podman rootless mode.
// Returns warnings for each resource field that is set and may be ignored.
func validatePodmanRootlessResources(spec *domain.DesiredServiceSpec, rootless bool) []string {
	if !rootless {
		return nil
	}
	if spec.DockerExtension == nil || len(spec.DockerExtension.HostConfig) == 0 {
		return nil
	}

	var warnings []string
	for _, field := range podmanRootlessResourceFields {
		if v, ok := spec.DockerExtension.HostConfig[field]; ok && v != nil {
			warnings = append(warnings, fmt.Sprintf(
				"podman rootless: HostConfig.%s is set but may be silently ignored "+
					"in rootless mode depending on cgroup version and kernel configuration",
				field))
		}
	}
	return warnings
}

// ---------------------------------------------------------------------------
// Podman system info rootless detection
// ---------------------------------------------------------------------------

// PodmanRootlessInfo holds the result of probing the Podman API for rootless
// mode. This is cached per observer instance.
type PodmanRootlessInfo struct {
	Rootless bool
	Probed   bool
}

// DetectRootless probes the Podman API /info endpoint to determine whether
// the daemon is running in rootless mode. The result is cached.
func (o *PodmanObserver) DetectRootless(ctx context.Context) (bool, error) {
	if o.rootlessInfo.Probed {
		return o.rootlessInfo.Rootless, nil
	}

	rootless, err := probePodmanRootless(ctx, o.DockerObserver)
	if err != nil {
		return false, err
	}
	o.rootlessInfo = PodmanRootlessInfo{Rootless: rootless, Probed: true}
	return rootless, nil
}

// probePodmanRootless queries the Podman-compatible /info endpoint and checks
// the host.security.rootless field.
func probePodmanRootless(ctx context.Context, obs *DockerObserver) (bool, error) {
	url := fmt.Sprintf("%s/v1.44/info", obs.host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("podman rootless probe: %w", err)
	}
	resp, err := obs.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("podman rootless probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("podman rootless probe: /info returned %d", resp.StatusCode)
	}

	// Parse the nested SecurityOptions / rootless field.
	// Podman's /info JSON includes host.security.rootless: true.
	var info struct {
		Host struct {
			Security struct {
				Rootless bool `json:"rootless"`
			} `json:"security"`
		} `json:"host"`
		// Fallback: Docker-compatible format uses SecurityOptions string list.
		SecurityOptions []string `json:"SecurityOptions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return false, fmt.Errorf("podman rootless probe: decoding /info: %w", err)
	}

	if info.Host.Security.Rootless {
		return true, nil
	}

	// Fallback: check SecurityOptions for "rootless" string.
	for _, opt := range info.SecurityOptions {
		if strings.Contains(strings.ToLower(opt), "rootless") {
			return true, nil
		}
	}

	return false, nil
}

// Compile-time interface checks.
var (
	_ Observer            = (*PodmanObserver)(nil)
	_ Runtime             = (*PodmanObserver)(nil)
	_ DesiredStateApplier = (*PodmanObserver)(nil)
)

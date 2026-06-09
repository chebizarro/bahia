package runtime

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Desired-state capability contract
// ---------------------------------------------------------------------------

// DesiredStateApplier is the capability interface for runtimes that support
// desired-state convergence. Runtimes that implement this interface can
// accept a declarative service specification and converge the actual runtime
// state to match.
//
// This is an additive seam — existing runtimes that do not yet support
// desired-state convergence continue to work through the imperative Runtime
// interface unchanged. Callers can probe for capability via type assertion
// or the SupportsDesiredState method.
type DesiredStateApplier interface {
	// ApplyDesiredState converges a single service toward its desired runtime
	// state. Secrets are resolved at apply time and supplied via the request;
	// they are never persisted in the desired-state plan.
	ApplyDesiredState(ctx context.Context, req DesiredStateApplyRequest) (*DesiredStateApplyResult, error)

	// SupportsDesiredState reports whether this runtime adapter currently
	// supports desired-state convergence. Adapters that return false will
	// return ErrDesiredStateNotSupported from ApplyDesiredState.
	SupportsDesiredState() bool
}

// ---------------------------------------------------------------------------
// Request / Result types
// ---------------------------------------------------------------------------

// DesiredStateApplyRequest carries everything needed to converge a single
// service to its desired runtime state.
type DesiredStateApplyRequest struct {
	// EnvironmentPlan is the full environment-level desired-state plan.
	// The applier uses it for cross-service context (e.g. network topology,
	// dependency ordering).
	EnvironmentPlan *domain.DesiredEnvironmentPlan

	// TargetService is the specific service spec to converge.
	TargetService *domain.DesiredServiceSpec

	// Secrets are plaintext secret values resolved at apply time, keyed by
	// environment variable name. These are never persisted in the plan.
	Secrets map[string]string

	// PullPolicy controls image pull behavior ("always", "if-not-present", "never").
	PullPolicy string

	// DryRun, when true, validates and renders the desired state without
	// mutating any runtime resources. The result still contains the rendered
	// hash and resource names for preview purposes.
	DryRun bool
}

// DesiredStateApplyResult reports the outcome of a desired-state convergence.
type DesiredStateApplyResult struct {
	// Renderer identifies which runtime rendered the state (e.g. "compose",
	// "docker", "podman", "kubernetes").
	Renderer string

	// ExecutionMode identifies whether runtime control executed through the
	// direct Engine API or explicit CLI compatibility mode.
	ExecutionMode RuntimeExecutionMode

	// DesiredHash is the deterministic hash of the applied service spec.
	DesiredHash string

	// EnvironmentRevision is the environment-level revision hash after apply.
	EnvironmentRevision string

	// ResourceIDs are runtime-specific identifiers for the created/updated
	// resources (e.g. container IDs, Compose service names).
	ResourceIDs []string

	// ResourceNames are human-readable resource names corresponding to
	// ResourceIDs (e.g. container names, service keys).
	ResourceNames []string

	// ObservationHints provides hints for post-apply observation, allowing
	// the observer to quickly locate the resources that were just applied.
	ObservationHints *ObservationHints

	// Warnings collects non-fatal issues encountered during apply (e.g.
	// image pull fallback, deprecated config, port conflicts resolved).
	Warnings []string
}

// ObservationHints carries runtime-specific identifiers that help the
// observation layer quickly locate resources after a desired-state apply.
type ObservationHints struct {
	// ContainerID is the primary container ID (Docker/Podman).
	ContainerID string

	// NetworkIDs are the IDs of networks the service is attached to.
	NetworkIDs []string

	// VolumeNames are the names of volumes mounted by the service.
	VolumeNames []string

	// PodName is the primary pod name (Kubernetes).
	PodName string

	// DeploymentName is the Deployment resource name (Kubernetes).
	DeploymentName string

	// Namespace is the Kubernetes namespace.
	Namespace string
}

// ---------------------------------------------------------------------------
// Sentinel error
// ---------------------------------------------------------------------------

// ErrDesiredStateNotSupported is returned by adapters that do not yet support
// desired-state convergence.
var ErrDesiredStateNotSupported = fmt.Errorf("runtime adapter does not support desired-state convergence")

// ---------------------------------------------------------------------------
// Stub implementations for adapters not yet migrated
// ---------------------------------------------------------------------------

// Compile-time assertions that implementations satisfy DesiredStateApplier.
var (
	_ DesiredStateApplier = (*DockerObserver)(nil)
	_ DesiredStateApplier = (*ComposeRuntime)(nil)
	_ DesiredStateApplier = (*KubernetesRuntime)(nil)
	_ DesiredStateApplier = (*PodmanObserver)(nil)
)

// Docker desired-state implementation lives in docker_apply.go.

// SupportsDesiredState returns true — the Compose adapter supports
// desired-state convergence via full-project apply.
func (r *ComposeRuntime) SupportsDesiredState() bool { return true }

// ApplyDesiredState converges the Compose project to match the desired
// environment plan using full-project render, staged validation, and
// `docker compose up -d --remove-orphans`.
func (r *ComposeRuntime) ApplyDesiredState(ctx context.Context, req DesiredStateApplyRequest) (*DesiredStateApplyResult, error) {
	applier := NewComposeDesiredStateApplier(r, r.logger) // uses production exec runner
	return applier.ApplyDesiredState(ctx, req)
}

// Kubernetes desired-state implementation lives in kubernetes_desired_state.go.

// Podman desired-state implementation lives in podman.go — it delegates to
// the Docker implementation after Podman-specific compatibility validation.

// ---------------------------------------------------------------------------
// Capability probe helper
// ---------------------------------------------------------------------------

// AsDesiredStateApplier checks whether a Runtime also implements
// DesiredStateApplier and returns it. This is a convenience for callers that
// hold a Runtime interface and want to probe for the capability.
func AsDesiredStateApplier(rt Runtime) (DesiredStateApplier, bool) {
	dsa, ok := rt.(DesiredStateApplier)
	if !ok {
		return nil, false
	}
	return dsa, dsa.SupportsDesiredState()
}

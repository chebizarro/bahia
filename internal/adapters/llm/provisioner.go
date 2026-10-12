package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ProvisionCandidateRequest contains the immutable context needed to provision
// or observe one selected LLM backend candidate.
type ProvisionCandidateRequest struct {
	Route       *domain.LLMRoute
	Release     *domain.LLMRelease
	Environment *domain.Environment
	Run         *domain.LLMDeploymentRun
	BackendKind domain.LLMBackendKind
	Worker      *domain.Worker
	TargetName  string
}

// ProvisionCandidateResult is returned after backend provisioning succeeds.
type ProvisionCandidateResult struct {
	BackendKind     domain.LLMBackendKind `json:"backend_kind"`
	BackendEndpoint string                `json:"backend_endpoint"`
	EndpointRef     string                `json:"endpoint_ref,omitempty"`
	TargetName      string                `json:"target_name"`
	WorkerPubkey    string                `json:"worker_pubkey,omitempty"`
	WorkerName      string                `json:"worker_name,omitempty"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
}

// BackendObservation captures normalized backend state.
type BackendObservation struct {
	BackendKind     domain.LLMBackendKind `json:"backend_kind"`
	BackendEndpoint string                `json:"backend_endpoint"`
	HealthStatus    domain.HealthStatus   `json:"health_status"`
	Source          string                `json:"source"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
}

// Provisioner provisions, observes, and deprovisions one backend family.
type Provisioner interface {
	Provision(ctx context.Context, req ProvisionCandidateRequest) (*ProvisionCandidateResult, error)
	Observe(ctx context.Context, req ProvisionCandidateRequest) (*BackendObservation, error)
	Deprovision(ctx context.Context, req ProvisionCandidateRequest) error
}

// ProvisionerResolver resolves a provisioner by backend kind.
type ProvisionerResolver interface {
	Resolve(kind domain.LLMBackendKind) (Provisioner, error)
}

// StaticProvisionerResolver is a simple map-backed resolver.
type StaticProvisionerResolver map[domain.LLMBackendKind]Provisioner

// Resolve returns the provisioner registered for kind.
func (r StaticProvisionerResolver) Resolve(kind domain.LLMBackendKind) (Provisioner, error) {
	p, ok := r[kind]
	if !ok || p == nil {
		return nil, fmt.Errorf("no LLM provisioner registered for backend kind %q", kind)
	}
	return p, nil
}

func runtimeManagedKind(kind domain.LLMBackendKind) bool {
	switch kind {
	case domain.LLMBackendKindVLLM, domain.LLMBackendKindOllama, domain.LLMBackendKindLlamaCPP:
		return true
	default:
		return false
	}
}

func targetNameFor(req ProvisionCandidateRequest) string {
	if strings.TrimSpace(req.TargetName) != "" {
		return sanitizeTargetName(req.TargetName)
	}
	routeName := "route"
	if req.Route != nil && req.Route.Name != "" {
		routeName = req.Route.Name
	}
	runID := "run"
	if req.Run != nil && req.Run.ID.String() != "" {
		runID = req.Run.ID.String()
	}
	if len(runID) > 8 {
		runID = runID[:8]
	}
	return sanitizeTargetName("llm-" + routeName + "-" + runID)
}

func sanitizeTargetName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "llm-backend"
	}
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

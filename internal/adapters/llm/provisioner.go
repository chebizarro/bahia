package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// SecretResolver resolves an opaque Bahia secret UUID and records the access.
type SecretResolver interface {
	ResolveSecretWithAudit(ctx context.Context, ref string, opts domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error)
}

func resolveHeaderSecretRefs(ctx context.Context, literal, refs map[string]string, resolver SecretResolver, reason string) (map[string]string, error) {
	if len(literal) == 0 && len(refs) == 0 {
		return nil, nil
	}
	headers := make(map[string]string, len(literal)+len(refs))
	for name, value := range literal {
		headers[name] = value
	}
	if len(refs) > 0 && resolver == nil {
		return nil, fmt.Errorf("%s secret refs require a secret resolver", reason)
	}
	for name, ref := range refs {
		if _, exists := headers[name]; exists {
			return nil, fmt.Errorf("%s header %q cannot define both a literal value and a secret ref", reason, name)
		}
		value, _, err := resolver.ResolveSecretWithAudit(ctx, ref, domain.SecretResolveOptions{
			Actor:     "bahia:llm",
			Reason:    reason,
			RequestID: name,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve %s header %q: %w", reason, name, err)
		}
		headers[name] = value
	}
	return headers, nil
}

// ResolveGatewayHeaders resolves secret-backed gateway headers without
// retaining plaintext in the LLM route or release records.
func ResolveGatewayHeaders(ctx context.Context, literal, refs map[string]string, resolver SecretResolver) (map[string]string, error) {
	return resolveHeaderSecretRefs(ctx, literal, refs, resolver, "llm_gateway_route")
}

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

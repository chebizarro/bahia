package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type fakeRuntime struct {
	deployedName  string
	deployedImage string
	deployOptions runtimeadapter.DeployOptions
	undeployed    string
}

type llmSecretResolverStub struct {
	values map[string]string
	calls  []domain.SecretResolveOptions
}

func (r *llmSecretResolverStub) ResolveSecretWithAudit(_ context.Context, ref string, opts domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error) {
	r.calls = append(r.calls, opts)
	value, ok := r.values[ref]
	if !ok {
		return "", domain.SecretAccessManifest{}, errors.New("secret not found")
	}
	return value, domain.SecretAccessManifest{}, nil
}

func (f *fakeRuntime) Type() domain.RuntimeType { return domain.RuntimeTypeDocker }
func (f *fakeRuntime) Deploy(_ context.Context, name, image string, opts runtimeadapter.DeployOptions) error {
	f.deployedName = name
	f.deployedImage = image
	f.deployOptions = opts
	return nil
}
func (f *fakeRuntime) Undeploy(_ context.Context, name string) error {
	f.undeployed = name
	return nil
}
func (f *fakeRuntime) StreamLogs(context.Context, string, runtimeadapter.LogOptions) (<-chan runtimeadapter.LogEntry, error) {
	return make(chan runtimeadapter.LogEntry), nil
}
func (f *fakeRuntime) Observe(context.Context, uuid.UUID, uuid.UUID, string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{HealthStatus: domain.HealthStatusStarting, Source: "fake"}, nil
}

func TestRuntimeProvisionerDeploysWithLabelsEnvAndEndpoint(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected health path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	healthURL, _ := url.Parse(health.URL)
	_, healthPort, _ := net.SplitHostPort(healthURL.Host)

	fake := &fakeRuntime{}
	p, err := NewRuntimeProvisioner(domain.LLMBackendKindVLLM, config.RuntimeConfig{}, zap.NewNop(),
		WithRuntimeFactory(func(*domain.WorkerRuntimeTarget) (runtimeadapter.Runtime, error) { return fake, nil }),
		WithHTTPClient(health.Client()),
	)
	if err != nil {
		t.Fatalf("new provisioner: %v", err)
	}
	routeID := uuid.New()
	releaseID := uuid.New()
	envID := uuid.New()
	runID := uuid.New()
	req := ProvisionCandidateRequest{
		Route:       &domain.LLMRoute{ID: routeID, Name: "chat", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "public-chat"}},
		Release:     &domain.LLMRelease{ID: releaseID, Version: "v1", ModelRef: "hf/model", ModelSource: domain.ModelSourceHuggingFace, RuntimeBackend: &domain.LLMRuntimeManagedBackendConfig{Image: "vllm:latest", HostPort: mustAtoi(healthPort), ContainerPort: 8000, HealthPath: "/health"}},
		Environment: &domain.Environment{ID: envID, Name: "prod"},
		Run:         &domain.LLMDeploymentRun{ID: runID},
		BackendKind: domain.LLMBackendKindVLLM,
		Worker:      &domain.Worker{PubKey: "pk", Name: "worker", RuntimeTarget: &domain.WorkerRuntimeTarget{EndpointRef: "prod", PublicBaseURL: health.URL}},
	}

	result, err := p.Provision(t.Context(), req)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.BackendEndpoint != health.URL {
		t.Fatalf("unexpected backend endpoint: %s", result.BackendEndpoint)
	}
	if fake.deployedImage != "vllm:latest" || fake.deployedName == "" {
		t.Fatalf("runtime deploy not called: %#v", fake)
	}
	if fake.deployOptions.Environment["BAHIA_MODEL_REF"] != "hf/model" || fake.deployOptions.Environment["BAHIA_PUBLIC_MODEL"] != "public-chat" {
		t.Fatalf("missing env injection: %#v", fake.deployOptions.Environment)
	}
	if fake.deployOptions.Labels["bahia.llm_route"] != routeID.String() || fake.deployOptions.Labels["bahia.llm_run"] != runID.String() {
		t.Fatalf("missing labels: %#v", fake.deployOptions.Labels)
	}
	if len(fake.deployOptions.Ports) != 1 || fake.deployOptions.Ports[0] != healthPort+":8000" {
		t.Fatalf("unexpected ports: %#v", fake.deployOptions.Ports)
	}

	obs, err := p.Observe(t.Context(), req)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Fatalf("HTTP health should be authoritative: %#v", obs)
	}
}

func mustAtoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func TestExternalAPIProvisionerAttachesHealthyUnmanagedBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	p := NewExternalAPIProvisioner(&http.Client{Timeout: time.Second})
	req := ProvisionCandidateRequest{Release: &domain.LLMRelease{ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: server.URL, HealthURL: server.URL + "/health"}}}
	result, err := p.Provision(t.Context(), req)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.BackendKind != domain.LLMBackendKindExternalAPI || result.BackendEndpoint != server.URL {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Metadata["lifecycle_mode"] != "unmanaged_attachment" || result.Metadata["provider_resource_created"] != false || result.Metadata["health_verified"] != true {
		t.Fatalf("external lifecycle metadata is not explicit: %#v", result.Metadata)
	}
	obs, err := p.Observe(t.Context(), req)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Fatalf("expected healthy external observation: %#v", obs)
	}
	if err := p.Deprovision(t.Context(), req); !errors.Is(err, ErrExternalLifecycleUnmanaged) {
		t.Fatalf("deprovision error = %v, want ErrExternalLifecycleUnmanaged", err)
	}
}

func TestExternalAPIProvisionerRejectsUnhealthyAttachment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	p := NewExternalAPIProvisioner(server.Client())
	req := ProvisionCandidateRequest{Release: &domain.LLMRelease{ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: server.URL, HealthURL: server.URL + "/health"}}}

	result, err := p.Provision(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "cannot attach unhealthy") {
		t.Fatalf("Provision() = (%#v, %v), want unhealthy attachment error", result, err)
	}
	if result != nil {
		t.Fatalf("Provision() result = %#v, want nil", result)
	}
}

func TestExternalAPIProvisionerHealthIgnoresChatBudgetResponses(t *testing.T) {
	var healthHits, chatHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			healthHits++
			w.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			chatHits++
			http.Error(w, "budget exhausted", http.StatusPaymentRequired)
		default:
			t.Fatalf("unexpected probe path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p := NewExternalAPIProvisioner(server.Client())
	req := ProvisionCandidateRequest{Release: &domain.LLMRelease{ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: server.URL, HealthURL: server.URL + "/healthz"}}}
	obs, err := p.Observe(t.Context(), req)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Fatalf("expected /healthz to be authoritative despite chat budget responses: %#v", obs)
	}
	if healthHits != 1 || chatHits != 0 {
		t.Fatalf("expected one health probe and no chat probes, health=%d chat=%d", healthHits, chatHits)
	}
}

func TestExternalAPIProvisionerResolvesSecretBackedHealthHeaders(t *testing.T) {
	const secretRef = "2e9b746d-58c3-4e86-9ca5-a0184bc2918e"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer health-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	resolver := &llmSecretResolverStub{values: map[string]string{secretRef: "Bearer health-token"}}
	p := NewExternalAPIProvisioner(server.Client(), WithExternalAPISecretResolver(resolver))
	req := ProvisionCandidateRequest{Release: &domain.LLMRelease{ExternalBackend: &domain.LLMExternalBackendConfig{
		BaseURL:                server.URL,
		HealthURL:              server.URL + "/healthz",
		HealthHeaderSecretRefs: map[string]string{"Authorization": secretRef},
	}}}

	obs, err := p.Observe(t.Context(), req)
	if err != nil || obs.HealthStatus != domain.HealthStatusHealthy {
		t.Fatalf("Observe() = (%#v, %v)", obs, err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0].Reason != "llm_external_health_probe" {
		t.Fatalf("secret resolution was not audited with health-probe context: %#v", resolver.calls)
	}
}

package service

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

const defaultAssistantContextMaxChars = 24000

// AssistantContextBuilder assembles bounded operational context for the assistant planner.
type AssistantContextBuilder struct {
	services AssistantServiceRegistry
	llm      AssistantLLMRegistry
	ml       AssistantMLRegistry
	dns      AssistantDNSRegistry
	workers  AssistantWorkerCatalog
	maxChars int
}

// AssistantContextBuilderConfig configures AssistantContextBuilder.
type AssistantContextBuilderConfig struct {
	MaxChars int
}

// AssistantServiceRegistry is the service-registry surface needed for context assembly.
type AssistantServiceRegistry interface {
	ListServices(ctx context.Context) ([]domain.Service, error)
	ListAllStates(ctx context.Context) ([]domain.EnvironmentServiceState, error)
	GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error)
}

// AssistantLLMRegistry is the LLM-registry surface needed for context assembly.
type AssistantLLMRegistry interface {
	ListRoutes(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error)
	ListAllRouteStates(ctx context.Context) ([]domain.LLMRouteState, error)
	GetRoute(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error)
}

// AssistantMLRegistry is the ML-registry surface needed for context assembly.
type AssistantMLRegistry interface {
	ListModels(ctx context.Context, task domain.MLTaskKind, limit, offset int) ([]domain.MLModel, error)
	ListInferenceEndpoints(ctx context.Context, envID uuid.UUID, limit, offset int) ([]domain.MLInferenceEndpoint, error)
	ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error)
	GetModel(ctx context.Context, id uuid.UUID) (*domain.MLModel, error)
	GetInferenceEndpoint(ctx context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error)
}

// AssistantDNSRegistry provides DNS state for assistant context.
type AssistantDNSRegistry interface {
	ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error)
	ListDNSZones(ctx context.Context) ([]domain.DNSZone, error)
	ListDNSPolicies(ctx context.Context) ([]domain.DNSPolicy, error)
}

// AssistantWorkerCatalog is the worker-catalog surface needed for context assembly.
type AssistantWorkerCatalog interface {
	GetOnlineWorkers() []*domain.Worker
	GetWorker(ctx context.Context, pubkey string) (*domain.Worker, error)
}

// NewAssistantContextBuilder creates a context builder.
func NewAssistantContextBuilder(
	services AssistantServiceRegistry,
	llm AssistantLLMRegistry,
	ml AssistantMLRegistry,
	dns AssistantDNSRegistry,
	workers AssistantWorkerCatalog,
	config AssistantContextBuilderConfig,
) *AssistantContextBuilder {
	if config.MaxChars <= 0 {
		config.MaxChars = defaultAssistantContextMaxChars
	}
	return &AssistantContextBuilder{services: services, llm: llm, ml: ml, dns: dns, workers: workers, maxChars: config.MaxChars}
}

// BuildContext returns a structured, bounded, secret-free text block for LLM planning.
func (b *AssistantContextBuilder) BuildContext(ctx context.Context, routeContext map[string]string, selectedRefs []string, transcriptSummary string) (string, error) {
	var out strings.Builder
	writeSection(&out, "Assistant Operational Context")
	writeKV(&out, "Context rules", "IDs, names, states, health, and timestamps only. Secret values are intentionally omitted.")

	if len(routeContext) > 0 {
		writeSection(&out, "Route Context")
		for _, key := range sortedMapKeys(routeContext) {
			writeKV(&out, key, routeContext[key])
		}
	}
	if strings.TrimSpace(transcriptSummary) != "" {
		writeSection(&out, "Transcript Summary")
		out.WriteString(truncateString(transcriptSummary, b.maxChars/5))
		out.WriteString("\n")
	}

	writeSection(&out, "Selected References")
	if len(selectedRefs) == 0 {
		out.WriteString("- none\n")
	} else {
		for _, ref := range selectedRefs {
			out.WriteString("- ")
			out.WriteString(ref)
			out.WriteString("\n")
		}
	}

	if err := b.appendResolvedResources(ctx, &out, routeContext, selectedRefs); err != nil {
		return "", err
	}
	if err := b.appendRegistrySummaries(ctx, &out); err != nil {
		return "", err
	}

	return truncateContext(out.String(), b.maxChars), nil
}

func (b *AssistantContextBuilder) appendResolvedResources(ctx context.Context, out *strings.Builder, routeContext map[string]string, selectedRefs []string) error {
	writeSection(out, "Resolved Resources")
	resolved := false
	for _, ref := range selectedRefs {
		if b.resolveRef(ctx, out, ref) {
			resolved = true
		}
	}
	if typ, id := routeContext["resource_type"], routeContext["resource_id"]; typ != "" && id != "" {
		if b.resolveRef(ctx, out, typ+":"+id) {
			resolved = true
		}
	}
	if !resolved {
		out.WriteString("- no direct resource matches; planner should use summaries below and ask for clarification when ambiguous\n")
	}
	return nil
}

func (b *AssistantContextBuilder) resolveRef(ctx context.Context, out *strings.Builder, ref string) bool {
	kind, idText := splitRef(ref)
	if kind == "worker" {
		if b.workers == nil {
			return false
		}
		worker, err := b.workers.GetWorker(ctx, idText)
		if err == nil && worker != nil {
			fmt.Fprintf(out, "- worker pubkey=%s name=%s status=%s queue_depth=%d last_advertisement_at=%s\n", worker.PubKey, worker.Name, worker.Status, worker.CurrentQueueDepth, worker.LastAdvertisementAt.Format(timeFormat))
			return true
		}
		return false
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		return false
	}
	switch kind {
	case "service", "deployment":
		if b.services == nil {
			return false
		}
		svc, err := b.services.GetService(ctx, id)
		if err == nil && svc != nil {
			fmt.Fprintf(out, "- service id=%s name=%s runtime=%s target=%s updated_at=%s\n", svc.ID, svc.Name, svc.RuntimeType, svc.RuntimeTargetName(), svc.UpdatedAt.Format(timeFormat))
			return true
		}
	case "llm", "llm_route", "route":
		if b.llm == nil {
			return false
		}
		route, err := b.llm.GetRoute(ctx, id)
		if err == nil && route != nil {
			fmt.Fprintf(out, "- llm_route id=%s name=%s updated_at=%s\n", route.ID, route.Name, route.UpdatedAt.Format(timeFormat))
			return true
		}
	case "ml_model", "model":
		if b.ml == nil {
			return false
		}
		model, err := b.ml.GetModel(ctx, id)
		if err == nil && model != nil {
			fmt.Fprintf(out, "- ml_model id=%s slug=%s name=%s tasks=%s updated_at=%s\n", model.ID, model.Slug, model.Name, joinMLTasks(model.TaskKinds), model.UpdatedAt.Format(timeFormat))
			return true
		}
	case "ml_endpoint", "endpoint":
		if b.ml == nil {
			return false
		}
		endpoint, err := b.ml.GetInferenceEndpoint(ctx, id)
		if err == nil && endpoint != nil {
			fmt.Fprintf(out, "- ml_endpoint id=%s name=%s environment_id=%s tasks=%s updated_at=%s\n", endpoint.ID, endpoint.Name, endpoint.EnvironmentID, joinMLTasks(endpoint.TaskKinds), endpoint.UpdatedAt.Format(timeFormat))
			return true
		}
	}
	return false
}

func (b *AssistantContextBuilder) appendRegistrySummaries(ctx context.Context, out *strings.Builder) error {
	if b.services != nil {
		services, err := b.services.ListServices(ctx)
		if err != nil {
			return fmt.Errorf("list services: %w", err)
		}
		states, err := b.services.ListAllStates(ctx)
		if err != nil {
			return fmt.Errorf("list service states: %w", err)
		}
		writeSection(out, "Services")
		for _, svc := range limitServices(services, 25) {
			fmt.Fprintf(out, "- id=%s name=%s runtime=%s target=%s updated_at=%s\n", svc.ID, svc.Name, svc.RuntimeType, svc.RuntimeTargetName(), svc.UpdatedAt.Format(timeFormat))
		}
		writeSection(out, "Service States")
		for _, st := range limitServiceStates(states, 40) {
			fmt.Fprintf(out, "- service_id=%s environment_id=%s drift=%s desired_artifact_id=%s active_observation_id=%s updated_at=%s\n", st.ServiceID, st.EnvironmentID, st.DriftStatus, uuidPtrString(st.DesiredArtifactID), uuidPtrString(st.CurrentObservationID), st.UpdatedAt.Format(timeFormat))
		}
	}
	if b.llm != nil {
		routes, err := b.llm.ListRoutes(ctx, 25, 0)
		if err != nil {
			return fmt.Errorf("list llm routes: %w", err)
		}
		states, err := b.llm.ListAllRouteStates(ctx)
		if err != nil {
			return fmt.Errorf("list llm route states: %w", err)
		}
		writeSection(out, "LLM Routes")
		for _, route := range routes {
			fmt.Fprintf(out, "- id=%s name=%s updated_at=%s\n", route.ID, route.Name, route.UpdatedAt.Format(timeFormat))
		}
		writeSection(out, "LLM Route States")
		for _, st := range limitLLMStates(states, 40) {
			fmt.Fprintf(out, "- route_id=%s environment_id=%s drift=%s gateway=%s backend_health=%s backend_kind=%s updated_at=%s\n", st.RouteID, st.EnvironmentID, st.DriftStatus, st.GatewayStatus, st.BackendHealth, st.BackendKind, st.UpdatedAt.Format(timeFormat))
		}
	}
	if b.ml != nil {
		models, err := b.ml.ListModels(ctx, "", 25, 0)
		if err != nil {
			return fmt.Errorf("list ml models: %w", err)
		}
		endpoints, err := b.ml.ListInferenceEndpoints(ctx, uuid.Nil, 25, 0)
		if err != nil {
			return fmt.Errorf("list ml endpoints: %w", err)
		}
		states, err := b.ml.ListInferenceStates(ctx)
		if err != nil {
			return fmt.Errorf("list ml inference states: %w", err)
		}
		writeSection(out, "ML Models")
		for _, model := range models {
			fmt.Fprintf(out, "- id=%s slug=%s name=%s tasks=%s updated_at=%s\n", model.ID, model.Slug, model.Name, joinMLTasks(model.TaskKinds), model.UpdatedAt.Format(timeFormat))
		}
		writeSection(out, "ML Endpoints")
		for _, endpoint := range endpoints {
			fmt.Fprintf(out, "- id=%s name=%s environment_id=%s tasks=%s updated_at=%s\n", endpoint.ID, endpoint.Name, endpoint.EnvironmentID, joinMLTasks(endpoint.TaskKinds), endpoint.UpdatedAt.Format(timeFormat))
		}
		writeSection(out, "ML Inference States")
		for _, st := range limitMLStates(states, 40) {
			fmt.Fprintf(out, "- endpoint_id=%s environment_id=%s drift=%s gateway=%s backend_health=%s runtime=%s updated_at=%s\n", st.EndpointID, st.EnvironmentID, st.DriftStatus, st.GatewayStatus, st.BackendHealth, st.RuntimeKind, st.UpdatedAt.Format(timeFormat))
		}
	}
	if b.dns != nil {
		zones, err := b.dns.ListDNSZones(ctx)
		if err != nil {
			return fmt.Errorf("list dns zones: %w", err)
		}
		endpoints, err := b.dns.ListDNSEndpoints(ctx)
		if err != nil {
			return fmt.Errorf("list dns endpoints: %w", err)
		}
		policies, err := b.dns.ListDNSPolicies(ctx)
		if err != nil {
			return fmt.Errorf("list dns policies: %w", err)
		}
		writeSection(out, "DNS Zones")
		for _, zone := range limitSlice(zones, 25) {
			fmt.Fprintf(out, "- name=%s visibility=%s backend=%s\n", zone.Name, zone.Visibility, zone.BackendRef)
		}
		writeSection(out, "DNS Endpoints")
		for _, ep := range limitSlice(endpoints, 40) {
			fmt.Fprintf(out, "- fqdn=%s family=%s zone=%s address=%s type=%s health=%s drift=%s\n", ep.FQDN, ep.Family, ep.Zone, ep.Address, assistantDNSRecordType(ep.Address), ep.Health, ep.DriftStatus)
		}
		writeSection(out, "DNS Policies")
		for _, p := range limitSlice(policies, 20) {
			fmt.Fprintf(out, "- id=%s name=%s zone=%s enabled=%t\n", p.ID, p.Name, uuidPtrString(p.ZoneID), p.Enabled)
		}
	}
	if b.workers != nil {
		writeSection(out, "Workers")
		workers := b.workers.GetOnlineWorkers()
		sort.Slice(workers, func(i, j int) bool { return workers[i].Name < workers[j].Name })
		if len(workers) > 25 {
			workers = workers[:25]
		}
		for _, worker := range workers {
			fmt.Fprintf(out, "- pubkey=%s name=%s status=%s queue_depth=%d max_jobs=%d arch=%s runtimes=%s accelerators=%s last_advertisement_at=%s\n", worker.PubKey, worker.Name, worker.Status, worker.CurrentQueueDepth, worker.MaxConcurrentJobs, worker.Architecture, joinMLRuntimes(worker.MLCapabilities.Runtimes), strings.Join(worker.MLCapabilities.Accelerators, ","), worker.LastAdvertisementAt.Format(timeFormat))
		}
	}
	return nil
}

const timeFormat = "2006-01-02T15:04:05Z07:00"

func writeSection(out *strings.Builder, title string) { fmt.Fprintf(out, "\n## %s\n", title) }
func writeKV(out *strings.Builder, key, value string) { fmt.Fprintf(out, "- %s: %s\n", key, value) }

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func splitRef(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	for _, sep := range []string{":", "/"} {
		if parts := strings.SplitN(ref, sep, 2); len(parts) == 2 {
			return strings.ToLower(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1])
		}
	}
	return "", ref
}

func truncateContext(value string, maxChars int) string {
	if maxChars <= 0 || len(value) <= maxChars {
		return value
	}
	marker := "\n\n[context truncated to fit assistant context budget]\n"
	if maxChars <= len(marker) {
		return value[:maxChars]
	}
	return value[:maxChars-len(marker)] + marker
}

func truncateString(value string, maxChars int) string {
	if maxChars <= 0 || len(value) <= maxChars {
		return value
	}
	return value[:maxChars] + "..."
}

func uuidPtrString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func joinMLTasks(tasks []domain.MLTaskKind) string {
	parts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		parts = append(parts, string(task))
	}
	return strings.Join(parts, ",")
}

func joinMLRuntimes(runtimes []domain.MLRuntimeKind) string {
	parts := make([]string, 0, len(runtimes))
	for _, runtime := range runtimes {
		parts = append(parts, string(runtime))
	}
	return strings.Join(parts, ",")
}

func limitServices(items []domain.Service, n int) []domain.Service {
	if len(items) <= n {
		return items
	}
	return items[:n]
}
func limitServiceStates(items []domain.EnvironmentServiceState, n int) []domain.EnvironmentServiceState {
	if len(items) <= n {
		return items
	}
	return items[:n]
}
func limitLLMStates(items []domain.LLMRouteState, n int) []domain.LLMRouteState {
	if len(items) <= n {
		return items
	}
	return items[:n]
}
func limitMLStates(items []domain.MLInferenceState, n int) []domain.MLInferenceState {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

func limitSlice[T any](items []T, n int) []T {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

func assistantDNSRecordType(address string) domain.DNSRecordType {
	ip := net.ParseIP(strings.TrimSpace(address))
	if ip == nil {
		return domain.DNSRecordTypeCNAME
	}
	if ip.To4() != nil {
		return domain.DNSRecordTypeA
	}
	return domain.DNSRecordTypeAAAA
}

var _ interface {
	BuildContext(ctx context.Context, routeContext map[string]string, selectedRefs []string, transcriptSummary string) (string, error)
} = (*AssistantContextBuilder)(nil)

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/google/uuid"
	userdocs "github.com/openagentsinc/bahia/internal/docs"
	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	defaultAssistantContextMaxChars = 24000
	// assistantDocsContextBudgetDivisor reserves at most one quarter of MaxChars for selected documentation refs.
	assistantDocsContextBudgetDivisor = 4
)

// AssistantContextBuilder assembles bounded operational context and replay-backed
// model history for the assistant planner/agent loop.
type AssistantContextBuilder struct {
	services          AssistantServiceRegistry
	llm               AssistantLLMRegistry
	ml                AssistantMLRegistry
	dns               AssistantDNSRegistry
	workers           AssistantWorkerCatalog
	docs              AssistantDocsProvider
	transcriptHistory AssistantTranscriptHistoryProvider
	maxChars          int
	transcriptLimit   int
}

// AssistantContextBuilderConfig configures AssistantContextBuilder.
type AssistantContextBuilderConfig struct {
	MaxChars          int
	TranscriptHistory AssistantTranscriptHistoryProvider
	TranscriptLimit   int
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

// AssistantDocsProvider is the central documentation surface needed for selected docs refs.
type AssistantDocsProvider interface {
	Read(ctx context.Context, topic string) (userdocs.Document, error)
}

// AssistantTranscriptHistoryProvider replays decrypted transcript messages for
// model history. AssistantTranscriptStore implements this interface.
type AssistantTranscriptHistoryProvider interface {
	BuildModelHistory(ctx context.Context, sessionID string, limit int) ([]domain.AssistantAgentMessage, error)
}

// NewAssistantContextBuilder creates a context builder.
func NewAssistantContextBuilder(
	services AssistantServiceRegistry,
	llm AssistantLLMRegistry,
	ml AssistantMLRegistry,
	dns AssistantDNSRegistry,
	workers AssistantWorkerCatalog,
	docs AssistantDocsProvider,
	config AssistantContextBuilderConfig,
) *AssistantContextBuilder {
	if config.MaxChars <= 0 {
		config.MaxChars = defaultAssistantContextMaxChars
	}
	if config.TranscriptLimit <= 0 {
		config.TranscriptLimit = defaultAssistantTranscriptReplayLimit
	}
	return &AssistantContextBuilder{services: services, llm: llm, ml: ml, dns: dns, workers: workers, docs: docs, transcriptHistory: config.TranscriptHistory, maxChars: config.MaxChars, transcriptLimit: config.TranscriptLimit}
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
	if b.transcriptHistory != nil {
		if sessionID := assistantSessionIDFromContext(routeContext); sessionID != "" {
			messages, err := b.transcriptHistory.BuildModelHistory(ctx, sessionID, b.transcriptLimit)
			if err != nil {
				return "", fmt.Errorf("replay assistant transcript history: %w", err)
			}
			b.appendTranscriptHistory(&out, messages, b.maxChars/5)
		}
	} else if strings.TrimSpace(transcriptSummary) != "" {
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

	b.appendDocumentationReferences(ctx, &out, selectedRefs)
	if err := b.appendResolvedResources(ctx, &out, routeContext, selectedRefs); err != nil {
		return "", err
	}
	if err := b.appendRegistrySummaries(ctx, &out); err != nil {
		return "", err
	}

	return truncateContext(out.String(), b.maxChars), nil
}

// BuildModelHistory returns provider-neutral messages for the agent loop: a
// system message with bounded operational context, decrypted transcript replay in
// chronological order, and the current operator prompt when provided. It is the
// memory path for agentic turns; TranscriptSummary is intentionally not used.
func (b *AssistantContextBuilder) BuildModelHistory(ctx context.Context, sessionID string, routeContext map[string]string, selectedRefs []string, currentOperatorPrompt string) ([]domain.AssistantAgentMessage, error) {
	contextRoute := cloneAssistantContextStringMap(routeContext)
	delete(contextRoute, "session_id")
	delete(contextRoute, "assistant_session_id")
	delete(contextRoute, "session")
	contextBlock, err := b.BuildContext(ctx, contextRoute, selectedRefs, "")
	if err != nil {
		return nil, err
	}
	messages := []domain.AssistantAgentMessage{{
		Role: domain.AssistantAgentMessageRoleSystem,
		Content: []domain.AssistantAgentContentBlock{{
			Type: domain.AssistantAgentContentText,
			Text: contextBlock,
		}},
	}}
	if b.transcriptHistory != nil && strings.TrimSpace(sessionID) != "" {
		history, err := b.transcriptHistory.BuildModelHistory(ctx, strings.TrimSpace(sessionID), b.transcriptLimit)
		if err != nil {
			return nil, fmt.Errorf("replay assistant transcript history: %w", err)
		}
		messages = append(messages, history...)
	}
	if strings.TrimSpace(currentOperatorPrompt) != "" {
		messages = append(messages, domain.AssistantAgentMessage{
			Role: domain.AssistantAgentMessageRoleUser,
			Content: []domain.AssistantAgentContentBlock{{
				Type: domain.AssistantAgentContentText,
				Text: strings.TrimSpace(currentOperatorPrompt),
			}},
		})
	}
	return messages, nil
}

func (b *AssistantContextBuilder) appendTranscriptHistory(out *strings.Builder, messages []domain.AssistantAgentMessage, maxChars int) {
	if len(messages) == 0 || maxChars <= 0 {
		return
	}
	var history strings.Builder
	writeSection(&history, "Transcript History")
	for _, message := range messages {
		line := fmt.Sprintf("- %s: %s\n", message.Role, assistantAgentMessageText(message))
		appendLineWithinBudget(&history, maxChars, line)
		if history.Len() >= maxChars {
			break
		}
	}
	out.WriteString(truncateToBudget(history.String(), maxChars))
	if !strings.HasSuffix(out.String(), "\n") {
		out.WriteString("\n")
	}
}

func assistantAgentMessageText(message domain.AssistantAgentMessage) string {
	parts := make([]string, 0, len(message.Content)+1)
	for _, block := range message.Content {
		switch block.Type {
		case domain.AssistantAgentContentText:
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, strings.TrimSpace(block.Text))
			}
		case domain.AssistantAgentContentJSON:
			if len(block.JSON) > 0 {
				encoded, err := json.Marshal(block.JSON)
				if err == nil {
					parts = append(parts, string(encoded))
				}
			}
		case domain.AssistantAgentContentToolCall:
			if block.ToolCall != nil {
				parts = append(parts, "tool_call "+block.ToolCall.Name)
			}
		case domain.AssistantAgentContentObservation:
			if block.Observation != nil {
				parts = append(parts, strings.TrimSpace(block.Observation.Summary))
			}
		}
	}
	if message.Observation != nil && strings.TrimSpace(message.Observation.Summary) != "" {
		parts = append(parts, strings.TrimSpace(message.Observation.Summary))
	}
	if len(parts) == 0 && len(message.ToolCalls) > 0 {
		names := make([]string, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			names = append(names, call.Name)
		}
		parts = append(parts, "tool_calls "+strings.Join(names, ","))
	}
	return strings.Join(parts, " ")
}

func assistantSessionIDFromContext(routeContext map[string]string) string {
	for _, key := range []string{"session_id", "assistant_session_id", "session"} {
		if value := strings.TrimSpace(routeContext[key]); value != "" {
			return value
		}
	}
	return ""
}

func cloneAssistantContextStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (b *AssistantContextBuilder) appendDocumentationReferences(ctx context.Context, out *strings.Builder, selectedRefs []string) {
	docRefs := selectedDocumentationRefs(selectedRefs)
	if len(docRefs) == 0 {
		return
	}

	docsBudget := b.maxChars / assistantDocsContextBudgetDivisor
	if docsBudget <= 0 {
		return
	}

	var docsOut strings.Builder
	writeSection(&docsOut, "Documentation References")
	omitted := false
	for i, docRef := range docRefs {
		remaining := docsBudget - docsOut.Len()
		if remaining <= 0 {
			omitted = true
			break
		}
		refsLeft := len(docRefs) - i
		entryBudget := remaining / refsLeft
		if refsLeft == 1 {
			entryBudget = remaining
		}
		if entryBudget <= 0 {
			omitted = true
			break
		}
		entry := b.documentationReferenceEntry(ctx, docRef, entryBudget)
		if len(entry) > entryBudget {
			entry = truncateToBudget(entry, entryBudget)
		}
		if entry == "" {
			omitted = true
			break
		}
		docsOut.WriteString(entry)
	}
	if omitted {
		appendLineWithinBudget(&docsOut, docsBudget, "- additional documentation refs omitted due to docs context budget\n")
	}

	out.WriteString(truncateToBudget(docsOut.String(), docsBudget))
	if !strings.HasSuffix(out.String(), "\n") {
		out.WriteString("\n")
	}
}

func (b *AssistantContextBuilder) documentationReferenceEntry(ctx context.Context, docRef documentationRef, entryBudget int) string {
	var entry strings.Builder
	if docRef.topic == "" {
		fmt.Fprintf(&entry, "- unresolved documentation ref=%s reason=invalid documentation topic\n", docRef.ref)
		return entry.String()
	}
	if b.docs == nil {
		fmt.Fprintf(&entry, "- unresolved documentation ref=%s topic=%s reason=documentation provider unavailable\n", docRef.ref, docRef.topic)
		return entry.String()
	}

	doc, err := b.docs.Read(ctx, docRef.topic)
	if err != nil {
		fmt.Fprintf(&entry, "- unresolved documentation ref=%s topic=%s reason=%s\n", docRef.ref, docRef.topic, documentationRefErrorReason(err))
		return entry.String()
	}

	fmt.Fprintf(&entry, "- ref=%s topic=%s title=%q source=%s href=%s\n", docRef.ref, doc.Topic.Topic, doc.Topic.Title, doc.Topic.SourcePath, doc.Topic.Href)
	excerptBudget := entryBudget - entry.Len() - len("  excerpt:\n")
	if excerptBudget <= 0 {
		appendLineWithinBudget(&entry, entryBudget, "  excerpt: omitted because documentation reference metadata filled its budget\n")
		return entry.String()
	}
	excerpt := documentationExcerpt(doc.Markdown, excerptBudget)
	if strings.TrimSpace(excerpt) == "" {
		appendLineWithinBudget(&entry, entryBudget, "  excerpt: empty documentation topic\n")
		return entry.String()
	}
	entry.WriteString("  excerpt:\n")
	for _, line := range strings.Split(excerpt, "\n") {
		appendLineWithinBudget(&entry, entryBudget, "    "+line+"\n")
		if entry.Len() >= entryBudget {
			break
		}
	}
	return entry.String()
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

type documentationRef struct {
	ref   string
	topic string
}

func selectedDocumentationRefs(selectedRefs []string) []documentationRef {
	seen := map[string]bool{}
	refs := []documentationRef{}
	for _, ref := range selectedRefs {
		topic, ok := documentationTopicFromRef(ref)
		if !ok {
			continue
		}
		key := topic
		if key == "" {
			key = strings.TrimSpace(ref)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, documentationRef{ref: strings.TrimSpace(ref), topic: topic})
	}
	return refs
}

func documentationTopicFromRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "docs:") {
		return strings.TrimSpace(ref[len("docs:"):]), true
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if strings.EqualFold(parsed.Scheme, "bahia") && strings.EqualFold(parsed.Host, "docs") {
		return strings.Trim(strings.TrimSpace(parsed.Path), "/"), true
	}
	return "", false
}

func documentationRefErrorReason(err error) string {
	switch {
	case errors.Is(err, userdocs.ErrNotFound):
		return "documentation topic not found"
	case errors.Is(err, userdocs.ErrInvalidTopic):
		return "invalid documentation topic"
	case errors.Is(err, userdocs.ErrOutsideDocsRoot):
		return "documentation path rejected"
	default:
		return err.Error()
	}
}

func appendLineWithinBudget(out *strings.Builder, maxChars int, line string) {
	remaining := maxChars - out.Len()
	if remaining <= 0 {
		return
	}
	out.WriteString(truncateToBudget(line, remaining))
}

func truncateToBudget(value string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if len(value) <= maxChars {
		return value
	}
	marker := "..."
	if maxChars <= len(marker) {
		return value[:maxChars]
	}
	return value[:maxChars-len(marker)] + marker
}

func documentationExcerpt(markdown string, maxChars int) string {
	markdown = strings.TrimSpace(strings.ReplaceAll(markdown, "\r\n", "\n"))
	if maxChars <= 0 || markdown == "" {
		return ""
	}
	if len(markdown) <= maxChars {
		return markdown
	}
	marker := "\n...[documentation excerpt truncated]..."
	if maxChars <= len(marker) {
		return strings.TrimSpace(markdown[:maxChars])
	}
	cut := maxChars - len(marker)
	if newline := strings.LastIndex(markdown[:cut], "\n"); newline > 0 && cut-newline < 160 {
		cut = newline
	}
	return strings.TrimSpace(markdown[:cut]) + marker
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
	BuildModelHistory(ctx context.Context, sessionID string, routeContext map[string]string, selectedRefs []string, currentOperatorPrompt string) ([]domain.AssistantAgentMessage, error)
} = (*AssistantContextBuilder)(nil)

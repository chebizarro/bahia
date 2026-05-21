package reconcile

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// LLMDNSProjectionSource exposes LLM route state to the DNS projector.
type LLMDNSProjectionSource interface {
	ListAllRouteStates(ctx context.Context) ([]domain.LLMRouteState, error)
	GetRoute(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error)
}

// MLDNSProjectionSource exposes ML inference state to the DNS projector.
type MLDNSProjectionSource interface {
	ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error)
	GetInferenceEndpoint(ctx context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error)
}

// WorkerDNSProjectionSource exposes worker state to the DNS projector.
type WorkerDNSProjectionSource interface {
	List(ctx context.Context, status string, limit int) ([]domain.Worker, error)
}

// DNSProjector derives DNS endpoints and records from authoritative infrastructure state.
type DNSProjector struct {
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	states       repository.EnvironmentServiceStateRepository
	observations repository.RuntimeObservationRepository
	llmSource    LLMDNSProjectionSource
	mlSource     MLDNSProjectionSource
	workers      WorkerDNSProjectionSource
	cfg          config.DNSConfig
	logger       *zap.Logger
}

func NewDNSProjector(services repository.ServiceRepository, environments repository.EnvironmentRepository, states repository.EnvironmentServiceStateRepository, observations repository.RuntimeObservationRepository, llmSource LLMDNSProjectionSource, mlSource MLDNSProjectionSource, workers WorkerDNSProjectionSource, cfg config.DNSConfig, logger *zap.Logger) *DNSProjector {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DNSProjector{services: services, environments: environments, states: states, observations: observations, llmSource: llmSource, mlSource: mlSource, workers: workers, cfg: cfg, logger: logger}
}

func (p *DNSProjector) ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error) {
	projectedAt := time.Now().UTC()
	servicesByID, err := p.servicesByID(ctx)
	if err != nil {
		return nil, err
	}
	envsByID, err := p.environmentsByID(ctx)
	if err != nil {
		return nil, err
	}

	var endpoints []domain.DNSEndpoint
	if p.cfg.Projection.Services && p.states != nil && p.observations != nil {
		serviceEndpoints, err := p.projectServiceEndpoints(ctx, servicesByID, envsByID, projectedAt)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, serviceEndpoints...)
	}
	if p.cfg.Projection.LLMRoutes && p.llmSource != nil {
		llmEndpoints, err := p.projectLLMEndpoints(ctx, envsByID, projectedAt)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, llmEndpoints...)
	}
	if p.cfg.Projection.MLEndpoints && p.mlSource != nil {
		mlEndpoints, err := p.projectMLEndpoints(ctx, envsByID, projectedAt)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, mlEndpoints...)
	}
	if p.cfg.Projection.Workers && p.workers != nil {
		workerEndpoints, err := p.projectWorkerEndpoints(ctx, projectedAt)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, workerEndpoints...)
	}
	return finalizeDNSEndpoints(endpoints)
}

func (p *DNSProjector) ProjectZoneRecords(ctx context.Context) (map[string][]domain.DNSRecord, error) {
	endpoints, err := p.ListDNSEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	ttls := p.zoneTTLs()
	recordsByZone := make(map[string][]domain.DNSRecord)
	for _, endpoint := range endpoints {
		ttl := ttls[endpoint.Zone]
		if ttl <= 0 {
			ttl = p.cfg.DefaultTTL
		}
		if ttl <= 0 {
			ttl = 300
		}
		recordsByZone[endpoint.Zone] = append(recordsByZone[endpoint.Zone], domain.DNSRecord{
			Zone:             endpoint.Zone,
			Name:             endpoint.Name,
			FQDN:             endpoint.FQDN,
			Type:             dnsRecordType(endpoint.Address),
			Value:            endpoint.Address,
			TTL:              ttl,
			SourceCoordinate: endpoint.Coordinate,
		})
	}
	for zone := range recordsByZone {
		sortDNSRecords(recordsByZone[zone])
	}
	return recordsByZone, nil
}

func (p *DNSProjector) servicesByID(ctx context.Context) (map[uuid.UUID]domain.Service, error) {
	out := map[uuid.UUID]domain.Service{}
	if p.services == nil {
		return out, nil
	}
	services, err := p.services.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services for DNS projection: %w", err)
	}
	for _, service := range services {
		out[service.ID] = service
	}
	return out, nil
}

func (p *DNSProjector) environmentsByID(ctx context.Context) (map[uuid.UUID]domain.Environment, error) {
	out := map[uuid.UUID]domain.Environment{}
	if p.environments == nil {
		return out, nil
	}
	environments, err := p.environments.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list environments for DNS projection: %w", err)
	}
	for _, environment := range environments {
		out[environment.ID] = environment
	}
	return out, nil
}

func (p *DNSProjector) projectServiceEndpoints(ctx context.Context, servicesByID map[uuid.UUID]domain.Service, envsByID map[uuid.UUID]domain.Environment, projectedAt time.Time) ([]domain.DNSEndpoint, error) {
	states, err := p.states.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list service states for DNS projection: %w", err)
	}
	endpoints := make([]domain.DNSEndpoint, 0, len(states))
	for _, state := range states {
		if state.DriftStatus != domain.DriftStatusInSync {
			continue
		}
		service, ok := servicesByID[state.ServiceID]
		if !ok {
			continue
		}
		environment, ok := envsByID[state.EnvironmentID]
		if !ok {
			continue
		}
		zone, ok := p.zoneForEnvironment(environment.Name)
		if !ok {
			p.logger.Warn("DNS service projection skipped because environment has no zone mapping", zap.String("environment", environment.Name), zap.String("service", service.Name))
			continue
		}
		observation, err := p.observations.GetLatest(ctx, state.ServiceID, state.EnvironmentID)
		if err != nil {
			return nil, fmt.Errorf("get latest runtime observation for DNS projection: %w", err)
		}
		if observation == nil || observation.HealthStatus != domain.HealthStatusHealthy || strings.TrimSpace(observation.ObservedHost) == "" {
			continue
		}
		name := dnsLabel(service.Name)
		endpoints = append(endpoints, domain.DNSEndpoint{
			ServiceID:      uuidPtr(service.ID),
			Family:         domain.DNSEndpointFamilyService,
			Name:           name,
			Environment:    strings.TrimSpace(environment.Name),
			Zone:           zone,
			FQDN:           fqdn(name, zone),
			Address:        strings.TrimSpace(observation.ObservedHost),
			Runtime:        string(service.RuntimeType),
			Health:         observation.HealthStatus,
			DriftStatus:    state.DriftStatus,
			Source:         "service_state",
			MaterializedAt: projectedAt,
		})
	}
	return endpoints, nil
}

func (p *DNSProjector) projectLLMEndpoints(ctx context.Context, envsByID map[uuid.UUID]domain.Environment, projectedAt time.Time) ([]domain.DNSEndpoint, error) {
	states, err := p.llmSource.ListAllRouteStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list LLM route states for DNS projection: %w", err)
	}
	endpoints := make([]domain.DNSEndpoint, 0, len(states))
	for _, state := range states {
		if state.GatewayStatus != domain.GatewayRouteStatusSynced || state.BackendHealth != domain.HealthStatusHealthy {
			continue
		}
		route, err := p.llmSource.GetRoute(ctx, state.RouteID)
		if err != nil {
			return nil, fmt.Errorf("get LLM route for DNS projection: %w", err)
		}
		if route == nil {
			continue
		}
		environment, ok := envsByID[state.EnvironmentID]
		if !ok {
			continue
		}
		zone, ok := p.zoneForEnvironment(environment.Name)
		if !ok {
			p.logger.Warn("DNS LLM projection skipped because environment has no zone mapping", zap.String("environment", environment.Name), zap.String("route", route.Name))
			continue
		}
		target := strings.TrimSpace(state.GatewayTarget)
		if target == "" {
			target = strings.TrimSpace(state.BackendEndpoint)
		}
		protocol, address, port, ok := parseEndpointTarget(target)
		if !ok {
			p.logger.Warn("DNS LLM projection skipped because target URL is invalid", zap.String("route", route.Name), zap.String("target", target))
			continue
		}
		name := dnsLabel(route.Name)
		endpoints = append(endpoints, domain.DNSEndpoint{
			LLMRouteID:     uuidPtr(route.ID),
			Family:         domain.DNSEndpointFamilyLLM,
			Name:           name,
			Environment:    strings.TrimSpace(environment.Name),
			Zone:           zone,
			FQDN:           fqdn(name, zone),
			Protocol:       protocol,
			Address:        address,
			Port:           port,
			Runtime:        string(state.BackendKind),
			Capabilities:   []string{"llm"},
			Health:         state.BackendHealth,
			DriftStatus:    state.DriftStatus,
			Source:         "llm_route_state",
			MaterializedAt: projectedAt,
		})
	}
	return endpoints, nil
}

func (p *DNSProjector) projectMLEndpoints(ctx context.Context, envsByID map[uuid.UUID]domain.Environment, projectedAt time.Time) ([]domain.DNSEndpoint, error) {
	states, err := p.mlSource.ListInferenceStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ML inference states for DNS projection: %w", err)
	}
	endpoints := make([]domain.DNSEndpoint, 0, len(states))
	for _, state := range states {
		if state.BackendHealth != domain.HealthStatusHealthy {
			continue
		}
		endpoint, err := p.mlSource.GetInferenceEndpoint(ctx, state.EndpointID)
		if err != nil {
			return nil, fmt.Errorf("get ML inference endpoint for DNS projection: %w", err)
		}
		if endpoint == nil {
			continue
		}
		environment, ok := envsByID[state.EnvironmentID]
		if !ok {
			continue
		}
		zone, ok := p.zoneForEnvironment(environment.Name)
		if !ok {
			p.logger.Warn("DNS ML projection skipped because environment has no zone mapping", zap.String("environment", environment.Name), zap.String("endpoint", endpoint.Name))
			continue
		}
		target := strings.TrimSpace(state.GatewayTarget)
		if target == "" {
			target = strings.TrimSpace(state.BackendEndpoint)
		}
		protocol, address, port, ok := parseEndpointTarget(target)
		if !ok {
			p.logger.Warn("DNS ML projection skipped because target URL is invalid", zap.String("endpoint", endpoint.Name), zap.String("target", target))
			continue
		}
		name := dnsLabel(endpoint.Name)
		endpoints = append(endpoints, domain.DNSEndpoint{
			MLEndpointID:   uuidPtr(endpoint.ID),
			Family:         domain.DNSEndpointFamilyML,
			Name:           name,
			Environment:    strings.TrimSpace(environment.Name),
			Zone:           zone,
			FQDN:           fqdn(name, zone),
			Protocol:       firstNonEmpty(protocol, endpoint.Protocol),
			Address:        address,
			Port:           port,
			Runtime:        string(state.RuntimeKind),
			Capabilities:   mlTaskCapabilities(endpoint.TaskKinds),
			Health:         state.BackendHealth,
			DriftStatus:    state.DriftStatus,
			Source:         "ml_inference_state",
			MaterializedAt: projectedAt,
		})
	}
	return endpoints, nil
}

func (p *DNSProjector) projectWorkerEndpoints(ctx context.Context, projectedAt time.Time) ([]domain.DNSEndpoint, error) {
	workers, err := p.workers.List(ctx, "", 0)
	if err != nil {
		return nil, fmt.Errorf("list workers for DNS projection: %w", err)
	}
	zone := strings.TrimSpace(p.cfg.Projection.WorkerZone)
	if zone == "" {
		return nil, nil
	}
	endpoints := make([]domain.DNSEndpoint, 0, len(workers))
	for _, worker := range workers {
		if worker.Status != domain.WorkerStatusOnline || worker.RuntimeTarget == nil || strings.TrimSpace(worker.RuntimeTarget.PublicBaseURL) == "" {
			continue
		}
		protocol, address, port, ok := parseEndpointTarget(worker.RuntimeTarget.PublicBaseURL)
		if !ok {
			p.logger.Warn("DNS worker projection skipped because public base URL is invalid", zap.String("worker_pubkey", worker.PubKey), zap.String("public_base_url", worker.RuntimeTarget.PublicBaseURL))
			continue
		}
		name := dnsLabel(worker.Name)
		if name == "" {
			name = dnsLabel(pubkeyPrefix(worker.PubKey))
		}
		endpoints = append(endpoints, domain.DNSEndpoint{
			WorkerPubkey:   strings.TrimSpace(worker.PubKey),
			Family:         domain.DNSEndpointFamilyWorker,
			Name:           name,
			Zone:           zone,
			FQDN:           fqdn(name, zone),
			Protocol:       protocol,
			Address:        address,
			Port:           port,
			Runtime:        string(worker.RuntimeTarget.Type),
			Hardware:       workerHardware(worker),
			Capabilities:   workerCapabilities(worker),
			Health:         domain.HealthStatusHealthy,
			DriftStatus:    domain.DriftStatusInSync,
			Source:         "worker_state",
			MaterializedAt: projectedAt,
		})
	}
	return endpoints, nil
}

func (p *DNSProjector) zoneForEnvironment(environment string) (string, bool) {
	zone := strings.TrimSpace(p.cfg.Projection.EnvironmentZones[strings.TrimSpace(environment)])
	return zone, zone != ""
}

func (p *DNSProjector) zoneTTLs() map[string]int {
	out := make(map[string]int, len(p.cfg.Zones))
	for _, zone := range p.cfg.Zones {
		out[strings.TrimSpace(zone.Name)] = zone.TTL
	}
	return out
}

func finalizeDNSEndpoints(endpoints []domain.DNSEndpoint) ([]domain.DNSEndpoint, error) {
	seen := make(map[string]struct{}, len(endpoints))
	for i := range endpoints {
		if err := domain.ValidateDNSEndpoint(&endpoints[i]); err != nil {
			return nil, err
		}
		if _, ok := seen[endpoints[i].Coordinate]; ok {
			return nil, fmt.Errorf("duplicate DNS endpoint coordinate %q", endpoints[i].Coordinate)
		}
		seen[endpoints[i].Coordinate] = struct{}{}
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Coordinate < endpoints[j].Coordinate })
	return endpoints, nil
}

func parseEndpointTarget(raw string) (string, string, *int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil, false
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
			return "", "", nil, false
		}
		port := parsePort(parsed.Port())
		return parsed.Scheme, parsed.Hostname(), port, true
	}
	if host, portString, err := net.SplitHostPort(raw); err == nil {
		return "", strings.Trim(host, "[]"), parsePort(portString), true
	}
	return "", strings.Trim(raw, "[]"), nil, true
}

func parsePort(raw string) *int {
	if raw == "" {
		return nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return nil
	}
	return &port
}

func dnsRecordType(address string) domain.DNSRecordType {
	ip := net.ParseIP(strings.TrimSpace(address))
	if ip == nil {
		return domain.DNSRecordTypeCNAME
	}
	if ip.To4() != nil {
		return domain.DNSRecordTypeA
	}
	return domain.DNSRecordTypeAAAA
}

func sortDNSRecords(records []domain.DNSRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].FQDN != records[j].FQDN {
			return records[i].FQDN < records[j].FQDN
		}
		if records[i].Type != records[j].Type {
			return records[i].Type < records[j].Type
		}
		return records[i].Value < records[j].Value
	})
}

func dnsLabel(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	lastHyphen := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func fqdn(name, zone string) string {
	return strings.TrimSuffix(strings.TrimSpace(name)+"."+strings.TrimSpace(zone), ".")
}

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mlTaskCapabilities(tasks []domain.MLTaskKind) []string {
	capabilities := make([]string, 0, len(tasks))
	for _, task := range tasks {
		capabilities = append(capabilities, string(task))
	}
	sort.Strings(capabilities)
	return capabilities
}

func workerHardware(worker domain.Worker) string {
	parts := make([]string, 0, len(worker.Accelerators))
	seen := map[string]struct{}{}
	for _, accelerator := range worker.Accelerators {
		model := strings.ToLower(strings.TrimSpace(accelerator.Model))
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		parts = append(parts, model)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func workerCapabilities(worker domain.Worker) []string {
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, task := range worker.MLCapabilities.Tasks {
		add(string(task))
	}
	for _, runtime := range worker.MLCapabilities.Runtimes {
		add(string(runtime))
	}
	for _, accelerator := range worker.MLCapabilities.Accelerators {
		add(accelerator)
	}
	for _, toolchain := range worker.MLCapabilities.Toolchains {
		add(toolchain)
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func pubkeyPrefix(pubkey string) string {
	pubkey = strings.TrimSpace(pubkey)
	if len(pubkey) <= 12 {
		return pubkey
	}
	return pubkey[:12]
}

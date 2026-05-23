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

// DNSPolicySource exposes enabled DNS policies to the DNS projector.
type DNSPolicySource interface {
	ListEnabledPolicies(ctx context.Context) ([]domain.DNSPolicy, error)
}

// ContinuityStatus describes the active continuity projection for a service.
type ContinuityStatus struct {
	ServiceKey         string
	ActiveProfile      domain.ContinuityMode
	OperationState     string
	ActiveWorkerPubKey string
}

// ContinuityStatusReader exposes service continuity status to the DNS projector.
type ContinuityStatusReader interface {
	GetServiceContinuityStatus(serviceKey string) (*ContinuityStatus, bool)
}

const (
	maxMeshHealthLoss = 0.5
	maxMeshHealthRTT  = 5 * time.Second
)

// DNSProjector derives DNS endpoints and records from authoritative infrastructure state.
type DNSProjector struct {
	services         repository.ServiceRepository
	environments     repository.EnvironmentRepository
	states           repository.EnvironmentServiceStateRepository
	observations     repository.RuntimeObservationRepository
	llmSource        LLMDNSProjectionSource
	mlSource         MLDNSProjectionSource
	workers          WorkerDNSProjectionSource
	policySource     DNSPolicySource
	continuityStatus ContinuityStatusReader
	cfg              config.DNSConfig
	logger           *zap.Logger
}

func NewDNSProjector(services repository.ServiceRepository, environments repository.EnvironmentRepository, states repository.EnvironmentServiceStateRepository, observations repository.RuntimeObservationRepository, llmSource LLMDNSProjectionSource, mlSource MLDNSProjectionSource, workers WorkerDNSProjectionSource, cfg config.DNSConfig, logger *zap.Logger, policySources ...DNSPolicySource) *DNSProjector {
	if logger == nil {
		logger = zap.NewNop()
	}
	var policySource DNSPolicySource
	if len(policySources) > 0 {
		policySource = policySources[0]
	}
	return &DNSProjector{services: services, environments: environments, states: states, observations: observations, llmSource: llmSource, mlSource: mlSource, workers: workers, policySource: policySource, cfg: cfg, logger: logger}
}

func (p *DNSProjector) SetContinuityStatusReader(reader ContinuityStatusReader) {
	p.continuityStatus = reader
}

func (p *DNSProjector) SetPolicySource(source DNSPolicySource) {
	p.policySource = source
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
	if p.cfg.Projection.MeshEndpoints && p.workers != nil {
		meshEndpoints, err := p.projectMeshEndpoints(ctx, projectedAt)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, meshEndpoints...)
	}
	return finalizeDNSEndpoints(endpoints)
}

func (p *DNSProjector) ProjectZoneRecords(ctx context.Context) (map[string][]domain.DNSRecord, error) {
	endpoints, err := p.ListDNSEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	endpoints, err = p.applyPolicies(ctx, endpoints)
	if err != nil {
		return nil, err
	}
	ttls := p.zoneTTLs()
	zoneVisibilities := p.zoneVisibilities()
	recordsByZone := make(map[string][]domain.DNSRecord)
	aliasCandidatesByZone := make(map[string]map[string]capabilityAliasCandidate)
	for _, endpoint := range endpoints {
		zoneVisibility, hasZoneVisibility := zoneVisibilities[endpoint.Zone]
		if hasZoneVisibility && !dnsEndpointVisibleInZone(endpoint, zoneVisibility, zoneVisibilities) {
			continue
		}
		ttl := dnsEndpointTTLOverride(endpoint)
		if ttl <= 0 {
			ttl = ttls[endpoint.Zone]
		}
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
		if endpoint.Family == domain.DNSEndpointFamilyML && endpoint.Port != nil {
			srvName := srvRecordName(endpoint.Protocol, endpoint.Name)
			priority := 10
			weight := 100 + dnsEndpointWeightBias(endpoint)
			if weight < 0 {
				weight = 0
			}
			recordsByZone[endpoint.Zone] = append(recordsByZone[endpoint.Zone], domain.DNSRecord{
				Zone:             endpoint.Zone,
				Name:             srvName,
				FQDN:             fqdn(srvName, endpoint.Zone),
				Type:             domain.DNSRecordTypeSRV,
				Value:            endpoint.Address,
				TTL:              ttl,
				Priority:         &priority,
				Weight:           &weight,
				Port:             endpoint.Port,
				SourceCoordinate: endpoint.Coordinate,
			})
		}
		if endpoint.Family == domain.DNSEndpointFamilyWorker {
			recordsByZone[endpoint.Zone] = append(recordsByZone[endpoint.Zone], hardwareAliasRecords(endpoint, ttl)...)
		}
		if p.cfg.Projection.CapabilityAliases {
			collectCapabilityAliasCandidates(aliasCandidatesByZone, endpoint, ttl)
		}
	}
	if p.cfg.Projection.CapabilityAliases {
		for zone, candidates := range aliasCandidatesByZone {
			for capability, candidate := range candidates {
				name := dnsLabel(capability)
				if name == "" || name == candidate.endpoint.Name {
					continue
				}
				recordsByZone[zone] = append(recordsByZone[zone], domain.DNSRecord{
					Zone:             zone,
					Name:             name,
					FQDN:             fqdn(name, zone),
					Type:             domain.DNSRecordTypeCNAME,
					Value:            candidate.endpoint.FQDN,
					TTL:              candidate.ttl,
					SourceCoordinate: candidate.endpoint.Coordinate,
				})
			}
		}
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
	var workersByPubKey map[string]domain.Worker
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
		status, hasContinuityStatus := p.serviceContinuityStatus(service)
		if hasContinuityStatus {
			switch status.ActiveProfile {
			case domain.ContinuityModeOffline:
				continue
			case domain.ContinuityModeDegraded, domain.ContinuityModeEmergency:
				if workersByPubKey == nil {
					workersByPubKey, err = p.workersByPubKey(ctx)
					if err != nil {
						return nil, err
					}
				}
				endpoint, ok := p.continuityServiceEndpoint(service, environment, state, zone, *status, workersByPubKey, projectedAt)
				if ok {
					endpoints = append(endpoints, endpoint)
				}
				continue
			}
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

func (p *DNSProjector) serviceContinuityStatus(service domain.Service) (*ContinuityStatus, bool) {
	if p.continuityStatus == nil {
		return nil, false
	}
	serviceKey := strings.TrimSpace(service.Name)
	if serviceKey == "" {
		return nil, false
	}
	status, ok := p.continuityStatus.GetServiceContinuityStatus(serviceKey)
	if !ok || status == nil {
		return nil, false
	}
	return status, true
}

func (p *DNSProjector) workersByPubKey(ctx context.Context) (map[string]domain.Worker, error) {
	out := map[string]domain.Worker{}
	if p.workers == nil {
		return out, nil
	}
	workers, err := p.workers.List(ctx, "", 0)
	if err != nil {
		return nil, fmt.Errorf("list workers for continuity DNS projection: %w", err)
	}
	for _, worker := range workers {
		pubkey := strings.TrimSpace(worker.PubKey)
		if pubkey == "" {
			continue
		}
		out[pubkey] = worker
	}
	return out, nil
}

func (p *DNSProjector) continuityServiceEndpoint(service domain.Service, environment domain.Environment, state domain.EnvironmentServiceState, zone string, status ContinuityStatus, workersByPubKey map[string]domain.Worker, projectedAt time.Time) (domain.DNSEndpoint, bool) {
	activeWorkerPubKey := strings.TrimSpace(status.ActiveWorkerPubKey)
	if activeWorkerPubKey == "" {
		p.logger.Warn("DNS continuity projection skipped because active worker pubkey is empty", zap.String("service", service.Name), zap.String("continuity_profile", string(status.ActiveProfile)))
		return domain.DNSEndpoint{}, false
	}
	worker, ok := workersByPubKey[activeWorkerPubKey]
	if !ok {
		p.logger.Warn("DNS continuity projection skipped because active worker was not found", zap.String("service", service.Name), zap.String("worker_pubkey", activeWorkerPubKey), zap.String("continuity_profile", string(status.ActiveProfile)))
		return domain.DNSEndpoint{}, false
	}
	if worker.RuntimeTarget == nil || strings.TrimSpace(worker.RuntimeTarget.PublicBaseURL) == "" {
		p.logger.Warn("DNS continuity projection skipped because active worker has no public base URL", zap.String("service", service.Name), zap.String("worker_pubkey", activeWorkerPubKey), zap.String("continuity_profile", string(status.ActiveProfile)))
		return domain.DNSEndpoint{}, false
	}
	protocol, address, port, ok := parseEndpointTarget(worker.RuntimeTarget.PublicBaseURL)
	if !ok {
		p.logger.Warn("DNS continuity projection skipped because active worker public base URL is invalid", zap.String("service", service.Name), zap.String("worker_pubkey", activeWorkerPubKey), zap.String("public_base_url", worker.RuntimeTarget.PublicBaseURL), zap.String("continuity_profile", string(status.ActiveProfile)))
		return domain.DNSEndpoint{}, false
	}
	name := dnsLabel(service.Name)
	return domain.DNSEndpoint{
		ServiceID:      uuidPtr(service.ID),
		WorkerPubkey:   activeWorkerPubKey,
		Family:         domain.DNSEndpointFamilyService,
		Name:           name,
		Environment:    strings.TrimSpace(environment.Name),
		Zone:           zone,
		FQDN:           fqdn(name, zone),
		Protocol:       protocol,
		Address:        address,
		Port:           port,
		Runtime:        string(worker.RuntimeTarget.Type),
		Health:         domain.HealthStatusHealthy,
		DriftStatus:    state.DriftStatus,
		Source:         "continuity_status",
		MaterializedAt: projectedAt,
	}, true
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
			Metadata:       workerDNSMetadata(worker),
			MaterializedAt: projectedAt,
		})
	}
	return endpoints, nil
}

func (p *DNSProjector) projectMeshEndpoints(ctx context.Context, projectedAt time.Time) ([]domain.DNSEndpoint, error) {
	workers, err := p.workers.List(ctx, "", 0)
	if err != nil {
		return nil, fmt.Errorf("list workers for mesh DNS projection: %w", err)
	}
	zone := strings.TrimSpace(p.cfg.Projection.MeshZone)
	if zone == "" {
		return nil, nil
	}
	endpoints := make([]domain.DNSEndpoint, 0, len(workers))
	for _, worker := range workers {
		address := strings.TrimSpace(worker.FIPSOverlayAddr)
		if worker.Status != domain.WorkerStatusOnline || address == "" {
			continue
		}
		if ip := net.ParseIP(address); ip == nil || ip.To4() != nil {
			p.logger.Warn("DNS mesh projection skipped because FIPS overlay address is invalid", zap.String("worker_pubkey", worker.PubKey), zap.String("fips_overlay_addr", worker.FIPSOverlayAddr))
			continue
		}
		if !meshHealthAllowsProjection(worker.MeshHealth) {
			continue
		}
		name := dnsLabel(worker.Name)
		if name == "" {
			name = dnsLabel(pubkeyPrefix(worker.PubKey))
		}
		endpoints = append(endpoints, domain.DNSEndpoint{
			WorkerPubkey:   strings.TrimSpace(worker.PubKey),
			Family:         domain.DNSEndpointFamilyMesh,
			Name:           name,
			Environment:    "mesh",
			Zone:           zone,
			FQDN:           fqdn(name, zone),
			Address:        address,
			Hardware:       workerHardware(worker),
			Capabilities:   workerCapabilities(worker),
			Health:         domain.HealthStatusHealthy,
			DriftStatus:    domain.DriftStatusInSync,
			Source:         "worker_fips_overlay",
			Metadata:       workerDNSMetadata(worker),
			MaterializedAt: projectedAt,
		})
	}
	return endpoints, nil
}

func meshHealthAllowsProjection(health *domain.MeshHealth) bool {
	if health == nil {
		return true
	}
	if health.Loss > maxMeshHealthLoss {
		return false
	}
	return health.RTT <= maxMeshHealthRTT
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

func (p *DNSProjector) zoneVisibilities() map[string]domain.ZoneVisibility {
	out := make(map[string]domain.ZoneVisibility, len(p.cfg.Zones))
	for _, zone := range p.cfg.Zones {
		name := strings.TrimSpace(zone.Name)
		if name == "" {
			continue
		}
		out[name] = domain.ZoneVisibility(strings.TrimSpace(zone.Visibility))
	}
	return out
}

func (p *DNSProjector) applyPolicies(ctx context.Context, endpoints []domain.DNSEndpoint) ([]domain.DNSEndpoint, error) {
	if p.policySource == nil || len(endpoints) == 0 {
		return endpoints, nil
	}
	policies, err := p.policySource.ListEnabledPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled DNS policies: %w", err)
	}
	if len(policies) == 0 {
		return endpoints, nil
	}
	zonePolicies := make([]domain.DNSPolicy, 0, len(policies))
	globalPolicies := make([]domain.DNSPolicy, 0, len(policies))
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		if policy.ZoneID != nil {
			zonePolicies = append(zonePolicies, policy)
			continue
		}
		globalPolicies = append(globalPolicies, policy)
	}

	out := make([]domain.DNSEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		current := endpoint
		excluded := false
		for _, policy := range zonePolicies {
			if !dnsPolicyAppliesToEndpointZone(policy, current) {
				continue
			}
			var ok bool
			current, ok = p.applyPolicy(current, policy)
			if !ok {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		for _, policy := range globalPolicies {
			var ok bool
			current, ok = p.applyPolicy(current, policy)
			if !ok {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, current)
		}
	}
	return out, nil
}

func (p *DNSProjector) applyPolicy(endpoint domain.DNSEndpoint, policy domain.DNSPolicy) (domain.DNSEndpoint, bool) {
	for _, rule := range policy.Rules {
		if !dnsPolicyRuleMatches(endpoint, rule.Match) {
			continue
		}
		if rule.Action.Exclude {
			return endpoint, false
		}
		return p.applyDNSPolicyAction(endpoint, rule.Action), true
	}
	return endpoint, true
}

func (p *DNSProjector) applyDNSPolicyAction(endpoint domain.DNSEndpoint, action domain.DNSPolicyAction) domain.DNSEndpoint {
	if action.Visibility != "" {
		endpoint.Metadata = dnsEndpointMetadataWithString(endpoint.Metadata, "policy_visibility", string(action.Visibility))
		if zone, ok := p.zoneForVisibility(action.Visibility); ok {
			endpoint.Zone = zone
			endpoint.FQDN = fqdn(endpoint.Name, zone)
		} else {
			p.logger.Warn("DNS policy visibility action skipped because no zone has matching visibility", zap.String("visibility", string(action.Visibility)), zap.String("endpoint", endpoint.Coordinate))
		}
	}
	if action.TTLOverride != nil {
		endpoint.Metadata = dnsEndpointMetadataWithInt(endpoint.Metadata, "policy_ttl_override", *action.TTLOverride)
	}
	if action.WeightBias != nil {
		endpoint.Metadata = dnsEndpointMetadataWithInt(endpoint.Metadata, "policy_weight_bias", *action.WeightBias)
	}
	return endpoint
}

func (p *DNSProjector) zoneForVisibility(visibility domain.ZoneVisibility) (string, bool) {
	for _, zone := range p.cfg.Zones {
		if domain.ZoneVisibility(strings.TrimSpace(zone.Visibility)) == visibility {
			name := strings.TrimSpace(zone.Name)
			return name, name != ""
		}
	}
	return "", false
}

func dnsPolicyRuleMatches(endpoint domain.DNSEndpoint, match domain.DNSPolicyMatch) bool {
	for _, capability := range match.Capabilities {
		if !dnsEndpointHasCapability(endpoint, capability) {
			return false
		}
	}
	if len(match.Hardware) > 0 && !dnsEndpointHasHardware(endpoint, match.Hardware) {
		return false
	}
	if match.Environment != "" && strings.TrimSpace(endpoint.Environment) != strings.TrimSpace(match.Environment) {
		return false
	}
	if match.Runtime != "" && strings.TrimSpace(endpoint.Runtime) != strings.TrimSpace(match.Runtime) {
		return false
	}
	return true
}

func dnsEndpointHasCapability(endpoint domain.DNSEndpoint, capability string) bool {
	capability = strings.ToLower(strings.TrimSpace(capability))
	if capability == "" {
		return true
	}
	for _, endpointCapability := range endpoint.Capabilities {
		if strings.ToLower(strings.TrimSpace(endpointCapability)) == capability {
			return true
		}
	}
	return false
}

func dnsEndpointHasHardware(endpoint domain.DNSEndpoint, hardware []string) bool {
	endpointHardware := strings.ToLower(strings.TrimSpace(endpoint.Hardware))
	if endpointHardware == "" {
		return false
	}
	for _, candidate := range hardware {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate != "" && strings.Contains(endpointHardware, candidate) {
			return true
		}
	}
	return false
}

func dnsPolicyAppliesToEndpointZone(policy domain.DNSPolicy, endpoint domain.DNSEndpoint) bool {
	if policy.ZoneID == nil {
		return true
	}
	if zoneID, ok := dnsPolicyMetadataString(policy.Metadata, "zone_id"); ok {
		return zoneID == policy.ZoneID.String() && zoneID == dnsPolicyZoneID(endpoint.Zone).String()
	}
	for _, key := range []string{"zone", "zone_name"} {
		if zone, ok := dnsPolicyMetadataString(policy.Metadata, key); ok && strings.TrimSpace(zone) == strings.TrimSpace(endpoint.Zone) {
			return true
		}
	}
	return *policy.ZoneID == dnsPolicyZoneID(endpoint.Zone)
}

func dnsPolicyMetadataString(metadata map[string]any, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	value, ok := metadata[key]
	if !ok {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), strings.TrimSpace(typed) != ""
	case fmt.Stringer:
		return strings.TrimSpace(typed.String()), strings.TrimSpace(typed.String()) != ""
	default:
		return "", false
	}
}

func dnsPolicyZoneID(zone string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("dns-zone:"+strings.TrimSpace(zone)))
}

func dnsEndpointMetadataWithInt(metadata map[string]any, key string, value int) map[string]any {
	out := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	out[key] = value
	return out
}

func dnsEndpointMetadataWithString(metadata map[string]any, key string, value string) map[string]any {
	out := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	out[key] = value
	return out
}

func dnsEndpointTTLOverride(endpoint domain.DNSEndpoint) int {
	return intMetadata(endpoint.Metadata, "policy_ttl_override")
}

func dnsEndpointWeightBias(endpoint domain.DNSEndpoint) int {
	return intMetadata(endpoint.Metadata, "policy_weight_bias")
}

func intMetadata(metadata map[string]any, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return 0
	}
}

func dnsEndpointVisibleInZone(endpoint domain.DNSEndpoint, zoneVisibility domain.ZoneVisibility, zoneVisibilities map[string]domain.ZoneVisibility) bool {
	endpointVisibility := dnsEndpointEffectiveVisibility(endpoint, zoneVisibilities)
	switch zoneVisibility {
	case domain.ZoneVisibilityExternal:
		return endpointVisibility == domain.ZoneVisibilityExternal
	case domain.ZoneVisibilityInternal:
		return endpointVisibility == domain.ZoneVisibilityInternal || endpointVisibility == domain.ZoneVisibilityExternal
	case domain.ZoneVisibilityEdge:
		return endpointVisibility == domain.ZoneVisibilityEdge
	default:
		return endpointVisibility == zoneVisibility
	}
}

func dnsEndpointEffectiveVisibility(endpoint domain.DNSEndpoint, zoneVisibilities map[string]domain.ZoneVisibility) domain.ZoneVisibility {
	if visibility, ok := stringMetadata(endpoint.Metadata, "policy_visibility"); ok {
		return domain.ZoneVisibility(visibility)
	}
	if visibility, ok := zoneVisibilities[endpoint.Zone]; ok {
		return visibility
	}
	return domain.ZoneVisibilityInternal
}

func stringMetadata(metadata map[string]any, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	value, ok := metadata[key]
	if !ok {
		return "", false
	}
	stringValue, ok := value.(string)
	if !ok {
		return "", false
	}
	stringValue = strings.TrimSpace(stringValue)
	return stringValue, stringValue != ""
}

type capabilityAliasCandidate struct {
	endpoint domain.DNSEndpoint
	ttl      int
}

func collectCapabilityAliasCandidates(candidatesByZone map[string]map[string]capabilityAliasCandidate, endpoint domain.DNSEndpoint, ttl int) {
	if endpoint.Health != domain.HealthStatusHealthy || len(endpoint.Capabilities) == 0 {
		return
	}
	zone := strings.TrimSpace(endpoint.Zone)
	if zone == "" {
		return
	}
	if candidatesByZone[zone] == nil {
		candidatesByZone[zone] = map[string]capabilityAliasCandidate{}
	}
	for _, rawCapability := range endpoint.Capabilities {
		capability := dnsLabel(rawCapability)
		if capability == "" {
			continue
		}
		candidate := capabilityAliasCandidate{endpoint: endpoint, ttl: ttl}
		current, exists := candidatesByZone[zone][capability]
		if !exists || betterCapabilityAliasCandidate(candidate, current) {
			candidatesByZone[zone][capability] = candidate
		}
	}
}

func betterCapabilityAliasCandidate(candidate, current capabilityAliasCandidate) bool {
	candidateScore := dnsHealthScore(candidate.endpoint.Health)
	currentScore := dnsHealthScore(current.endpoint.Health)
	if candidateScore != currentScore {
		return candidateScore > currentScore
	}
	if candidate.endpoint.FQDN != current.endpoint.FQDN {
		return candidate.endpoint.FQDN < current.endpoint.FQDN
	}
	return candidate.endpoint.Coordinate < current.endpoint.Coordinate
}

func dnsHealthScore(health domain.HealthStatus) int {
	switch health {
	case domain.HealthStatusHealthy:
		return 4
	case domain.HealthStatusStarting:
		return 3
	case domain.HealthStatusUnknown:
		return 2
	case domain.HealthStatusUnhealthy:
		return 1
	default:
		return 0
	}
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

func srvRecordName(protocol, name string) string {
	protocol = dnsLabel(protocol)
	if protocol == "" {
		protocol = "http"
	}
	return "_" + protocol + "._tcp." + dnsLabel(name)
}

func hardwareAliasRecords(endpoint domain.DNSEndpoint, ttl int) []domain.DNSRecord {
	models := stringSliceMetadata(endpoint.Metadata, "accelerator_models")
	gpuModels := map[string]struct{}{}
	for _, model := range stringSliceMetadata(endpoint.Metadata, "gpu_accelerator_models") {
		gpuModels[model] = struct{}{}
	}
	records := make([]domain.DNSRecord, 0, len(models)*2)
	for _, model := range models {
		name := dnsLabel(model)
		if name == "" {
			continue
		}
		records = append(records, domain.DNSRecord{
			Zone:             endpoint.Zone,
			Name:             name,
			FQDN:             fqdn(name, endpoint.Zone),
			Type:             dnsRecordType(endpoint.Address),
			Value:            endpoint.Address,
			TTL:              ttl,
			SourceCoordinate: endpoint.Coordinate,
		})
		if _, ok := gpuModels[model]; ok {
			gpuName := name + ".gpu"
			records = append(records, domain.DNSRecord{
				Zone:             endpoint.Zone,
				Name:             gpuName,
				FQDN:             fqdn(gpuName, endpoint.Zone),
				Type:             dnsRecordType(endpoint.Address),
				Value:            endpoint.Address,
				TTL:              ttl,
				SourceCoordinate: endpoint.Coordinate,
			})
		}
	}
	return records
}

func stringSliceMetadata(metadata map[string]any, key string) []string {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[key]
	if !ok {
		return nil
	}
	stringsValue, ok := value.([]string)
	if ok {
		return stringsValue
	}
	anyValues, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(anyValues))
	for _, item := range anyValues {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	return out
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
	parts := acceleratorModels(worker.Accelerators)
	return strings.Join(parts, ",")
}

func workerDNSMetadata(worker domain.Worker) map[string]any {
	models := acceleratorModels(worker.Accelerators)
	if len(models) == 0 {
		return nil
	}
	gpuModels := gpuAcceleratorModels(worker.Accelerators)
	metadata := map[string]any{"accelerator_models": models}
	if len(gpuModels) > 0 {
		metadata["gpu_accelerator_models"] = gpuModels
	}
	return metadata
}

func acceleratorModels(accelerators []domain.WorkerAccelerator) []string {
	parts := make([]string, 0, len(accelerators))
	seen := map[string]struct{}{}
	for _, accelerator := range accelerators {
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
	return parts
}

func gpuAcceleratorModels(accelerators []domain.WorkerAccelerator) []string {
	parts := make([]string, 0, len(accelerators))
	seen := map[string]struct{}{}
	for _, accelerator := range accelerators {
		model := strings.ToLower(strings.TrimSpace(accelerator.Model))
		if model == "" || !isGPUAccelerator(accelerator) {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		parts = append(parts, model)
	}
	sort.Strings(parts)
	return parts
}

func isGPUAccelerator(accelerator domain.WorkerAccelerator) bool {
	value := strings.ToLower(strings.Join([]string{accelerator.Vendor, accelerator.Model, accelerator.Driver}, " "))
	return strings.Contains(value, "gpu") || strings.Contains(value, "nvidia") || strings.Contains(value, "cuda") || strings.Contains(value, "amd") || strings.Contains(value, "rocm") || strings.Contains(value, "l40") || strings.Contains(value, "a100") || strings.Contains(value, "h100")
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

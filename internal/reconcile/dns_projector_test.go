package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestDNSProjectorProjectionRulesAndRecordTypes(t *testing.T) {
	ctx := context.Background()
	envID := uuid.New()
	serviceID := uuid.New()
	driftedServiceID := uuid.New()
	unhealthyServiceID := uuid.New()
	llmRouteID := uuid.New()
	unsyncedRouteID := uuid.New()
	mlEndpointID := uuid.New()
	unhealthyMLEndpointID := uuid.New()
	cfg := testDNSConfig()

	projector := NewDNSProjector(
		&fakeServiceRepo{services: []domain.Service{
			{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker},
			{ID: driftedServiceID, Name: "drifted", RuntimeType: domain.RuntimeTypeDocker},
			{ID: unhealthyServiceID, Name: "unhealthy", RuntimeType: domain.RuntimeTypeDocker},
		}},
		&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
		&fakeStateRepo{states: []domain.EnvironmentServiceState{
			{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync},
			{ServiceID: driftedServiceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusDrifted},
			{ServiceID: unhealthyServiceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync},
		}},
		&fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{
			dnsTestStateKey(serviceID, envID):          {ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "10.0.0.10", HealthStatus: domain.HealthStatusHealthy},
			dnsTestStateKey(unhealthyServiceID, envID): {ServiceID: unhealthyServiceID, EnvironmentID: envID, ObservedHost: "10.0.0.11", HealthStatus: domain.HealthStatusUnhealthy},
		}},
		&fakeLLMSource{
			routes: map[uuid.UUID]*domain.LLMRoute{llmRouteID: {ID: llmRouteID, Name: "review"}, unsyncedRouteID: {ID: unsyncedRouteID, Name: "unsynced"}},
			states: []domain.LLMRouteState{
				{RouteID: llmRouteID, EnvironmentID: envID, GatewayStatus: domain.GatewayRouteStatusSynced, BackendHealth: domain.HealthStatusHealthy, BackendEndpoint: "http://llm-backend.internal:8000", BackendKind: domain.LLMBackendKindVLLM, DriftStatus: domain.DriftStatusInSync},
				{RouteID: unsyncedRouteID, EnvironmentID: envID, GatewayStatus: domain.GatewayRouteStatusPending, BackendHealth: domain.HealthStatusHealthy, BackendEndpoint: "http://10.0.0.20:8000", BackendKind: domain.LLMBackendKindVLLM},
			},
		},
		&fakeMLSource{
			endpoints: map[uuid.UUID]*domain.MLInferenceEndpoint{mlEndpointID: {ID: mlEndpointID, Name: "embeddings", EnvironmentID: envID, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindEmbeddings}}, unhealthyMLEndpointID: {ID: unhealthyMLEndpointID, Name: "vision", EnvironmentID: envID}},
			states: []domain.MLInferenceState{
				{EndpointID: mlEndpointID, EnvironmentID: envID, BackendHealth: domain.HealthStatusHealthy, BackendEndpoint: "http://10.0.0.30:8080", RuntimeKind: domain.MLRuntimeKindONNXRuntime},
				{EndpointID: unhealthyMLEndpointID, EnvironmentID: envID, BackendHealth: domain.HealthStatusUnhealthy, BackendEndpoint: "http://10.0.0.31:8080"},
			},
		},
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "online-worker", Name: "gpu-node", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{Type: domain.RuntimeTypeCompose, PublicBaseURL: "http://[2001:db8::5]:9000"}, Accelerators: []domain.WorkerAccelerator{{Model: "L40S"}}, MLCapabilities: domain.WorkerMLCapabilities{Tasks: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}}},
			{PubKey: "offline-worker", Name: "offline-node", Status: domain.WorkerStatusOffline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.0.40"}},
		}},
		cfg,
		nil,
	)

	endpoints, err := projector.ListDNSEndpoints(ctx)
	if err != nil {
		t.Fatalf("ListDNSEndpoints returned error: %v", err)
	}
	if got, want := len(endpoints), 4; got != want {
		t.Fatalf("endpoint count = %d, want %d: %#v", got, want, endpoints)
	}
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyService, "api", "10.0.0.10")
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyLLM, "review", "llm-backend.internal")
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyML, "embeddings", "10.0.0.30")
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyWorker, "gpu-node", "2001:db8::5")
	for i := 1; i < len(endpoints); i++ {
		if endpoints[i-1].Coordinate > endpoints[i].Coordinate {
			t.Fatalf("endpoints not sorted by coordinate: %#v", endpoints)
		}
	}
	for _, endpoint := range endpoints {
		if endpoint.ID != domain.DeterministicDNSEndpointID(endpoint.Coordinate) {
			t.Fatalf("endpoint %s has non-deterministic ID %s", endpoint.Coordinate, endpoint.ID)
		}
	}

	recordsByZone, err := projector.ProjectZoneRecords(ctx)
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	records := recordsByZone["prod.example"]
	if got, want := len(records), 4; got != want {
		t.Fatalf("prod record count = %d, want %d: %#v", got, want, records)
	}
	assertRecord(t, records, "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
	assertRecord(t, records, "review.prod.example", domain.DNSRecordTypeCNAME, "llm-backend.internal")
	assertRecord(t, records, "embeddings.prod.example", domain.DNSRecordTypeA, "10.0.0.30")
	assertSRVRecord(t, records, "_http._tcp.embeddings.prod.example", "10.0.0.30", 8080, 10, 100)
	assertRecord(t, recordsByZone["edge.example"], "gpu-node.edge.example", domain.DNSRecordTypeAAAA, "2001:db8::5")
	assertRecord(t, recordsByZone["edge.example"], "l40s.edge.example", domain.DNSRecordTypeAAAA, "2001:db8::5")
	assertRecord(t, recordsByZone["edge.example"], "l40s.gpu.edge.example", domain.DNSRecordTypeAAAA, "2001:db8::5")
}

func TestDNSProjectorHardwareAliasesRoundRobinSharedAcceleratorModels(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "a", Name: "worker-a", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.44:9000"}, Accelerators: []domain.WorkerAccelerator{{Vendor: "NVIDIA", Model: "L40S", Driver: "cuda"}}},
			{PubKey: "b", Name: "worker-b", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.45:9000"}, Accelerators: []domain.WorkerAccelerator{{Vendor: "NVIDIA", Model: "L40S", Driver: "cuda"}}},
			{PubKey: "c", Name: "worker-c", Status: domain.WorkerStatusOffline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.46:9000"}, Accelerators: []domain.WorkerAccelerator{{Vendor: "NVIDIA", Model: "L40S", Driver: "cuda"}}},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Workers: true, WorkerZone: "edge.cascadia"}},
		nil,
	)
	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	records := recordsByZone["edge.cascadia"]
	assertRecord(t, records, "l40s.edge.cascadia", domain.DNSRecordTypeA, "10.0.1.44")
	assertRecord(t, records, "l40s.edge.cascadia", domain.DNSRecordTypeA, "10.0.1.45")
	assertRecord(t, records, "l40s.gpu.edge.cascadia", domain.DNSRecordTypeA, "10.0.1.44")
	assertRecord(t, records, "l40s.gpu.edge.cascadia", domain.DNSRecordTypeA, "10.0.1.45")
	assertNoRecord(t, records, "l40s.edge.cascadia", domain.DNSRecordTypeA, "10.0.1.46")
}

func TestDNSProjectorHardwareAliasesRequireWorkerProjection(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{{PubKey: "a", Name: "worker-a", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.44:9000"}, Accelerators: []domain.WorkerAccelerator{{Vendor: "NVIDIA", Model: "L40S", Driver: "cuda"}}}}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Workers: false, WorkerZone: "edge.cascadia"}},
		nil,
	)
	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	if len(recordsByZone["edge.cascadia"]) != 0 {
		t.Fatalf("hardware aliases should not be projected when worker projection is disabled: %#v", recordsByZone)
	}
}

func TestDNSProjectorDuplicateCoordinateDetection(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "a", Name: "same-worker", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.0.1"}},
			{PubKey: "b", Name: "same-worker", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.0.2"}},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Workers: true, WorkerZone: "edge.example"}},
		nil,
	)
	_, err := projector.ListDNSEndpoints(context.Background())
	if err == nil {
		t.Fatal("expected duplicate coordinate error")
	}
}

func TestDNSProjectorContinuityDegradedAndEmergencyUseActiveWorkerEndpoint(t *testing.T) {
	for _, mode := range []domain.ContinuityMode{domain.ContinuityModeDegraded, domain.ContinuityModeEmergency} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			envID := uuid.New()
			serviceID := uuid.New()
			projector := NewDNSProjector(
				&fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
				&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
				&fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}},
				&fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{}},
				nil,
				nil,
				&fakeWorkerSource{workers: []domain.Worker{
					{PubKey: "standby-worker", Name: "standby", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{Type: domain.RuntimeTypeCompose, PublicBaseURL: "https://standby.internal:9443"}},
				}},
				config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{"prod": "prod.example"}}},
				nil,
			)
			projector.SetContinuityStatusReader(fakeContinuityStatusReader{
				"api": {ServiceKey: "api", ActiveProfile: mode, OperationState: "failover_in_progress", ActiveWorkerPubKey: "standby-worker"},
			})

			endpoints, err := projector.ListDNSEndpoints(ctx)
			if err != nil {
				t.Fatalf("ListDNSEndpoints returned error: %v", err)
			}
			if got, want := len(endpoints), 1; got != want {
				t.Fatalf("endpoint count = %d, want %d: %#v", got, want, endpoints)
			}
			endpoint := endpoints[0]
			if endpoint.Name != "api" || endpoint.Address != "standby.internal" || endpoint.WorkerPubkey != "standby-worker" {
				t.Fatalf("continuity endpoint = %#v", endpoint)
			}
			if endpoint.Protocol != "https" {
				t.Fatalf("endpoint protocol = %q, want https", endpoint.Protocol)
			}
			if endpoint.Port == nil || *endpoint.Port != 9443 {
				t.Fatalf("endpoint port = %v, want 9443", endpoint.Port)
			}
			if endpoint.Source != "continuity_status" {
				t.Fatalf("endpoint source = %q, want continuity_status", endpoint.Source)
			}
		})
	}
}

func TestDNSProjectorContinuityFullPreservesPrimaryEndpoint(t *testing.T) {
	ctx := context.Background()
	envID := uuid.New()
	serviceID := uuid.New()
	projector := NewDNSProjector(
		&fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
		&fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}},
		&fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{
			dnsTestStateKey(serviceID, envID): {ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "10.0.0.10", HealthStatus: domain.HealthStatusHealthy},
		}},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "standby-worker", RuntimeTarget: &domain.WorkerRuntimeTarget{Type: domain.RuntimeTypeCompose, PublicBaseURL: "https://standby.internal:9443"}},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{"prod": "prod.example"}}},
		nil,
	)
	projector.SetContinuityStatusReader(fakeContinuityStatusReader{
		"api": {ServiceKey: "api", ActiveProfile: domain.ContinuityModeFull, OperationState: "steady", ActiveWorkerPubKey: "standby-worker"},
	})

	endpoints, err := projector.ListDNSEndpoints(ctx)
	if err != nil {
		t.Fatalf("ListDNSEndpoints returned error: %v", err)
	}
	if got, want := len(endpoints), 1; got != want {
		t.Fatalf("endpoint count = %d, want %d: %#v", got, want, endpoints)
	}
	if endpoints[0].Address != "10.0.0.10" || endpoints[0].Source != "service_state" || endpoints[0].WorkerPubkey != "" {
		t.Fatalf("full continuity endpoint should preserve primary projection: %#v", endpoints[0])
	}
}

func TestDNSProjectorContinuityOfflineOmitsServiceEndpoint(t *testing.T) {
	ctx := context.Background()
	envID := uuid.New()
	serviceID := uuid.New()
	projector := NewDNSProjector(
		&fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
		&fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}},
		&fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{
			dnsTestStateKey(serviceID, envID): {ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "10.0.0.10", HealthStatus: domain.HealthStatusHealthy},
		}},
		nil,
		nil,
		nil,
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{"prod": "prod.example"}}},
		nil,
	)
	projector.SetContinuityStatusReader(fakeContinuityStatusReader{
		"api": {ServiceKey: "api", ActiveProfile: domain.ContinuityModeOffline, OperationState: "steady"},
	})

	endpoints, err := projector.ListDNSEndpoints(ctx)
	if err != nil {
		t.Fatalf("ListDNSEndpoints returned error: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("offline continuity status should omit service endpoint: %#v", endpoints)
	}
}

func TestDNSProjectorPolicyExcludeRemovesMatchedEndpoint(t *testing.T) {
	projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
	projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{
		ID:      uuid.New(),
		Name:    "exclude-prod",
		Enabled: true,
		Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Exclude: true}}},
	}}})

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertNoRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
}

func TestDNSProjectorPolicyVisibilityOverrideReroutesEndpoint(t *testing.T) {
	projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
	projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{
		ID:      uuid.New(),
		Name:    "edge-api",
		Enabled: true,
		Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Visibility: domain.ZoneVisibilityEdge}}},
	}}})

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertNoRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
	assertRecord(t, recordsByZone["edge.example"], "api.edge.example", domain.DNSRecordTypeA, "10.0.0.10")
}

func TestDNSProjectorPolicyTTLOverrideChangesRecordTTL(t *testing.T) {
	projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
	ttl := 42
	projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{
		ID:      uuid.New(),
		Name:    "short-api-ttl",
		Enabled: true,
		Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{TTLOverride: &ttl}}},
	}}})

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	record := findRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
	if record.TTL != ttl {
		t.Fatalf("record TTL = %d, want %d", record.TTL, ttl)
	}
}

func TestDNSProjectorPolicyMatchingCriteria(t *testing.T) {
	t.Run("capability", func(t *testing.T) {
		projector := newDNSPolicyWorkerProjector(domain.Worker{
			PubKey: "worker-capability", Name: "gpu-node", Status: domain.WorkerStatusOnline,
			RuntimeTarget:  &domain.WorkerRuntimeTarget{Type: domain.RuntimeTypeCompose, PublicBaseURL: "http://10.0.1.10:9000"},
			MLCapabilities: domain.WorkerMLCapabilities{Tasks: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}},
		})
		projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{ID: uuid.New(), Name: "exclude-chat", Enabled: true, Rules: []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Capabilities: []string{string(domain.MLTaskKindChatCompletions)}}, Action: domain.DNSPolicyAction{Exclude: true}}}}}})
		recordsByZone, err := projector.ProjectZoneRecords(context.Background())
		if err != nil {
			t.Fatalf("ProjectZoneRecords returned error: %v", err)
		}
		assertNoRecord(t, recordsByZone["edge.example"], "gpu-node.edge.example", domain.DNSRecordTypeA, "10.0.1.10")
	})
	t.Run("hardware", func(t *testing.T) {
		projector := newDNSPolicyWorkerProjector(domain.Worker{
			PubKey: "worker-hardware", Name: "gpu-node", Status: domain.WorkerStatusOnline,
			RuntimeTarget: &domain.WorkerRuntimeTarget{Type: domain.RuntimeTypeCompose, PublicBaseURL: "http://10.0.1.11:9000"},
			Accelerators:  []domain.WorkerAccelerator{{Vendor: "NVIDIA", Model: "L40S", Driver: "cuda"}},
		})
		projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{ID: uuid.New(), Name: "exclude-l40s", Enabled: true, Rules: []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Hardware: []string{"l40s"}}, Action: domain.DNSPolicyAction{Exclude: true}}}}}})
		recordsByZone, err := projector.ProjectZoneRecords(context.Background())
		if err != nil {
			t.Fatalf("ProjectZoneRecords returned error: %v", err)
		}
		assertNoRecord(t, recordsByZone["edge.example"], "gpu-node.edge.example", domain.DNSRecordTypeA, "10.0.1.11")
	})
	t.Run("environment", func(t *testing.T) {
		projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
		projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{ID: uuid.New(), Name: "exclude-prod", Enabled: true, Rules: []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Exclude: true}}}}}})
		recordsByZone, err := projector.ProjectZoneRecords(context.Background())
		if err != nil {
			t.Fatalf("ProjectZoneRecords returned error: %v", err)
		}
		assertNoRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
	})
}

func TestDNSProjectorPolicyANDLogicRequiresAllCriteria(t *testing.T) {
	projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
	projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{
		ID:      uuid.New(),
		Name:    "prod-kubernetes-only",
		Enabled: true,
		Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod", Runtime: string(domain.RuntimeTypeK8s)}, Action: domain.DNSPolicyAction{Exclude: true}}},
	}}})

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
}

func TestDNSProjectorZoneScopedPolicyOnlyAppliesToEndpointInThatZone(t *testing.T) {
	projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
	stagingZoneID := dnsPolicyZoneID("staging.example")
	projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{
		ID:      uuid.New(),
		Name:    "exclude-staging",
		ZoneID:  &stagingZoneID,
		Enabled: true,
		Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Exclude: true}}},
	}}})

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
}

func TestDNSProjectorNoPoliciesDoesNotChangeEndpoints(t *testing.T) {
	projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
	projector.SetPolicySource(fakeDNSPolicySource{})

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	record := findRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
	if record.TTL != 120 {
		t.Fatalf("record TTL = %d, want zone TTL 120", record.TTL)
	}
}

func assertEndpoint(t *testing.T, endpoints []domain.DNSEndpoint, family domain.DNSEndpointFamily, name, address string) {
	t.Helper()
	for _, endpoint := range endpoints {
		if endpoint.Family == family && endpoint.Name == name && endpoint.Address == address {
			return
		}
	}
	t.Fatalf("missing endpoint family=%s name=%s address=%s in %#v", family, name, address, endpoints)
}

func assertRecord(t *testing.T, records []domain.DNSRecord, fqdn string, recordType domain.DNSRecordType, value string) {
	t.Helper()
	_ = findRecord(t, records, fqdn, recordType, value)
}

func findRecord(t *testing.T, records []domain.DNSRecord, fqdn string, recordType domain.DNSRecordType, value string) domain.DNSRecord {
	t.Helper()
	for _, record := range records {
		if record.FQDN == fqdn && record.Type == recordType && record.Value == value {
			return record
		}
	}
	t.Fatalf("missing record fqdn=%s type=%s value=%s in %#v", fqdn, recordType, value, records)
	return domain.DNSRecord{}
}

func assertNoRecord(t *testing.T, records []domain.DNSRecord, fqdn string, recordType domain.DNSRecordType, value string) {
	t.Helper()
	for _, record := range records {
		if record.FQDN == fqdn && record.Type == recordType && record.Value == value {
			t.Fatalf("unexpected record fqdn=%s type=%s value=%s in %#v", fqdn, recordType, value, records)
		}
	}
}

func assertSRVRecord(t *testing.T, records []domain.DNSRecord, fqdn string, value string, port, priority, weight int) {
	t.Helper()
	for _, record := range records {
		if record.FQDN != fqdn || record.Type != domain.DNSRecordTypeSRV || record.Value != value {
			continue
		}
		if record.Port == nil || *record.Port != port || record.Priority == nil || *record.Priority != priority || record.Weight == nil || *record.Weight != weight {
			t.Fatalf("SRV record fields mismatch for %s: %#v", fqdn, record)
		}
		return
	}
	t.Fatalf("missing SRV record fqdn=%s value=%s in %#v", fqdn, value, records)
}

func testDNSConfig() config.DNSConfig {
	return config.DNSConfig{
		DefaultTTL: 300,
		Zones: []config.DNSZoneConfig{
			{Name: "prod.example", Visibility: "internal", Backend: "test", TTL: 120},
			{Name: "edge.example", Visibility: "edge", Backend: "test", TTL: 60},
		},
		Projection: config.DNSProjectionConfig{Services: true, LLMRoutes: true, MLEndpoints: true, Workers: true, EnvironmentZones: map[string]string{"prod": "prod.example"}, WorkerZone: "edge.example"},
	}
}

func newDNSPolicyServiceProjector(t *testing.T, environmentName, serviceName, address string) *DNSProjector {
	t.Helper()
	envID := uuid.New()
	serviceID := uuid.New()
	cfg := testDNSConfig()
	cfg.Projection = config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{environmentName: "prod.example"}, WorkerZone: "edge.example"}
	return NewDNSProjector(
		&fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: serviceName, RuntimeType: domain.RuntimeTypeDocker}}},
		&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: environmentName}}},
		&fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}},
		&fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{dnsTestStateKey(serviceID, envID): {ServiceID: serviceID, EnvironmentID: envID, ObservedHost: address, HealthStatus: domain.HealthStatusHealthy}}},
		nil,
		nil,
		nil,
		cfg,
		nil,
	)
}

func newDNSPolicyWorkerProjector(worker domain.Worker) *DNSProjector {
	cfg := testDNSConfig()
	cfg.Projection = config.DNSProjectionConfig{Workers: true, WorkerZone: "edge.example"}
	return NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{worker}},
		cfg,
		nil,
	)
}

type fakeServiceRepo struct{ services []domain.Service }

func (r *fakeServiceRepo) Create(context.Context, *domain.Service) error { return nil }
func (r *fakeServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	for i := range r.services {
		if r.services[i].ID == id {
			return &r.services[i], nil
		}
	}
	return nil, nil
}
func (r *fakeServiceRepo) GetByName(_ context.Context, name string) (*domain.Service, error) {
	for i := range r.services {
		if r.services[i].Name == name {
			return &r.services[i], nil
		}
	}
	return nil, nil
}
func (r *fakeServiceRepo) List(context.Context) ([]domain.Service, error) { return r.services, nil }
func (r *fakeServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return r.services, nil
}
func (r *fakeServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (r *fakeServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type fakeEnvironmentRepo struct{ environments []domain.Environment }

func (r *fakeEnvironmentRepo) Create(context.Context, *domain.Environment) error { return nil }
func (r *fakeEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	for i := range r.environments {
		if r.environments[i].ID == id {
			return &r.environments[i], nil
		}
	}
	return nil, nil
}
func (r *fakeEnvironmentRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for i := range r.environments {
		if r.environments[i].Name == name {
			return &r.environments[i], nil
		}
	}
	return nil, nil
}
func (r *fakeEnvironmentRepo) List(context.Context) ([]domain.Environment, error) {
	return r.environments, nil
}
func (r *fakeEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return r.environments, nil
}
func (r *fakeEnvironmentRepo) Update(context.Context, *domain.Environment) error { return nil }
func (r *fakeEnvironmentRepo) Delete(context.Context, uuid.UUID) error           { return nil }

type fakeStateRepo struct {
	states []domain.EnvironmentServiceState
}

func (r *fakeStateRepo) Upsert(context.Context, *domain.EnvironmentServiceState) error { return nil }
func (r *fakeStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	for i := range r.states {
		if r.states[i].ServiceID == serviceID && r.states[i].EnvironmentID == envID {
			return &r.states[i], nil
		}
	}
	return nil, nil
}
func (r *fakeStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return filterStates(r.states, func(state domain.EnvironmentServiceState) bool { return state.EnvironmentID == envID }), nil
}
func (r *fakeStateRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return filterStates(r.states, func(state domain.EnvironmentServiceState) bool { return state.ServiceID == serviceID }), nil
}
func (r *fakeStateRepo) ListDrifted(context.Context) ([]domain.EnvironmentServiceState, error) {
	return filterStates(r.states, func(state domain.EnvironmentServiceState) bool { return state.DriftStatus == domain.DriftStatusDrifted }), nil
}
func (r *fakeStateRepo) ListAll(context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.states, nil
}

func filterStates(states []domain.EnvironmentServiceState, keep func(domain.EnvironmentServiceState) bool) []domain.EnvironmentServiceState {
	out := []domain.EnvironmentServiceState{}
	for _, state := range states {
		if keep(state) {
			out = append(out, state)
		}
	}
	return out
}

type fakeObservationRepo struct {
	latest map[string]*domain.RuntimeObservation
}

func (r *fakeObservationRepo) Create(context.Context, *domain.RuntimeObservation) error { return nil }
func (r *fakeObservationRepo) GetLatest(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return r.latest[dnsTestStateKey(serviceID, envID)], nil
}
func (r *fakeObservationRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.RuntimeObservation, error) {
	return nil, nil
}

func dnsTestStateKey(a, b uuid.UUID) string { return a.String() + ":" + b.String() }

type fakeLLMSource struct {
	states []domain.LLMRouteState
	routes map[uuid.UUID]*domain.LLMRoute
	err    error
}

func (s *fakeLLMSource) ListAllRouteStates(context.Context) ([]domain.LLMRouteState, error) {
	return s.states, s.err
}
func (s *fakeLLMSource) GetRoute(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.routes[id], nil
}

type fakeMLSource struct {
	states    []domain.MLInferenceState
	endpoints map[uuid.UUID]*domain.MLInferenceEndpoint
	err       error
}

func (s *fakeMLSource) ListInferenceStates(context.Context) ([]domain.MLInferenceState, error) {
	return s.states, s.err
}
func (s *fakeMLSource) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.endpoints[id], nil
}

type fakeWorkerSource struct {
	workers []domain.Worker
	err     error
}

func (s *fakeWorkerSource) List(context.Context, string, int) ([]domain.Worker, error) {
	return s.workers, s.err
}

type fakeDNSPolicySource struct {
	policies []domain.DNSPolicy
	err      error
}

func (s fakeDNSPolicySource) ListEnabledPolicies(context.Context) ([]domain.DNSPolicy, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]domain.DNSPolicy, 0, len(s.policies))
	for _, policy := range s.policies {
		if policy.Enabled {
			out = append(out, policy)
		}
	}
	return out, nil
}

type fakeContinuityStatusReader map[string]ContinuityStatus

func (r fakeContinuityStatusReader) GetServiceContinuityStatus(serviceKey string) (*ContinuityStatus, bool) {
	status, ok := r[serviceKey]
	if !ok {
		return nil, false
	}
	return &status, true
}

var errFake = errors.New("fake error")
var _ = errFake
var _ = time.Time{}

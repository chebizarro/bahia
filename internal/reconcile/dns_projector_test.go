package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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

func TestDNSProjectorServiceHostOverridesAndBareHostSafety(t *testing.T) {
	ctx := context.Background()
	envID := uuid.New()
	astilleroID := uuid.New()
	apiID := uuid.New()

	tests := []struct {
		name          string
		hostOverrides map[string]string
		wantType      domain.DNSRecordType
		wantValue     string
		wantAstillero bool
		wantWarning   bool
	}{
		{
			name:          "IP override produces A record",
			hostOverrides: map[string]string{"edge-01-docker": "192.168.40.104"},
			wantType:      domain.DNSRecordTypeA,
			wantValue:     "192.168.40.104",
			wantAstillero: true,
		},
		{
			name:        "no override skips bare alias",
			wantWarning: true,
		},
		{
			name:          "FQDN override produces CNAME",
			hostOverrides: map[string]string{"edge-01-docker": "edge-01.sharegap.net"},
			wantType:      domain.DNSRecordTypeCNAME,
			wantValue:     "edge-01.sharegap.net",
			wantAstillero: true,
		},
		{
			name:          "IPv6 override produces AAAA record",
			hostOverrides: map[string]string{"edge-01-docker": "2001:db8::104"},
			wantType:      domain.DNSRecordTypeAAAA,
			wantValue:     "2001:db8::104",
			wantAstillero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testDNSConfig()
			cfg.Projection = config.DNSProjectionConfig{
				Services:         true,
				EnvironmentZones: map[string]string{"prod": "prod.example"},
				HostOverrides:    tt.hostOverrides,
			}
			core, logs := observer.New(zap.WarnLevel)
			projector := NewDNSProjector(
				&fakeServiceRepo{services: []domain.Service{
					{ID: astilleroID, Name: "astillero", RuntimeType: domain.RuntimeTypeDocker},
					{ID: apiID, Name: "api", RuntimeType: domain.RuntimeTypeDocker},
				}},
				&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
				&fakeStateRepo{states: []domain.EnvironmentServiceState{
					{ServiceID: astilleroID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync},
					{ServiceID: apiID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync},
				}},
				&fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{
					dnsTestStateKey(astilleroID, envID): {ServiceID: astilleroID, EnvironmentID: envID, ObservedHost: "edge-01-docker", HealthStatus: domain.HealthStatusHealthy},
					dnsTestStateKey(apiID, envID):       {ServiceID: apiID, EnvironmentID: envID, ObservedHost: "192.168.40.105", HealthStatus: domain.HealthStatusHealthy},
				}},
				nil, nil, nil,
				cfg,
				zap.New(core),
			)

			recordsByZone, err := projector.ProjectZoneRecords(ctx)
			if err != nil {
				t.Fatalf("ProjectZoneRecords returned error: %v", err)
			}
			records := recordsByZone["prod.example"]
			assertRecord(t, records, "api.prod.example", domain.DNSRecordTypeA, "192.168.40.105")
			if tt.wantAstillero {
				assertRecord(t, records, "astillero.prod.example", tt.wantType, tt.wantValue)
			} else {
				for _, record := range records {
					if record.FQDN == "astillero.prod.example" {
						t.Fatalf("bare observed host produced record: %#v", record)
					}
				}
			}

			warnings := logs.FilterMessage("observed host is not a resolvable DNS target; configure dns.projection.host_overrides")
			if tt.wantWarning {
				if warnings.Len() != 1 {
					t.Fatalf("warning count = %d, want 1: %#v", warnings.Len(), logs.All())
				}
				fields := warnings.All()[0].ContextMap()
				if fields["service"] != "astillero" || fields["host"] != "edge-01-docker" {
					t.Fatalf("warning fields = %#v", fields)
				}
			} else if warnings.Len() != 0 {
				t.Fatalf("unexpected unresolvable-host warning: %#v", warnings.All())
			}
		})
	}
}

func TestDNSProjectorMeshEndpointsProjectFIPSOverlayAAAARecords(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "mesh-worker", Name: "fips-node", Status: domain.WorkerStatusOnline, FIPSOverlayAddr: "fd00:ab:cd::1"},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{MeshEndpoints: true, MeshZone: "mesh.cascadia"}},
		nil,
	)

	endpoints, err := projector.ListDNSEndpoints(context.Background())
	if err != nil {
		t.Fatalf("ListDNSEndpoints returned error: %v", err)
	}
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyMesh, "fips-node", "fd00:ab:cd::1")

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertRecord(t, recordsByZone["mesh.cascadia"], "fips-node.mesh.cascadia", domain.DNSRecordTypeAAAA, "fd00:ab:cd::1")
}

func TestDNSProjectorMeshEndpointsGateByMeshHealth(t *testing.T) {
	testCases := []struct {
		name        string
		meshHealth  *domain.MeshHealth
		wantProject bool
	}{
		{name: "good mesh health projects", meshHealth: &domain.MeshHealth{Loss: 0.1, RTT: 100 * time.Millisecond}, wantProject: true},
		{name: "high loss excludes", meshHealth: &domain.MeshHealth{Loss: 0.51, RTT: 100 * time.Millisecond}, wantProject: false},
		{name: "high RTT excludes", meshHealth: &domain.MeshHealth{Loss: 0.1, RTT: 6 * time.Second}, wantProject: false},
		{name: "nil mesh health projects", meshHealth: nil, wantProject: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			projector := NewDNSProjector(
				&fakeServiceRepo{},
				&fakeEnvironmentRepo{},
				&fakeStateRepo{},
				&fakeObservationRepo{},
				nil,
				nil,
				&fakeWorkerSource{workers: []domain.Worker{
					{PubKey: "mesh-worker", Name: "fips-node", Status: domain.WorkerStatusOnline, FIPSOverlayAddr: "fd00:ab:cd::1", MeshHealth: tc.meshHealth},
				}},
				config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{MeshEndpoints: true, MeshZone: "mesh.cascadia"}},
				nil,
			)

			recordsByZone, err := projector.ProjectZoneRecords(context.Background())
			if err != nil {
				t.Fatalf("ProjectZoneRecords returned error: %v", err)
			}
			if tc.wantProject {
				assertRecord(t, recordsByZone["mesh.cascadia"], "fips-node.mesh.cascadia", domain.DNSRecordTypeAAAA, "fd00:ab:cd::1")
				return
			}
			assertNoRecord(t, recordsByZone["mesh.cascadia"], "fips-node.mesh.cascadia", domain.DNSRecordTypeAAAA, "fd00:ab:cd::1")
		})
	}
}

func TestDNSProjectorMeshEndpointsSkipWorkersWithoutFIPSOverlayAddr(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "no-mesh-worker", Name: "plain-node", Status: domain.WorkerStatusOnline},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{MeshEndpoints: true, MeshZone: "mesh.cascadia"}},
		nil,
	)

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertNoRecord(t, recordsByZone["mesh.cascadia"], "plain-node.mesh.cascadia", domain.DNSRecordTypeAAAA, "fd00:ab:cd::1")
}

func TestDNSProjectorMeshEndpointsRequireConfigFlag(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "disabled-mesh-worker", Name: "disabled-node", Status: domain.WorkerStatusOnline, FIPSOverlayAddr: "fd00:ab:cd::2"},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{MeshEndpoints: false, MeshZone: "mesh.cascadia"}},
		nil,
	)

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertNoRecord(t, recordsByZone["mesh.cascadia"], "disabled-node.mesh.cascadia", domain.DNSRecordTypeAAAA, "fd00:ab:cd::2")
}

func TestDNSProjectorMeshEndpointsSkipUnhealthyWorkers(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "offline-mesh-worker", Name: "offline-mesh", Status: domain.WorkerStatusOffline, FIPSOverlayAddr: "fd00:ab:cd::3"},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{MeshEndpoints: true, MeshZone: "mesh.cascadia"}},
		nil,
	)

	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertNoRecord(t, recordsByZone["mesh.cascadia"], "offline-mesh.mesh.cascadia", domain.DNSRecordTypeAAAA, "fd00:ab:cd::3")
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

func TestDNSProjectorCapabilityAliasesCreateDeterministicCNAMEs(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{
			{PubKey: "b", Name: "worker-b", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.45:9000"}, MLCapabilities: domain.WorkerMLCapabilities{Tasks: []domain.MLTaskKind{domain.MLTaskKindSpeechToText}, Accelerators: []string{"gpu"}}},
			{PubKey: "a", Name: "worker-a", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.44:9000"}, MLCapabilities: domain.WorkerMLCapabilities{Tasks: []domain.MLTaskKind{domain.MLTaskKindSpeechToText}, Accelerators: []string{"gpu"}}},
			{PubKey: "c", Name: "worker-c", Status: domain.WorkerStatusOffline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.46:9000"}, MLCapabilities: domain.WorkerMLCapabilities{Tasks: []domain.MLTaskKind{domain.MLTaskKindSpeechToText}}},
		}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Workers: true, CapabilityAliases: true, WorkerZone: "edge.cascadia"}},
		nil,
	)
	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	records := recordsByZone["edge.cascadia"]
	assertRecord(t, records, "speech-to-text.edge.cascadia", domain.DNSRecordTypeCNAME, "worker-a.edge.cascadia")
	assertRecord(t, records, "gpu.edge.cascadia", domain.DNSRecordTypeCNAME, "worker-a.edge.cascadia")
	assertNoRecord(t, records, "speech-to-text.edge.cascadia", domain.DNSRecordTypeCNAME, "worker-c.edge.cascadia")
}

func TestDNSProjectorCapabilityAliasesRequireConfigFlag(t *testing.T) {
	projector := NewDNSProjector(
		&fakeServiceRepo{},
		&fakeEnvironmentRepo{},
		&fakeStateRepo{},
		&fakeObservationRepo{},
		nil,
		nil,
		&fakeWorkerSource{workers: []domain.Worker{{PubKey: "a", Name: "worker-a", Status: domain.WorkerStatusOnline, RuntimeTarget: &domain.WorkerRuntimeTarget{PublicBaseURL: "http://10.0.1.44:9000"}, MLCapabilities: domain.WorkerMLCapabilities{Accelerators: []string{"gpu"}}}}},
		config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Workers: true, WorkerZone: "edge.cascadia"}},
		nil,
	)
	recordsByZone, err := projector.ProjectZoneRecords(context.Background())
	if err != nil {
		t.Fatalf("ProjectZoneRecords returned error: %v", err)
	}
	assertNoRecord(t, recordsByZone["edge.cascadia"], "gpu.edge.cascadia", domain.DNSRecordTypeCNAME, "worker-a.edge.cascadia")
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

func TestDNSProjectorWarnsOnceWhenProjectedServiceBecomesIneligibleAndClearsOnRecovery(t *testing.T) {
	tests := []struct {
		name        string
		reason      string
		makeLost    func(*fakeStateRepo, *fakeObservationRepo, string)
		makeHealthy func(*fakeStateRepo, *fakeObservationRepo, string)
	}{
		{
			name: "drift status", reason: "drift_status",
			makeLost: func(states *fakeStateRepo, _ *fakeObservationRepo, _ string) {
				states.states[0].DriftStatus = domain.DriftStatusDrifted
			},
			makeHealthy: func(states *fakeStateRepo, _ *fakeObservationRepo, _ string) {
				states.states[0].DriftStatus = domain.DriftStatusInSync
			},
		},
		{
			name: "observation missing", reason: "observation_missing",
			makeLost: func(_ *fakeStateRepo, observations *fakeObservationRepo, key string) {
				delete(observations.latest, key)
			},
			makeHealthy: func(_ *fakeStateRepo, observations *fakeObservationRepo, key string) {
				observations.latest[key] = &domain.RuntimeObservation{ObservedHost: "10.0.0.10", HealthStatus: domain.HealthStatusHealthy}
			},
		},
		{
			name: "observation health", reason: "observation_health",
			makeLost: func(_ *fakeStateRepo, observations *fakeObservationRepo, key string) {
				observations.latest[key].HealthStatus = domain.HealthStatusUnhealthy
			},
			makeHealthy: func(_ *fakeStateRepo, observations *fakeObservationRepo, key string) {
				observations.latest[key].HealthStatus = domain.HealthStatusHealthy
			},
		},
		{
			name: "empty observed host", reason: "observed_host_empty",
			makeLost: func(_ *fakeStateRepo, observations *fakeObservationRepo, key string) {
				observations.latest[key].ObservedHost = " "
			},
			makeHealthy: func(_ *fakeStateRepo, observations *fakeObservationRepo, key string) {
				observations.latest[key].ObservedHost = "10.0.0.10"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			envID := uuid.New()
			serviceID := uuid.New()
			stateRepo := &fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}}
			observationKey := dnsTestStateKey(serviceID, envID)
			observationRepo := &fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{
				observationKey: {ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "10.0.0.10", HealthStatus: domain.HealthStatusHealthy},
			}}
			core, logs := observer.New(zap.WarnLevel)
			projector := NewDNSProjector(
				&fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
				&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
				stateRepo,
				observationRepo,
				nil, nil, nil,
				config.DNSConfig{DefaultTTL: 300, Projection: config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{"prod": "prod.example"}}},
				zap.New(core),
			)

			if _, err := projector.ListDNSEndpoints(ctx); err != nil {
				t.Fatalf("initial projection returned error: %v", err)
			}
			tt.makeLost(stateRepo, observationRepo, observationKey)
			for pass := 1; pass <= 2; pass++ {
				if endpoints, err := projector.ListDNSEndpoints(ctx); err != nil || len(endpoints) != 0 {
					t.Fatalf("loss projection pass %d = %#v, err %v; want no endpoints", pass, endpoints, err)
				}
			}
			entries := logs.FilterMessage("DNS service projection became ineligible").All()
			if len(entries) != 1 {
				t.Fatalf("projection-loss warnings = %#v, want exactly one", logs.All())
			}
			fields := entries[0].ContextMap()
			if fields["service"] != "api" || fields["zone"] != "prod.example" || fields["reason"] != tt.reason {
				t.Fatalf("projection-loss warning fields = %#v", fields)
			}

			tt.makeHealthy(stateRepo, observationRepo, observationKey)
			if endpoints, err := projector.ListDNSEndpoints(ctx); err != nil || len(endpoints) != 1 {
				t.Fatalf("recovery projection = %#v, err %v; want one endpoint", endpoints, err)
			}
			tt.makeLost(stateRepo, observationRepo, observationKey)
			if _, err := projector.ListDNSEndpoints(ctx); err != nil {
				t.Fatalf("second loss transition returned error: %v", err)
			}
			if entries := logs.FilterMessage("DNS service projection became ineligible").All(); len(entries) != 2 {
				t.Fatalf("warnings after recovery and second loss = %#v, want 2", logs.All())
			}
		})
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

func TestDNSProjectorContinuityLossWarnsOnceAndRecoveryAllowsLaterHealthWarning(t *testing.T) {
	ctx := context.Background()
	envID := uuid.New()
	serviceID := uuid.New()
	stateRepo := &fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}}
	observationKey := dnsTestStateKey(serviceID, envID)
	observationRepo := &fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{
		observationKey: {ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "10.0.0.10", HealthStatus: domain.HealthStatusHealthy},
	}}
	continuity := fakeContinuityStatusReader{}
	core, logs := observer.New(zap.WarnLevel)
	projector := NewDNSProjector(
		&fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
		stateRepo,
		observationRepo,
		nil, nil, nil,
		config.DNSConfig{DefaultTTL: 300, Zones: []config.DNSZoneConfig{{Name: "prod.example", TTL: 120}}, Projection: config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{"prod": "prod.example"}}},
		zap.New(core),
	)
	projector.SetContinuityStatusReader(continuity)

	assertZoneRecordCount := func(want int) {
		t.Helper()
		records, err := projector.ProjectZoneRecords(ctx)
		if err != nil {
			t.Fatalf("ProjectZoneRecords returned error: %v", err)
		}
		if got := len(records["prod.example"]); got != want {
			t.Fatalf("projected record count = %d, want %d: %#v", got, want, records)
		}
	}

	assertZoneRecordCount(1)
	continuity["api"] = ContinuityStatus{ServiceKey: "api", ActiveProfile: domain.ContinuityModeOffline, OperationState: "steady"}
	assertZoneRecordCount(0)
	assertZoneRecordCount(0)
	entries := logs.FilterMessage("DNS service projection became ineligible").All()
	if len(entries) != 1 {
		t.Fatalf("continuity-loss warnings = %#v, want exactly one", logs.All())
	}
	fields := entries[0].ContextMap()
	if fields["service"] != "api" || fields["zone"] != "prod.example" || fields["reason"] != "continuity mode offline" || fields["continuity_mode"] != "offline" {
		t.Fatalf("continuity-loss warning fields = %#v", fields)
	}

	delete(continuity, "api")
	assertZoneRecordCount(1)
	observationRepo.latest[observationKey].HealthStatus = domain.HealthStatusUnhealthy
	assertZoneRecordCount(0)
	entries = logs.FilterMessage("DNS service projection became ineligible").All()
	if len(entries) != 2 {
		t.Fatalf("warnings after recovery and health loss = %#v, want 2", logs.All())
	}
	fields = entries[1].ContextMap()
	if fields["reason"] != "observation_health" || fields["observation_health"] != string(domain.HealthStatusUnhealthy) {
		t.Fatalf("health-loss warning fields = %#v", fields)
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

func TestDNSProjectorSplitHorizonFiltersEndpointVisibilityAfterPolicy(t *testing.T) {
	t.Run("internal visibility does not leak into external zone", func(t *testing.T) {
		projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
		projector.cfg.Zones = []config.DNSZoneConfig{{Name: "prod.example", Visibility: string(domain.ZoneVisibilityExternal), Backend: "test", TTL: 120}}
		projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{
			ID:      uuid.New(),
			Name:    "internal-api",
			Enabled: true,
			Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Visibility: domain.ZoneVisibilityInternal}}},
		}}})

		recordsByZone, err := projector.ProjectZoneRecords(context.Background())
		if err != nil {
			t.Fatalf("ProjectZoneRecords returned error: %v", err)
		}
		assertNoRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
	})

	t.Run("edge visibility stays out of internal zone", func(t *testing.T) {
		projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
		projector.cfg.Zones = []config.DNSZoneConfig{{Name: "prod.example", Visibility: string(domain.ZoneVisibilityInternal), Backend: "test", TTL: 120}}
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
	})

	t.Run("external visibility remains visible in internal zone", func(t *testing.T) {
		projector := newDNSPolicyServiceProjector(t, "prod", "api", "10.0.0.10")
		projector.cfg.Zones = []config.DNSZoneConfig{{Name: "prod.example", Visibility: string(domain.ZoneVisibilityInternal), Backend: "test", TTL: 120}}
		projector.SetPolicySource(fakeDNSPolicySource{policies: []domain.DNSPolicy{{
			ID:      uuid.New(),
			Name:    "external-api",
			Enabled: true,
			Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Visibility: domain.ZoneVisibilityExternal}}},
		}}})

		recordsByZone, err := projector.ProjectZoneRecords(context.Background())
		if err != nil {
			t.Fatalf("ProjectZoneRecords returned error: %v", err)
		}
		assertRecord(t, recordsByZone["prod.example"], "api.prod.example", domain.DNSRecordTypeA, "10.0.0.10")
	})
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
func (r *fakeStateRepo) ListDueForObservation(_ context.Context, dueBefore time.Time) ([]domain.EnvironmentServiceState, error) {
	return filterStates(r.states, func(state domain.EnvironmentServiceState) bool {
		return state.LastReconciledAt == nil || !state.LastReconciledAt.After(dueBefore)
	}), nil
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

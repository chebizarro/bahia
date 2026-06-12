package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestFIPSToolsAreRegistered(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	required := map[string]bool{
		"bahia_fips_list_mesh_nodes": false,
		"bahia_fips_mesh_status":     false,
	}
	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing FIPS tool %s", name)
		}
	}
}

func TestFIPSListMeshNodesFiltersMeshRecordsAndEnrichesWorkers(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{
		DNSEndpoints: dnsEndpointListerFunc(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
			return []domain.DNSEndpoint{
				{Family: domain.DNSEndpointFamilyService, Name: "api", FQDN: "api.prod.example", Address: "10.0.0.10", Health: domain.HealthStatusHealthy, DriftStatus: domain.DriftStatusInSync},
				{Family: domain.DNSEndpointFamilyMesh, Name: "node-a", Zone: "mesh.example", FQDN: "node-a.mesh.example", WorkerPubkey: testHexPubkey, Address: "fd00::1", Health: domain.HealthStatusHealthy, DriftStatus: domain.DriftStatusInSync, Source: "worker_fips_overlay", MaterializedAt: now},
			}, nil
		}),
		Workers: &fakeWorkerRepository{workers: []domain.Worker{{
			PubKey:          testHexPubkey,
			Name:            "operator-node-a",
			Status:          domain.WorkerStatusOnline,
			FIPSOverlayAddr: "fd00::1",
			FIPSEndpoints:   []domain.FIPSTransportEndpoint{{Transport: "quic", Address: "node-a.fips:4433"}},
			MeshHealth:      &domain.MeshHealth{RTT: 20 * time.Millisecond, Loss: 0.01, Jitter: 3 * time.Millisecond, Goodput: 42, LastReport: now},
		}},
		},
	})

	res, err := server.CallTool(context.Background(), "bahia_fips_list_mesh_nodes", map[string]interface{}{})
	if err != nil || res.IsError {
		t.Fatalf("list mesh nodes result=%#v err=%v", res, err)
	}
	var got struct {
		Nodes []fipsMeshNode `json:"nodes"`
		Total int            `json:"total"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	if got.Total != 1 || len(got.Nodes) != 1 {
		t.Fatalf("expected one mesh node, got %#v", got)
	}
	node := got.Nodes[0]
	if node.WorkerPubkey != testHexPubkey || node.WorkerNpub == "" {
		t.Fatalf("expected worker pubkey and npub, got %#v", node)
	}
	if node.WorkerName != "operator-node-a" || node.OverlayAddress != "fd00::1" || node.DNSHostname != "node-a.mesh.example" {
		t.Fatalf("unexpected node identity: %#v", node)
	}
	if len(node.TransportEndpoints) != 1 || node.TransportEndpoints[0].Transport != "quic" {
		t.Fatalf("expected transport endpoints from worker state, got %#v", node.TransportEndpoints)
	}
	if node.MeshHealth == nil || node.MeshHealth.RTTMillis != 20 || !node.MeshHealth.ProjectionHealthy {
		t.Fatalf("expected mesh health metadata, got %#v", node.MeshHealth)
	}
}

func TestFIPSMeshStatusEmptyStateReturnsEmptyStatus(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{
		DNSEndpoints: dnsEndpointListerFunc(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
			return []domain.DNSEndpoint{}, nil
		}),
	})

	res, err := server.CallTool(context.Background(), "bahia_fips_mesh_status", map[string]interface{}{})
	if err != nil || res.IsError {
		t.Fatalf("mesh status result=%#v err=%v", res, err)
	}
	var got struct {
		TotalNodes            int            `json:"total_nodes"`
		HealthyProjectedNodes int            `json:"healthy_projected_nodes"`
		HealthCounts          map[string]int `json:"health_counts"`
		ProjectionCounts      map[string]int `json:"projection_counts"`
		Nodes                 []fipsMeshNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.TotalNodes != 0 || got.HealthyProjectedNodes != 0 || len(got.Nodes) != 0 {
		t.Fatalf("expected empty status, got %#v", got)
	}
	if len(got.HealthCounts) != 0 || len(got.ProjectionCounts) != 0 {
		t.Fatalf("expected empty count maps, got %#v", got)
	}
}

func TestFIPSResourcesListIncludesOnlyMeshResources(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{
		DNSEndpoints: dnsEndpointListerFunc(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
			return []domain.DNSEndpoint{
				{Family: domain.DNSEndpointFamilyService, Name: "api", Environment: "prod", Zone: "prod.example", FQDN: "api.prod.example", Address: "10.0.0.10", Health: domain.HealthStatusHealthy, DriftStatus: domain.DriftStatusInSync},
				{Family: domain.DNSEndpointFamilyMesh, Name: "node-a", Environment: "mesh", Zone: "mesh.example", FQDN: "node-a.mesh.example", WorkerPubkey: testHexPubkey, Address: "fd00::1", Health: domain.HealthStatusHealthy, DriftStatus: domain.DriftStatusInSync},
			}, nil
		}),
	})

	resources, err := server.GetResources(context.Background())
	if err != nil {
		t.Fatalf("GetResources returned error: %v", err)
	}
	var fipsResources []Resource
	for _, resource := range resources {
		if resource.URI == "bahia://fips/mesh/node/node-a.mesh.example" {
			fipsResources = append(fipsResources, resource)
		}
		if resource.URI == "bahia://fips/mesh/node/api.prod.example" {
			t.Fatalf("non-mesh endpoint was exposed as FIPS resource: %#v", resource)
		}
	}
	if len(fipsResources) != 1 {
		t.Fatalf("expected one FIPS mesh resource, got resources %#v", resources)
	}
	if fipsResources[0].Metadata["overlay_address"] != "fd00::1" || fipsResources[0].Metadata["dns_hostname"] != "node-a.mesh.example" {
		t.Fatalf("unexpected FIPS metadata: %#v", fipsResources[0].Metadata)
	}
}

var testHexPubkey = func() string {
	secret, err := nostr.SecretKeyFromHex("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		panic(err)
	}
	return secret.Public().Hex()
}()

type fakeWorkerRepository struct {
	workers []domain.Worker
}

func (r *fakeWorkerRepository) Upsert(ctx context.Context, w *domain.Worker) error { return nil }

func (r *fakeWorkerRepository) GetByPubKey(ctx context.Context, pubkey string) (*domain.Worker, error) {
	for i := range r.workers {
		if r.workers[i].PubKey == pubkey {
			return &r.workers[i], nil
		}
	}
	return nil, nil
}

func (r *fakeWorkerRepository) List(ctx context.Context, status string, limit int) ([]domain.Worker, error) {
	return append([]domain.Worker(nil), r.workers...), nil
}

func (r *fakeWorkerRepository) UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error {
	return nil
}

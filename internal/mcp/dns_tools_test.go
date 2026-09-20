package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestDNSReadOnlyToolsListEndpointsAndDrift(t *testing.T) {
	now := time.Now().UTC()
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{DNSEndpoints: dnsEndpointListerFunc(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
		return []domain.DNSEndpoint{
			{Name: "sync", FQDN: "sync.prod.example", DriftStatus: domain.DriftStatusInSync, MaterializedAt: now.Add(-2 * time.Minute)},
			{Name: "old", FQDN: "old.prod.example", DriftStatus: domain.DriftStatusDrifted, MaterializedAt: now.Add(-1 * time.Hour)},
			{Name: "new", FQDN: "new.prod.example", DriftStatus: domain.DriftStatusDeploying, MaterializedAt: now},
		}, nil
	})})

	endpointsRes, err := server.CallTool(authorizedMCPContext(), "bahia_assistant_dns_list_endpoints", map[string]interface{}{"limit": float64(2)})
	if err != nil || endpointsRes.IsError {
		t.Fatalf("list endpoints result=%#v err=%v", endpointsRes, err)
	}
	var endpoints struct {
		Endpoints []domain.DNSEndpoint `json:"endpoints"`
		Total     int                  `json:"total"`
	}
	if err := json.Unmarshal([]byte(endpointsRes.Content[0].Text), &endpoints); err != nil {
		t.Fatalf("decode endpoints: %v", err)
	}
	if endpoints.Total != 3 || len(endpoints.Endpoints) != 2 {
		t.Fatalf("unexpected endpoints response: %#v", endpoints)
	}

	driftRes, err := server.CallTool(authorizedMCPContext(), "bahia_dns_list_drift", map[string]interface{}{"limit": float64(1)})
	if err != nil || driftRes.IsError {
		t.Fatalf("list drift result=%#v err=%v", driftRes, err)
	}
	var drift struct {
		Drift []domain.DNSEndpoint `json:"drift"`
		Total int                  `json:"total"`
	}
	if err := json.Unmarshal([]byte(driftRes.Content[0].Text), &drift); err != nil {
		t.Fatalf("decode drift: %v", err)
	}
	if drift.Total != 2 || len(drift.Drift) != 1 || drift.Drift[0].Name != "new" {
		t.Fatalf("unexpected drift response: %#v", drift)
	}
}
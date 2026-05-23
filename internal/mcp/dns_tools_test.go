package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestDNSAssistantToolsAreRegistered(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	required := map[string]bool{
		"bahia_dns_list_endpoints":            false,
		"bahia_dns_list_drift":                false,
		"bahia_assistant_dns_zone_create":     false,
		"bahia_assistant_dns_policy_apply":    false,
		"bahia_assistant_dns_record_override": false,
		"bahia_assistant_dns_drift_remediate": false,
		"bahia_assistant_dns_list_endpoints":  false,
		"bahia_assistant_dns_list_drift":      false,
	}
	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing DNS tool %s", name)
		}
	}
}

func TestDNSAssistantAsyncToolsPublishRequestsAndReturnCorrelation(t *testing.T) {
	publisher := &captureDNSCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{DNSCommandPublisher: publisher})

	calls := []struct {
		tool     string
		args     map[string]interface{}
		wantKind int
	}{
		{tool: "bahia_assistant_dns_zone_create", wantKind: controlplane.KindDNSZoneCreateRequest, args: map[string]interface{}{"name": "prod.example", "visibility": "internal", "backend_ref": "fs-prod", "ttl": float64(60), "idempotency_key": "dns-zone:1"}},
		{tool: "bahia_assistant_dns_policy_apply", wantKind: controlplane.KindDNSPolicyApplyRequest, args: map[string]interface{}{"name": "internal-only", "rules": []interface{}{map[string]interface{}{"action": map[string]interface{}{"visibility": "internal"}}}, "idempotency_key": "dns-policy:1"}},
		{tool: "bahia_assistant_dns_record_override", wantKind: controlplane.KindDNSRecordOverrideRequest, args: map[string]interface{}{"zone_name": "prod.example", "record_name": "api", "record_type": "A", "value": "10.0.1.99", "ttl": float64(30), "reason": "operator requested", "idempotency_key": "dns-override:1"}},
		{tool: "bahia_assistant_dns_drift_remediate", wantKind: controlplane.KindDNSDriftRemediateRequest, args: map[string]interface{}{"zone": "prod.example", "idempotency_key": "dns-remediate:1"}},
	}

	for _, tc := range calls {
		res, err := server.CallTool(context.Background(), tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s returned error: %v", tc.tool, err)
		}
		if res.IsError {
			t.Fatalf("%s returned tool error: %s", tc.tool, res.Content[0].Text)
		}
		var receipt domain.AsyncToolReceipt
		if err := json.Unmarshal([]byte(res.Content[0].Text), &receipt); err != nil {
			t.Fatalf("%s invalid JSON receipt: %v", tc.tool, err)
		}
		if receipt.ToolName != tc.tool || receipt.RequestKind != tc.wantKind {
			t.Fatalf("%s receipt mismatch: %#v", tc.tool, receipt)
		}
		if len(receipt.StatusKinds) != 1 || receipt.StatusKinds[0] != controlplane.KindDNSOperationStatus {
			t.Fatalf("%s status kinds mismatch: %#v", tc.tool, receipt.StatusKinds)
		}
	}

	if len(publisher.calls) != len(calls) {
		t.Fatalf("expected %d publish calls, got %d", len(calls), len(publisher.calls))
	}
	if publisher.calls[0].payload.AgentID != assistantAgentID || publisher.calls[0].payload.IdempotencyKey != "dns-zone:1" {
		t.Fatalf("unexpected first payload: %#v", publisher.calls[0].payload)
	}
}

func TestDNSAssistantAsyncToolsRequirePublisherAndIdempotencyKey(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(context.Background(), "bahia_assistant_dns_drift_remediate", map[string]interface{}{"zone": "prod.example", "idempotency_key": "dns:1"})
	if err != nil {
		t.Fatalf("CallTool returned error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "DNS command publisher is not configured") {
		t.Fatalf("expected missing publisher error, got %#v", res)
	}

	configured := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{DNSCommandPublisher: &captureDNSCommandPublisher{}})
	res, err = configured.CallTool(context.Background(), "bahia_assistant_dns_drift_remediate", map[string]interface{}{"zone": "prod.example"})
	if err != nil {
		t.Fatalf("CallTool returned error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "idempotency_key is required") {
		t.Fatalf("expected idempotency key error, got %#v", res)
	}
}

func TestDNSReadOnlyToolsListEndpointsAndDrift(t *testing.T) {
	now := time.Now().UTC()
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{DNSEndpoints: dnsEndpointListerFunc(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
		return []domain.DNSEndpoint{
			{Name: "sync", FQDN: "sync.prod.example", DriftStatus: domain.DriftStatusInSync, MaterializedAt: now.Add(-2 * time.Minute)},
			{Name: "old", FQDN: "old.prod.example", DriftStatus: domain.DriftStatusDrifted, MaterializedAt: now.Add(-1 * time.Hour)},
			{Name: "new", FQDN: "new.prod.example", DriftStatus: domain.DriftStatusDeploying, MaterializedAt: now},
		}, nil
	})})

	endpointsRes, err := server.CallTool(context.Background(), "bahia_assistant_dns_list_endpoints", map[string]interface{}{"limit": float64(2)})
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

	driftRes, err := server.CallTool(context.Background(), "bahia_dns_list_drift", map[string]interface{}{"limit": float64(1)})
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

type captureDNSCommandPublisher struct {
	calls []captureDNSCommandCall
}

type captureDNSCommandCall struct {
	kind    int
	payload DNSCommandPayload
}

func (p *captureDNSCommandPublisher) PublishDNSZoneCreateRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error) {
	return p.capture(controlplane.KindDNSZoneCreateRequest, controlplane.KindDNSZoneCreateResult, cmd), nil
}

func (p *captureDNSCommandPublisher) PublishDNSPolicyApplyRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error) {
	return p.capture(controlplane.KindDNSPolicyApplyRequest, controlplane.KindDNSPolicyApplyResult, cmd), nil
}

func (p *captureDNSCommandPublisher) PublishDNSRecordOverrideRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error) {
	return p.capture(controlplane.KindDNSRecordOverrideRequest, controlplane.KindDNSRecordOverrideResult, cmd), nil
}

func (p *captureDNSCommandPublisher) PublishDNSDriftRemediateRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error) {
	return p.capture(controlplane.KindDNSDriftRemediateRequest, controlplane.KindDNSDriftRemediateResult, cmd), nil
}

func (p *captureDNSCommandPublisher) capture(kind, resultKind int, cmd DNSCommandPayload) *DNSCommandReceipt {
	p.calls = append(p.calls, captureDNSCommandCall{kind: kind, payload: cmd})
	return &DNSCommandReceipt{
		RequestEventID:  "dns-event-id",
		RequestPubkey:   "dns-pubkey",
		RequestKind:     kind,
		StatusKind:      controlplane.KindDNSOperationStatus,
		ResultKind:      resultKind,
		DTag:            cmd.IdempotencyKey,
		PublishedRelays: 1,
		ResourceTags:    map[string]string{"zone": cmd.Zone},
	}
}

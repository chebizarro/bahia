package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

// DNSCommandPublisher emits canonical DNS control-plane request events.
type DNSCommandPublisher interface {
	PublishDNSZoneCreateRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error)
	PublishDNSPolicyApplyRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error)
	PublishDNSRecordOverrideRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error)
	PublishDNSDriftRemediateRequest(ctx context.Context, cmd DNSCommandPayload) (*DNSCommandReceipt, error)
}

type DNSCommandPayload struct {
	Content        map[string]any    `json:"content,omitempty"`
	Zone           string            `json:"zone,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	AgentID        string            `json:"agent_id,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

type DNSCommandReceipt struct {
	RequestEventID  string            `json:"request_event_id"`
	RequestPubkey   string            `json:"request_pubkey"`
	RequestKind     int               `json:"request_kind"`
	StatusKind      int               `json:"status_kind,omitempty"`
	ResultKind      int               `json:"result_kind"`
	DTag            string            `json:"d_tag,omitempty"`
	PublishedRelays int               `json:"published_relays"`
	ResourceTags    map[string]string `json:"resource_tags,omitempty"`
}

func dnsToolDefinitions() []Tool {
	return []Tool{
		{Name: "bahia_dns_list_endpoints", Description: "List current DNS endpoints from the DNS read model", InputSchema: dnsObjectSchema(map[string]interface{}{
			"limit":  map[string]interface{}{"type": "integer"},
			"offset": map[string]interface{}{"type": "integer"},
		})},
		{Name: "bahia_dns_list_drift", Description: "List recent DNS endpoint drift from the DNS read model", InputSchema: dnsObjectSchema(map[string]interface{}{
			"limit":  map[string]interface{}{"type": "integer"},
			"offset": map[string]interface{}{"type": "integer"},
		})},
	}
}

func dnsAssistantToolDefinitions() []Tool {
	stringProp := map[string]interface{}{"type": "string"}
	objectProp := map[string]interface{}{"type": "object"}
	integerProp := map[string]interface{}{"type": "integer"}
	return []Tool{
		{Name: "bahia_assistant_dns_zone_create", Description: "Assistant-safe async DNS zone create command via ContextVM", InputSchema: dnsObjectSchema(map[string]interface{}{
			"name": stringProp, "zone": stringProp, "visibility": stringProp, "backend_ref": stringProp, "ttl": integerProp, "idempotency_key": stringProp, "tags": objectProp,
		}, "idempotency_key")},
		{Name: "bahia_assistant_dns_policy_apply", Description: "Assistant-safe async DNS policy apply command via ContextVM", InputSchema: dnsObjectSchema(map[string]interface{}{
			"policy_id": stringProp, "name": stringProp, "zone_id": stringProp, "environment_id": stringProp, "rules": map[string]interface{}{"type": "array", "items": objectProp}, "enabled": map[string]interface{}{"type": "boolean"}, "metadata": objectProp, "idempotency_key": stringProp, "tags": objectProp,
		}, "idempotency_key")},
		{Name: "bahia_assistant_dns_record_override", Description: "Assistant-safe async DNS record override command via ContextVM", InputSchema: dnsObjectSchema(map[string]interface{}{
			"override_id": stringProp, "zone_name": stringProp, "record_name": stringProp, "record_type": stringProp, "value": stringProp, "ttl": integerProp, "reason": stringProp, "expires_at": stringProp, "idempotency_key": stringProp, "tags": objectProp,
		}, "zone_name", "record_name", "record_type", "value", "ttl", "reason", "idempotency_key")},
		{Name: "bahia_assistant_dns_drift_remediate", Description: "Assistant-safe async DNS drift remediation command via ContextVM", InputSchema: dnsObjectSchema(map[string]interface{}{
			"zone": stringProp, "zone_name": stringProp, "idempotency_key": stringProp, "tags": objectProp,
		}, "idempotency_key")},
		{Name: "bahia_assistant_dns_list_endpoints", Description: "Assistant-safe DNS endpoint read model query", InputSchema: dnsObjectSchema(map[string]interface{}{
			"limit": integerProp, "offset": integerProp,
		})},
		{Name: "bahia_assistant_dns_list_drift", Description: "Assistant-safe DNS drift read model query", InputSchema: dnsObjectSchema(map[string]interface{}{
			"limit": integerProp, "offset": integerProp,
		})},
	}
}

func dnsObjectSchema(props map[string]interface{}, required ...string) map[string]interface{} {
	schema := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (s *Server) handleDNSAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	receipt, err := s.invokeDNSAssistantAsyncTool(ctx, name, args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(receipt)
}

func (s *Server) invokeDNSAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	if s.dnsCommands == nil {
		return nil, fmt.Errorf("DNS command publisher is not configured")
	}
	key := strings.TrimSpace(stringArg(args, "idempotency_key"))
	if key == "" {
		return nil, fmt.Errorf("idempotency_key is required")
	}
	cmd := dnsCommandPayloadFromArgs(args, key)
	var receipt *DNSCommandReceipt
	var err error
	switch name {
	case "bahia_assistant_dns_zone_create":
		receipt, err = s.dnsCommands.PublishDNSZoneCreateRequest(ctx, cmd)
	case "bahia_assistant_dns_policy_apply":
		receipt, err = s.dnsCommands.PublishDNSPolicyApplyRequest(ctx, cmd)
	case "bahia_assistant_dns_record_override":
		receipt, err = s.dnsCommands.PublishDNSRecordOverrideRequest(ctx, cmd)
	case "bahia_assistant_dns_drift_remediate":
		receipt, err = s.dnsCommands.PublishDNSDriftRemediateRequest(ctx, cmd)
	default:
		return nil, fmt.Errorf("DNS assistant tool %q is not allowlisted", name)
	}
	if err != nil {
		return nil, err
	}
	return dnsAsyncReceipt(name, key, receipt), nil
}

func dnsCommandPayloadFromArgs(args map[string]interface{}, key string) DNSCommandPayload {
	content := make(map[string]any, len(args))
	for k, v := range args {
		if k == "idempotency_key" || k == "tags" {
			continue
		}
		content[k] = v
	}
	zone := strings.TrimSpace(stringArg(args, "zone"))
	if zone == "" {
		zone = strings.TrimSpace(stringArg(args, "zone_name"))
	}
	return DNSCommandPayload{Content: content, Zone: zone, IdempotencyKey: key, AgentID: assistantAgentID, Tags: stringMapFromArg(args["tags"])}
}

func dnsAsyncReceipt(tool, key string, receipt *DNSCommandReceipt) *domain.AsyncToolReceipt {
	out := &domain.AsyncToolReceipt{ToolName: tool, IdempotencyKey: key, StatusKinds: []int{controlplane.KindDNSOperationStatus}}
	if receipt == nil {
		return out
	}
	out.RequestEventID = receipt.RequestEventID
	out.RequestKind = receipt.RequestKind
	out.ResultKinds = []int{receipt.ResultKind}
	out.DTag = receipt.DTag
	out.PublishedRelays = []string{fmt.Sprint(receipt.PublishedRelays)}
	out.ResourceTags = receipt.ResourceTags
	return out
}

func (s *Server) handleDNSListEndpoints(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.dnsEndpoints == nil {
		return errorResult("DNS endpoint lister is not configured"), nil
	}
	endpoints, err := s.dnsEndpoints.ListDNSEndpoints(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list DNS endpoints: %v", err)), nil
	}
	page := pageDNSEndpointsForMCP(endpoints, args)
	return jsonResult(map[string]any{"endpoints": page, "total": len(endpoints)})
}

func (s *Server) handleDNSListDrift(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.dnsEndpoints == nil {
		return errorResult("DNS endpoint lister is not configured"), nil
	}
	endpoints, err := s.dnsEndpoints.ListDNSEndpoints(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list DNS drift: %v", err)), nil
	}
	drifted := make([]domain.DNSEndpoint, 0)
	for _, endpoint := range endpoints {
		if endpoint.DriftStatus != "" && endpoint.DriftStatus != domain.DriftStatusInSync {
			drifted = append(drifted, endpoint)
		}
	}
	sort.SliceStable(drifted, func(i, j int) bool {
		return drifted[i].MaterializedAt.After(drifted[j].MaterializedAt)
	})
	page := pageDNSEndpointsForMCP(drifted, args)
	return jsonResult(map[string]any{"drift": page, "total": len(drifted)})
}

func pageDNSEndpointsForMCP(endpoints []domain.DNSEndpoint, args map[string]interface{}) []domain.DNSEndpoint {
	limit := optionalIntArg(args, "limit", len(endpoints))
	offset := optionalIntArg(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	if offset >= len(endpoints) {
		return []domain.DNSEndpoint{}
	}
	if limit <= 0 || offset+limit > len(endpoints) {
		limit = len(endpoints) - offset
	}
	return endpoints[offset : offset+limit]
}

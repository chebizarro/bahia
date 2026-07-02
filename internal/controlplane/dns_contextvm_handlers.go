package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// RegisterDNSContextVMHandlers bridges encrypted ContextVM DNS methods from the
// browser to the app-owned DNS reconciliation and persistence boundary.
func RegisterDNSContextVMHandlers(transport *EncryptedRequestTransport, operator DNSControlPlaneOperator) {
	if transport == nil || operator == nil {
		return
	}
	h := dnsContextVMHandlers{operator: operator}
	transport.RegisterContextVMHandler(ContextVMMethodDNSZoneCreate, h.zoneCreate)
	transport.RegisterContextVMHandler(ContextVMMethodDNSPolicyApply, h.policyApply)
	transport.RegisterContextVMHandler(ContextVMMethodDNSRecordSet, h.recordSet)
	transport.RegisterContextVMHandler(ContextVMMethodDNSDriftRemediate, h.driftRemediate)
}

type dnsContextVMHandlers struct {
	operator DNSControlPlaneOperator
}

func (h dnsContextVMHandlers) zoneCreate(ctx context.Context, request ContextVMRequest) (any, error) {
	persistence, _ := h.operator.(DNSPersistenceOperator)
	if persistence != nil {
		var zone domain.DNSZone
		if err := decodeContextVMParams(request.RPC.Params, &zone); err != nil {
			return nil, fmt.Errorf("invalid DNS zone JSON content: %w", err)
		}
		if err := domain.ValidateDNSZone(&zone); err != nil {
			return dnsResult(dnsActionZoneCreate, "error", "validation_error", err.Error(), map[string]any{"zone": zone.Name}), nil
		}
		if err := persistence.CreateZone(ctx, zone); err != nil {
			return dnsResult(dnsActionZoneCreate, "error", "persist_failed", err.Error(), map[string]any{"zone": zone.Name}), nil
		}
		if err := h.operator.ReconcileZone(ctx, zone.Name); err != nil {
			return dnsResult(dnsActionZoneCreate, "error", "reconcile_failed", err.Error(), map[string]any{"zone": zone.Name}), nil
		}
		return dnsResult(dnsActionZoneCreate, "success", "completed", "DNS zone persisted; reconcile completed", map[string]any{"zone": zone.Name}), nil
	}
	zoneName, err := dnsZoneFromParams(request.RPC.Params)
	if err != nil {
		return dnsResult(dnsActionZoneCreate, "error", "parse_error", err.Error(), nil), nil
	}
	if zoneName == "" {
		return dnsResult(dnsActionZoneCreate, "error", "validation_error", "zone selector is required", nil), nil
	}
	if !h.operator.HasZone(zoneName) {
		return dnsResult(dnsActionZoneCreate, "failed", "unsupported", dnsUnsupportedDynamicZoneCreation, map[string]any{"zone": zoneName}), nil
	}
	if err := h.operator.ReconcileZone(ctx, zoneName); err != nil {
		return dnsResult(dnsActionZoneCreate, "error", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName}), nil
	}
	return dnsResult(dnsActionZoneCreate, "success", "completed", "Configured DNS zone exists; reconcile completed", map[string]any{"zone": zoneName}), nil
}

func (h dnsContextVMHandlers) policyApply(ctx context.Context, request ContextVMRequest) (any, error) {
	provider, _ := h.operator.(DNSPolicyRepositoryProvider)
	if provider == nil || provider.DNSPolicyRepository() == nil {
		zoneName, _ := dnsZoneFromParams(request.RPC.Params)
		return dnsResult(dnsActionPolicyApply, "failed", "unsupported", dnsUnsupportedPolicyApply, map[string]any{"zone": zoneName}), nil
	}
	var policy domain.DNSPolicy
	if err := decodeContextVMParams(request.RPC.Params, &policy); err != nil {
		return dnsResult(dnsActionPolicyApply, "error", "parse_error", fmt.Sprintf("invalid DNS policy JSON content: %v", err), nil), nil
	}
	if policy.ID == uuid.Nil {
		policy.ID = uuid.New()
	}
	now := time.Now().UTC()
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = now
	}
	if policy.UpdatedAt.IsZero() {
		policy.UpdatedAt = now
	}
	if err := domain.ValidateDNSPolicy(&policy); err != nil {
		return dnsResult(dnsActionPolicyApply, "error", "validation_error", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String()}), nil
	}
	if err := provider.DNSPolicyRepository().Create(ctx, &policy); err != nil {
		return dnsResult(dnsActionPolicyApply, "error", "persist_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)}), nil
	}
	if err := h.operator.ReconcileAll(ctx); err != nil {
		return dnsResult(dnsActionPolicyApply, "error", "reconcile_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)}), nil
	}
	return dnsResult(dnsActionPolicyApply, "success", "completed", fmt.Sprintf("DNS policy %s accepted with %d rule(s); reconcile completed", policy.Name, len(policy.Rules)), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)}), nil
}

func (h dnsContextVMHandlers) recordSet(ctx context.Context, request ContextVMRequest) (any, error) {
	persistence, _ := h.operator.(DNSPersistenceOperator)
	if persistence == nil {
		zoneName, _ := dnsZoneFromParams(request.RPC.Params)
		return dnsResult(dnsActionRecordOverride, "failed", "unsupported", dnsUnsupportedRecordOverride, map[string]any{"zone": zoneName}), nil
	}
	var override domain.DNSRecordOverride
	if err := decodeContextVMParams(request.RPC.Params, &override); err != nil {
		return dnsResult(dnsActionRecordOverride, "error", "parse_error", fmt.Sprintf("invalid DNS record override JSON content: %v", err), nil), nil
	}
	if override.ID == uuid.Nil {
		override.ID = uuid.New()
	}
	if override.CreatedAt.IsZero() {
		override.CreatedAt = time.Now().UTC()
	}
	if request.Event != nil {
		override.OperatorPubkey = request.Event.PubKey.Hex()
	}
	if err := domain.ValidateDNSRecordOverride(&override); err != nil {
		return dnsResult(dnsActionRecordOverride, "error", "validation_error", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}), nil
	}
	if err := persistence.CreateOverride(ctx, override); err != nil {
		return dnsResult(dnsActionRecordOverride, "error", "persist_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}), nil
	}
	if err := h.operator.ReconcileZone(ctx, override.ZoneName); err != nil {
		return dnsResult(dnsActionRecordOverride, "error", "reconcile_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}), nil
	}
	return dnsResult(dnsActionRecordOverride, "success", "completed", "DNS record override persisted; reconcile completed", map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}), nil
}

func (h dnsContextVMHandlers) driftRemediate(ctx context.Context, request ContextVMRequest) (any, error) {
	zoneName, err := dnsZoneFromParams(request.RPC.Params)
	if err != nil {
		return dnsResult(dnsActionDriftRemediate, "error", "parse_error", err.Error(), nil), nil
	}
	if zoneName != "" {
		err = h.operator.ReconcileZone(ctx, zoneName)
	} else {
		err = h.operator.ReconcileAll(ctx)
	}
	if err != nil {
		return dnsResult(dnsActionDriftRemediate, "error", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName}), nil
	}
	message := "DNS reconcile completed"
	if zoneName != "" {
		message = fmt.Sprintf("DNS reconcile completed for zone %s", zoneName)
	}
	return dnsResult(dnsActionDriftRemediate, "success", "completed", message, map[string]any{"zone": zoneName}), nil
}

func dnsZoneFromParams(params json.RawMessage) (string, error) {
	if len(params) == 0 || strings.TrimSpace(string(params)) == "" || strings.TrimSpace(string(params)) == "null" {
		return "", nil
	}
	var payload struct {
		Zone     string `json:"zone"`
		ZoneName string `json:"zone_name"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return "", fmt.Errorf("invalid JSON content: %w", err)
	}
	for _, candidate := range []string{payload.Zone, payload.ZoneName, payload.Name} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value, nil
		}
	}
	return "", nil
}

func dnsResult(action, status, step, message string, details map[string]any) map[string]any {
	content := map[string]any{"action": action, "status": status, "step": step, "message": message, "recorded_at": time.Now().UTC().Format(time.RFC3339)}
	for key, value := range details {
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		if value != nil {
			content[key] = value
		}
	}
	return content
}

package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

const dnsOrchestrationDisabledMessage = "DNS orchestration is not enabled; set dns.enabled and configure a backend"

type dnsOpResult struct {
	Action  string
	Status  string
	Step    string
	Message string
	Details map[string]any
}

func (r *dnsOpResult) toMap() map[string]any {
	return dnsResult(r.Action, r.Status, r.Step, r.Message, r.Details)
}

func dnsZoneCreateOp(ctx context.Context, op DNSControlPlaneOperator, rawParams json.RawMessage, tags nostr.Tags) *dnsOpResult {
	persistence, _ := op.(DNSPersistenceOperator)
	if persistence != nil {
		var zone domain.DNSZone
		if err := json.Unmarshal(rawParams, &zone); err != nil {
			return &dnsOpResult{dnsActionZoneCreate, "failed", "parse_error", fmt.Sprintf("invalid DNS zone JSON content: %v", err), nil}
		}
		if err := domain.ValidateDNSZone(&zone); err != nil {
			return &dnsOpResult{dnsActionZoneCreate, "failed", "validation_error", err.Error(), map[string]any{"zone": zone.Name}}
		}
		if err := validateDNSZoneBackend(op, zone); err != nil {
			return &dnsOpResult{dnsActionZoneCreate, "failed", "unknown_backend", err.Error(), map[string]any{"zone": zone.Name, "backend_ref": zone.BackendRef}}
		}
		if err := persistence.CreateZone(ctx, zone); err != nil {
			return &dnsOpResult{dnsActionZoneCreate, "failed", "persist_failed", err.Error(), map[string]any{"zone": zone.Name}}
		}
		if err := op.ReconcileZone(ctx, zone.Name); err != nil {
			return &dnsOpResult{dnsActionZoneCreate, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": zone.Name}}
		}
		return &dnsOpResult{dnsActionZoneCreate, "succeeded", "completed", "DNS zone persisted; reconcile completed", map[string]any{"zone": zone.Name}}
	}
	zoneName, err := dnsZoneFromTagsOrParams(tags, rawParams)
	if err != nil {
		return &dnsOpResult{dnsActionZoneCreate, "failed", "parse_error", err.Error(), nil}
	}
	if zoneName == "" {
		return &dnsOpResult{dnsActionZoneCreate, "failed", "validation_error", "zone selector is required", nil}
	}
	if !op.HasZone(zoneName) {
		return &dnsOpResult{dnsActionZoneCreate, "failed", "unsupported", dnsUnsupportedDynamicZoneCreation, map[string]any{"zone": zoneName}}
	}
	if err := op.ReconcileZone(ctx, zoneName); err != nil {
		return &dnsOpResult{dnsActionZoneCreate, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName}}
	}
	return &dnsOpResult{dnsActionZoneCreate, "succeeded", "completed", "Configured DNS zone exists; reconcile completed", map[string]any{"zone": zoneName}}
}

func dnsPolicyApplyOp(ctx context.Context, op DNSControlPlaneOperator, rawParams json.RawMessage) *dnsOpResult {
	provider, _ := op.(DNSPolicyRepositoryProvider)
	if provider == nil || provider.DNSPolicyRepository() == nil {
		zoneName, _ := dnsZoneFromParams(rawParams)
		return &dnsOpResult{dnsActionPolicyApply, "failed", "unsupported", dnsUnsupportedPolicyApply, map[string]any{"zone": zoneName}}
	}
	var policy domain.DNSPolicy
	if err := json.Unmarshal(rawParams, &policy); err != nil {
		return &dnsOpResult{dnsActionPolicyApply, "failed", "parse_error", fmt.Sprintf("invalid DNS policy JSON content: %v", err), nil}
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
		return &dnsOpResult{dnsActionPolicyApply, "failed", "validation_error", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String()}}
	}
	if err := provider.DNSPolicyRepository().Create(ctx, &policy); err != nil {
		return &dnsOpResult{dnsActionPolicyApply, "failed", "persist_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)}}
	}
	if err := op.ReconcileAll(ctx); err != nil {
		return &dnsOpResult{dnsActionPolicyApply, "failed", "reconcile_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)}}
	}
	return &dnsOpResult{dnsActionPolicyApply, "succeeded", "completed", fmt.Sprintf("DNS policy %s accepted with %d rule(s); reconcile completed", policy.Name, len(policy.Rules)), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)}}
}

func dnsRecordSetOp(ctx context.Context, op DNSControlPlaneOperator, rawParams json.RawMessage, operatorPubkey string) *dnsOpResult {
	persistence, _ := op.(DNSPersistenceOperator)
	if persistence == nil {
		zoneName, _ := dnsZoneFromParams(rawParams)
		return &dnsOpResult{dnsActionRecordOverride, "failed", "unsupported", dnsUnsupportedRecordOverride, map[string]any{"zone": zoneName}}
	}
	var override domain.DNSRecordOverride
	if err := json.Unmarshal(rawParams, &override); err != nil {
		return &dnsOpResult{dnsActionRecordOverride, "failed", "parse_error", fmt.Sprintf("invalid DNS record override JSON content: %v", err), nil}
	}
	if override.ID == uuid.Nil {
		override.ID = uuid.New()
	}
	if override.CreatedAt.IsZero() {
		override.CreatedAt = time.Now().UTC()
	}
	if operatorPubkey != "" {
		override.OperatorPubkey = operatorPubkey
	}
	if err := domain.ValidateDNSRecordOverride(&override); err != nil {
		return &dnsOpResult{dnsActionRecordOverride, "failed", "validation_error", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}}
	}
	if err := persistence.CreateOverride(ctx, override); err != nil {
		return &dnsOpResult{dnsActionRecordOverride, "failed", "persist_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}}
	}
	if err := op.ReconcileZone(ctx, override.ZoneName); err != nil {
		return &dnsOpResult{dnsActionRecordOverride, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}}
	}
	return &dnsOpResult{dnsActionRecordOverride, "succeeded", "completed", "DNS record override persisted; reconcile completed", map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()}}
}

func dnsOverrideRetireOp(ctx context.Context, op DNSControlPlaneOperator, rawParams json.RawMessage, operatorPubkey string) *dnsOpResult {
	retirer, _ := op.(DNSOverrideRetirementOperator)
	if retirer == nil {
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "unsupported", dnsUnsupportedOverrideRetire, nil}
	}
	var params struct {
		OverrideID string `json:"override_id"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "parse_error", fmt.Sprintf("invalid DNS override retire JSON content: %v", err), nil}
	}
	params.Reason = strings.TrimSpace(params.Reason)
	params.OverrideID = strings.TrimSpace(params.OverrideID)
	if params.OverrideID == "" {
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "validation_error", "override_id is required", nil}
	}
	if params.Reason == "" {
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "validation_error", "reason is required", nil}
	}
	overrideID, err := uuid.Parse(params.OverrideID)
	if err != nil {
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "validation_error", fmt.Sprintf("invalid override_id: %v", err), nil}
	}
	retirement, err := retireDNSOverride(ctx, retirer, overrideID, time.Now().UTC(), params.Reason)
	if errors.Is(err, errDNSOverrideNotFound) {
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "not_found", fmt.Sprintf("DNS record override %s not found", params.OverrideID), map[string]any{"override_id": params.OverrideID}}
	}
	if err != nil {
		details := map[string]any{"override_id": params.OverrideID}
		if retirement.Override != nil {
			details["zone"] = retirement.Override.ZoneName
		}
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "persist_failed", err.Error(), details}
	}
	existing := retirement.Override
	alreadyInactive := retirement.AlreadyInactive
	now := retirement.RetiredAt
	if err := op.ReconcileZone(ctx, existing.ZoneName); err != nil {
		return &dnsOpResult{dnsActionOverrideRetire, "failed", "reconcile_failed", err.Error(), map[string]any{"override_id": params.OverrideID, "zone": existing.ZoneName}}
	}
	details := map[string]any{"override_id": params.OverrideID, "zone": existing.ZoneName, "retired_at": now.Format(time.RFC3339), "reason": params.Reason, "operator_pubkey": operatorPubkey}
	if alreadyInactive {
		return &dnsOpResult{dnsActionOverrideRetire, "succeeded", "already_inactive", fmt.Sprintf("DNS record override %s was already inactive", params.OverrideID), details}
	}
	return &dnsOpResult{dnsActionOverrideRetire, "succeeded", "completed", fmt.Sprintf("DNS record override %s retired; reconcile completed", params.OverrideID), details}
}

func dnsDriftRemediateOp(ctx context.Context, op DNSControlPlaneOperator, rawParams json.RawMessage, tags nostr.Tags) *dnsOpResult {
	zoneName, err := dnsZoneFromTagsOrParams(tags, rawParams)
	if err != nil {
		return &dnsOpResult{dnsActionDriftRemediate, "failed", "parse_error", err.Error(), nil}
	}
	if zoneName != "" {
		err = op.ReconcileZone(ctx, zoneName)
	} else {
		err = op.ReconcileAll(ctx)
	}
	if err != nil {
		return &dnsOpResult{dnsActionDriftRemediate, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName}}
	}
	message := "DNS reconcile completed"
	if zoneName != "" {
		message = fmt.Sprintf("DNS reconcile completed for zone %s", zoneName)
	}
	return &dnsOpResult{dnsActionDriftRemediate, "succeeded", "completed", message, map[string]any{"zone": zoneName}}
}

func dnsZoneFromTagsOrParams(tags nostr.Tags, rawParams json.RawMessage) (string, error) {
	zoneName := tagValueNostr(tags, "zone")
	if zoneName != "" {
		return strings.TrimSpace(zoneName), nil
	}
	return dnsZoneFromParams(rawParams)
}

// RegisterDNSContextVMHandlers bridges encrypted ContextVM DNS methods from the
// browser to the app-owned DNS reconciliation and persistence boundary.
func RegisterDNSContextVMHandlers(transport *EncryptedRequestTransport, operator DNSControlPlaneOperator, enabled bool, gate *FleetOperatorGate) {
	if transport == nil {
		return
	}
	h := dnsContextVMHandlers{operator: operator, enabled: enabled}
	transport.RegisterContextVMHandler(ContextVMMethodDNSZoneCreate, gate.wrap(h.whenEnabled(h.zoneCreate)))
	transport.RegisterContextVMHandler(ContextVMMethodDNSPolicyApply, gate.wrap(h.whenEnabled(h.policyApply)))
	transport.RegisterContextVMHandler(ContextVMMethodDNSRecordSet, gate.wrap(h.whenEnabled(h.recordSet)))
	transport.RegisterContextVMHandler(ContextVMMethodDNSDriftRemediate, gate.wrap(h.whenEnabled(h.driftRemediate)))
	transport.RegisterContextVMHandler(ContextVMMethodDNSOverrideRetire, gate.wrap(h.whenEnabled(h.overrideRetire)))
}

type dnsContextVMHandlers struct {
	operator DNSControlPlaneOperator
	enabled  bool
}

func (h dnsContextVMHandlers) whenEnabled(next ContextVMHandler) ContextVMHandler {
	return func(ctx context.Context, request ContextVMRequest) (any, error) {
		if !h.enabled || h.operator == nil {
			return nil, errors.New(dnsOrchestrationDisabledMessage)
		}
		return next(ctx, request)
	}
}

func (h dnsContextVMHandlers) zoneCreate(ctx context.Context, request ContextVMRequest) (any, error) {
	return dnsZoneCreateOp(ctx, h.operator, request.RPC.Params, nil).toMap(), nil
}

func (h dnsContextVMHandlers) policyApply(ctx context.Context, request ContextVMRequest) (any, error) {
	return dnsPolicyApplyOp(ctx, h.operator, request.RPC.Params).toMap(), nil
}

func (h dnsContextVMHandlers) recordSet(ctx context.Context, request ContextVMRequest) (any, error) {
	pubkey := ""
	if request.Event != nil {
		pubkey = request.Event.PubKey.Hex()
	}
	return dnsRecordSetOp(ctx, h.operator, request.RPC.Params, pubkey).toMap(), nil
}

func (h dnsContextVMHandlers) overrideRetire(ctx context.Context, request ContextVMRequest) (any, error) {
	pubkey := ""
	if request.Event != nil {
		pubkey = request.Event.PubKey.Hex()
	}
	return dnsOverrideRetireOp(ctx, h.operator, request.RPC.Params, pubkey).toMap(), nil
}

func (h dnsContextVMHandlers) driftRemediate(ctx context.Context, request ContextVMRequest) (any, error) {
	return dnsDriftRemediateOp(ctx, h.operator, request.RPC.Params, nil).toMap(), nil
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

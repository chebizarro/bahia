package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	dnsActionZoneCreate      = "dns_zone_create"
	dnsActionPolicyApply     = "dns_policy_apply"
	dnsActionRecordOverride  = "dns_record_override"
	dnsActionDriftRemediate  = "dns_drift_remediate"
	dnsActionBackendRegister = "dns_backend_register"

	dnsUnsupportedDynamicZoneCreation = "Phase-1 DNS runtime is config-backed; dynamic durable zone creation is unavailable"
	dnsUnsupportedRecordOverride      = "current DNS backend interface has no record-level mutation primitive and no override persistence exists"
	dnsUnsupportedPolicyApply         = "DNS policy persistence and application are unavailable in Phase-1 DNS runtime"
	dnsUnsupportedBackendRegister     = "Phase-1 DNS runtime is config-backed; dynamic durable backend registration is unavailable"
)

// DNSControlPlaneOperator is the app-owned DNS reconciliation boundary used by Nostr DNS commands.
type DNSControlPlaneOperator interface {
	ReconcileAll(ctx context.Context) error
	ReconcileZone(ctx context.Context, zoneName string) error
	HasZone(zoneName string) bool
}

// DNSPersistenceOperator is the app-owned durable DNS command boundary.
type DNSPersistenceOperator interface {
	CreateZone(ctx context.Context, zone domain.DNSZone) error
	CreateOverride(ctx context.Context, override domain.DNSRecordOverride) error
	ListOverridesByZone(ctx context.Context, zoneName string) ([]domain.DNSRecordOverride, error)
}

type DNSPolicyRepositoryProvider interface {
	DNSPolicyRepository() repository.DNSPolicyRepository
}

func (r *Reactor) handleDNSRequest(ctx context.Context, event *nostr.Event) {
	switch event.Kind {
	case KindDNSDriftRemediateRequest:
		r.handleDNSDriftRemediate(ctx, event)
	case KindDNSZoneCreateRequest:
		r.handleDNSZoneCreate(ctx, event)
	case KindDNSRecordOverrideRequest:
		r.handleDNSRecordOverride(ctx, event)
	case KindDNSPolicyApplyRequest:
		r.handleDNSPolicyApply(ctx, event)
	case KindDNSBackendRegisterRequest:
		r.publishDNSUnsupported(ctx, event, KindDNSBackendRegisterResult, dnsActionBackendRegister, dnsUnsupportedBackendRegister)
	default:
		r.logger.Warn("unexpected DNS control-plane kind", "kind", event.Kind, "event_id", event.ID)
	}
}

func (r *Reactor) handleDNSDriftRemediate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS drift remediation request")
		_ = r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "error", "unauthorized", "requester not in authorized list", nil)
		return
	}
	zoneName, err := parseDNSZoneSelector(event)
	if err != nil {
		_ = r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "error", "parse_error", err.Error(), nil)
		return
	}
	if err := r.publishDNSOperationStatus(ctx, event, dnsActionDriftRemediate, "reconciling", "DNS drift remediation reconcile requested", zoneName); err != nil {
		logger.Warn("publish DNS remediation status failed", "error", err)
	}
	if zoneName != "" {
		err = r.dnsOperator.ReconcileZone(ctx, zoneName)
	} else {
		err = r.dnsOperator.ReconcileAll(ctx)
	}
	if err != nil {
		logger.Warn("DNS drift remediation failed", "zone", zoneName, "error", err)
		_ = r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "error", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName})
		return
	}
	message := "DNS reconcile completed"
	if zoneName != "" {
		message = fmt.Sprintf("DNS reconcile completed for zone %s", zoneName)
	}
	_ = r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "success", "completed", message, map[string]any{"zone": zoneName})
}

func (r *Reactor) handleDNSZoneCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS zone create request")
		_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "unauthorized", "requester not in authorized list", nil)
		return
	}
	persistence := r.dnsPersistenceOperator()
	if persistence != nil {
		var zone domain.DNSZone
		if err := json.Unmarshal([]byte(event.Content), &zone); err != nil {
			_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "parse_error", fmt.Sprintf("invalid DNS zone JSON content: %v", err), nil)
			return
		}
		if err := domain.ValidateDNSZone(&zone); err != nil {
			_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "validation_error", err.Error(), map[string]any{"zone": zone.Name})
			return
		}
		if err := persistence.CreateZone(ctx, zone); err != nil {
			logger.Warn("DNS zone persistence failed", "zone", zone.Name, "error", err)
			_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "persist_failed", err.Error(), map[string]any{"zone": zone.Name})
			return
		}
		if err := r.publishDNSOperationStatus(ctx, event, dnsActionZoneCreate, "reconciling", "DNS zone persisted; reconcile requested", zone.Name); err != nil {
			logger.Warn("publish DNS zone create status failed", "error", err)
		}
		if err := r.dnsOperator.ReconcileZone(ctx, zone.Name); err != nil {
			logger.Warn("DNS zone reconcile failed", "zone", zone.Name, "error", err)
			_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "reconcile_failed", err.Error(), map[string]any{"zone": zone.Name})
			return
		}
		_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "success", "completed", "DNS zone persisted; reconcile completed", map[string]any{"zone": zone.Name})
		return
	}
	zoneName, err := parseDNSZoneSelector(event)
	if err != nil {
		_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "parse_error", err.Error(), nil)
		return
	}
	if zoneName == "" {
		_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "validation_error", "zone selector is required", nil)
		return
	}
	if !r.dnsOperator.HasZone(zoneName) {
		_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "unsupported", dnsUnsupportedDynamicZoneCreation, map[string]any{"zone": zoneName})
		return
	}
	if err := r.publishDNSOperationStatus(ctx, event, dnsActionZoneCreate, "reconciling", "Configured DNS zone exists; reconcile requested", zoneName); err != nil {
		logger.Warn("publish DNS zone create status failed", "error", err)
	}
	if err := r.dnsOperator.ReconcileZone(ctx, zoneName); err != nil {
		logger.Warn("DNS zone reconcile failed", "zone", zoneName, "error", err)
		_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "error", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName})
		return
	}
	_ = r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "success", "completed", "Configured DNS zone exists; reconcile completed", map[string]any{"zone": zoneName})
}

func (r *Reactor) handleDNSRecordOverride(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS record override request")
		_ = r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "error", "unauthorized", "requester not in authorized list", nil)
		return
	}
	persistence := r.dnsPersistenceOperator()
	if persistence == nil {
		r.publishDNSUnsupported(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, dnsUnsupportedRecordOverride)
		return
	}
	var override domain.DNSRecordOverride
	if err := json.Unmarshal([]byte(event.Content), &override); err != nil {
		_ = r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "error", "parse_error", fmt.Sprintf("invalid DNS record override JSON content: %v", err), nil)
		return
	}
	if override.ID == uuid.Nil {
		override.ID = uuid.New()
	}
	if override.CreatedAt.IsZero() {
		override.CreatedAt = time.Now().UTC()
	}
	override.OperatorPubkey = event.PubKey.Hex()
	if err := domain.ValidateDNSRecordOverride(&override); err != nil {
		_ = r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "error", "validation_error", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
		return
	}
	if err := persistence.CreateOverride(ctx, override); err != nil {
		logger.Warn("DNS record override persistence failed", "zone", override.ZoneName, "override_id", override.ID.String(), "error", err)
		_ = r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "error", "persist_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
		return
	}
	if err := r.publishDNSOperationStatus(ctx, event, dnsActionRecordOverride, "reconciling", "DNS record override persisted; reconcile requested", override.ZoneName); err != nil {
		logger.Warn("publish DNS record override status failed", "error", err)
	}
	if err := r.dnsOperator.ReconcileZone(ctx, override.ZoneName); err != nil {
		logger.Warn("DNS record override reconcile failed", "zone", override.ZoneName, "override_id", override.ID.String(), "error", err)
		_ = r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "error", "reconcile_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
		return
	}
	_ = r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "success", "completed", "DNS record override persisted; reconcile completed", map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
}

func (r *Reactor) handleDNSPolicyApply(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS policy apply request")
		_ = r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "error", "unauthorized", "requester not in authorized list", nil)
		return
	}
	policyRepo := r.dnsPolicyRepository()
	if r.dnsOperator == nil || policyRepo == nil {
		zoneName, _ := parseDNSZoneSelector(event)
		_ = r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "unsupported", dnsUnsupportedPolicyApply, map[string]any{"zone": zoneName})
		return
	}
	var policy domain.DNSPolicy
	if err := json.Unmarshal([]byte(event.Content), &policy); err != nil {
		_ = r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "error", "parse_error", fmt.Sprintf("invalid DNS policy JSON content: %v", err), nil)
		return
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
		_ = r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "error", "validation_error", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String()})
		return
	}
	if err := policyRepo.Create(ctx, &policy); err != nil {
		logger.Warn("DNS policy persistence failed", "policy_id", policy.ID.String(), "policy", policy.Name, "error", err)
		_ = r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "error", "persist_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)})
		return
	}
	logger.Info("persisted DNS policy apply request", "policy_id", policy.ID.String(), "policy", policy.Name, "rules", len(policy.Rules))
	if err := r.publishDNSOperationStatus(ctx, event, dnsActionPolicyApply, "reconciling", "DNS policy persisted; reconcile requested", ""); err != nil {
		logger.Warn("publish DNS policy apply status failed", "error", err)
	}
	if err := r.dnsOperator.ReconcileAll(ctx); err != nil {
		logger.Warn("DNS policy apply reconcile failed", "policy_id", policy.ID.String(), "error", err)
		_ = r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "error", "reconcile_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)})
		return
	}
	_ = r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "success", "completed", fmt.Sprintf("DNS policy %s accepted with %d rule(s); reconcile completed", policy.Name, len(policy.Rules)), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)})
}

func (r *Reactor) dnsPolicyRepository() repository.DNSPolicyRepository {
	provider, ok := r.dnsOperator.(DNSPolicyRepositoryProvider)
	if !ok || provider == nil {
		return nil
	}
	return provider.DNSPolicyRepository()
}

func (r *Reactor) dnsPersistenceOperator() DNSPersistenceOperator {
	persistence, ok := r.dnsOperator.(DNSPersistenceOperator)
	if !ok || persistence == nil {
		return nil
	}
	return persistence
}

func (r *Reactor) publishDNSUnsupported(ctx context.Context, event *nostr.Event, resultKind int, action, reason string) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		_ = r.publishDNSOperationResult(ctx, event, resultKind, action, "error", "unauthorized", "requester not in authorized list", nil)
		return
	}
	zoneName, _ := parseDNSZoneSelector(event)
	_ = r.publishDNSOperationResult(ctx, event, resultKind, action, "failed", "unsupported", reason, map[string]any{"zone": zoneName})
}

func parseDNSZoneSelector(event *nostr.Event) (string, error) {
	zoneName := tagValueNostr(event.Tags, "zone")
	trimmedContent := strings.TrimSpace(event.Content)
	if trimmedContent == "" {
		return strings.TrimSpace(zoneName), nil
	}
	if event.Kind == KindDNSPolicyApplyRequest {
		return strings.TrimSpace(zoneName), nil
	}
	var content struct {
		Zone     string `json:"zone"`
		ZoneName string `json:"zone_name"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return "", fmt.Errorf("invalid JSON content: %w", err)
	}
	for _, candidate := range []string{content.Zone, content.ZoneName, content.Name, zoneName} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value, nil
		}
	}
	return "", nil
}

func (r *Reactor) publishDNSOperationStatus(ctx context.Context, requestEvent *nostr.Event, action, step, message, zoneName string) error {
	content := map[string]any{
		"action":      action,
		"status":      "processing",
		"step":        step,
		"message":     message,
		"recorded_at": time.Now().UTC().Format(time.RFC3339),
	}
	if zoneName != "" {
		content["zone"] = zoneName
	}
	tags := nostr.Tags{{"domain", "dns"}, {"schema", "bahia.status.dns.v1"}, {"legacy_kind", fmt.Sprintf("%d", KindDNSOperationStatus)}, {"status", "processing"}, {"action", action}, {"step", step}}
	if zoneName != "" {
		tags = append(tags, nostr.Tag{"zone", zoneName})
	}
	return r.publishCanonicalStatus(ctx, requestEvent, tags, content)
}

func (r *Reactor) publishDNSOperationResult(ctx context.Context, requestEvent *nostr.Event, resultKind int, action, status, step, message string, details map[string]any) error {
	content := map[string]any{
		"action":      action,
		"status":      status,
		"step":        step,
		"message":     message,
		"recorded_at": time.Now().UTC().Format(time.RFC3339),
	}
	for key, value := range details {
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		if value != nil {
			content[key] = value
		}
	}
	tags := nostr.Tags{{"domain", "dns"}, {"schema", "bahia.result.dns.v1"}, {"legacy_kind", fmt.Sprintf("%d", resultKind)}, {"status", status}, {"action", action}, {"step", step}}
	if zoneName, ok := content["zone"].(string); ok && zoneName != "" {
		tags = append(tags, nostr.Tag{"zone", zoneName})
	}
	var rpcErr *JSONRPCError
	if step == "unsupported" || step == "parse_error" || step == "validation_error" || step == "reconcile_failed" || step == "unauthorized" || status == "error" || status == "failed" {
		tags = append(tags, nostr.Tag{"error", message})
		rpcErr = &JSONRPCError{Code: -32000, Message: message}
	}
	return r.publishContextVMResult(ctx, requestEvent, content, tags, rpcErr)
}

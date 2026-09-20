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
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	dnsActionZoneCreate      = "dns_zone_create"
	dnsActionPolicyApply     = "dns_policy_apply"
	dnsActionRecordOverride  = "dns_record_override"
	dnsActionDriftRemediate  = "dns_drift_remediate"
	dnsActionBackendRegister = "dns_backend_register"
	dnsActionOverrideRetire  = "dns_override_retire"

	dnsUnsupportedDynamicZoneCreation = "Phase-1 DNS runtime is config-backed; dynamic durable zone creation is unavailable"
	dnsUnsupportedRecordOverride      = "current DNS backend interface has no record-level mutation primitive and no override persistence exists"
	dnsUnsupportedOverrideRetire      = "current DNS persistence backend cannot retire an existing record override"
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

// DNSOverrideRetirementOperator is an OPTIONAL capability bolted alongside
// DNSPersistenceOperator rather than folded into it.
//
// Widening DNSPersistenceOperator would silently demote every existing
// implementation that has not yet grown these methods: the handlers type-assert
// that interface, so a stale implementor would start failing zone-create and
// record-set as "unsupported" too, not just retirement. Keeping retirement in a
// separate assertion means a backend that cannot retire loses exactly one
// operation and nothing else.
type DNSOverrideRetirementOperator interface {
	GetOverride(ctx context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error)
	ExpireOverride(ctx context.Context, id uuid.UUID, at time.Time, reason string) error
}

// errDNSOverrideNotFound signals that no DNS record override exists for an ID.
var errDNSOverrideNotFound = errors.New("dns record override not found")

// dnsOverrideRetirement describes the effect of a retirement request.
type dnsOverrideRetirement struct {
	Override        *domain.DNSRecordOverride
	RetiredAt       time.Time
	AlreadyInactive bool
}

// retireDNSOverride expires a DNS record override idempotently.
//
// An override is inactive once its expiry is at or before now, which is exactly
// the predicate the override read path uses. Retiring an already-inactive
// override therefore performs no write and reports the ORIGINAL retirement
// instant: repeating the call must not move the recorded retirement time, or the
// audit trail would drift on every retry. An override whose expiry is still in
// the future is active and is brought forward to now.
func retireDNSOverride(ctx context.Context, retirer DNSOverrideRetirementOperator, id uuid.UUID, now time.Time, reason string) (dnsOverrideRetirement, error) {
	existing, err := retirer.GetOverride(ctx, id)
	if err != nil {
		return dnsOverrideRetirement{}, err
	}
	if existing == nil {
		return dnsOverrideRetirement{}, errDNSOverrideNotFound
	}
	if existing.ExpiresAt != nil && !existing.ExpiresAt.After(now) {
		return dnsOverrideRetirement{Override: existing, RetiredAt: existing.ExpiresAt.UTC(), AlreadyInactive: true}, nil
	}
	if err := retirer.ExpireOverride(ctx, id, now, reason); err != nil {
		return dnsOverrideRetirement{Override: existing}, err
	}
	return dnsOverrideRetirement{Override: existing, RetiredAt: now}, nil
}

type DNSPolicyRepositoryProvider interface {
	DNSPolicyRepository() repository.DNSPolicyRepository
}

// DNSBackendProvider exposes the configured backend references accepted by
// durable zone creation.
type DNSBackendProvider interface {
	HasBackend(ref string) bool
}

func validateDNSZoneBackend(operator DNSControlPlaneOperator, zone domain.DNSZone) error {
	provider, ok := operator.(DNSBackendProvider)
	if !ok || !provider.HasBackend(zone.BackendRef) {
		return fmt.Errorf("DNS backend %q is not configured", zone.BackendRef)
	}
	return nil
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
	case KindDNSOverrideRetireRequest:
		r.handleDNSOverrideRetire(ctx, event)
	default:
		r.logger.Warn("unexpected DNS control-plane kind", "kind", event.Kind, "event_id", event.ID)
	}
}

func (r *Reactor) handleDNSDriftRemediate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS drift remediation request")
		r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	zoneName, err := parseDNSZoneSelector(event)
	if err != nil {
		r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "failed", "parse_error", err.Error(), nil)
		return
	}
	r.publishDNSOperationStatus(ctx, event, dnsActionDriftRemediate, "reconciling", "DNS drift remediation reconcile requested", zoneName)
	if zoneName != "" {
		err = r.dnsOperator.ReconcileZone(ctx, zoneName)
	} else {
		err = r.dnsOperator.ReconcileAll(ctx)
	}
	if err != nil {
		logger.Warn("DNS drift remediation failed", "zone", zoneName, "error", err)
		r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName})
		return
	}
	message := "DNS reconcile completed"
	if zoneName != "" {
		message = fmt.Sprintf("DNS reconcile completed for zone %s", zoneName)
	}
	r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "succeeded", "completed", message, map[string]any{"zone": zoneName})
}

func (r *Reactor) handleDNSZoneCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS zone create request")
		r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	persistence := r.dnsPersistenceOperator()
	if persistence != nil {
		var zone domain.DNSZone
		if err := json.Unmarshal([]byte(event.Content), &zone); err != nil {
			r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "parse_error", fmt.Sprintf("invalid DNS zone JSON content: %v", err), nil)
			return
		}
		if err := domain.ValidateDNSZone(&zone); err != nil {
			r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "validation_error", err.Error(), map[string]any{"zone": zone.Name})
			return
		}
		if err := validateDNSZoneBackend(r.dnsOperator, zone); err != nil {
			r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "unknown_backend", err.Error(), map[string]any{"zone": zone.Name, "backend_ref": zone.BackendRef})
			return
		}
		if err := persistence.CreateZone(ctx, zone); err != nil {
			logger.Warn("DNS zone persistence failed", "zone", zone.Name, "error", err)
			r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "persist_failed", err.Error(), map[string]any{"zone": zone.Name})
			return
		}
		r.publishDNSOperationStatus(ctx, event, dnsActionZoneCreate, "reconciling", "DNS zone persisted; reconcile requested", zone.Name)
		if err := r.dnsOperator.ReconcileZone(ctx, zone.Name); err != nil {
			logger.Warn("DNS zone reconcile failed", "zone", zone.Name, "error", err)
			r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": zone.Name})
			return
		}
		r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "succeeded", "completed", "DNS zone persisted; reconcile completed", map[string]any{"zone": zone.Name})
		return
	}
	zoneName, err := parseDNSZoneSelector(event)
	if err != nil {
		r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "parse_error", err.Error(), nil)
		return
	}
	if zoneName == "" {
		r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "validation_error", "zone selector is required", nil)
		return
	}
	if !r.dnsOperator.HasZone(zoneName) {
		r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "unsupported", dnsUnsupportedDynamicZoneCreation, map[string]any{"zone": zoneName})
		return
	}
	r.publishDNSOperationStatus(ctx, event, dnsActionZoneCreate, "reconciling", "Configured DNS zone exists; reconcile requested", zoneName)
	if err := r.dnsOperator.ReconcileZone(ctx, zoneName); err != nil {
		logger.Warn("DNS zone reconcile failed", "zone", zoneName, "error", err)
		r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": zoneName})
		return
	}
	r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "succeeded", "completed", "Configured DNS zone exists; reconcile completed", map[string]any{"zone": zoneName})
}

func (r *Reactor) handleDNSRecordOverride(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS record override request")
		r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	persistence := r.dnsPersistenceOperator()
	if persistence == nil {
		r.publishDNSUnsupported(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, dnsUnsupportedRecordOverride)
		return
	}
	var override domain.DNSRecordOverride
	if err := json.Unmarshal([]byte(event.Content), &override); err != nil {
		r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "failed", "parse_error", fmt.Sprintf("invalid DNS record override JSON content: %v", err), nil)
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
		r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "failed", "validation_error", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
		return
	}
	if err := persistence.CreateOverride(ctx, override); err != nil {
		logger.Warn("DNS record override persistence failed", "zone", override.ZoneName, "override_id", override.ID.String(), "error", err)
		r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "failed", "persist_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
		return
	}
	r.publishDNSOperationStatus(ctx, event, dnsActionRecordOverride, "reconciling", "DNS record override persisted; reconcile requested", override.ZoneName)
	if err := r.dnsOperator.ReconcileZone(ctx, override.ZoneName); err != nil {
		logger.Warn("DNS record override reconcile failed", "zone", override.ZoneName, "override_id", override.ID.String(), "error", err)
		r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "failed", "reconcile_failed", err.Error(), map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
		return
	}
	r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "succeeded", "completed", "DNS record override persisted; reconcile completed", map[string]any{"zone": override.ZoneName, "override_id": override.ID.String()})
}

func (r *Reactor) handleDNSOverrideRetire(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS override retire request")
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	retirer, _ := r.dnsOperator.(DNSOverrideRetirementOperator)
	if retirer == nil {
		r.publishDNSUnsupported(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, dnsUnsupportedOverrideRetire)
		return
	}
	var params struct {
		OverrideID string `json:"override_id"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(event.Content), &params); err != nil {
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "parse_error", fmt.Sprintf("invalid DNS override retire JSON content: %v", err), nil)
		return
	}
	params.Reason = strings.TrimSpace(params.Reason)
	params.OverrideID = strings.TrimSpace(params.OverrideID)
	if params.OverrideID == "" || params.Reason == "" {
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "validation_error", "override_id and reason are required", nil)
		return
	}
	overrideID, err := uuid.Parse(params.OverrideID)
	if err != nil {
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "validation_error", fmt.Sprintf("invalid override_id: %v", err), nil)
		return
	}
	retirement, err := retireDNSOverride(ctx, retirer, overrideID, time.Now().UTC(), params.Reason)
	if errors.Is(err, errDNSOverrideNotFound) {
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "not_found", fmt.Sprintf("DNS record override %s not found", params.OverrideID), map[string]any{"override_id": params.OverrideID})
		return
	}
	if err != nil {
		logger.Warn("DNS override retire failed", "override_id", params.OverrideID, "error", err)
		details := map[string]any{"override_id": params.OverrideID}
		if retirement.Override != nil {
			details["zone"] = retirement.Override.ZoneName
		}
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "persist_failed", err.Error(), details)
		return
	}
	existing := retirement.Override
	alreadyInactive := retirement.AlreadyInactive
	now := retirement.RetiredAt
	r.publishDNSOperationStatus(ctx, event, dnsActionOverrideRetire, "reconciling", "DNS override retired; reconcile requested", existing.ZoneName)
	if err := r.dnsOperator.ReconcileZone(ctx, existing.ZoneName); err != nil {
		logger.Warn("DNS override retire reconcile failed", "zone", existing.ZoneName, "override_id", params.OverrideID, "error", err)
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "reconcile_failed", err.Error(), map[string]any{"override_id": params.OverrideID, "zone": existing.ZoneName})
		return
	}
	operatorPubkey := event.PubKey.Hex()
	if alreadyInactive {
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "succeeded", "already_inactive", fmt.Sprintf("DNS record override %s was already inactive", params.OverrideID), map[string]any{"override_id": params.OverrideID, "zone": existing.ZoneName, "retired_at": now.Format(time.RFC3339), "reason": params.Reason, "operator_pubkey": operatorPubkey})
		return
	}
	r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "succeeded", "completed", fmt.Sprintf("DNS record override %s retired; reconcile completed", params.OverrideID), map[string]any{"override_id": params.OverrideID, "zone": existing.ZoneName, "retired_at": now.Format(time.RFC3339), "reason": params.Reason, "operator_pubkey": operatorPubkey})
}

func (r *Reactor) handleDNSPolicyApply(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized DNS policy apply request")
		r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	policyRepo := r.dnsPolicyRepository()
	if r.dnsOperator == nil || policyRepo == nil {
		zoneName, _ := parseDNSZoneSelector(event)
		r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "unsupported", dnsUnsupportedPolicyApply, map[string]any{"zone": zoneName})
		return
	}
	var policy domain.DNSPolicy
	if err := json.Unmarshal([]byte(event.Content), &policy); err != nil {
		r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "parse_error", fmt.Sprintf("invalid DNS policy JSON content: %v", err), nil)
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
		r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "validation_error", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String()})
		return
	}
	if err := policyRepo.Create(ctx, &policy); err != nil {
		logger.Warn("DNS policy persistence failed", "policy_id", policy.ID.String(), "policy", policy.Name, "error", err)
		r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "persist_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)})
		return
	}
	logger.Info("persisted DNS policy apply request", "policy_id", policy.ID.String(), "policy", policy.Name, "rules", len(policy.Rules))
	r.publishDNSOperationStatus(ctx, event, dnsActionPolicyApply, "reconciling", "DNS policy persisted; reconcile requested", "")
	if err := r.dnsOperator.ReconcileAll(ctx); err != nil {
		logger.Warn("DNS policy apply reconcile failed", "policy_id", policy.ID.String(), "error", err)
		r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "reconcile_failed", err.Error(), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)})
		return
	}
	r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "succeeded", "completed", fmt.Sprintf("DNS policy %s accepted with %d rule(s); reconcile completed", policy.Name, len(policy.Rules)), map[string]any{"policy": policy.Name, "policy_id": policy.ID.String(), "rule_count": len(policy.Rules)})
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
		r.publishDNSOperationResult(ctx, event, resultKind, action, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	zoneName, _ := parseDNSZoneSelector(event)
	r.publishDNSOperationResult(ctx, event, resultKind, action, "failed", "unsupported", reason, map[string]any{"zone": zoneName})
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

func (r *Reactor) publishDNSOperationStatus(ctx context.Context, requestEvent *nostr.Event, action, step, message, zoneName string) {
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
	if err := r.publishCanonicalStatus(ctx, requestEvent, tags, content); err != nil {
		r.zapLog.Warn("publish DNS operation status failed", zap.Error(err))
	}
}

func (r *Reactor) publishDNSOperationResult(ctx context.Context, requestEvent *nostr.Event, resultKind int, action, status, code, message string, details map[string]any) {
	content := map[string]any{
		"action":      action,
		"status":      status,
		"step":        code,
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
	tags := nostr.Tags{{"legacy_kind", fmt.Sprintf("%d", resultKind)}, {"status", status}, {"action", action}, {"step", code}}
	if zoneName, ok := content["zone"].(string); ok && zoneName != "" {
		tags = append(tags, nostr.Tag{"zone", zoneName})
	}
	if status == "failed" {
		tags = append(tags, nostr.Tag{"error", message})
	}
	r.publishDomainResult(ctx, requestEvent, "dns", "bahia.result.dns.v1", status, code, message, content, tags)
}

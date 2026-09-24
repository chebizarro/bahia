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

func (r *Reactor) publishDNSOperationStatusForAction(ctx context.Context, event *nostr.Event, action, message string) {
	zoneName, _ := parseDNSZoneSelector(event)
	r.publishDNSOperationStatus(ctx, event, action, "reconciling", message, zoneName)
}

func (r *Reactor) handleDNSDriftRemediate(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, dnsActionDriftRemediate, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	r.publishDNSOperationStatusForAction(ctx, event, dnsActionDriftRemediate, "DNS drift remediation reconcile requested")
	result := dnsDriftRemediateOp(ctx, r.dnsOperator, json.RawMessage(event.Content), event.Tags)
	r.publishDNSOperationResult(ctx, event, KindDNSDriftRemediateResult, result.Action, result.Status, result.Step, result.Message, result.Details)
	if result.Status == "failed" {
		r.logger.Warn("DNS drift remediation failed", "event_id", event.ID, "error", result.Message)
	}
}

func (r *Reactor) handleDNSZoneCreate(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, dnsActionZoneCreate, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	r.publishDNSOperationStatusForAction(ctx, event, dnsActionZoneCreate, "DNS zone create requested; reconciling")
	result := dnsZoneCreateOp(ctx, r.dnsOperator, json.RawMessage(event.Content), event.Tags)
	r.publishDNSOperationResult(ctx, event, KindDNSZoneCreateResult, result.Action, result.Status, result.Step, result.Message, result.Details)
	if result.Status == "failed" {
		r.logger.Warn("DNS zone create failed", "event_id", event.ID, "error", result.Message)
	}
}

func (r *Reactor) handleDNSRecordOverride(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, dnsActionRecordOverride, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	r.publishDNSOperationStatusForAction(ctx, event, dnsActionRecordOverride, "DNS record override requested; reconciling")
	result := dnsRecordSetOp(ctx, r.dnsOperator, json.RawMessage(event.Content), event.PubKey.Hex())
	r.publishDNSOperationResult(ctx, event, KindDNSRecordOverrideResult, result.Action, result.Status, result.Step, result.Message, result.Details)
	if result.Status == "failed" {
		r.logger.Warn("DNS record override failed", "event_id", event.ID, "error", result.Message)
	}
}

func (r *Reactor) handleDNSOverrideRetire(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, dnsActionOverrideRetire, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	r.publishDNSOperationStatusForAction(ctx, event, dnsActionOverrideRetire, "DNS override retire requested; reconciling")
	result := dnsOverrideRetireOp(ctx, r.dnsOperator, json.RawMessage(event.Content), event.PubKey.Hex())
	r.publishDNSOperationResult(ctx, event, KindDNSOverrideRetireResult, result.Action, result.Status, result.Step, result.Message, result.Details)
	if result.Status == "failed" {
		r.logger.Warn("DNS override retire failed", "event_id", event.ID, "error", result.Message)
	}
}

func (r *Reactor) handleDNSPolicyApply(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, dnsActionPolicyApply, "failed", "unauthorized", "requester not in authorized list", nil)
		return
	}
	r.publishDNSOperationStatusForAction(ctx, event, dnsActionPolicyApply, "DNS policy apply requested; reconciling")
	result := dnsPolicyApplyOp(ctx, r.dnsOperator, json.RawMessage(event.Content))
	r.publishDNSOperationResult(ctx, event, KindDNSPolicyApplyResult, result.Action, result.Status, result.Step, result.Message, result.Details)
	switch result.Status {
	case "succeeded":
		r.logger.Info("persisted DNS policy apply request", "event_id", event.ID, "policy_id", result.Details["policy_id"], "policy", result.Details["policy"], "rules", result.Details["rule_count"])
	case "failed":
		r.logger.Warn("DNS policy apply failed", "event_id", event.ID, "error", result.Message)
	}
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

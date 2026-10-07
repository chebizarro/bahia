package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type DNSIntentCanonicalPublisher interface {
	PublishZone(context.Context, domain.DNSZone) error
	PublishPolicy(context.Context, domain.DNSPolicy) error
}

// DNSIntentHandler routes signed desired state to the durable DNS mutation service.
type DNSIntentHandler struct {
	operator  DNSControlPlaneOperator
	canonical DNSIntentCanonicalPublisher
	mutations *service.DNSMutationService
}

func NewDNSIntentHandler(operator DNSControlPlaneOperator, canonical DNSIntentCanonicalPublisher, mutations ...*service.DNSMutationService) *DNSIntentHandler {
	h := &DNSIntentHandler{operator: operator, canonical: canonical}
	if len(mutations) > 0 {
		h.mutations = mutations[0]
	}
	return h
}

func (*DNSIntentHandler) PermissionFor(string) domain.Permission { return domain.PermWriteServices }
func (*DNSIntentHandler) IsFleetScoped() bool                    { return true }

func (h *DNSIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	// Create and override operations do not consume revision tokens.
	if intent.ExpectedUpdatedAt != nil && (intent.Op == "zone-create" || intent.Op == "policy-apply" || intent.Op == "record-set" || intent.Op == "override-retire" || intent.Op == "drift-remediate") {
		return fmt.Errorf("expected_updated_at is not supported for DNS %s", intent.Op)
	}
	if h.operator == nil {
		return fmt.Errorf("DNS operator is not configured")
	}
	content, err := json.Marshal(intent.Content)
	if err != nil {
		return fmt.Errorf("marshal DNS intent: %w", err)
	}
	var result *dnsOpResult
	switch intent.Op {
	case "drift-remediate":
		zone, err := dnsZoneFromParams(content)
		if err != nil {
			return err
		}
		coordinate := "dns-remediate:all"
		if zone != "" {
			coordinate = "dns-remediate:" + zone
		}
		if intent.Coordinate != coordinate {
			return fmt.Errorf("DNS remediation coordinate does not match zone")
		}
		result = dnsDriftRemediateOp(ctx, h.operator, content, nil)
		if result.Status != "succeeded" {
			return fmt.Errorf("DNS drift remediation: %s", result.Message)
		}
		intent.Result = result.toMap()
		return nil
	case "zone-update":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var zone domain.DNSZone
		if err := json.Unmarshal(content, &zone); err != nil {
			return err
		}
		if err := domain.ValidateDNSZone(&zone); err != nil {
			return err
		}
		if intent.Coordinate != "zone:"+zone.Name {
			return fmt.Errorf("DNS zone coordinate does not match name")
		}
		existing, err := h.mutations.GetZone(ctx, zone.Name)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("DNS zone %q not found", zone.Name)
		}
		if err := checkIntentRevision(intent, zone.Name, true, existing.UpdatedAt); err != nil {
			return err
		}
		return h.mutations.UpdateZone(ctx, &zone)
	case "zone-delete":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var payload struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(content, &payload); err != nil {
			return err
		}
		name := domain.NormalizeDNSZoneName(payload.Name)
		if name == "" || intent.Coordinate != "zone:"+name {
			return fmt.Errorf("DNS zone coordinate does not match name")
		}
		existing, err := h.mutations.GetZone(ctx, name)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("DNS zone %q not found", name)
		}
		if err := checkIntentRevision(intent, name, true, existing.UpdatedAt); err != nil {
			return err
		}
		return h.mutations.DeleteZone(ctx, name)
	case "policy-update":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var policy domain.DNSPolicy
		if err := json.Unmarshal(content, &policy); err != nil {
			return err
		}
		if policy.ID == uuid.Nil || intent.Coordinate != "dnspolicy:"+policy.ID.String() {
			return fmt.Errorf("DNS policy coordinate does not match id")
		}
		existing, err := h.mutations.GetPolicy(ctx, policy.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("DNS policy %s not found", policy.ID)
		}
		if err := checkIntentRevision(intent, policy.ID.String(), true, existing.UpdatedAt); err != nil {
			return err
		}
		return h.mutations.UpdatePolicy(ctx, &policy)
	case "policy-delete":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var payload struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(content, &payload); err != nil {
			return err
		}
		if payload.ID == uuid.Nil || intent.Coordinate != "dnspolicy:"+payload.ID.String() {
			return fmt.Errorf("DNS policy coordinate does not match id")
		}
		existing, err := h.mutations.GetPolicy(ctx, payload.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("DNS policy %s not found", payload.ID)
		}
		if err := checkIntentRevision(intent, payload.ID.String(), true, existing.UpdatedAt); err != nil {
			return err
		}
		return h.mutations.DeletePolicy(ctx, payload.ID)
	case "endpoint-create", "endpoint-update":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var endpoint domain.DNSEndpoint
		if err := json.Unmarshal(content, &endpoint); err != nil {
			return err
		}
		if err := domain.ValidateDNSEndpoint(&endpoint); err != nil {
			return err
		}
		if intent.Coordinate != endpoint.Coordinate {
			return fmt.Errorf("DNS endpoint coordinate does not match content")
		}
		existing, err := h.mutations.GetEndpoint(ctx, endpoint.Coordinate)
		if err != nil {
			return err
		}
		if intent.Op == "endpoint-update" && existing == nil {
			return fmt.Errorf("DNS endpoint %q not found", endpoint.Coordinate)
		}
		if err := checkIntentRevision(intent, endpoint.Coordinate, existing != nil, func() time.Time {
			if existing == nil {
				return time.Time{}
			}
			return existing.UpdatedAt
		}()); err != nil {
			return err
		}
		return h.mutations.UpsertEndpoint(ctx, &endpoint)
	case "endpoint-delete":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var payload struct {
			Coordinate string `json:"coordinate"`
		}
		if err := json.Unmarshal(content, &payload); err != nil {
			return err
		}
		if payload.Coordinate == "" || intent.Coordinate != payload.Coordinate {
			return fmt.Errorf("DNS endpoint coordinate does not match content")
		}
		existing, err := h.mutations.GetEndpoint(ctx, payload.Coordinate)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("DNS endpoint %q not found", payload.Coordinate)
		}
		if err := checkIntentRevision(intent, payload.Coordinate, true, existing.UpdatedAt); err != nil {
			return err
		}
		return h.mutations.DeleteEndpoint(ctx, payload.Coordinate)
	case "backend-create", "backend-update":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var backend domain.DNSBackendState
		if err := json.Unmarshal(content, &backend); err != nil {
			return err
		}
		if backend.Ref == "" || intent.Coordinate != "dnsbackend:"+strings.TrimSpace(backend.Ref) {
			return fmt.Errorf("DNS backend coordinate does not match ref")
		}
		existing, err := h.mutations.GetBackend(ctx, backend.Ref)
		if err != nil {
			return err
		}
		if intent.Op == "backend-update" && existing == nil {
			return fmt.Errorf("DNS backend %q not found", backend.Ref)
		}
		if err := checkIntentRevision(intent, backend.Ref, existing != nil, func() time.Time {
			if existing == nil {
				return time.Time{}
			}
			return existing.UpdatedAt
		}()); err != nil {
			return err
		}
		return h.mutations.UpsertBackend(ctx, &backend)
	case "backend-delete":
		if h.mutations == nil {
			return fmt.Errorf("DNS mutation service is not configured")
		}
		var payload struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(content, &payload); err != nil {
			return err
		}
		if payload.Ref == "" || intent.Coordinate != "dnsbackend:"+payload.Ref {
			return fmt.Errorf("DNS backend coordinate does not match ref")
		}
		existing, err := h.mutations.GetBackend(ctx, payload.Ref)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("DNS backend %q not found", payload.Ref)
		}
		if err := checkIntentRevision(intent, payload.Ref, true, existing.UpdatedAt); err != nil {
			return err
		}
		return h.mutations.DeleteBackend(ctx, payload.Ref)
	case "zone-create":
		var zone domain.DNSZone
		if err := json.Unmarshal(content, &zone); err != nil {
			return err
		}
		if intent.Coordinate != "zone:"+strings.TrimSpace(zone.Name) {
			return fmt.Errorf("DNS zone coordinate does not match name")
		}
		if h.mutations != nil {
			return h.mutations.CreateZone(ctx, &zone)
		}
		if _, ok := h.operator.(DNSPersistenceOperator); !ok {
			return unsupportedDNSIntent(intent.Op)
		}
		result = dnsZoneCreateOp(ctx, h.operator, content, nil)
		if result.Status != "succeeded" {
			return fmt.Errorf("DNS %s: %s", intent.Op, result.Message)
		}
		if h.canonical == nil {
			return fmt.Errorf("DNS canonical publisher is not configured")
		}
		return h.canonical.PublishZone(ctx, zone)
	case "policy-apply":
		var policy domain.DNSPolicy
		if err := json.Unmarshal(content, &policy); err != nil {
			return err
		}
		if policy.ID == uuid.Nil {
			return fmt.Errorf("DNS policy intent requires a client-minted id")
		}
		if intent.Coordinate != "dnspolicy:"+policy.ID.String() {
			return fmt.Errorf("DNS policy coordinate does not match id")
		}
		if h.mutations != nil {
			return h.mutations.CreatePolicy(ctx, &policy)
		}
		provider, ok := h.operator.(DNSPolicyRepositoryProvider)
		if !ok || provider.DNSPolicyRepository() == nil {
			return unsupportedDNSIntent(intent.Op)
		}
		result = dnsPolicyApplyOp(ctx, h.operator, content)
		if result.Status != "succeeded" {
			return fmt.Errorf("DNS %s: %s", intent.Op, result.Message)
		}
		if h.canonical == nil {
			return fmt.Errorf("DNS canonical publisher is not configured")
		}
		return h.canonical.PublishPolicy(ctx, policy)
	case "record-set":
		if _, ok := h.operator.(DNSPersistenceOperator); !ok {
			return unsupportedDNSIntent(intent.Op)
		}
		var override domain.DNSRecordOverride
		if err := json.Unmarshal(content, &override); err != nil {
			return err
		}
		if override.ID == uuid.Nil || intent.Coordinate != "dns-override:"+override.ID.String() {
			return fmt.Errorf("DNS override intent requires a matching client-minted id and coordinate")
		}
		result = dnsRecordSetOp(ctx, h.operator, content, intent.Actor)
	case "override-retire":
		if _, ok := h.operator.(DNSOverrideRetirementOperator); !ok {
			return unsupportedDNSIntent(intent.Op)
		}
		var payload struct {
			OverrideID string `json:"override_id"`
		}
		if err := json.Unmarshal(content, &payload); err != nil {
			return err
		}
		if intent.Coordinate != "dns-override:"+payload.OverrideID {
			return fmt.Errorf("DNS override coordinate does not match id")
		}
		result = dnsOverrideRetireOp(ctx, h.operator, content, intent.Actor)
	default:
		return unsupportedDNSIntent(intent.Op)
	}
	if result.Status != "succeeded" {
		return fmt.Errorf("DNS %s: %s", intent.Op, result.Message)
	}
	// Override operations call ReconcileZone, whose DNSCanonicalPublisher emits
	// the materialized endpoint state. Publishing it here would double-sign.
	return nil
}

func unsupportedDNSIntent(op string) error {
	return fmt.Errorf("unsupported op: dns %s — no durable mutation path", op)
}

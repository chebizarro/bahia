package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type DNSIntentCanonicalPublisher interface {
	PublishZone(context.Context, domain.DNSZone) error
	PublishPolicy(context.Context, domain.DNSPolicy) error
}

// DNSIntentHandler reconciles only DNS mutations backed by the existing
// ContextVM persistence boundary. Endpoint and backend records are derived
// from infrastructure/configuration and have no durable mutation API.
type DNSIntentHandler struct {
	operator  DNSControlPlaneOperator
	canonical DNSIntentCanonicalPublisher
}

func NewDNSIntentHandler(operator DNSControlPlaneOperator, canonical DNSIntentCanonicalPublisher) *DNSIntentHandler {
	return &DNSIntentHandler{operator: operator, canonical: canonical}
}

func (*DNSIntentHandler) PermissionFor(string) domain.Permission { return domain.PermWriteServices }
func (*DNSIntentHandler) IsFleetScoped() bool                    { return true }

func (h *DNSIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	// Supported DNS operations create or retire resources; none is a revisioned
	// update of an existing canonical record. Do not silently ignore a token.
	if intent.ExpectedUpdatedAt != nil {
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
	case "zone-create":
		if _, ok := h.operator.(DNSPersistenceOperator); !ok {
			return unsupportedDNSIntent(intent.Op)
		}
		var zone domain.DNSZone
		if err := json.Unmarshal(content, &zone); err != nil {
			return err
		}
		if intent.Coordinate != "zone:"+strings.TrimSpace(zone.Name) {
			return fmt.Errorf("DNS zone coordinate does not match name")
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
		provider, ok := h.operator.(DNSPolicyRepositoryProvider)
		if !ok || provider.DNSPolicyRepository() == nil {
			return unsupportedDNSIntent(intent.Op)
		}
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

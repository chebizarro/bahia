package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/readmodel"
)

// VirtualizationIntentHandler admits no provider work until a canonical
// operation outcome and restart-safe executor are available. Registering the
// domain lets the normal signed-intent pipeline reject requests explicitly
// instead of silently ignoring them or falling through to the SQL journal.
type VirtualizationIntentHandler struct{}

func NewVirtualizationIntentHandler() *VirtualizationIntentHandler {
	return &VirtualizationIntentHandler{}
}

func (*VirtualizationIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermWriteDeployments
}

func (*VirtualizationIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateVirtualizationOperationIntent(intent); err != nil {
		return err
	}
	return readmodel.ErrVirtualizationUnavailable
}

type virtualizationOperationRequest struct {
	OperationID        uuid.UUID                         `json:"operation_id"`
	ResourceID         uuid.UUID                         `json:"resource_id"`
	ResourceKind       domain.VirtualizationResourceKind `json:"resource_kind"`
	Action             domain.VMOperationKind            `json:"action"`
	ExpectedGeneration int64                             `json:"expected_generation"`
	IdempotencyKey     string                            `json:"idempotency_key"`
	Reason             string                            `json:"reason"`
}

func validateVirtualizationOperationIntent(intent *Intent) error {
	if intent == nil || intent.Event == nil || !intent.Event.CheckID() || !intent.Event.VerifySignature() {
		return fmt.Errorf("virtualization request requires a signed event")
	}
	parsed, err := ParseIntent(intent.Event)
	if err != nil || parsed.Domain != kinds.VirtualizationDomain || parsed.Op != "request" ||
		parsed.Schema != kinds.VirtualizationIntentSchema ||
		parsed.OrgID == uuid.Nil ||
		intent.Actor != intent.Event.PubKey.Hex() ||
		intent.Domain != parsed.Domain || intent.Op != parsed.Op ||
		intent.Schema != parsed.Schema || intent.OrgID != parsed.OrgID ||
		intent.IntentID != parsed.IntentID || intent.Coordinate != parsed.Coordinate ||
		!virtualizationSameContent(intent.Content, parsed.Content) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "d", parsed.Coordinate) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "domain", kinds.VirtualizationDomain) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "op", "request") ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "schema", kinds.VirtualizationIntentSchema) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "org", intent.OrgID.String()) ||
		!virtualizationTagExactlyOnce(intent.Event.Tags, "intent_id", intent.IntentID) ||
		!virtualizationTopicsValid(intent.Event.Tags) {
		return fmt.Errorf("virtualization request envelope does not match the signed intent")
	}
	intentID, err := uuid.Parse(intent.IntentID)
	if err != nil || intentID.Version() != 7 || len(intent.Event.Content) == 0 || len(intent.Event.Content) > 1<<20 {
		return fmt.Errorf("virtualization request content is invalid")
	}
	var request virtualizationOperationRequest
	decoder := json.NewDecoder(strings.NewReader(intent.Event.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("virtualization request content is invalid: %w", err)
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("virtualization request content has trailing data")
	}
	if request.OperationID == uuid.Nil || request.OperationID.Version() != 7 || request.ResourceID == uuid.Nil ||
		request.OperationID == request.ResourceID || request.ResourceKind != domain.PersistentVMResource ||
		request.ExpectedGeneration < 1 || request.IdempotencyKey != intent.IntentID ||
		strings.TrimSpace(request.Reason) == "" ||
		!virtualizationOperationAction(request.Action) ||
		intent.Coordinate != "vm-operation:"+request.OperationID.String() {
		return fmt.Errorf("virtualization request action or resource binding is invalid")
	}
	return nil
}

func virtualizationOperationAction(action domain.VMOperationKind) bool {
	switch action {
	case domain.VMOperationStart, domain.VMOperationGracefulStop, domain.VMOperationReboot:
		return true
	default:
		return false
	}
}

func virtualizationTagExactlyOnce(tags nostr.Tags, key, value string) bool {
	count := 0
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != key {
			continue
		}
		if tag[1] != value {
			return false
		}
		count++
	}
	return count == 1
}

func virtualizationTopicsValid(tags nostr.Tags) bool {
	intent, domainTopic := 0, 0
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != "t" {
			continue
		}
		switch tag[1] {
		case "bahia-intent":
			intent++
		case kinds.VirtualizationDomain:
			domainTopic++
		default:
			return false
		}
	}
	return intent == 1 && domainTopic == 1
}

func virtualizationSameContent(a, b map[string]any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// SecurityScanIntentHandler submits one scan. Scan status and findings are
// published by SecurityScanner, not by the intent handler.
type SecurityScanIntentHandler struct{ scanner SecurityScannerControlPlane }

func NewSecurityScanIntentHandler(scanner SecurityScannerControlPlane) *SecurityScanIntentHandler {
	return &SecurityScanIntentHandler{scanner: scanner}
}
func (*SecurityScanIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermWriteServices
}
func (*SecurityScanIntentHandler) IsFleetScoped() bool { return true }

func (h *SecurityScanIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent.Op != "scan-run" || intent.Coordinate != "security-scan:"+intent.IntentID {
		return fmt.Errorf("security scan-run requires a security-scan:<intent_id> coordinate")
	}
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("security scan-run does not support expected_updated_at")
	}
	if h.scanner == nil {
		return fmt.Errorf("security scanner is not configured")
	}
	var payload securityScanParams
	if err := decodeIntentContent(intent.Content, &payload); err != nil {
		return err
	}
	if err := validateSecurityTargetInput(payload.Target); err != nil {
		return err
	}
	requestEventID := ""
	if intent.Event != nil {
		requestEventID = intent.Event.ID.Hex()
	}
	ack, err := h.scanner.SubmitScan(ctx, service.SecurityScanRequest{
		Target: payload.Target, Trigger: domain.SecurityTriggerManual, RequestedBy: intent.Actor,
		RequestEventID: requestEventID, RequestDTag: intent.Coordinate, Force: payload.Force,
	})
	if err != nil {
		return err
	}
	if ack == nil {
		return fmt.Errorf("security scanner returned no acceptance")
	}
	intent.Result = map[string]any{"status": ack.Status, "run_id": ack.RunID.String(), "target_key_hash": ack.TargetKeyHash,
		"target_type": ack.TargetType, "duplicate": ack.Duplicate, "skipped": ack.Skipped, "observables": ack.Observables}
	intent.StatusData = intent.Result
	return nil
}

// SBOMIntentHandler enqueues work in the same runner as ContextVM.
// The runner/orchestrator alone owns SBOM canonical publication.
type SBOMIntentHandler struct{ runner sbomRequestRunner }

func NewSBOMIntentHandler(runner sbomRequestRunner) *SBOMIntentHandler {
	return &SBOMIntentHandler{runner: runner}
}
func (*SBOMIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermWriteServices
}
func (*SBOMIntentHandler) IsFleetScoped() bool { return true }

func (h *SBOMIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "generate":
		if intent.Coordinate != "sbom-generate:"+intent.IntentID {
			return fmt.Errorf("sbom generate requires an sbom-generate:<intent_id> coordinate")
		}
	case "import":
		if intent.Coordinate != "sbom-import:"+intent.IntentID {
			return fmt.Errorf("sbom import requires an sbom-import:<intent_id> coordinate")
		}
	default:
		return fmt.Errorf("unsupported SBOM op %q", intent.Op)
	}
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("sbom requests do not support expected_updated_at")
	}
	if h.runner == nil {
		return fmt.Errorf("SBOM runner is not configured")
	}
	if intent.Op == "import" {
		return h.handleImport(ctx, intent)
	}
	var payload service.SBOMGenerateRequest
	if err := decodeIntentContent(intent.Content, &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.IDempotencyKey) == "" {
		payload.IDempotencyKey = intent.IntentID
	} else if payload.IDempotencyKey != intent.IntentID {
		return fmt.Errorf("SBOM idempotencyKey must match intent_id")
	}
	ack, err := h.runner.EnqueueGenerate(ctx, payload)
	if err != nil {
		return err
	}
	intent.Result = map[string]any{"accepted": ack.Accepted, "status": ack.Status, "run_id": ack.RunID,
		"status_d_tag": ack.StatusDTag, "idempotencyKey": ack.IDempotencyKey, "observable_kinds": ack.ObservableKinds}
	intent.StatusData = intent.Result
	return nil
}

func (h *SBOMIntentHandler) handleImport(ctx context.Context, intent *Intent) error {
	var payload sbomImportParams
	if err := decodeIntentContent(intent.Content, &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.IDempotencyKey) == "" {
		payload.IDempotencyKey = intent.IntentID
	} else if payload.IDempotencyKey != intent.IntentID {
		return fmt.Errorf("SBOM idempotencyKey must match intent_id")
	}
	var bytes []byte
	if payload.PayloadBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(payload.PayloadBase64)
		if err != nil {
			return fmt.Errorf("decode sbom/import payloadBase64: %w", err)
		}
		if len(decoded) > maxContextVMInlineSBOMBytes {
			return fmt.Errorf("sbom/import inline payload is %d bytes, over the %d-byte limit; upload the SBOM to Blossom and import it by location", len(decoded), maxContextVMInlineSBOMBytes)
		}
		bytes = decoded
	}
	ack, err := h.runner.EnqueueImport(ctx, service.SBOMImportRequest{
		IDempotencyKey: payload.IDempotencyKey, Subject: payload.Subject, SubjectLocator: payload.SubjectLocator,
		Format: payload.Format, Payload: bytes, Location: payload.Location, Storage: payload.Storage, Generator: payload.Generator,
	})
	if err != nil {
		return err
	}
	intent.Result = map[string]any{"accepted": ack.Accepted, "status": ack.Status, "run_id": ack.RunID,
		"status_d_tag": ack.StatusDTag, "idempotencyKey": ack.IDempotencyKey, "observable_kinds": ack.ObservableKinds}
	intent.StatusData = intent.Result
	return nil
}

func decodeIntentContent(content map[string]interface{}, target any) error {
	if content == nil {
		return fmt.Errorf("intent content is required")
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	return nil
}

// RelayPolicyIntentHandler retains the existing audited publisher and
// projection-precondition checks. The canonical read model is the complete
// relay-settings cp-state payload, not the ContextVM projection response.
type RelayPolicyIntentHandler struct{ settings *RelaySettingsHandlers }

func NewRelayPolicyIntentHandler(settings *RelaySettingsHandlers) *RelayPolicyIntentHandler {
	return &RelayPolicyIntentHandler{settings: settings}
}
func (*RelayPolicyIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermManageSettings
}
func (*RelayPolicyIntentHandler) IsFleetScoped() bool { return true }

func (h *RelayPolicyIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent.Op != "policy-set" || intent.Coordinate != RelaySettingsDTag {
		return fmt.Errorf("relay policy-set requires the %q coordinate", RelaySettingsDTag)
	}
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("relay policy-set uses expected_projection, not expected_updated_at")
	}
	if h.settings == nil || intent.Event == nil {
		return fmt.Errorf("relay policy publisher or signed request is unavailable")
	}
	raw, err := json.Marshal(intent.Content)
	if err != nil {
		return err
	}
	result, err := h.settings.applyPolicyDirect(ctx, ContextVMRequest{
		Event: intent.Event, RPC: ContextVMJSONRPCRequest{Params: raw},
	})
	if err != nil {
		return err
	}
	ack, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("relay policy publisher returned an invalid acceptance")
	}
	intent.Result = ack
	intent.StatusData = map[string]any{"status": "accepted", "replacement_confirmation_recorded": ack["replacement_confirmation_recorded"]}
	return nil
}

type relayPolicyConflictError struct{ reason string }

func (e *relayPolicyConflictError) Error() string { return e.reason }

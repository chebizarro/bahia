package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const (
	ContextVMMethodSecurityScan          = "security/scan"
	ContextVMMethodSecurityRescan        = "security/rescan"
	ContextVMMethodSecurityFindingsList  = "security/findings-list"
	ContextVMMethodSecuritySchedulesList = "security/schedules-list"
)

type SecurityScannerControlPlane interface {
	SubmitScan(ctx context.Context, req service.SecurityScanRequest) (*service.SecurityScanAccepted, error)
	Rescan(ctx context.Context, req service.SecurityRescanRequest) (*service.SecurityScanAccepted, error)
	ListFindings(ctx context.Context, req service.SecurityFindingsListRequest) (*service.SecurityFindingsListResult, error)
	ListSchedules(ctx context.Context, req service.SecuritySchedulesListRequest) (*service.SecuritySchedulesListResult, error)
}

type securityContextVMHandler struct {
	scanner SecurityScannerControlPlane
}

func RegisterSecurityContextVMHandlers(transport *EncryptedRequestTransport, scanner SecurityScannerControlPlane) {
	if transport == nil || scanner == nil {
		return
	}
	h := securityContextVMHandler{scanner: scanner}
	transport.RegisterContextVMHandler(ContextVMMethodSecurityScan, h.scan)
	transport.RegisterContextVMHandler(ContextVMMethodSecurityRescan, h.rescan)
	transport.RegisterContextVMHandler(ContextVMMethodSecurityFindingsList, h.findingsList)
	transport.RegisterContextVMHandler(ContextVMMethodSecuritySchedulesList, h.schedulesList)
}

type securityScanParams struct {
	Target service.SecurityScanTargetInput `json:"target"`
	Force  bool                            `json:"force,omitempty"`
}

func (h securityContextVMHandler) scan(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload securityScanParams
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode security/scan params: %w", err)
	}
	if err := validateSecurityTargetInput(payload.Target); err != nil {
		return nil, err
	}
	return h.scanner.SubmitScan(ctx, service.SecurityScanRequest{Target: payload.Target, Trigger: domain.SecurityTriggerManual, RequestedBy: req.Event.PubKey.Hex(), RequestEventID: eventIDHex(req.OuterEvent, req.Event), RequestDTag: requestDTag(req), Force: payload.Force})
}

type securityRescanParams struct {
	TargetKeyHash string `json:"target_key_hash"`
}

func (h securityContextVMHandler) rescan(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload securityRescanParams
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode security/rescan params: %w", err)
	}
	payload.TargetKeyHash = strings.TrimSpace(payload.TargetKeyHash)
	if payload.TargetKeyHash == "" {
		return nil, fmt.Errorf("security/rescan target_key_hash is required")
	}
	return h.scanner.Rescan(ctx, service.SecurityRescanRequest{TargetKeyHash: payload.TargetKeyHash, RequestedBy: req.Event.PubKey.Hex(), RequestEventID: eventIDHex(req.OuterEvent, req.Event), RequestDTag: requestDTag(req)})
}

type securityFindingsListParams struct {
	RunID         string                  `json:"run_id,omitempty"`
	TargetKeyHash string                  `json:"target_key_hash,omitempty"`
	Severity      domain.SecuritySeverity `json:"severity,omitempty"`
	OSVID         string                  `json:"osv_id,omitempty"`
	Limit         int                     `json:"limit,omitempty"`
	Offset        int                     `json:"offset,omitempty"`
}

func (h securityContextVMHandler) findingsList(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload securityFindingsListParams
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode security/findings-list params: %w", err)
	}
	payload.TargetKeyHash = strings.TrimSpace(payload.TargetKeyHash)
	payload.OSVID = strings.TrimSpace(payload.OSVID)
	var runID *uuid.UUID
	if strings.TrimSpace(payload.RunID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(payload.RunID))
		if err != nil {
			return nil, fmt.Errorf("security/findings-list run_id is invalid: %w", err)
		}
		runID = &parsed
	}
	return h.scanner.ListFindings(ctx, service.SecurityFindingsListRequest{RunID: runID, TargetKeyHash: payload.TargetKeyHash, Severity: payload.Severity, OSVID: payload.OSVID, Limit: payload.Limit, Offset: payload.Offset})
}

type securitySchedulesListParams struct {
	PolicyID      string `json:"policy_id,omitempty"`
	TargetKeyHash string `json:"target_key_hash,omitempty"`
	EnabledOnly   bool   `json:"enabled_only,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
}

func (h securityContextVMHandler) schedulesList(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload securitySchedulesListParams
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode security/schedules-list params: %w", err)
	}
	var policyID *uuid.UUID
	if strings.TrimSpace(payload.PolicyID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(payload.PolicyID))
		if err != nil {
			return nil, fmt.Errorf("security/schedules-list policy_id is invalid: %w", err)
		}
		policyID = &parsed
	}
	return h.scanner.ListSchedules(ctx, service.SecuritySchedulesListRequest{PolicyID: policyID, TargetKeyHash: strings.TrimSpace(payload.TargetKeyHash), EnabledOnly: payload.EnabledOnly, Limit: payload.Limit, Offset: payload.Offset})
}

func validateSecurityTargetInput(input service.SecurityScanTargetInput) error {
	switch input.Type {
	case domain.SecurityTargetSBOM:
		if input.SBOM == nil {
			return fmt.Errorf("security/scan sbom target is required")
		}
		if input.SBOM.Subject.Type == "" || (strings.TrimSpace(input.SBOM.Subject.Digest) == "" && strings.TrimSpace(input.SBOM.Subject.ID) == "") {
			return fmt.Errorf("security/scan sbom subject type and digest or id are required")
		}
		if input.SBOM.Format == "" || input.SBOM.Storage == "" || strings.TrimSpace(input.SBOM.LocationURI) == "" || strings.TrimSpace(input.SBOM.PayloadSHA256) == "" || strings.TrimSpace(input.SBOM.ReferenceDTag) == "" {
			return fmt.Errorf("security/scan sbom format, storage, location_uri, payload_sha256, and reference_d_tag are required")
		}
	case domain.SecurityTargetPackage:
		if input.Package == nil || strings.TrimSpace(input.Package.Ecosystem) == "" || strings.TrimSpace(input.Package.Name) == "" {
			return fmt.Errorf("security/scan package ecosystem and name are required")
		}
	case domain.SecurityTargetPURL:
		if strings.TrimSpace(input.PURL) == "" {
			return fmt.Errorf("security/scan purl is required")
		}
	case domain.SecurityTargetCommit:
		if input.Commit == nil || strings.TrimSpace(input.Commit.CommitHash) == "" {
			return fmt.Errorf("security/scan commit hash is required")
		}
	default:
		return fmt.Errorf("security/scan target type must be sbom, package, purl, or commit")
	}
	return nil
}

func eventIDHex(outer, inner *nostr.Event) string {
	if outer != nil {
		return outer.ID.Hex()
	}
	if inner != nil {
		return inner.ID.Hex()
	}
	return ""
}

func requestDTag(req ContextVMRequest) string {
	if req.Event == nil {
		return ""
	}
	for _, tag := range req.Event.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			return tag[1]
		}
	}
	return ""
}

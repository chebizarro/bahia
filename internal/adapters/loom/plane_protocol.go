package loom

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// PlaneSchema identifies the versioned administrative contract, not Loom jobs.
// Discovery is a 11316 announcement at d=<endpoint UUID> with ExecutionPlaneSupport
// content. State is 30900 at d=loom:execution_plane:<plane UUID> with a complete
// ExecutionPlaneObservation. Both require domain, schema, host and endpoint tags.
// Inspect/probe observations additionally carry e=<request event ID>. A 25910
// JSON-RPC response acknowledges admission only, never installed state or probes.
const PlaneSchema = "cascadia.loom.execution-plane.v1"
const PlaneDomain = "loom"

func PlaneStateCoordinate(id uuid.UUID) string { return "loom:execution_plane:" + id.String() }

func planeError(code domain.VMErrorCode, cause error) error {
	return &domain.VMProviderError{Code: code, Retryable: code == domain.VMErrorUnavailable, Cause: cause}
}

func planeEndpointValid(e domain.ExecutionPlaneEndpoint) bool {
	_, err := nostr.PubKeyFromHex(e.Author)
	return err == nil && e.HostID != uuid.Nil && e.EndpointRef != uuid.Nil
}

func planeClassesValid(classes []domain.VMLifecycleClass) bool {
	if len(classes) < 1 || len(classes) > 2 {
		return false
	}
	seen := map[domain.VMLifecycleClass]bool{}
	for _, class := range classes {
		if seen[class] || (class != domain.VMLifecycleLoomFirecracker && class != domain.VMLifecycleLoomQEMU) {
			return false
		}
		seen[class] = true
	}
	return true
}

// Single-valued routing tags reject duplicate/ambiguous identities.
func planeTag(ev *nostr.Event, key, value string) bool {
	count := 0
	for _, tag := range ev.Tags {
		if len(tag) > 0 && tag[0] == key {
			if len(tag) != 2 || tag[1] != value {
				return false
			}
			count++
		}
	}
	return count == 1
}

func validatePlaneEvent(ev *nostr.Event, endpoint domain.ExecutionPlaneEndpoint, now time.Time) error {
	if !planeEndpointValid(endpoint) {
		return planeError(domain.VMErrorInvalid, nil)
	}
	if err := nostrAdapter.ValidateInboundEvent(ev, now, nostrAdapter.InboundEventMaxFutureSkew); err != nil {
		return planeError(domain.VMErrorIntegrity, err)
	}
	if ev.PubKey.Hex() != endpoint.Author || !planeTag(ev, "domain", PlaneDomain) || !planeTag(ev, "schema", PlaneSchema) || !planeTag(ev, "host", endpoint.HostID.String()) || !planeTag(ev, "endpoint", endpoint.EndpointRef.String()) {
		return planeError(domain.VMErrorIntegrity, fmt.Errorf("plane event routing mismatch"))
	}
	return nil
}

func decodePlaneSupport(ev *nostr.Event, endpoint domain.ExecutionPlaneEndpoint, now time.Time) (domain.ExecutionPlaneSupport, error) {
	var support domain.ExecutionPlaneSupport
	if err := validatePlaneEvent(ev, endpoint, now); err != nil {
		return support, err
	}
	if ev.Kind != nostr.Kind(kinds.ContextVMServerAnnouncement) || !planeTag(ev, "d", endpoint.EndpointRef.String()) {
		return support, planeError(domain.VMErrorIntegrity, nil)
	}
	if err := domain.DecodeVirtualizationDocument([]byte(ev.Content), &support); err != nil {
		return support, planeError(domain.VMErrorIntegrity, err)
	}
	if support.ProtocolVersion != domain.ExecutionPlaneProtocolVersion || !planeClassesValid(support.LifecycleClasses) {
		return support, planeError(domain.VMErrorUnsupported, nil)
	}
	for _, tool := range []string{domain.ExecutionPlaneInspectTool, domain.ExecutionPlaneApplyTool, domain.ExecutionPlaneProbeTool} {
		if !slices.Contains(support.Tools, tool) {
			return support, planeError(domain.VMErrorUnsupported, nil)
		}
	}
	return support, nil
}

// DecodePlaneObservation validates the signed transport identity as well as every
// governed observation field. In particular a Windows claim is an integrity
// failure, not a capability to silently copy into the worker catalog.
func DecodePlaneObservation(ev *nostr.Event, endpoint domain.ExecutionPlaneEndpoint, planeID uuid.UUID, now time.Time) (*domain.ExecutionPlaneObservation, error) {
	if err := validatePlaneEvent(ev, endpoint, now); err != nil {
		return nil, err
	}
	if planeID == uuid.Nil || ev.Kind != nostr.Kind(kinds.CASControlState) || !planeTag(ev, "d", PlaneStateCoordinate(planeID)) {
		return nil, planeError(domain.VMErrorIntegrity, nil)
	}
	var observation domain.ExecutionPlaneObservation
	if err := domain.DecodeVirtualizationDocument([]byte(ev.Content), &observation); err != nil {
		return nil, planeError(domain.VMErrorIntegrity, err)
	}
	if !planeClassesValid(observation.LifecycleClasses) || observation.ObservedAt.After(now) {
		return nil, planeError(domain.VMErrorIntegrity, nil)
	}
	identity := &domain.ExecutionPlaneDeployment{
		VirtualizationResourceMeta: domain.VirtualizationResourceMeta{ID: planeID, Generation: observation.ObservedGeneration},
		HostID:                     endpoint.HostID, ManagementAuthor: endpoint.Author,
		Desired: domain.ExecutionPlaneDesired{LifecycleClasses: observation.LifecycleClasses},
	}
	if err := domain.ValidateExecutionPlaneObservation(identity, &observation); err != nil {
		return nil, planeError(domain.VMErrorIntegrity, err)
	}
	return &observation, nil
}

type planeRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

func decodePlaneAck(ev *nostr.Event, endpoint domain.ExecutionPlaneEndpoint, request *nostr.Event, operation uuid.UUID, now time.Time) (*domain.ExecutionPlaneAcknowledgment, error) {
	if err := validatePlaneEvent(ev, endpoint, now); err != nil {
		return nil, err
	}
	if ev.Kind != nostr.Kind(kinds.ContextVMMessage) || !planeTag(ev, "p", request.PubKey.Hex()) || !planeTag(ev, "e", request.ID.Hex()) || ev.CreatedAt < request.CreatedAt {
		return nil, planeError(domain.VMErrorIntegrity, nil)
	}
	var response planeRPCResponse
	if err := domain.DecodeVirtualizationDocument([]byte(ev.Content), &response); err != nil {
		return nil, planeError(domain.VMErrorIntegrity, err)
	}
	var id string
	if json.Unmarshal(response.ID, &id) != nil || id != operation.String() || response.JSONRPC != "2.0" {
		return nil, planeError(domain.VMErrorIntegrity, nil)
	}
	if response.Error != nil {
		if len(response.Result) != 0 {
			return nil, planeError(domain.VMErrorIntegrity, nil)
		}
		return nil, planeError(domain.VMErrorUnavailable, nil)
	}
	var ack domain.ExecutionPlaneAcknowledgment
	if err := domain.DecodeVirtualizationDocument(response.Result, &ack); err != nil {
		return nil, planeError(domain.VMErrorIntegrity, err)
	}
	if ack.OperationID != operation || !ack.Accepted || ack.Diagnostic.Code != "" || ack.Diagnostic.EvidenceDigest != "" {
		return nil, planeError(domain.VMErrorUnavailable, nil)
	}
	return &ack, nil
}

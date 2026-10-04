package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const (
	ContextVMMethodSBOMGenerate = "sbom/generate"
	ContextVMMethodSBOMImport   = "sbom/import"
)

// maxContextVMInlineSBOMBytes bounds a decoded inline SBOM. The request is one
// Nostr message: base64 inflates the document by 4/3, and Bahia's relay closes
// a websocket frame over 512,000 bytes (NIP-11 max_message_length) without an
// OK. 360 KiB encodes to 491,520 bytes and leaves room for the envelope.
// Larger SBOMs are uploaded to Blossom and imported by location. The web
// client enforces the same limit (MAX_CONTEXTVM_INLINE_SBOM_BYTES).
const maxContextVMInlineSBOMBytes = 360 * 1024

type sbomRequestRunner interface {
	EnqueueGenerate(context.Context, service.SBOMGenerateRequest) (service.SBOMAcceptedAck, error)
	EnqueueImport(context.Context, service.SBOMImportRequest) (service.SBOMAcceptedAck, error)
}

type sbomContextVMHandler struct {
	runner    sbomRequestRunner
	processor *IntentProcessor
}

func RegisterSBOMContextVMHandlers(transport *EncryptedRequestTransport, runner sbomRequestRunner, gate *FleetOperatorGate, processors ...*IntentProcessor) {
	if transport == nil || runner == nil {
		return
	}
	h := sbomContextVMHandler{runner: runner}
	if len(processors) > 0 {
		h.processor = processors[0]
	}
	transport.RegisterContextVMHandler(ContextVMMethodSBOMGenerate, gate.wrap(h.generate))
	transport.RegisterContextVMHandler(ContextVMMethodSBOMImport, gate.wrap(h.importSBOM))
}

func (h sbomContextVMHandler) generate(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload service.SBOMGenerateRequest
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode sbom/generate params: %w", err)
	}
	if h.processor != nil && h.processor.Handler("sbom") != nil {
		if req.Event == nil {
			return nil, fmt.Errorf("sbom generate requires an authenticated requester")
		}
		var content map[string]interface{}
		if err := json.Unmarshal(req.RPC.Params, &content); err != nil {
			return nil, err
		}
		id := intentIDFromContextVM(req, payload.IDempotencyKey)
		if id == "" {
			id = req.Event.ID.Hex()
		}
		intent := &Intent{Event: req.Event, Domain: "sbom", Op: "generate", Coordinate: "sbom-generate:" + id,
			IntentID: id, Content: content, Actor: req.Event.PubKey.Hex()}
		if err := h.processor.ProcessInProcess(ctx, intent); err != nil {
			return nil, err
		}
		return intent.Result, nil
	}
	return h.runner.EnqueueGenerate(ctx, payload)
}

type sbomImportParams struct {
	IDempotencyKey string                    `json:"idempotencyKey"`
	Subject        domain.SBOMSubject        `json:"subject"`
	SubjectLocator domain.SBOMSubjectLocator `json:"subjectLocator,omitempty"`
	Format         domain.SBOMFormat         `json:"format,omitempty"`
	PayloadBase64  string                    `json:"payloadBase64,omitempty"`
	Location       *domain.SBOMLocation      `json:"location,omitempty"`
	Storage        domain.SBOMStorageType    `json:"storage"`
	Generator      domain.SBOMGenerator      `json:"generator,omitempty"`
}

func (h sbomContextVMHandler) importSBOM(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload sbomImportParams
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode sbom/import params: %w", err)
	}
	var bytes []byte
	if payload.PayloadBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(payload.PayloadBase64)
		if err != nil {
			return nil, fmt.Errorf("decode sbom/import payloadBase64: %w", err)
		}
		if len(decoded) > maxContextVMInlineSBOMBytes {
			return nil, fmt.Errorf("sbom/import inline payload is %d bytes, over the %d-byte ContextVM limit; upload the SBOM to Blossom and import it by location", len(decoded), maxContextVMInlineSBOMBytes)
		}
		bytes = decoded
	}
	if h.processor != nil && h.processor.Handler("sbom") != nil {
		if req.Event == nil {
			return nil, fmt.Errorf("sbom import requires an authenticated requester")
		}
		var content map[string]interface{}
		if err := json.Unmarshal(req.RPC.Params, &content); err != nil {
			return nil, err
		}
		id := intentIDFromContextVM(req, payload.IDempotencyKey)
		if id == "" {
			id = req.Event.ID.Hex()
		}
		intent := &Intent{Event: req.Event, Domain: "sbom", Op: "import", Coordinate: "sbom-import:" + id,
			IntentID: id, Content: content, Actor: req.Event.PubKey.Hex()}
		if err := h.processor.ProcessInProcess(ctx, intent); err != nil {
			return nil, err
		}
		return intent.Result, nil
	}
	return h.runner.EnqueueImport(ctx, service.SBOMImportRequest{IDempotencyKey: payload.IDempotencyKey, Subject: payload.Subject, SubjectLocator: payload.SubjectLocator, Format: payload.Format, Payload: bytes, Location: payload.Location, Storage: payload.Storage, Generator: payload.Generator})
}

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
	maxContextVMInlineSBOMBytes = 512 * 1024
)

type sbomRequestRunner interface {
	EnqueueGenerate(context.Context, service.SBOMGenerateRequest) (service.SBOMAcceptedAck, error)
	EnqueueImport(context.Context, service.SBOMImportRequest) (service.SBOMAcceptedAck, error)
}

type sbomContextVMHandler struct {
	runner sbomRequestRunner
}

func RegisterSBOMContextVMHandlers(transport *EncryptedRequestTransport, runner sbomRequestRunner) {
	if transport == nil || runner == nil {
		return
	}
	h := sbomContextVMHandler{runner: runner}
	transport.RegisterContextVMHandler(ContextVMMethodSBOMGenerate, h.generate)
	transport.RegisterContextVMHandler(ContextVMMethodSBOMImport, h.importSBOM)
}

func (h sbomContextVMHandler) generate(ctx context.Context, req ContextVMRequest) (any, error) {
	var payload service.SBOMGenerateRequest
	if err := json.Unmarshal(req.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("decode sbom/generate params: %w", err)
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
			return nil, fmt.Errorf("sbom/import inline payload exceeds %d byte ContextVM limit; use a Blossom or REST compatibility import reference", maxContextVMInlineSBOMBytes)
		}
		bytes = decoded
	}
	return h.runner.EnqueueImport(ctx, service.SBOMImportRequest{IDempotencyKey: payload.IDempotencyKey, Subject: payload.Subject, SubjectLocator: payload.SubjectLocator, Format: payload.Format, Payload: bytes, Location: payload.Location, Storage: payload.Storage, Generator: payload.Generator})
}

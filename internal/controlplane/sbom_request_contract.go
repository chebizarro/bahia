package controlplane

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const maxContextVMInlineSBOMBytes = 360 * 1024

type sbomRequestRunner interface {
	EnqueueGenerate(context.Context, service.SBOMGenerateRequest) (service.SBOMAcceptedAck, error)
	EnqueueImport(context.Context, service.SBOMImportRequest) (service.SBOMAcceptedAck, error)
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

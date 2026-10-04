package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type AdoptionOperatorService interface {
	Scan(context.Context, service.AdoptionScanRequest) ([]service.AdoptionPreview, error)
	Import(context.Context, service.AdoptionImportRequest) ([]service.AdoptionImportResult, error)
}

// RuntimeLifecycleOperatorService is the narrow service surface required by the
// signer-first direct-runtime control-plane transport.
type RuntimeLifecycleOperatorService interface {
	BuildDesiredStateSnapshot(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID) (*domain.DesiredServiceSpec, error)
	DeployDesiredStateSnapshot(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, *domain.DesiredServiceSpec, service.DeployStatusCallback) (*domain.RuntimeObservation, error)
	Deploy(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (*domain.RuntimeObservation, error)
	DeployWithStatus(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, service.DeployStatusCallback) (*domain.RuntimeObservation, error)
	Restart(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
	Stop(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error)
}

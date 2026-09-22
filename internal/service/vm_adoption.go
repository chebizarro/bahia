package service

import (
	"context"
	"reflect"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func (s *PersistentVMService) measureAdoption(ctx context.Context, h domain.VirtualizationHost, v domain.PersistentVMDeployment, image domain.VMImage) (*domain.VMAdoptionMeasurement, error) {
	provider, ok := s.cfg.Provider.(domain.VMAdoptionProvider)
	if !ok {
		return nil, vmError(domain.VMErrorUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(h.OperationLimits.InspectSeconds)*time.Second)
	defer cancel()
	m, err := provider.MeasureAdoption(ctx, domain.VMChangeRequest{Host: h, Current: v, Desired: v, Image: image})
	if err != nil {
		return nil, err
	}
	if domain.ValidateVMAdoptionMeasurement(m) != nil || !reflect.DeepEqual(m.Identity, v.Identity) || m.Generation != v.Generation || m.ImageID != image.ID || m.ImageDigest != image.ManifestDigest || m.StoragePoolRef != v.StoragePoolRef || m.ConfigDigest != domain.VMAdoptionConfigDigest(v, m.ProviderFingerprint) {
		return nil, vmError(domain.VMErrorIntegrity)
	}
	return m, nil
}

func adoptionDigest(op domain.VMOperation) string {
	if op.Adoption == nil {
		return ""
	}
	return op.Adoption.Digest
}

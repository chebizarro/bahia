package hiveci

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type OCIReleaseObjectResolver struct {
	registry *service.OCIRegistryService
}

func NewOCIReleaseObjectResolver(registry *service.OCIRegistryService) *OCIReleaseObjectResolver {
	return &OCIReleaseObjectResolver{registry: registry}
}

func (r *OCIReleaseObjectResolver) ResolveReleaseObject(ctx context.Context, descriptor domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error) {
	object, err := r.registry.ResolveObjectByDigest(ctx, descriptor.Repository, descriptor.Digest, descriptor.Size)
	if err != nil {
		return ResolvedReleaseArtifact{}, err
	}
	return ResolvedReleaseArtifact{Content: object.Content, MediaType: object.MediaType, Size: object.Size}, nil
}

var _ ReleaseObjectResolver = (*OCIReleaseObjectResolver)(nil)

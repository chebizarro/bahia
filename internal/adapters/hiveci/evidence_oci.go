package hiveci

import (
	"context"
	"fmt"
	"strings"

	registryadapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type OCIReleaseObjectResolver struct {
	registry *service.OCIRegistryService
	external registryadapter.DigestObjectResolver
}

func NewOCIReleaseObjectResolver(
	registry *service.OCIRegistryService,
	inspectors ...registryadapter.ImageInspector,
) *OCIReleaseObjectResolver {
	resolver := &OCIReleaseObjectResolver{registry: registry}
	if len(inspectors) > 0 {
		resolver.external, _ = inspectors[0].(registryadapter.DigestObjectResolver)
	}
	return resolver
}

func (r *OCIReleaseObjectResolver) ResolveReleaseObject(ctx context.Context, descriptor domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error) {
	if isFullyQualifiedOCIRepository(descriptor.Repository) {
		if r == nil || r.external == nil {
			return ResolvedReleaseArtifact{}, fmt.Errorf(
				"fully-qualified release repository requires a configured registry byte-by-digest resolver",
			)
		}
		object, err := r.external.ResolveObjectByDigest(
			ctx, descriptor.Repository, descriptor.Digest, descriptor.MediaType, descriptor.Size,
		)
		if err != nil {
			return ResolvedReleaseArtifact{}, err
		}
		return ResolvedReleaseArtifact{Content: object.Content, MediaType: object.MediaType, Size: object.Size}, nil
	}
	if r == nil || r.registry == nil {
		return ResolvedReleaseArtifact{}, fmt.Errorf("local OCI release evidence resolver is not configured")
	}
	object, err := r.registry.ResolveObjectByDigest(ctx, descriptor.Repository, descriptor.Digest, descriptor.Size)
	if err != nil {
		return ResolvedReleaseArtifact{}, err
	}
	return ResolvedReleaseArtifact{Content: object.Content, MediaType: object.MediaType, Size: object.Size}, nil
}

func isFullyQualifiedOCIRepository(repository string) bool {
	first, _, found := strings.Cut(strings.TrimSpace(repository), "/")
	return found && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost")
}

var _ ReleaseObjectResolver = (*OCIReleaseObjectResolver)(nil)

package nostr

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// AdoptionCanonicalPublisher publishes canonical service-registry and
// environment-registry cp-state records after adoption imports. It replaces
// the projector's reactive EventAdoptionImported handler (publishServiceByID,
// publishEnvironmentByID) with direct publication from the mutation site
// (Phase 3 X1, bahia-irsry.11.17).
type AdoptionCanonicalPublisher struct {
	projector *Projector
	logger    *zap.Logger
}

// NewAdoptionCanonicalPublisher creates a publisher that delegates to the
// projector's shared signing, dedupe, and outbox pipeline.
func NewAdoptionCanonicalPublisher(projector *Projector, logger *zap.Logger) *AdoptionCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AdoptionCanonicalPublisher{
		projector: projector,
		logger:    logger.Named("adoption-canonical"),
	}
}

// PublishServiceRegistry publishes the canonical service-registry cp-state
// record for an adopted service. The projector's dedupe pipeline ensures
// unchanged records are not re-signed or re-queued.
func (p *AdoptionCanonicalPublisher) PublishServiceRegistry(ctx context.Context, svc *domain.Service) error {
	if p.projector == nil || !p.projector.Enabled() || svc == nil {
		return nil
	}
	return p.projector.publishServiceRegistry(ctx, svc, false)
}

// PublishEnvironmentRegistry publishes the canonical environment-registry
// cp-state record for an adopted environment.
func (p *AdoptionCanonicalPublisher) PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment) error {
	if p.projector == nil || !p.projector.Enabled() || env == nil {
		return nil
	}
	return p.projector.publishEnvironmentRegistry(ctx, env, false)
}

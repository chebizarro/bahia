package factory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// deferredBackend is a backend whose configuration names secrets. Its
// credentials are resolved through the secret store, as one batch, on the
// backend's first use rather than at startup, so the daemon starts while the
// store is unavailable and the backend fails closed until they resolve. The
// built backend (and the credentials in it) is held only until the store's
// delivery lease ends; the next use resolves and builds again, which picks
// up a re-pointed secret and fails closed on a revoked, expired or deleted
// one. A failed rebuild drops the held backend. Type and Capabilities depend
// only on non-secret configuration and come from the same configuration
// built without credentials, which never makes a request.
type deferredBackend struct {
	shape    packagebackend.Backend
	cfg      config.PackageBackendConfig
	resolver SecretResolver
	now      func() time.Time

	mu         sync.Mutex
	backend    packagebackend.Backend
	validUntil time.Time
}

func newDeferredBackend(ctx context.Context, cfg config.PackageBackendConfig, resolver SecretResolver) (*deferredBackend, error) {
	if resolver == nil {
		return nil, fmt.Errorf("package backend secret refs require a secrets resolver")
	}
	if strings.TrimSpace(cfg.AuthSecretRef) != cfg.AuthSecretRef && strings.TrimSpace(cfg.AuthSecretRef) == "" ||
		strings.TrimSpace(cfg.TLSSecretRef) != cfg.TLSSecretRef && strings.TrimSpace(cfg.TLSSecretRef) == "" {
		return nil, fmt.Errorf("package backend secret references must not be blank")
	}
	for key, ref := range cfg.SecretRefs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("secret_refs require nonempty keys and references")
		}
	}
	shapeCfg := cfg
	shapeCfg.AuthSecretRef, shapeCfg.TLSSecretRef, shapeCfg.SecretRefs = "", "", nil
	shape, err := BuildBackendWithSecrets(ctx, shapeCfg, nil)
	if err != nil {
		return nil, err
	}
	return &deferredBackend{shape: shape, cfg: cfg, resolver: resolver, now: time.Now}, nil
}

func hasSecretRefs(cfg config.PackageBackendConfig) bool {
	return cfg.AuthSecretRef != "" || cfg.TLSSecretRef != "" || len(cfg.SecretRefs) > 0
}

func (d *deferredBackend) get(ctx context.Context) (packagebackend.Backend, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.backend != nil && d.now().Before(d.validUntil) {
		return d.backend, nil
	}
	backend, validUntil, err := buildBackend(ctx, d.cfg, d.resolver)
	if err != nil {
		d.backend, d.validUntil = nil, time.Time{}
		return nil, fmt.Errorf("package backend %s credentials: %w", strings.TrimSpace(d.cfg.Type), err)
	}
	d.backend, d.validUntil = backend, validUntil
	return backend, nil
}

func (d *deferredBackend) Type() domain.PackageBackendType { return d.shape.Type() }

func (d *deferredBackend) Capabilities() packagebackend.Capabilities { return d.shape.Capabilities() }

func (d *deferredBackend) EnsureRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return b.EnsureRepository(ctx, repo)
}

func (d *deferredBackend) DeleteRepository(ctx context.Context, repo domain.PackageRepository, force bool) (packagebackend.RepositoryObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return b.DeleteRepository(ctx, repo, force)
}

func (d *deferredBackend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return b.ObserveRepository(ctx, repo)
}

func (d *deferredBackend) StoreArtifact(ctx context.Context, repo domain.PackageRepository, req packagebackend.StoreArtifactRequest) (packagebackend.ArtifactObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return b.StoreArtifact(ctx, repo, req)
}

func (d *deferredBackend) GetArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactStream, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.ArtifactStream{}, err
	}
	return b.GetArtifact(ctx, repo, artifact)
}

func (d *deferredBackend) ListArtifacts(ctx context.Context, repo domain.PackageRepository) ([]packagebackend.ArtifactObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return nil, err
	}
	return b.ListArtifacts(ctx, repo)
}

func (d *deferredBackend) PromoteArtifact(ctx context.Context, sourceRepo, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req packagebackend.PromoteArtifactRequest) (packagebackend.ArtifactObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return b.PromoteArtifact(ctx, sourceRepo, targetRepo, artifact, req)
}

func (d *deferredBackend) YankArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact, reason string) (packagebackend.ArtifactObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return b.YankArtifact(ctx, repo, artifact, reason)
}

func (d *deferredBackend) ObserveArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactObservation, error) {
	b, err := d.get(ctx)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return b.ObserveArtifact(ctx, repo, artifact)
}

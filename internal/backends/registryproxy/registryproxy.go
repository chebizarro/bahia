package registryproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Config describes an externally served package registry that Bahia controls by
// declaration and health/drift observation rather than by writing protocol data.
type Config struct {
	Type          domain.PackageBackendType
	BaseURL       string
	PublicBaseURL string
	HTTPClient    *http.Client
}

// Backend represents protocol-native registries such as Athens and Verdaccio.
type Backend struct {
	typ           domain.PackageBackendType
	format        domain.PackageRepositoryFormat
	baseURL       string
	publicBaseURL string
	httpClient    *http.Client
}

func New(cfg Config) (*Backend, error) {
	baseURL, err := packagebackend.ValidateEndpoint(cfg.BaseURL, string(cfg.Type)+" base_url")
	if err != nil {
		return nil, err
	}
	publicBaseURL := strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if publicBaseURL == "" {
		publicBaseURL = baseURL
	} else if publicBaseURL, err = packagebackend.ValidateEndpoint(publicBaseURL, string(cfg.Type)+" public_base_url"); err != nil {
		return nil, err
	}
	format, err := formatForType(cfg.Type)
	if err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Backend{typ: cfg.Type, format: format, baseURL: baseURL, publicBaseURL: publicBaseURL, httpClient: client}, nil
}

func formatForType(typ domain.PackageBackendType) (domain.PackageRepositoryFormat, error) {
	switch typ {
	case domain.PackageBackendAthens:
		return domain.PackageRepositoryFormatGoModules, nil
	case domain.PackageBackendVerdaccio:
		return domain.PackageRepositoryFormatNPM, nil
	default:
		return "", fmt.Errorf("unsupported registry proxy backend type %q", typ)
	}
}

func (b *Backend) Type() domain.PackageBackendType { return b.typ }

func (b *Backend) Capabilities() packagebackend.Capabilities {
	return packagebackend.Capabilities{
		Formats:             []domain.PackageRepositoryFormat{b.format},
		CanCreateRepository: true,
		CanDeleteRepository: true,
		CanObserveDrift:     true,
	}
}

func (b *Backend) EnsureRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	if err := b.validateRepo(repo); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if err := b.checkHealth(ctx); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"mode": "protocol_native"}}, nil
}

func (b *Backend) DeleteRepository(ctx context.Context, repo domain.PackageRepository, _ bool) (packagebackend.RepositoryObservation, error) {
	if err := b.validateRepo(repo); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if err := b.checkHealth(ctx); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"mode": "protocol_native", "deleted": "projection_only"}}, nil
}

func (b *Backend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	if err := b.validateRepo(repo); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if err := b.checkHealth(ctx); err != nil {
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"mode": "protocol_native"}}, nil
	}
	return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"mode": "protocol_native"}}, nil
}

func (b *Backend) StoreArtifact(context.Context, domain.PackageRepository, packagebackend.StoreArtifactRequest) (packagebackend.ArtifactObservation, error) {
	return packagebackend.ArtifactObservation{}, b.unsupported("store artifact")
}

func (b *Backend) GetArtifact(context.Context, domain.PackageRepository, domain.PackageArtifact) (packagebackend.ArtifactStream, error) {
	return packagebackend.ArtifactStream{}, b.unsupported("get artifact")
}

func (b *Backend) ListArtifacts(context.Context, domain.PackageRepository) ([]packagebackend.ArtifactObservation, error) {
	return nil, b.unsupported("list artifacts")
}

func (b *Backend) PromoteArtifact(context.Context, domain.PackageRepository, domain.PackageRepository, domain.PackageArtifact, packagebackend.PromoteArtifactRequest) (packagebackend.ArtifactObservation, error) {
	return packagebackend.ArtifactObservation{}, b.unsupported("promote artifact")
}

func (b *Backend) YankArtifact(context.Context, domain.PackageRepository, domain.PackageArtifact, string) (packagebackend.ArtifactObservation, error) {
	return packagebackend.ArtifactObservation{}, b.unsupported("yank artifact")
}

func (b *Backend) ObserveArtifact(context.Context, domain.PackageRepository, domain.PackageArtifact) (packagebackend.ArtifactObservation, error) {
	return packagebackend.ArtifactObservation{}, b.unsupported("observe artifact")
}

func (b *Backend) validateRepo(repo domain.PackageRepository) error {
	if repo.Format != b.format {
		return fmt.Errorf("%s backend supports %q repositories, got %q", b.typ, b.format, repo.Format)
	}
	return nil
}

func (b *Backend) repositoryURL(repo domain.PackageRepository) string {
	name := strings.Trim(strings.TrimSpace(repo.ExternalRepositoryName), "/")
	if name == "" {
		name = strings.Trim(strings.TrimSpace(repo.Name), "/")
	}
	if name == "" {
		return b.publicBaseURL
	}
	return b.publicBaseURL + "/" + name
}

func (b *Backend) checkHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL, nil)
	if err != nil {
		return err
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%s health check failed: status=%d", b.typ, resp.StatusCode)
	}
	return nil
}

func (b *Backend) unsupported(operation string) error {
	return fmt.Errorf("%s backend is protocol-native; %s is not supported through Bahia package artifacts", b.typ, operation)
}

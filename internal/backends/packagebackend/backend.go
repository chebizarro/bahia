package packagebackend

import (
	"context"
	"io"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Capabilities advertises the package formats and operations supported by a backend.
// Bahia remains a control plane: format support here means the backend can store,
// observe, and route artifacts for that ecosystem; it does not imply Bahia serves
// package-manager protocol indexes for that format.
type Capabilities struct {
	Formats             []domain.PackageRepositoryFormat `json:"formats"`
	CanCreateRepository bool                             `json:"can_create_repository"`
	CanDeleteRepository bool                             `json:"can_delete_repository"`
	CanStoreArtifact    bool                             `json:"can_store_artifact"`
	CanGetArtifact      bool                             `json:"can_get_artifact"`
	CanListArtifacts    bool                             `json:"can_list_artifacts"`
	CanPromoteArtifact  bool                             `json:"can_promote_artifact"`
	CanYankArtifact     bool                             `json:"can_yank_artifact"`
	CanObserveDrift     bool                             `json:"can_observe_drift"`
}

// Backend is the pluggable package repository data-plane adapter used by the
// package control-plane service. Implementations must be idempotent where
// possible: create existing repositories, yanking missing artifacts, and deleting
// missing repositories should not corrupt control-plane projections.
type Backend interface {
	Type() domain.PackageBackendType
	Capabilities() Capabilities

	EnsureRepository(ctx context.Context, repo domain.PackageRepository) (RepositoryObservation, error)
	DeleteRepository(ctx context.Context, repo domain.PackageRepository, force bool) (RepositoryObservation, error)
	ObserveRepository(ctx context.Context, repo domain.PackageRepository) (RepositoryObservation, error)

	StoreArtifact(ctx context.Context, repo domain.PackageRepository, req StoreArtifactRequest) (ArtifactObservation, error)
	GetArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (ArtifactStream, error)
	ListArtifacts(ctx context.Context, repo domain.PackageRepository) ([]ArtifactObservation, error)
	PromoteArtifact(ctx context.Context, sourceRepo domain.PackageRepository, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req PromoteArtifactRequest) (ArtifactObservation, error)
	YankArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact, reason string) (ArtifactObservation, error)
	ObserveArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (ArtifactObservation, error)
}

// RepositoryObservation is backend-observed repository state used for projections
// and drift checks.
type RepositoryObservation struct {
	Exists    bool              `json:"exists"`
	PublicURL string            `json:"public_url,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// StoreArtifactRequest is a streaming artifact write request.
type StoreArtifactRequest struct {
	Namespace   string
	PackageName string
	Version     string
	Filename    string
	ContentType string
	SHA256      string
	SizeBytes   int64
	Metadata    map[string]any
	Reader      io.Reader
}

// PromoteArtifactRequest describes a promotion into a target repository/channel.
type PromoteArtifactRequest struct {
	Environment string
	Channel     string
	ApprovedBy  string
	PolicyRef   string
	Metadata    map[string]any
}

// ArtifactObservation is backend-observed artifact state used for projections
// and drift checks.
type ArtifactObservation struct {
	Exists      bool              `json:"exists"`
	DownloadURL string            `json:"download_url,omitempty"`
	BackendPath string            `json:"backend_path,omitempty"`
	SHA256      string            `json:"sha256,omitempty"`
	SizeBytes   int64             `json:"size_bytes,omitempty"`
	Yanked      bool              `json:"yanked,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ArtifactStream is a readable backend artifact. Callers must close ReadCloser.
type ArtifactStream struct {
	ReadCloser  io.ReadCloser
	ContentType string
	SHA256      string
	SizeBytes   int64
	BackendPath string
}

// Registry stores configured backend adapters by backend ref.
type Registry map[string]Backend

// Get returns a backend by ref.
func (r Registry) Get(ref string) (Backend, bool) {
	b, ok := r[ref]
	return b, ok
}

// SupportedFormats returns the first-class package formats required by the user brief.
func SupportedFormats() []domain.PackageRepositoryFormat {
	return []domain.PackageRepositoryFormat{
		domain.PackageRepositoryFormatNPM,
		domain.PackageRepositoryFormatPyPI,
		domain.PackageRepositoryFormatConan,
		domain.PackageRepositoryFormatDeb,
		domain.PackageRepositoryFormatRPM,
		domain.PackageRepositoryFormatPub,
		domain.PackageRepositoryFormatGoModules,
		domain.PackageRepositoryFormatGradle,
	}
}

// SupportsFormat reports whether the advertised capabilities include format.
func SupportsFormat(c Capabilities, format domain.PackageRepositoryFormat) bool {
	for _, candidate := range c.Formats {
		if candidate == format {
			return true
		}
	}
	return false
}

// CommonCapabilities returns the default byte-storage capability set shared by
// filesystem_mock, Nexus raw, and Pulp file adapters in this control-plane slice.
func CommonCapabilities() Capabilities {
	return Capabilities{
		Formats:             SupportedFormats(),
		CanCreateRepository: true,
		CanDeleteRepository: true,
		CanStoreArtifact:    true,
		CanGetArtifact:      true,
		CanListArtifacts:    true,
		CanPromoteArtifact:  true,
		CanYankArtifact:     true,
		CanObserveDrift:     true,
	}
}

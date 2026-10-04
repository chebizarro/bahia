package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// SignatureStatePublisher is the canonical state sink at the signature insert boundary.
type SignatureStatePublisher interface {
	PublishArtifactSignature(context.Context, *domain.ArtifactSignature) error
}

type canonicalSignatureRepository struct {
	repository.ArtifactSignatureRepository
	publisher SignatureStatePublisher
	logger    *zap.Logger
}

func NewCanonicalSignatureRepository(repo repository.ArtifactSignatureRepository, publisher SignatureStatePublisher, logger *zap.Logger) repository.ArtifactSignatureRepository {
	return &canonicalSignatureRepository{repo, publisher, logger}
}
func (r *canonicalSignatureRepository) Create(ctx context.Context, sig *domain.ArtifactSignature) error {
	if err := r.ArtifactSignatureRepository.Create(ctx, sig); err != nil {
		return err
	}
	if r.publisher != nil {
		if err := r.publisher.PublishArtifactSignature(ctx, sig); err != nil {
			r.logger.Warn("publish artifact signature cp-state failed", zap.Error(err))
		}
	}
	return nil
}

// SBOMStatePublisher indexes manifest metadata and packages as separate records.
type SBOMStatePublisher interface {
	PublishArtifactSBOM(context.Context, *domain.ArtifactSBOM) error
	PublishSBOMPackage(context.Context, *domain.SBOMPackage) error
}

type canonicalSBOMRepository struct {
	repository.SBOMRepository
	publisher SBOMStatePublisher
	logger    *zap.Logger
}

func NewCanonicalSBOMRepository(repo repository.SBOMRepository, publisher SBOMStatePublisher, logger *zap.Logger) repository.SBOMRepository {
	return &canonicalSBOMRepository{repo, publisher, logger}
}
func (r *canonicalSBOMRepository) CreateSBOM(ctx context.Context, sbom *domain.ArtifactSBOM) error {
	if err := r.SBOMRepository.CreateSBOM(ctx, sbom); err != nil {
		return err
	}
	if r.publisher != nil {
		if err := r.publisher.PublishArtifactSBOM(ctx, sbom); err != nil {
			r.logger.Warn("publish artifact SBOM cp-state failed", zap.Error(err))
		}
	}
	return nil
}
func (r *canonicalSBOMRepository) CreatePackages(ctx context.Context, packages []domain.SBOMPackage) error {
	if err := r.SBOMRepository.CreatePackages(ctx, packages); err != nil {
		return err
	}
	if r.publisher != nil {
		for i := range packages {
			if err := r.publisher.PublishSBOMPackage(ctx, &packages[i]); err != nil {
				r.logger.Warn("publish SBOM package cp-state failed", zap.Error(err))
			}
		}
	}
	return nil
}

// canonicalSBOMManifestRepository covers the generator/importer projection path.
// ProjectManifest writes compatibility artifact_sboms/sbom_packages directly,
// bypassing SBOMRepository.CreateSBOM/CreatePackages; publish after its commit.
type canonicalSBOMManifestRepository struct {
	repository.SBOMManifestRepository
	compatibility repository.SBOMRepository
	publisher     SBOMStatePublisher
	logger        *zap.Logger
}

func NewCanonicalSBOMManifestRepository(repo repository.SBOMManifestRepository, compatibility repository.SBOMRepository, publisher SBOMStatePublisher, logger *zap.Logger) repository.SBOMManifestRepository {
	return &canonicalSBOMManifestRepository{repo, compatibility, publisher, logger}
}
func (r *canonicalSBOMManifestRepository) markRepair() {
	if marker, ok := r.publisher.(interface{ MarkBackfillDirty() error }); ok {
		if err := marker.MarkBackfillDirty(); err != nil {
			r.logger.Error("mark SBOM canonical backfill dirty failed", zap.Error(err))
		}
	}
}
func (r *canonicalSBOMManifestRepository) publishCompatibility(ctx context.Context, artifactID uuid.UUID, hash string, seen map[uuid.UUID]struct{}) error {
	sbom, err := r.compatibility.GetSBOMByHash(ctx, hash)
	if err != nil {
		return err
	}
	if sbom.ArtifactID != artifactID {
		return fmt.Errorf("artifact SBOM projection subject mismatch for %s", artifactID)
	}
	if err := r.publisher.PublishArtifactSBOM(ctx, sbom); err != nil {
		return err
	}
	packages, err := r.compatibility.ListPackagesBySBOM(ctx, sbom.ID)
	if err != nil {
		return err
	}
	for i := range packages {
		if _, already := seen[packages[i].ID]; already {
			continue
		}
		if err := r.publisher.PublishSBOMPackage(ctx, &packages[i]); err != nil {
			return err
		}
	}
	return nil
}
func (r *canonicalSBOMManifestRepository) ProjectManifest(ctx context.Context, manifest *domain.SBOMManifest, packages []domain.SBOMManifestPackage) error {
	seen := make(map[uuid.UUID]struct{})
	if manifest.Subject.Type == domain.SBOMSubjectArtifact && r.publisher != nil {
		existing, err := r.compatibility.GetSBOMByHash(ctx, manifest.PayloadSHA256)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return err
		}
		if existing != nil {
			prior, err := r.compatibility.ListPackagesBySBOM(ctx, existing.ID)
			if err != nil {
				return err
			}
			for _, pkg := range prior {
				seen[pkg.ID] = struct{}{}
			}
		}
	}
	if err := r.SBOMManifestRepository.ProjectManifest(ctx, manifest, packages); err != nil {
		return err
	}
	if manifest.Subject.Type != domain.SBOMSubjectArtifact || r.publisher == nil {
		return nil
	}
	id, err := uuid.Parse(manifest.Subject.ID)
	if err != nil {
		return err
	}
	if err := r.publishCompatibility(ctx, id, manifest.PayloadSHA256, seen); err != nil {
		r.markRepair()
		r.logger.Warn("publish projected artifact SBOM cp-state failed", zap.Error(err))
	}
	return nil
}
func (r *canonicalSBOMManifestRepository) UpdateCompatibilityVulnerabilityCounts(ctx context.Context, artifactID uuid.UUID, hash string, counts domain.SecuritySeverityCounts, total int) error {
	if err := r.SBOMManifestRepository.UpdateCompatibilityVulnerabilityCounts(ctx, artifactID, hash, counts, total); err != nil {
		return err
	}
	if r.publisher == nil {
		return nil
	}
	sbom, err := r.compatibility.GetSBOMByHash(ctx, hash)
	if err != nil {
		r.markRepair()
		r.logger.Warn("read updated artifact SBOM for cp-state failed", zap.Error(err))
		return nil
	}
	if sbom.ArtifactID != artifactID {
		r.markRepair()
		r.logger.Warn("artifact SBOM count subject mismatch after update", zap.String("artifact_id", artifactID.String()))
		return nil
	}
	if err := r.publisher.PublishArtifactSBOM(ctx, sbom); err != nil {
		r.logger.Warn("publish updated artifact SBOM counts failed", zap.Error(err))
	}
	return nil
}

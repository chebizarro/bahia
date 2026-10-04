package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const f74aBackfillPageSize = 250

type F74aBackfillMarker interface {
	GetControlRecord(family, id string) ([]byte, error)
	PutControlRecord(family, id string, value []byte) error
}
type F74aSBOMBackfillSource interface {
	ListAllSBOMs(context.Context, int, int) ([]domain.ArtifactSBOM, error)
	ListAllPackages(context.Context, int, int) ([]domain.SBOMPackage, error)
}
type F74aReleaseLister interface {
	ListRoutes(context.Context, int, int) ([]domain.LLMRoute, error)
	ListReleases(context.Context, uuid.UUID, int, int) ([]domain.LLMRelease, error)
}
type F74aBackfillPublisher interface {
	PublishLLMRelease(context.Context, *domain.LLMRelease) error
	PublishArtifactSignature(context.Context, *domain.ArtifactSignature) error
	PublishArtifactSBOM(context.Context, *domain.ArtifactSBOM) error
	PublishSBOMPackage(context.Context, *domain.SBOMPackage) error
	PublishRuntimeObservation(context.Context, *domain.RuntimeObservation) error
}
type F74aBackfillConfig struct {
	Marker       F74aBackfillMarker
	Publisher    F74aBackfillPublisher
	LLM          F74aReleaseLister
	Services     repository.ServiceRepository
	Artifacts    repository.ArtifactRepository
	Signatures   repository.ArtifactSignatureRepository
	SBOMs        F74aSBOMBackfillSource
	Observations repository.RuntimeObservationRepository
	States       repository.EnvironmentServiceStateRepository
}

// BootstrapF74aCanonical queues historical rows exactly once before MCP starts
// serving the new families. The durable marker is written only after every
// projection has reached the shared signed outbox; a failed run retries on the
// next startup. DB-less callers skip this migration and warm from relays.
func BootstrapF74aCanonical(ctx context.Context, c F74aBackfillConfig) error {
	if c.Marker == nil || c.Publisher == nil {
		return nil
	}
	marker, err := c.Marker.GetControlRecord("bootstrap", "f74a-canonical-v1")
	if err != nil || string(marker) == "1" {
		return err
	}
	if c.LLM != nil {
		for offset := 0; ; offset += f74aBackfillPageSize {
			routes, err := c.LLM.ListRoutes(ctx, f74aBackfillPageSize, offset)
			if err != nil {
				return err
			}
			for _, route := range routes {
				for releaseOffset := 0; ; releaseOffset += f74aBackfillPageSize {
					releases, err := c.LLM.ListReleases(ctx, route.ID, f74aBackfillPageSize, releaseOffset)
					if err != nil {
						return err
					}
					for i := range releases {
						if err := c.Publisher.PublishLLMRelease(ctx, &releases[i]); err != nil {
							return err
						}
					}
					if len(releases) < f74aBackfillPageSize {
						break
					}
				}
			}
			if len(routes) < f74aBackfillPageSize {
				break
			}
		}
	}
	if c.Services != nil && c.Artifacts != nil && c.Signatures != nil {
		services, err := c.Services.List(ctx)
		if err != nil {
			return err
		}
		for _, svc := range services {
			for offset := 0; ; offset += f74aBackfillPageSize {
				artifacts, err := c.Artifacts.ListByService(ctx, svc.ID, f74aBackfillPageSize, offset)
				if err != nil {
					return err
				}
				for _, artifact := range artifacts {
					signatures, err := c.Signatures.ListByArtifact(ctx, artifact.ID)
					if err != nil {
						return err
					}
					for i := range signatures {
						if err := c.Publisher.PublishArtifactSignature(ctx, &signatures[i]); err != nil {
							return err
						}
					}
				}
				if len(artifacts) < f74aBackfillPageSize {
					break
				}
			}
		}
	}
	if c.SBOMs != nil {
		for offset := 0; ; offset += f74aBackfillPageSize {
			sboms, err := c.SBOMs.ListAllSBOMs(ctx, f74aBackfillPageSize, offset)
			if err != nil {
				return err
			}
			for i := range sboms {
				if err := c.Publisher.PublishArtifactSBOM(ctx, &sboms[i]); err != nil {
					return err
				}
			}
			if len(sboms) < f74aBackfillPageSize {
				break
			}
		}
		for offset := 0; ; offset += f74aBackfillPageSize {
			packages, err := c.SBOMs.ListAllPackages(ctx, f74aBackfillPageSize, offset)
			if err != nil {
				return err
			}
			for i := range packages {
				if err := c.Publisher.PublishSBOMPackage(ctx, &packages[i]); err != nil {
					return err
				}
			}
			if len(packages) < f74aBackfillPageSize {
				break
			}
		}
	}
	if c.States != nil && c.Observations != nil {
		states, err := c.States.ListAll(ctx)
		if err != nil {
			return err
		}
		for _, state := range states {
			obs, err := c.Observations.GetLatest(ctx, state.ServiceID, state.EnvironmentID)
			if err != nil {
				return err
			}
			if obs != nil {
				if err := c.Publisher.PublishRuntimeObservation(ctx, obs); err != nil {
					return err
				}
			}
		}
	}
	return c.Marker.PutControlRecord("bootstrap", "f74a-canonical-v1", []byte("1"))
}

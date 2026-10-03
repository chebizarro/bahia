package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// MLTombstonePublisher extends the canonical ML publisher for destructive and
// identity-changing mutations. The old coordinate must be retired before a
// new identity becomes visible on the relay.
type MLTombstonePublisher interface {
	PublishModelTombstone(context.Context, *domain.MLModel) error
	PublishModelVersionTombstone(context.Context, *domain.MLModelVersion, string) error
	PublishEndpointTombstone(context.Context, *domain.MLInferenceEndpoint) error
}

type mlDeletionRepository interface {
	DeleteModel(context.Context, uuid.UUID) error
	DeleteModelVersion(context.Context, uuid.UUID) error
	DeleteInferenceEndpoint(context.Context, uuid.UUID) error
}

func (s *MLRegistryService) deletionRepository() (mlDeletionRepository, error) {
	repo, ok := s.repo.(mlDeletionRepository)
	if !ok {
		return nil, fmt.Errorf("ML registry deletion repository is not configured")
	}
	return repo, nil
}

func (s *MLRegistryService) requireMLTombstones() error {
	if _, ok := s.cpState.(MLTombstonePublisher); !ok {
		return fmt.Errorf("ML canonical tombstone publisher is not configured")
	}
	return nil
}

func (s *MLRegistryService) publishMLModelTombstone(ctx context.Context, model *domain.MLModel) error {
	return s.cpState.(MLTombstonePublisher).PublishModelTombstone(ctx, model)
}

func (s *MLRegistryService) publishMLVersionTombstone(ctx context.Context, version *domain.MLModelVersion, slug string) error {
	return s.cpState.(MLTombstonePublisher).PublishModelVersionTombstone(ctx, version, slug)
}

func (s *MLRegistryService) publishMLEndpointTombstone(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error {
	return s.cpState.(MLTombstonePublisher).PublishEndpointTombstone(ctx, endpoint)
}

func (s *MLRegistryService) allModelVersions(ctx context.Context, modelID uuid.UUID) ([]domain.MLModelVersion, error) {
	const pageSize = 100
	var all []domain.MLModelVersion
	for offset := 0; ; offset += pageSize {
		page, err := s.repo.ListModelVersions(ctx, modelID, pageSize, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
	}
}

func (s *MLRegistryService) DeleteModel(ctx context.Context, id uuid.UUID) error {
	deletions, err := s.deletionRepository()
	if err != nil {
		return err
	}
	model, err := s.repo.GetModel(ctx, id)
	if err != nil {
		return err
	}
	if model == nil {
		return fmt.Errorf("ML model %s: %w", id, repository.ErrNotFound)
	}
	if err := s.requireMLTombstones(); err != nil {
		return err
	}
	versions, err := s.allModelVersions(ctx, id)
	if err != nil {
		return err
	}
	if err := deletions.DeleteModel(ctx, id); err != nil {
		return err
	}
	for i := range versions {
		if err := s.publishMLVersionTombstone(ctx, &versions[i], model.Slug); err != nil {
			return err
		}
	}
	if err := s.publishMLModelTombstone(ctx, model); err != nil {
		return err
	}
	s.publish(ctx, EventMLModelChanged, id.String(), map[string]any{"model_id": id.String(), "deleted": true})
	return nil
}

func (s *MLRegistryService) DeleteModelVersion(ctx context.Context, id uuid.UUID) error {
	deletions, err := s.deletionRepository()
	if err != nil {
		return err
	}
	version, err := s.repo.GetModelVersion(ctx, id)
	if err != nil {
		return err
	}
	if version == nil {
		return fmt.Errorf("ML model version %s: %w", id, repository.ErrNotFound)
	}
	model, err := s.repo.GetModel(ctx, version.ModelID)
	if err != nil {
		return err
	}
	if model == nil {
		return fmt.Errorf("ML model %s: %w", version.ModelID, repository.ErrNotFound)
	}
	if err := s.requireMLTombstones(); err != nil {
		return err
	}
	if err := deletions.DeleteModelVersion(ctx, id); err != nil {
		return err
	}
	if err := s.publishMLVersionTombstone(ctx, version, model.Slug); err != nil {
		return err
	}
	s.publish(ctx, EventMLVersionChanged, id.String(), map[string]any{"model_version_id": id.String(), "deleted": true})
	return nil
}

func (s *MLRegistryService) DeleteInferenceEndpoint(ctx context.Context, id uuid.UUID) error {
	deletions, err := s.deletionRepository()
	if err != nil {
		return err
	}
	endpoint, err := s.repo.GetInferenceEndpoint(ctx, id)
	if err != nil {
		return err
	}
	if endpoint == nil {
		return fmt.Errorf("ML endpoint %s: %w", id, repository.ErrNotFound)
	}
	if err := s.requireMLTombstones(); err != nil {
		return err
	}
	if err := deletions.DeleteInferenceEndpoint(ctx, id); err != nil {
		return err
	}
	if err := s.publishMLEndpointTombstone(ctx, endpoint); err != nil {
		return err
	}
	s.publish(ctx, EventMLEndpointChanged, id.String(), map[string]any{"endpoint_id": id.String(), "deleted": true})
	return nil
}

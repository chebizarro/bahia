package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
)

// LocalMLRegistryRepository owns model, version, and endpoint desired state in
// the durable local store. Other ML registry families still use the optional
// PostgreSQL repository until their relay-first migration.
type LocalMLRegistryRepository struct {
	MLRegistryRepository
	store *localstore.Outbox
}

func NewLocalMLRegistryRepository(store *localstore.Outbox, other MLRegistryRepository) *LocalMLRegistryRepository {
	return &LocalMLRegistryRepository{MLRegistryRepository: other, store: store}
}

func (r *LocalMLRegistryRepository) get(family string, id uuid.UUID, target any) (bool, error) {
	data, err := r.store.GetControlRecord(family, id.String())
	if err != nil || data == nil {
		return false, err
	}
	return true, json.Unmarshal(data, target)
}

func (r *LocalMLRegistryRepository) put(family string, id uuid.UUID, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.store.PutControlRecord(family, id.String(), data)
}

func (r *LocalMLRegistryRepository) list(family string, target any) error {
	data, err := r.store.ListControlRecords(family)
	if err != nil {
		return err
	}
	items := make([]json.RawMessage, 0, len(data))
	for _, item := range data {
		items = append(items, item)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func (r *LocalMLRegistryRepository) UpsertModel(ctx context.Context, model *domain.MLModel) error {
	models, err := r.ListModels(ctx, "", 0, 0)
	if err != nil {
		return err
	}
	for _, existing := range models {
		if existing.Slug == model.Slug && existing.ID != model.ID {
			return fmt.Errorf("ML model slug %q already exists", model.Slug)
		}
	}
	now := time.Now().UTC()
	if model.CreatedAt.IsZero() {
		model.CreatedAt = now
	}
	model.UpdatedAt = now
	return r.put("ml-model", model.ID, model)
}
func (r *LocalMLRegistryRepository) GetModel(_ context.Context, id uuid.UUID) (*domain.MLModel, error) {
	var value domain.MLModel
	ok, err := r.get("ml-model", id, &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalMLRegistryRepository) GetModelBySlug(ctx context.Context, slug string) (*domain.MLModel, error) {
	models, err := r.ListModels(ctx, "", 0, 0)
	if err != nil {
		return nil, err
	}
	for i := range models {
		if models[i].Slug == slug {
			return &models[i], nil
		}
	}
	return nil, nil
}
func (r *LocalMLRegistryRepository) ListModels(_ context.Context, task domain.MLTaskKind, limit, offset int) ([]domain.MLModel, error) {
	var values []domain.MLModel
	if err := r.list("ml-model", &values); err != nil {
		return nil, err
	}
	var filtered []domain.MLModel
	for _, model := range values {
		if task == "" {
			filtered = append(filtered, model)
			continue
		}
		for _, candidate := range model.TaskKinds {
			if candidate == task {
				filtered = append(filtered, model)
				break
			}
		}
	}
	return pageLocal(filtered, limit, offset), nil
}
func (r *LocalMLRegistryRepository) DeleteModel(_ context.Context, id uuid.UUID) error {
	versions, err := r.ListModelVersions(context.Background(), id, 0, 0)
	if err != nil {
		return err
	}
	found, err := r.store.DeleteControlRecord("ml-model", id.String())
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ML model %s: %w", id, ErrNotFound)
	}
	for _, version := range versions {
		if _, err := r.store.DeleteControlRecord("ml-version", version.ID.String()); err != nil {
			return err
		}
	}
	return nil
}

func (r *LocalMLRegistryRepository) UpsertModelVersion(ctx context.Context, version *domain.MLModelVersion) error {
	versions, err := r.ListModelVersions(ctx, version.ModelID, 0, 0)
	if err != nil {
		return err
	}
	for _, existing := range versions {
		if existing.Version == version.Version && existing.ID != version.ID {
			return fmt.Errorf("ML model version %q already exists", version.Version)
		}
	}
	now := time.Now().UTC()
	if version.CreatedAt.IsZero() {
		version.CreatedAt = now
	}
	version.UpdatedAt = now
	return r.put("ml-version", version.ID, version)
}
func (r *LocalMLRegistryRepository) GetModelVersion(_ context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	var value domain.MLModelVersion
	ok, err := r.get("ml-version", id, &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalMLRegistryRepository) GetModelVersionByModelVersion(ctx context.Context, modelID uuid.UUID, version string) (*domain.MLModelVersion, error) {
	versions, err := r.ListModelVersions(ctx, modelID, 0, 0)
	if err != nil {
		return nil, err
	}
	for i := range versions {
		if versions[i].Version == version {
			return &versions[i], nil
		}
	}
	return nil, nil
}
func (r *LocalMLRegistryRepository) ListModelVersions(_ context.Context, modelID uuid.UUID, limit, offset int) ([]domain.MLModelVersion, error) {
	var values []domain.MLModelVersion
	if err := r.list("ml-version", &values); err != nil {
		return nil, err
	}
	var filtered []domain.MLModelVersion
	for _, version := range values {
		if modelID == uuid.Nil || version.ModelID == modelID {
			filtered = append(filtered, version)
		}
	}
	return pageLocal(filtered, limit, offset), nil
}
func (r *LocalMLRegistryRepository) DeleteModelVersion(_ context.Context, id uuid.UUID) error {
	found, err := r.store.DeleteControlRecord("ml-version", id.String())
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ML model version %s: %w", id, ErrNotFound)
	}
	return nil
}

func (r *LocalMLRegistryRepository) UpsertInferenceEndpoint(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error {
	endpoints, err := r.ListInferenceEndpoints(ctx, uuid.Nil, 0, 0)
	if err != nil {
		return err
	}
	for _, existing := range endpoints {
		if existing.Name == endpoint.Name && existing.EnvironmentID == endpoint.EnvironmentID && existing.ID != endpoint.ID {
			return fmt.Errorf("ML endpoint %q already exists in environment %s", endpoint.Name, endpoint.EnvironmentID)
		}
	}
	now := time.Now().UTC()
	if endpoint.CreatedAt.IsZero() {
		endpoint.CreatedAt = now
	}
	endpoint.UpdatedAt = now
	return r.put("ml-endpoint", endpoint.ID, endpoint)
}
func (r *LocalMLRegistryRepository) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	var value domain.MLInferenceEndpoint
	ok, err := r.get("ml-endpoint", id, &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalMLRegistryRepository) GetInferenceEndpointByNameEnv(ctx context.Context, name string, envID uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	endpoints, err := r.ListInferenceEndpoints(ctx, envID, 0, 0)
	if err != nil {
		return nil, err
	}
	for i := range endpoints {
		if endpoints[i].Name == name {
			return &endpoints[i], nil
		}
	}
	return nil, nil
}
func (r *LocalMLRegistryRepository) ListInferenceEndpoints(_ context.Context, envID uuid.UUID, limit, offset int) ([]domain.MLInferenceEndpoint, error) {
	var values []domain.MLInferenceEndpoint
	if err := r.list("ml-endpoint", &values); err != nil {
		return nil, err
	}
	var filtered []domain.MLInferenceEndpoint
	for _, endpoint := range values {
		if envID == uuid.Nil || endpoint.EnvironmentID == envID {
			filtered = append(filtered, endpoint)
		}
	}
	return pageLocal(filtered, limit, offset), nil
}
func (r *LocalMLRegistryRepository) DeleteInferenceEndpoint(_ context.Context, id uuid.UUID) error {
	found, err := r.store.DeleteControlRecord("ml-endpoint", id.String())
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ML endpoint %s: %w", id, ErrNotFound)
	}
	return nil
}

func pageLocal[T any](items []T, limit, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}
	items = items[offset:]
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

func (r *LocalMLRegistryRepository) ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error) {
	if r.MLRegistryRepository == nil {
		return nil, nil
	}
	return r.MLRegistryRepository.ListInferenceStates(ctx)
}

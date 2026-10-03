package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// MLIntentRegistry is the existing ML registry service mutation boundary.
// Its methods publish canonical records through MLCanonicalPublisher.
type MLIntentRegistry interface {
	CreateOrUpdateModel(context.Context, *domain.MLModel) error
	GetModel(context.Context, uuid.UUID) (*domain.MLModel, error)
	CreateOrUpdateModelVersion(context.Context, *domain.MLModelVersion) error
	GetModelVersion(context.Context, uuid.UUID) (*domain.MLModelVersion, error)
	CreateOrUpdateInferenceEndpoint(context.Context, *domain.MLInferenceEndpoint) error
	GetInferenceEndpoint(context.Context, uuid.UUID) (*domain.MLInferenceEndpoint, error)
}

type MLIntentHandler struct{ registry MLIntentRegistry }

func NewMLIntentHandler(registry MLIntentRegistry) *MLIntentHandler {
	return &MLIntentHandler{registry: registry}
}
func (*MLIntentHandler) PermissionFor(string) domain.Permission { return domain.PermWriteServices }
func (*MLIntentHandler) IsFleetScoped() bool                    { return true }

func (h *MLIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if h.registry == nil {
		return fmt.Errorf("ML registry is not configured")
	}
	content, err := json.Marshal(intent.Content)
	if err != nil {
		return fmt.Errorf("marshal ML intent: %w", err)
	}
	switch intent.Op {
	case "model-create", "model-update":
		var model domain.MLModel
		if err := json.Unmarshal(content, &model); err != nil {
			return err
		}
		if model.ID == uuid.Nil || intent.Coordinate != "model:"+strings.TrimSpace(model.Slug) {
			return fmt.Errorf("ML model id and model:<slug> coordinate are required")
		}
		existing, err := h.registry.GetModel(ctx, model.ID)
		if err != nil {
			return err
		}
		if err := checkMLRevision(intent, model.ID.String(), existing != nil, func() time.Time {
			if existing == nil {
				return time.Time{}
			}
			return existing.UpdatedAt
		}()); err != nil {
			return err
		}
		if existing != nil {
			if existing.Slug != model.Slug {
				return fmt.Errorf("unsupported op: ml model slug change — canonical coordinate cannot be tombstoned")
			}
			if model.CreatedAt.IsZero() {
				model.CreatedAt = existing.CreatedAt
			}
		}
		return h.registry.CreateOrUpdateModel(ctx, &model)
	case "version-create", "version-update":
		var version domain.MLModelVersion
		if err := json.Unmarshal(content, &version); err != nil {
			return err
		}
		if version.ID == uuid.Nil || intent.Coordinate != "model-version:"+version.ID.String() {
			return fmt.Errorf("ML model version id and coordinate must match")
		}
		if intent.ExpectedUpdatedAt != nil {
			return fmt.Errorf("ML model versions have no updated_at revision")
		}
		existing, err := h.registry.GetModelVersion(ctx, version.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.ModelID != version.ModelID || existing.Version != version.Version {
				return fmt.Errorf("unsupported op: ml model version identity change — canonical coordinate cannot be tombstoned")
			}
			if version.CreatedAt.IsZero() {
				version.CreatedAt = existing.CreatedAt
			}
		}
		return h.registry.CreateOrUpdateModelVersion(ctx, &version)
	case "endpoint-create", "endpoint-update":
		var endpoint domain.MLInferenceEndpoint
		if err := json.Unmarshal(content, &endpoint); err != nil {
			return err
		}
		if endpoint.ID == uuid.Nil || intent.Coordinate != "endpoint:"+endpoint.ID.String() {
			return fmt.Errorf("ML endpoint id and coordinate must match")
		}
		existing, err := h.registry.GetInferenceEndpoint(ctx, endpoint.ID)
		if err != nil {
			return err
		}
		if err := checkMLRevision(intent, endpoint.ID.String(), existing != nil, func() time.Time {
			if existing == nil {
				return time.Time{}
			}
			return existing.UpdatedAt
		}()); err != nil {
			return err
		}
		if existing != nil {
			if existing.Name != endpoint.Name || existing.EnvironmentID != endpoint.EnvironmentID {
				return fmt.Errorf("unsupported op: ml endpoint identity change — canonical coordinate cannot be tombstoned")
			}
			if endpoint.CreatedAt.IsZero() {
				endpoint.CreatedAt = existing.CreatedAt
			}
		}
		return h.registry.CreateOrUpdateInferenceEndpoint(ctx, &endpoint)
	default:
		return fmt.Errorf("unsupported op: ml %s — no durable mutation path", intent.Op)
	}
}

func checkMLRevision(intent *Intent, entity string, exists bool, actual time.Time) error {
	if intent.ExpectedUpdatedAt == nil {
		return nil
	}
	expected := time.Unix(*intent.ExpectedUpdatedAt, 0).UTC()
	if raw, ok := intent.Content["expected_updated_at"].(string); ok {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return fmt.Errorf("invalid expected_updated_at: %w", err)
		}
		expected = parsed
	}
	if !exists || !actual.UTC().Equal(expected) {
		return &intentRevisionConflictError{entity: entity, expected: expected, actual: actual}
	}
	return nil
}

type intentRevisionConflictError struct {
	entity   string
	expected time.Time
	actual   time.Time
}

func (e *intentRevisionConflictError) Error() string {
	return fmt.Sprintf("%s revision conflict (expected %s, actual %s)", e.entity,
		e.expected.UTC().Format(time.RFC3339Nano), e.actual.UTC().Format(time.RFC3339Nano))
}

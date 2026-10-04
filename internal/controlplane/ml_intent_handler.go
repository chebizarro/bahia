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
	DeleteModel(context.Context, uuid.UUID) error
	CreateOrUpdateModelVersion(context.Context, *domain.MLModelVersion) error
	GetModelVersion(context.Context, uuid.UUID) (*domain.MLModelVersion, error)
	DeleteModelVersion(context.Context, uuid.UUID) error
	CreateOrUpdateInferenceEndpoint(context.Context, *domain.MLInferenceEndpoint) error
	GetInferenceEndpoint(context.Context, uuid.UUID) (*domain.MLInferenceEndpoint, error)
	DeleteInferenceEndpoint(context.Context, uuid.UUID) error
}

type MLIntentHandler struct {
	registry     MLIntentRegistry
	environments interface {
		GetEnvironmentByName(context.Context, string) (*domain.Environment, error)
	}
}

func NewMLIntentHandler(registry MLIntentRegistry, environments ...interface {
	GetEnvironmentByName(context.Context, string) (*domain.Environment, error)
}) *MLIntentHandler {
	h := &MLIntentHandler{registry: registry}
	if len(environments) > 0 {
		h.environments = environments[0]
	}
	return h
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
	case "model-import", "recipe-apply", "recipe-run", "inference-deploy", "inference-approval", "inference-rollback":
		return h.handleMLOperation(ctx, intent, content)
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
		if err := checkIntentRevision(intent, model.ID.String(), existing != nil, func() time.Time {
			if existing == nil {
				return time.Time{}
			}
			return existing.UpdatedAt
		}()); err != nil {
			return err
		}
		if existing != nil {
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
		existing, err := h.registry.GetModelVersion(ctx, version.ID)
		if err != nil {
			return err
		}
		if err := checkIntentRevision(intent, version.ID.String(), existing != nil, func() time.Time {
			if existing == nil {
				return time.Time{}
			}
			return existing.UpdatedAt
		}()); err != nil {
			return err
		}
		if existing != nil {
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
		if err := checkIntentRevision(intent, endpoint.ID.String(), existing != nil, func() time.Time {
			if existing == nil {
				return time.Time{}
			}
			return existing.UpdatedAt
		}()); err != nil {
			return err
		}
		if existing != nil {
			if endpoint.CreatedAt.IsZero() {
				endpoint.CreatedAt = existing.CreatedAt
			}
		}
		return h.registry.CreateOrUpdateInferenceEndpoint(ctx, &endpoint)
	case "model-delete", "version-delete", "endpoint-delete":
		var payload struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(content, &payload); err != nil {
			return err
		}
		if payload.ID == uuid.Nil {
			return fmt.Errorf("ML %s requires an id", intent.Op)
		}
		switch intent.Op {
		case "model-delete":
			model, err := h.registry.GetModel(ctx, payload.ID)
			if err != nil {
				return err
			}
			if model == nil {
				return fmt.Errorf("ML model %s not found", payload.ID)
			}
			if intent.Coordinate != "model:"+model.Slug {
				return fmt.Errorf("ML model coordinate does not match slug")
			}
			if err := checkIntentRevision(intent, payload.ID.String(), true, model.UpdatedAt); err != nil {
				return err
			}
			return h.registry.DeleteModel(ctx, payload.ID)
		case "version-delete":
			version, err := h.registry.GetModelVersion(ctx, payload.ID)
			if err != nil {
				return err
			}
			if version == nil {
				return fmt.Errorf("ML model version %s not found", payload.ID)
			}
			if intent.Coordinate != "model-version:"+payload.ID.String() {
				return fmt.Errorf("ML model version coordinate does not match id")
			}
			if err := checkIntentRevision(intent, payload.ID.String(), true, version.UpdatedAt); err != nil {
				return err
			}
			return h.registry.DeleteModelVersion(ctx, payload.ID)
		default:
			endpoint, err := h.registry.GetInferenceEndpoint(ctx, payload.ID)
			if err != nil {
				return err
			}
			if endpoint == nil {
				return fmt.Errorf("ML endpoint %s not found", payload.ID)
			}
			if intent.Coordinate != "endpoint:"+payload.ID.String() {
				return fmt.Errorf("ML endpoint coordinate does not match id")
			}
			if err := checkIntentRevision(intent, payload.ID.String(), true, endpoint.UpdatedAt); err != nil {
				return err
			}
			return h.registry.DeleteInferenceEndpoint(ctx, payload.ID)
		}
	default:
		return fmt.Errorf("unsupported op: ml %s — no durable mutation path", intent.Op)
	}
}

func checkIntentRevision(intent *Intent, entity string, exists bool, actual time.Time) error {
	if intent.ExpectedUpdatedAt == nil {
		return nil
	}
	expected := *intent.ExpectedUpdatedAt
	if !exists || !intent.RevisionMatches(actual) {
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

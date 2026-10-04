package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// RegisterMLRegistryContextVMHandlers preserves the encrypted legacy registry
// methods while routing them through the signed-intent processor when enabled.
func RegisterMLRegistryContextVMHandlers(transport *EncryptedRequestTransport, registry MLIntentRegistry, gate *FleetOperatorGate, processor *IntentProcessor, environments ...interface {
	GetEnvironmentByName(context.Context, string) (*domain.Environment, error)
}) {
	if transport == nil || registry == nil {
		return
	}
	h := mlRegistryContextVMHandlers{registry: registry, processor: processor}
	if len(environments) > 0 {
		h.environments = environments[0]
	}
	for _, method := range []string{
		ContextVMMethodMLModelCreate, ContextVMMethodMLModelUpdate, ContextVMMethodMLModelDelete,
		ContextVMMethodMLVersionCreate, ContextVMMethodMLVersionUpdate, ContextVMMethodMLVersionDelete,
		ContextVMMethodMLEndpointCreate, ContextVMMethodMLEndpointUpdate, ContextVMMethodMLEndpointDelete,
	} {
		op := strings.TrimPrefix(method, "ml/")
		transport.RegisterContextVMHandler(method, gate.wrap(func(ctx context.Context, request ContextVMRequest) (any, error) {
			return h.mutate(ctx, request, op)
		}))
	}
	for _, method := range []string{"ml/model-import", ContextVMMethodMLRecipeRun, "ml/inference-deploy", "ml/inference-approval", "ml/inference-rollback"} {
		op := strings.TrimPrefix(method, "ml/")
		transport.RegisterContextVMHandler(method, gate.wrap(func(ctx context.Context, request ContextVMRequest) (any, error) {
			return h.operation(ctx, request, op)
		}))
	}
}

type mlRegistryContextVMHandlers struct {
	registry     MLIntentRegistry
	processor    *IntentProcessor
	environments interface {
		GetEnvironmentByName(context.Context, string) (*domain.Environment, error)
	}
}

func (h mlRegistryContextVMHandlers) operation(ctx context.Context, request ContextVMRequest, op string) (any, error) {
	if request.Event == nil {
		return nil, fmt.Errorf("ML operation requires an authenticated requester")
	}
	content := map[string]any{}
	if err := json.Unmarshal(request.RPC.Params, &content); err != nil {
		return nil, err
	}
	handler := NewMLIntentHandler(h.registry, h.environments)
	intentID := effectiveIdempotencyKey(request, request.Event.ID.Hex())
	coordinate := ""
	if h.processor != nil && h.processor.Handler("ml") != nil {
		if recorded := h.processor.ProcessedIntent(intentID); recorded != nil {
			coordinate = recorded.Coordinate
		}
	}
	if coordinate == "" {
		var err error
		coordinate, err = handler.operationCoordinate(ctx, op, content)
		if err != nil {
			return nil, err
		}
	}
	intent := &Intent{Event: request.Event, Domain: "ml", Op: op, Coordinate: coordinate,
		IntentID: intentID, Content: content, Actor: request.Event.PubKey.Hex()}
	var err error
	if h.processor != nil && h.processor.Handler("ml") != nil {
		err = h.processor.ProcessInProcess(ctx, intent)
	} else {
		if rawRevision, ok := content["expected_updated_at"]; ok {
			intent.ExpectedUpdatedAt, err = parseIntentRevision(rawRevision)
			if err != nil {
				return nil, err
			}
		}
		err = handler.HandleIntent(ctx, intent)
	}
	if err != nil {
		return nil, err
	}
	if intent.Result != nil {
		return intent.Result, nil
	}
	return map[string]any{"status": "accepted", "coordinate": coordinate}, nil
}

func (h mlRegistryContextVMHandlers) mutate(ctx context.Context, request ContextVMRequest, op string) (any, error) {
	if request.Event == nil {
		return nil, fmt.Errorf("ML registry mutation requires an authenticated requester")
	}
	var content map[string]interface{}
	if err := json.Unmarshal(request.RPC.Params, &content); err != nil {
		return nil, err
	}
	if content == nil {
		return nil, fmt.Errorf("ML registry mutation requires JSON object content")
	}
	id, err := uuid.Parse(fmt.Sprint(content["id"]))
	if err != nil || id == uuid.Nil {
		if strings.HasSuffix(op, "-create") {
			id = domain.NewEntityID()
			content["id"] = id.String()
		} else {
			return nil, fmt.Errorf("ML %s requires an id", op)
		}
	}
	var coordinate string
	switch {
	case strings.HasPrefix(op, "model-"):
		slug := strings.TrimSpace(fmt.Sprint(content["slug"]))
		if op == "model-delete" {
			model, err := h.registry.GetModel(ctx, id)
			if err != nil {
				return nil, err
			}
			if model == nil {
				return nil, fmt.Errorf("ML model %s not found", id)
			}
			slug = model.Slug
		}
		if slug == "" || slug == "<nil>" {
			return nil, fmt.Errorf("ML model slug is required")
		}
		coordinate = "model:" + slug
	case strings.HasPrefix(op, "version-"):
		coordinate = "model-version:" + id.String()
	case strings.HasPrefix(op, "endpoint-"):
		coordinate = "endpoint:" + id.String()
	default:
		return nil, fmt.Errorf("unsupported ML registry op %q", op)
	}
	intent := &Intent{Domain: "ml", Op: op, Coordinate: coordinate, IntentID: effectiveIdempotencyKey(request, request.Event.ID.Hex()), Content: content, Actor: request.Event.PubKey.Hex()}
	if h.processor != nil && h.processor.Handler("ml") != nil {
		if err := h.processor.ProcessInProcess(ctx, intent); err != nil {
			return nil, err
		}
	} else {
		if raw, ok := content["expected_updated_at"]; ok {
			revision, err := parseIntentRevision(raw)
			if err != nil {
				return nil, err
			}
			intent.ExpectedUpdatedAt = revision
		}
		if err := NewMLIntentHandler(h.registry).HandleIntent(ctx, intent); err != nil {
			return nil, err
		}
	}
	return map[string]any{"status": "accepted", "op": op, "coordinate": coordinate, "id": id.String()}, nil
}

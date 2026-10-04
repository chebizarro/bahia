package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type mlOperationRegistry interface {
	GetModelBySlug(context.Context, string) (*domain.MLModel, error)
	GetModelVersionByModelVersion(context.Context, uuid.UUID, string) (*domain.MLModelVersion, error)
	CreateOrUpdateRecipe(context.Context, *domain.MLRecipe) error
	GetRecipe(context.Context, uuid.UUID) (*domain.MLRecipe, error)
	GetRecipeByNameVersion(context.Context, string, string) (*domain.MLRecipe, error)
	CreateOrUpdateRecipeRun(context.Context, *domain.MLRecipeRun) error
	GetInferenceEndpointByNameEnv(context.Context, string, uuid.UUID) (*domain.MLInferenceEndpoint, error)
	CreateDeploymentIntent(context.Context, *domain.MLDeploymentIntent) error
	GetDeploymentIntent(context.Context, uuid.UUID) (*domain.MLDeploymentIntent, error)
	ApproveDeploymentIntent(context.Context, uuid.UUID) error
	RejectDeploymentIntent(context.Context, uuid.UUID) error
	RollbackWithMetadata(context.Context, uuid.UUID, uuid.UUID, string, map[string]any) (*domain.MLDeploymentIntent, error)
}

var mlOperationNamespace = uuid.MustParse("d744ca11-56ed-4245-a1f7-4296e970922a")

func (h *MLIntentHandler) operationCoordinate(ctx context.Context, op string, content map[string]any) (string, error) {
	ops, ok := h.registry.(mlOperationRegistry)
	if !ok {
		return "", fmt.Errorf("ML operation registry is not configured")
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	switch op {
	case "model-import":
		model, _ := content["model"].(string)
		slug := strings.TrimPrefix(strings.TrimSpace(model), "model:")
		if slug == "" || strings.Contains(slug, ":") {
			return "", fmt.Errorf("model must be model:<slug>")
		}
		return "model:" + slug, nil
	case "recipe-run":
		recipe, err := h.resolveRecipe(ctx, raw, ops)
		if err != nil {
			return "", err
		}
		return "recipe-run:" + recipe.ID.String(), nil
	case "inference-deploy":
		var req mlDeployRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return "", err
		}
		endpoint, err := h.resolveEndpoint(ctx, req.EndpointID, req.Endpoint, ops)
		if err != nil {
			return "", err
		}
		return "inference-deploy:" + endpoint.ID.String(), nil
	case "inference-approval":
		var req struct {
			IntentID uuid.UUID `json:"intent_id"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return "", err
		}
		if req.IntentID == uuid.Nil {
			return "", fmt.Errorf("intent_id is required")
		}
		return "inference-approval:" + req.IntentID.String(), nil
	case "inference-rollback":
		var req struct {
			EndpointID string `json:"endpoint_id"`
			Endpoint   string `json:"endpoint"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return "", err
		}
		endpoint, err := h.resolveEndpoint(ctx, req.EndpointID, req.Endpoint, ops)
		if err != nil {
			return "", err
		}
		return "inference-rollback:" + endpoint.ID.String(), nil
	default:
		return "", fmt.Errorf("unsupported ML operation %q", op)
	}
}

func (h *MLIntentHandler) handleMLOperation(ctx context.Context, intent *Intent, raw []byte) error {
	ops, ok := h.registry.(mlOperationRegistry)
	if !ok {
		return fmt.Errorf("ML operation registry is not configured")
	}
	switch intent.Op {
	case "model-import":
		return h.importModel(ctx, intent, raw, ops)
	case "recipe-apply":
		return h.applyRecipe(ctx, intent, raw, ops)
	case "recipe-run":
		return h.runRecipe(ctx, intent, raw, ops)
	case "inference-deploy":
		return h.deployInference(ctx, intent, raw, ops)
	case "inference-approval":
		return h.approveInference(ctx, intent, raw, ops)
	case "inference-rollback":
		return h.rollbackInference(ctx, intent, raw, ops)
	default:
		return fmt.Errorf("unsupported ML operation %q", intent.Op)
	}
}

func (h *MLIntentHandler) importModel(ctx context.Context, intent *Intent, raw []byte, ops mlOperationRegistry) error {
	var req struct {
		Model        string `json:"model"`
		ModelVersion string `json:"model_version"`
		Name         string `json:"name"`
		Source       string `json:"source"`
		URI          string `json:"uri"`
		SourceURI    string `json:"source_uri"`
		Revision     string `json:"revision"`
		Task         string `json:"task"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	slug := strings.TrimPrefix(strings.TrimSpace(req.Model), "model:")
	if slug == "" || strings.Contains(slug, ":") || intent.Coordinate != "model:"+slug {
		return fmt.Errorf("model-import requires model:<slug> coordinate")
	}
	uri := firstNonEmpty(req.SourceURI, req.URI)
	if strings.TrimSpace(uri) == "" || strings.TrimSpace(req.Source) == "" {
		return fmt.Errorf("model-import requires source and source_uri")
	}
	model, err := ops.GetModelBySlug(ctx, slug)
	if err != nil {
		return err
	}
	if err := checkIntentRevision(intent, slug, model != nil, func() time.Time {
		if model != nil {
			return model.UpdatedAt
		}
		return time.Time{}
	}()); err != nil {
		return err
	}
	source := domain.MLSourceRef{Kind: strings.TrimSpace(req.Source), URI: strings.TrimSpace(uri), Revision: strings.TrimSpace(req.Revision)}
	if model == nil {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = slug
		}
		model = &domain.MLModel{ID: uuid.NewSHA1(mlOperationNamespace, []byte("model:"+slug)), Slug: slug, Name: name, Source: &source}
	} else {
		model.Source = &source
		if strings.TrimSpace(req.Name) != "" {
			model.Name = strings.TrimSpace(req.Name)
		}
	}
	if req.Task != "" {
		model.TaskKinds = []domain.MLTaskKind{domain.MLTaskKind(req.Task)}
	}
	versionName := strings.TrimSpace(req.Revision)
	if req.ModelVersion != "" {
		prefix := "model-version:" + slug + ":"
		if !strings.HasPrefix(req.ModelVersion, prefix) {
			return fmt.Errorf("model_version must match model slug")
		}
		versionName = strings.TrimPrefix(req.ModelVersion, prefix)
	}
	var version *domain.MLModelVersion
	if versionName != "" {
		version, err = ops.GetModelVersionByModelVersion(ctx, model.ID, versionName)
		if err != nil {
			return err
		}
		if version == nil {
			version = &domain.MLModelVersion{ID: uuid.NewSHA1(mlOperationNamespace, []byte("model-version:"+slug+":"+versionName)), ModelID: model.ID, Version: versionName}
		}
		version.Source = source
		if err := domain.ValidateMLModelVersion(version); err != nil {
			return err
		}
	}
	if err := domain.ValidateMLModel(model); err != nil {
		return err
	}
	if err := h.registry.CreateOrUpdateModel(ctx, model); err != nil {
		return err
	}
	if version != nil {
		if err := h.registry.CreateOrUpdateModelVersion(ctx, version); err != nil {
			return err
		}
	}
	intent.StatusData = map[string]any{"model_id": model.ID.String(), "model": "model:" + slug}
	if version != nil {
		intent.StatusData["model_version_id"] = version.ID.String()
	}
	intent.Result = intent.StatusData
	return nil
}

func (h *MLIntentHandler) applyRecipe(ctx context.Context, intent *Intent, raw []byte, ops mlOperationRegistry) error {
	var recipe domain.MLRecipe
	if err := json.Unmarshal(raw, &recipe); err != nil {
		return err
	}
	if recipe.Name == "" || recipe.Version == "" || intent.Coordinate != "recipe:"+recipe.Name+":"+recipe.Version {
		return fmt.Errorf("recipe coordinate must be recipe:<name>:<version>")
	}
	normalized, err := service.ValidateMLRecipeYAML([]byte(recipe.YAML))
	if err != nil {
		return err
	}
	if normalized["name"] != recipe.Name || fmt.Sprint(normalized["version"]) != recipe.Version {
		return fmt.Errorf("recipe YAML name/version must match intent coordinate")
	}
	existing, err := ops.GetRecipeByNameVersion(ctx, recipe.Name, recipe.Version)
	if err != nil {
		return err
	}
	if err := checkIntentRevision(intent, intent.Coordinate, existing != nil, func() time.Time {
		if existing != nil {
			return existing.UpdatedAt
		}
		return time.Time{}
	}()); err != nil {
		return err
	}
	if existing != nil {
		recipe.ID = existing.ID
		recipe.CreatedAt = existing.CreatedAt
	} else {
		recipe.ID = uuid.NewSHA1(mlOperationNamespace, []byte(intent.Coordinate))
	}
	if err := ops.CreateOrUpdateRecipe(ctx, &recipe); err != nil {
		return err
	}
	intent.Result = map[string]any{"recipe_id": recipe.ID.String()}
	return nil
}

func (h *MLIntentHandler) resolveRecipe(ctx context.Context, raw []byte, ops mlOperationRegistry) (*domain.MLRecipe, error) {
	var req mlRecipeRunRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.RecipeID != "" {
		id, err := uuid.Parse(req.RecipeID)
		if err != nil {
			return nil, err
		}
		recipe, err := ops.GetRecipe(ctx, id)
		if err != nil {
			return nil, err
		}
		if recipe == nil {
			return nil, fmt.Errorf("ML recipe %s not found", id)
		}
		return recipe, nil
	}
	parts := strings.SplitN(strings.TrimPrefix(req.Recipe, "recipe:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("recipe coordinate must be recipe:<name>:<version>")
	}
	recipe, err := ops.GetRecipeByNameVersion(ctx, parts[0], parts[1])
	if err != nil {
		return nil, err
	}
	if recipe == nil {
		return nil, fmt.Errorf("ML recipe %q version %q not found", parts[0], parts[1])
	}
	return recipe, nil
}

func (h *MLIntentHandler) runRecipe(ctx context.Context, intent *Intent, raw []byte, ops mlOperationRegistry) error {
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("recipe run does not support expected_updated_at")
	}
	recipe, err := h.resolveRecipe(ctx, raw, ops)
	if err != nil {
		return err
	}
	if intent.Coordinate != "recipe-run:"+recipe.ID.String() {
		return fmt.Errorf("recipe-run coordinate does not match recipe")
	}
	var req mlRecipeRunRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	run := &domain.MLRecipeRun{ID: uuid.NewSHA1(mlOperationNamespace, []byte("recipe-run:"+intent.IntentID)), RecipeID: recipe.ID, RequestedBy: intent.Actor, Status: domain.RunStatusQueued, Inputs: req.Inputs, Parameters: req.Parameters, Metadata: mlIntentMetadata(intent)}
	if err := ops.CreateOrUpdateRecipeRun(ctx, run); err != nil {
		return err
	}
	intent.Result = map[string]any{"run_id": run.ID.String(), "recipe_id": recipe.ID.String(), "status": run.Status}
	return nil
}

func (h *MLIntentHandler) resolveEndpoint(ctx context.Context, idText, coordinate string, ops mlOperationRegistry) (*domain.MLInferenceEndpoint, error) {
	if idText != "" {
		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, err
		}
		endpoint, err := h.registry.GetInferenceEndpoint(ctx, id)
		if err != nil {
			return nil, err
		}
		if endpoint == nil {
			return nil, fmt.Errorf("ML endpoint %s not found", id)
		}
		return endpoint, nil
	}
	name, envRef, ok := strings.Cut(strings.TrimPrefix(coordinate, "endpoint:"), ":")
	if !ok || name == "" || envRef == "" {
		return nil, fmt.Errorf("endpoint coordinate must be endpoint:<name>:<environment>")
	}
	envID, err := uuid.Parse(envRef)
	if err != nil {
		if h.environments == nil {
			return nil, fmt.Errorf("environment name resolution is unavailable")
		}
		env, err := h.environments.GetEnvironmentByName(ctx, envRef)
		if err != nil {
			return nil, err
		}
		if env == nil {
			return nil, fmt.Errorf("environment %q not found", envRef)
		}
		envID = env.ID
	}
	endpoint, err := ops.GetInferenceEndpointByNameEnv(ctx, name, envID)
	if err != nil {
		return nil, err
	}
	if endpoint == nil {
		return nil, fmt.Errorf("ML endpoint %s not found in environment %s", name, envRef)
	}
	return endpoint, nil
}

func (h *MLIntentHandler) resolveVersion(ctx context.Context, idText, coordinate string, ops mlOperationRegistry) (*domain.MLModelVersion, error) {
	if idText != "" {
		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, err
		}
		version, err := h.registry.GetModelVersion(ctx, id)
		if err != nil {
			return nil, err
		}
		if version == nil {
			return nil, fmt.Errorf("ML model version %s not found", id)
		}
		return version, nil
	}
	parts := strings.SplitN(strings.TrimPrefix(coordinate, "model-version:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("model version coordinate must be model-version:<model-slug>:<version>")
	}
	model, err := ops.GetModelBySlug(ctx, parts[0])
	if err != nil {
		return nil, err
	}
	if model == nil {
		return nil, fmt.Errorf("ML model %q not found", parts[0])
	}
	version, err := ops.GetModelVersionByModelVersion(ctx, model.ID, parts[1])
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, fmt.Errorf("ML model version %q not found", coordinate)
	}
	return version, nil
}

func (h *MLIntentHandler) deployInference(ctx context.Context, intent *Intent, raw []byte, ops mlOperationRegistry) error {
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("inference deploy does not support expected_updated_at")
	}
	var req mlDeployRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	endpoint, err := h.resolveEndpoint(ctx, req.EndpointID, req.Endpoint, ops)
	if err != nil {
		return err
	}
	if intent.Coordinate != "inference-deploy:"+endpoint.ID.String() {
		return fmt.Errorf("inference-deploy coordinate does not match endpoint")
	}
	version, err := h.resolveVersion(ctx, req.ModelVersionID, req.ModelVersion, ops)
	if err != nil {
		return err
	}
	deployment := &domain.MLDeploymentIntent{ID: uuid.NewSHA1(mlOperationNamespace, []byte("inference-deploy:"+intent.IntentID)), EndpointID: endpoint.ID, EnvironmentID: endpoint.EnvironmentID, ModelVersionID: version.ID, RequestedBy: intent.Actor, SourceKind: domain.SourceKindEventTriggered, RuntimePreference: domain.MLRuntimeKind(req.RuntimePreference), Metadata: mlIntentMetadata(intent)}
	if err := ops.CreateDeploymentIntent(ctx, deployment); err != nil {
		return err
	}
	intent.Result = map[string]any{"deployment_intent_id": deployment.ID.String(), "endpoint_id": endpoint.ID.String(), "model_version_id": version.ID.String(), "status": deployment.Status}
	return nil
}

func (h *MLIntentHandler) approveInference(ctx context.Context, intent *Intent, raw []byte, ops mlOperationRegistry) error {
	var req struct {
		IntentID uuid.UUID `json:"intent_id"`
		Decision string    `json:"decision"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	if req.IntentID == uuid.Nil || intent.Coordinate != "inference-approval:"+req.IntentID.String() {
		return fmt.Errorf("inference-approval coordinate does not match intent_id")
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		return fmt.Errorf("decision must be approve or reject")
	}
	current, err := ops.GetDeploymentIntent(ctx, req.IntentID)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("ML deployment intent %s not found", req.IntentID)
	}
	if err := checkIntentRevision(intent, req.IntentID.String(), true, current.UpdatedAt); err != nil {
		return err
	}
	if req.Decision == "approve" {
		err = ops.ApproveDeploymentIntent(ctx, req.IntentID)
	} else {
		err = ops.RejectDeploymentIntent(ctx, req.IntentID)
	}
	if err != nil {
		return err
	}
	intent.Result = map[string]any{"deployment_intent_id": req.IntentID.String(), "decision": req.Decision}
	return nil
}

func (h *MLIntentHandler) rollbackInference(ctx context.Context, intent *Intent, raw []byte, ops mlOperationRegistry) error {
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("inference rollback does not support expected_updated_at")
	}
	var req struct {
		EndpointID  string `json:"endpoint_id"`
		Endpoint    string `json:"endpoint"`
		RequestedBy string `json:"requested_by"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	if req.RequestedBy != "" && req.RequestedBy != intent.Actor {
		return fmt.Errorf("requested_by must match intent actor")
	}
	endpoint, err := h.resolveEndpoint(ctx, req.EndpointID, req.Endpoint, ops)
	if err != nil {
		return err
	}
	if intent.Coordinate != "inference-rollback:"+endpoint.ID.String() {
		return fmt.Errorf("inference-rollback coordinate does not match endpoint")
	}
	deployment, err := ops.RollbackWithMetadata(ctx, endpoint.ID, endpoint.EnvironmentID, intent.Actor, mlIntentMetadata(intent))
	if err != nil {
		return err
	}
	intent.Result = map[string]any{"deployment_intent_id": deployment.ID.String(), "endpoint_id": endpoint.ID.String(), "model_version_id": deployment.ModelVersionID.String(), "status": deployment.Status}
	return nil
}

func mlIntentMetadata(intent *Intent) map[string]any {
	metadata := map[string]any{"intent_id": intent.IntentID, "requester_pubkey": intent.Actor}
	if intent.Event != nil {
		metadata["nostr_event_id"] = intent.Event.ID.Hex()
	}
	return metadata
}

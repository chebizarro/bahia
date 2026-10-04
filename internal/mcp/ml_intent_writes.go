package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

// ML command tools retain their public arguments, but resolve named references
// against the signed canonical read models before submitting D79 intents.
func (s *Server) mlOperationIntentWrite(ctx context.Context, name string, args map[string]interface{}, intentID string) (intentWrite, error) {
	w := intentWrite{domain: "ml", content: map[string]any{}}
	var err error
	switch name {
	case "bahia_ml_import_model":
		model := strings.TrimSpace(stringArg(args, "model"))
		slug := strings.TrimPrefix(model, "model:")
		if !strings.HasPrefix(model, "model:") || slug == "" || strings.Contains(slug, ":") {
			return w, fmt.Errorf("model must be model:<slug>")
		}
		w.op, w.coordinate = "model-import", model
		w.family, w.stateKey, w.stateValue = nostrpool.KindMLModelRegistry, "slug", slug
		w.orgID, err = optionalMLIntentOrg(args)
		if err != nil {
			return w, err
		}
		copyMCPIntentArgs(w.content, args, "model", "model_version", "name", "source", "revision", "task", "expected_updated_at")
		uri := strings.TrimSpace(stringArg(args, "source_uri"))
		if uri == "" {
			uri = strings.TrimSpace(stringArg(args, "uri"))
		}
		if uri == "" {
			return w, fmt.Errorf("source_uri or uri is required")
		}
		w.content["source_uri"] = uri
	case "bahia_ml_run_recipe":
		id, e := s.mlRecipeID(ctx, args)
		if e != nil {
			return w, e
		}
		w.op, w.coordinate, w.statusOnly = "recipe-run", "recipe-run:"+id.String(), true
		w.orgID, err = optionalMLIntentOrg(args)
		if err != nil {
			return w, err
		}
		w.content["recipe_id"] = id.String()
		copyMCPIntentArgs(w.content, args, "inputs", "parameters")
	case "bahia_ml_deploy", "bahia_assistant_ml_deploy", "bahia_ml_rollback", "bahia_assistant_ml_rollback":
		id, e := s.mlEndpointID(ctx, args)
		if e != nil {
			return w, e
		}
		w.orgID, err = s.intentOrgFromState(ctx, args, nostrpool.KindMLInferenceEndpointRegistry, "id", id.String())
		if err != nil {
			return w, err
		}
		w.content["endpoint_id"] = id.String()
		w.statusOnly = true
		if name == "bahia_ml_rollback" || name == "bahia_assistant_ml_rollback" {
			w.op, w.coordinate = "inference-rollback", "inference-rollback:"+id.String()
			copyMCPIntentArgs(w.content, args, "requested_by")
		} else {
			versionID, e := s.mlVersionID(ctx, args)
			if e != nil {
				return w, e
			}
			w.op, w.coordinate = "inference-deploy", "inference-deploy:"+id.String()
			w.content["model_version_id"] = versionID.String()
			copyMCPIntentArgs(w.content, args, "runtime_preference")
			if w.content["runtime_preference"] == nil && stringArg(args, "runtime") != "" {
				w.content["runtime_preference"] = stringArg(args, "runtime")
			}
		}
	case "bahia_assistant_ml_approve_deployment":
		id, e := parseRequiredUUIDArg(args, "intent_id")
		if e != nil {
			return w, e
		}
		decision := strings.ToLower(strings.TrimSpace(stringArg(args, "decision")))
		if decision != "approve" && decision != "reject" {
			return w, fmt.Errorf("decision must be approve or reject")
		}
		w.op, w.coordinate, w.statusOnly = "inference-approval", "inference-approval:"+id.String(), true
		w.orgID, err = optionalMLIntentOrg(args)
		if err != nil {
			return w, err
		}
		w.content["intent_id"], w.content["decision"] = id.String(), decision
		copyMCPIntentArgs(w.content, args, "expected_updated_at")
	default:
		return w, fmt.Errorf("unsupported ML intent tool %q", name)
	}
	return w, nil
}

func optionalMLIntentOrg(args map[string]any) (uuid.UUID, error) {
	if strings.TrimSpace(stringArg(args, "org_id")) == "" {
		return uuid.Nil, nil // ML operations are fleet-scoped, not tenant-scoped.
	}
	return parseRequiredUUIDArg(args, "org_id")
}

func copyMCPIntentArgs(content, args map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := args[key]; ok {
			content[key] = value
		}
	}
}

func (s *Server) mlRecipeID(ctx context.Context, args map[string]any) (uuid.UUID, error) {
	if stringArg(args, "recipe_id") != "" {
		return parseRequiredUUIDArg(args, "recipe_id")
	}
	coordinate := strings.TrimPrefix(strings.TrimSpace(stringArg(args, "recipe")), "recipe:")
	name, version, ok := strings.Cut(coordinate, ":")
	if !ok || name == "" || version == "" {
		return uuid.Nil, fmt.Errorf("recipe must be recipe:<name>:<version> or recipe_id must be supplied")
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindMLRecipeRegistry)
	if err != nil {
		return uuid.Nil, err
	}
	for _, record := range records {
		if record.Fields["name"] == name && record.Fields["version"] == version {
			return uuid.Parse(stringFromRecord(record.Fields, "id"))
		}
	}
	return uuid.Nil, fmt.Errorf("canonical ML recipe %s not found", coordinate)
}

func (s *Server) mlEndpointID(ctx context.Context, args map[string]any) (uuid.UUID, error) {
	if stringArg(args, "endpoint_id") != "" {
		return parseRequiredUUIDArg(args, "endpoint_id")
	}
	coordinate := strings.TrimPrefix(strings.TrimSpace(stringArg(args, "endpoint")), "endpoint:")
	name, environment, ok := strings.Cut(coordinate, ":")
	if !ok || name == "" || environment == "" {
		return uuid.Nil, fmt.Errorf("endpoint must be endpoint:<name>:<environment> or endpoint_id must be supplied")
	}
	environmentID, err := uuid.Parse(environment)
	if err != nil {
		records, e := s.readStateFamily(ctx, nostrpool.KindEnvironmentRegistry)
		if e != nil {
			return uuid.Nil, e
		}
		for _, record := range records {
			if record.Fields["name"] == environment {
				environmentID, err = uuid.Parse(stringFromRecord(record.Fields, "id"))
				break
			}
		}
		if err != nil {
			return uuid.Nil, fmt.Errorf("canonical environment %s not found", environment)
		}
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindMLInferenceEndpointRegistry)
	if err != nil {
		return uuid.Nil, err
	}
	for _, record := range records {
		if record.Fields["name"] == name && record.Fields["environment_id"] == environmentID.String() {
			return uuid.Parse(stringFromRecord(record.Fields, "id"))
		}
	}
	return uuid.Nil, fmt.Errorf("canonical ML endpoint %s not found", coordinate)
}

func (s *Server) mlVersionID(ctx context.Context, args map[string]any) (uuid.UUID, error) {
	if stringArg(args, "model_version_id") != "" {
		return parseRequiredUUIDArg(args, "model_version_id")
	}
	coordinate := strings.TrimPrefix(strings.TrimSpace(stringArg(args, "model_version")), "model-version:")
	slug, version, ok := strings.Cut(coordinate, ":")
	if !ok || slug == "" || version == "" {
		return uuid.Nil, fmt.Errorf("model_version must be model-version:<slug>:<version> or model_version_id must be supplied")
	}
	model, err := s.readStateOne(ctx, nostrpool.KindMLModelRegistry, "slug", slug)
	if err != nil {
		return uuid.Nil, err
	}
	if model == nil {
		return uuid.Nil, fmt.Errorf("canonical ML model %s not found", slug)
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindMLModelVersionRegistry)
	if err != nil {
		return uuid.Nil, err
	}
	for _, record := range records {
		if record.Fields["model_id"] == model.Fields["id"] && record.Fields["version"] == version {
			return uuid.Parse(stringFromRecord(record.Fields, "id"))
		}
	}
	return uuid.Nil, fmt.Errorf("canonical ML version %s not found", coordinate)
}

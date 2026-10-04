package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

func packageToolDefinitions() []Tool {
	return []Tool{
		{Name: "bahia_package_repository_apply", Description: "Submit a signed Nostr request to create/update a package repository", InputSchema: packageSchema([]string{"name", "format", "backend_ref"})},
		{Name: "bahia_package_repository_delete", Description: "Submit a signed Nostr request to delete a package repository", InputSchema: packageSchema(nil)},
		{Name: "bahia_package_upload", Description: "Submit a signed Nostr package publication intent using source_url bytes", InputSchema: packageSchema([]string{"package_name", "version", "filename", "source_url", "sha256", "size_bytes"})},
		{Name: "bahia_package_promote", Description: "Submit a signed Nostr package promotion request", InputSchema: packageSchema([]string{"target_repository_name", "package_name", "version", "filename"})},
		{Name: "bahia_package_yank", Description: "Submit a signed Nostr yank/deprecate request for a package artifact", InputSchema: packageSchema([]string{"package_name", "version", "filename"})},
		{Name: "bahia_package_drift_detect", Description: "Submit a signed Nostr package drift-detection request", InputSchema: packageSchema(nil)},
		{Name: "bahia_package_list", Description: "List package repositories or artifacts from Nostr-derived projections", InputSchema: packageSchema(nil)},
		{Name: "bahia_package_get", Description: "Get a package repository or artifact from Nostr-derived projections", InputSchema: packageSchema(nil)},
		{Name: "bahia_package_status", Description: "Get package intent/promotion status from Nostr-derived projections", InputSchema: packageSchema(nil)},
	}
}

func packageSchema(required []string) map[string]interface{} {
	props := map[string]interface{}{
		"repository_id":            map[string]interface{}{"type": "string"},
		"repository_name":          map[string]interface{}{"type": "string"},
		"name":                     map[string]interface{}{"type": "string"},
		"format":                   map[string]interface{}{"type": "string", "enum": []string{"npm", "pypi", "conan", "deb", "rpm", "pub", "go_modules", "gradle"}},
		"backend_ref":              map[string]interface{}{"type": "string"},
		"backend_type":             map[string]interface{}{"type": "string", "enum": []string{"nexus", "pulp", "filesystem_mock"}},
		"external_repository_name": map[string]interface{}{"type": "string"},
		"namespace":                map[string]interface{}{"type": "string"},
		"package_name":             map[string]interface{}{"type": "string"},
		"version":                  map[string]interface{}{"type": "string"},
		"filename":                 map[string]interface{}{"type": "string"},
		"source_url":               map[string]interface{}{"type": "string"},
		"sha256":                   map[string]interface{}{"type": "string"},
		"size_bytes":               map[string]interface{}{"type": "integer"},
		"content_type":             map[string]interface{}{"type": "string"},
		"target_repository_id":     map[string]interface{}{"type": "string"},
		"target_repository_name":   map[string]interface{}{"type": "string"},
		"environment":              map[string]interface{}{"type": "string"},
		"channel":                  map[string]interface{}{"type": "string"},
		"approved_by":              map[string]interface{}{"type": "string"},
		"policy_ref":               map[string]interface{}{"type": "string"},
		"reason":                   map[string]interface{}{"type": "string"},
		"deprecated":               map[string]interface{}{"type": "boolean"},
		"include_artifacts":        map[string]interface{}{"type": "boolean"},
		"include_deleted":          map[string]interface{}{"type": "boolean"},
		"intent_id":                map[string]interface{}{"type": "string"},
		"request_event_id":         map[string]interface{}{"type": "string"},
		"metadata":                 map[string]interface{}{"type": "object"},
		"policy":                   map[string]interface{}{"type": "object"},
	}
	schema := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (s *Server) handlePackageRepositoryApply(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_package_repository_apply", args)
}

func (s *Server) handlePackageRepositoryDelete(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_package_repository_delete", args)
}

func (s *Server) handlePackageUpload(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_package_upload", args)
}

func (s *Server) handlePackagePromote(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_package_promote", args)
}

func (s *Server) handlePackageYank(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_package_yank", args)
}

func (s *Server) handlePackageDriftDetect(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_package_drift_detect", args)
}

// Phase 5 P1: no canonical family yet — see bahia-irsry.13.11. Needs PackageIntentState.
func (s *Server) handlePackageStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.packageProjection == nil {
		return errorResult("package projection repository is not configured"), nil
	}
	if id := optionalUUIDArg(args, "intent_id"); id != uuid.Nil {
		intent, err := s.packageProjection.GetIntent(ctx, id)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		if intent == nil {
			return errorResult("package intent not found"), nil
		}
		return jsonResult(intent)
	}
	if requestID := stringArg(args, "request_event_id"); requestID != "" {
		intent, err := s.packageProjection.GetIntentByRequestEventID(ctx, requestID)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		if intent == nil {
			return errorResult("package intent not found"), nil
		}
		return jsonResult(intent)
	}
	return errorResult("intent_id or request_event_id is required"), nil
}

func packagePolicyArg(args map[string]interface{}) (domain.PackageRepositoryPolicy, error) {
	var policy domain.PackageRepositoryPolicy
	raw, ok := args["policy"]
	if !ok || raw == nil {
		return policy, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return policy, err
	}
	if err := json.Unmarshal(b, &policy); err != nil {
		return policy, fmt.Errorf("policy must match package repository policy schema: %w", err)
	}
	return policy, nil
}

func lookupPackageProjectionRepository(ctx context.Context, repo repository.PackageControlPlaneRepository, id uuid.UUID, name string) (*domain.PackageRepository, error) {
	if id != uuid.Nil {
		out, err := repo.GetRepository(ctx, id)
		if err != nil {
			return nil, err
		}
		if out != nil {
			return out, nil
		}
	}
	if name != "" {
		out, err := repo.GetRepositoryByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if out != nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("package repository not found")
}

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
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

func (s *Server) requirePackageCommands() (PackageCommandPublisher, *ToolResult) {
	if s.packageCommands == nil {
		return nil, errorResult("package command publisher is not configured")
	}
	return s.packageCommands, nil
}

func (s *Server) handlePackageRepositoryApply(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requirePackageCommands()
	if errResult != nil {
		return errResult, nil
	}
	policy, err := packagePolicyArg(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishPackageRepositoryApplyRequest(ctx, controlplane.PackageRepositoryApplyCommand{RepositoryID: optionalUUIDArg(args, "repository_id"), Name: stringArg(args, "name"), Format: domain.PackageRepositoryFormat(stringArg(args, "format")), BackendRef: stringArg(args, "backend_ref"), BackendType: domain.PackageBackendType(stringArg(args, "backend_type")), ExternalRepositoryName: stringArg(args, "external_repository_name"), Description: stringArg(args, "description"), NamespacePrefix: stringArg(args, "namespace_prefix"), Policy: policy, Metadata: metadata})
	return packageReceiptResult(receipt, err)
}

func (s *Server) handlePackageRepositoryDelete(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requirePackageCommands()
	if errResult != nil {
		return errResult, nil
	}
	receipt, err := publisher.PublishPackageRepositoryDeleteRequest(ctx, controlplane.PackageRepositoryDeleteCommand{RepositoryID: optionalUUIDArg(args, "repository_id"), RepositoryName: stringArg(args, "repository_name"), Force: boolArg(args, "force"), Reason: stringArg(args, "reason")})
	return packageReceiptResult(receipt, err)
}

func (s *Server) handlePackageUpload(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requirePackageCommands()
	if errResult != nil {
		return errResult, nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishPackagePublishRequest(ctx, controlplane.PackagePublishCommand{RepositoryID: optionalUUIDArg(args, "repository_id"), RepositoryName: stringArg(args, "repository_name"), Namespace: stringArg(args, "namespace"), PackageName: stringArg(args, "package_name"), Version: stringArg(args, "version"), Filename: stringArg(args, "filename"), SourceURL: stringArg(args, "source_url"), SHA256: stringArg(args, "sha256"), SizeBytes: int64Arg(args, "size_bytes"), ContentType: stringArg(args, "content_type"), ApprovedBy: stringArg(args, "approved_by"), PolicyRef: stringArg(args, "policy_ref"), Metadata: metadata})
	return packageReceiptResult(receipt, err)
}

func (s *Server) handlePackagePromote(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requirePackageCommands()
	if errResult != nil {
		return errResult, nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishPackagePromotionRequest(ctx, controlplane.PackagePromotionCommand{SourceRepositoryID: optionalUUIDArg(args, "source_repository_id"), SourceRepositoryName: firstNonEmpty(stringArg(args, "source_repository_name"), stringArg(args, "repository_name")), TargetRepositoryID: optionalUUIDArg(args, "target_repository_id"), TargetRepositoryName: stringArg(args, "target_repository_name"), Namespace: stringArg(args, "namespace"), PackageName: stringArg(args, "package_name"), Version: stringArg(args, "version"), Filename: stringArg(args, "filename"), Environment: stringArg(args, "environment"), Channel: stringArg(args, "channel"), ApprovedBy: stringArg(args, "approved_by"), PolicyRef: stringArg(args, "policy_ref"), Metadata: metadata})
	return packageReceiptResult(receipt, err)
}

func (s *Server) handlePackageYank(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requirePackageCommands()
	if errResult != nil {
		return errResult, nil
	}
	metadata, err := optionalMapArg(args, "metadata")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishPackageYankRequest(ctx, controlplane.PackageYankCommand{RepositoryID: optionalUUIDArg(args, "repository_id"), RepositoryName: stringArg(args, "repository_name"), Namespace: stringArg(args, "namespace"), PackageName: stringArg(args, "package_name"), Version: stringArg(args, "version"), Filename: stringArg(args, "filename"), Reason: stringArg(args, "reason"), Deprecated: boolArg(args, "deprecated"), Metadata: metadata})
	return packageReceiptResult(receipt, err)
}

func (s *Server) handlePackageDriftDetect(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requirePackageCommands()
	if errResult != nil {
		return errResult, nil
	}
	receipt, err := publisher.PublishPackageDriftDetectRequest(ctx, controlplane.PackageDriftDetectCommand{RepositoryID: optionalUUIDArg(args, "repository_id"), RepositoryName: stringArg(args, "repository_name"), IncludeArtifacts: boolArg(args, "include_artifacts")})
	return packageReceiptResult(receipt, err)
}

func (s *Server) handlePackageList(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.packageProjection == nil {
		return errorResult("package projection repository is not configured"), nil
	}
	if repoID := optionalUUIDArg(args, "repository_id"); repoID != uuid.Nil {
		artifacts, err := s.packageProjection.ListArtifacts(ctx, repoID, optionalIntArg(args, "limit", 100), optionalIntArg(args, "offset", 0))
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{"artifacts": artifacts})
	}
	repos, err := s.packageProjection.ListRepositories(ctx, boolArg(args, "include_deleted"))
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(map[string]any{"repositories": repos})
}

func (s *Server) handlePackageGet(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.packageProjection == nil {
		return errorResult("package projection repository is not configured"), nil
	}
	repo, err := lookupPackageProjectionRepository(ctx, s.packageProjection, optionalUUIDArg(args, "repository_id"), firstNonEmpty(stringArg(args, "repository_name"), stringArg(args, "name")))
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if stringArg(args, "package_name") == "" {
		return jsonResult(repo)
	}
	artifact, err := s.packageProjection.GetArtifact(ctx, repo.ID, stringArg(args, "namespace"), stringArg(args, "package_name"), stringArg(args, "version"), stringArg(args, "filename"))
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if artifact == nil {
		return errorResult("package artifact not found"), nil
	}
	return jsonResult(artifact)
}

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

func packageReceiptResult(receipt *controlplane.PackageCommandReceipt, err error) (*ToolResult, error) {
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(map[string]any{"status": "submitted", "request_event_id": receipt.RequestEventID, "request_pubkey": receipt.RequestPubkey, "request_kind": receipt.RequestKind, "status_kind": receipt.StatusKind, "result_kind": receipt.ResultKind, "repository_registry_kind": receipt.RepositoryRegistryKind, "artifact_registry_kind": receipt.ArtifactRegistryKind, "promotion_registry_kind": receipt.PromotionRegistryKind, "drift_event_kind": receipt.DriftEventKind, "published_relays": receipt.PublishedRelays, "repository_id": receipt.RepositoryID, "repository_name": receipt.RepositoryName, "package_name": receipt.PackageName, "version": receipt.Version, "filename": receipt.Filename})
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

func stringArg(args map[string]interface{}, name string) string {
	if v, ok := args[name].(string); ok {
		return v
	}
	return ""
}

func boolArg(args map[string]interface{}, name string) bool {
	if v, ok := args[name].(bool); ok {
		return v
	}
	return false
}

func optionalUUIDArg(args map[string]interface{}, name string) uuid.UUID {
	value := stringArg(args, name)
	if value == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func int64Arg(args map[string]interface{}, name string) int64 {
	switch v := args[name].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

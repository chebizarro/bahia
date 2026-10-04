package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
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

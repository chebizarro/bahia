package mcp

import (
	"context"

	"github.com/google/uuid"
)

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

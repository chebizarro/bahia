package mcp

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func (s *Server) handleMLListState(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).MLRegistry == nil {
		return errorResult("ML registry is not configured"), nil
	}
	states, err := legacyFor(s).MLRegistry.ListInferenceStates(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list ML state: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"states": states, "total": len(states), "read_model_kind": kinds.MLInferenceEndpointState})
}

func (s *Server) handleMLGetState(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).MLRegistry == nil {
		return errorResult("ML registry is not configured"), nil
	}
	endpointID, err := parseRequiredUUIDArg(args, "endpoint_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	envID, err := parseRequiredUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	state, err := legacyFor(s).MLRegistry.GetInferenceState(ctx, endpointID, envID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get ML state: %v", err)), nil
	}
	if state == nil {
		return errorResult("ML state not found"), nil
	}
	return jsonResult(map[string]interface{}{"state": state, "read_model_kind": kinds.MLInferenceEndpointState})
}

func (s *Server) handleMLGetProvenance(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).MLRegistry == nil {
		return errorResult("ML registry is not configured"), nil
	}
	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	artifact, err := legacyFor(s).MLRegistry.GetArtifactRef(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get ML artifact: %v", err)), nil
	}
	if artifact == nil {
		return errorResult("ML artifact not found"), nil
	}
	edges, err := legacyFor(s).MLRegistry.ListProvenanceEdgesByArtifact(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list ML provenance: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"artifact": artifact, "edges": edges, "read_model_kind": controlplane.KindMLArtifactProvenanceGraph})
}

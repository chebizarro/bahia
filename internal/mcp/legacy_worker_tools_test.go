package mcp

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func (s *Server) handleWorkerGetAssignments(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).WorkerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	state, err := legacyFor(s).WorkerReadModels.GetAssignmentState(ctx, stringArg(args, "worker_pubkey"))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get worker assignments: %v", err)), nil
	}
	if state == nil {
		return errorResult("worker not found"), nil
	}
	return jsonResult(map[string]interface{}{"assignment_state": state, "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerAssignmentTopic})
}

func (s *Server) handleWorkerListAssignments(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).WorkerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	workers, err := legacyFor(s).Workers.List(ctx, "", 1000)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list worker assignments: %v", err)), nil
	}
	states := make([]domain.WorkerAssignmentState, 0, len(workers))
	for _, worker := range workers {
		state, err := legacyFor(s).WorkerReadModels.GetAssignmentState(ctx, worker.PubKey)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to get worker assignments: %v", err)), nil
		}
		if state != nil {
			states = append(states, *state)
		}
	}
	return jsonResult(map[string]interface{}{"assignment_states": states, "total": len(states), "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerAssignmentTopic})
}

func (s *Server) handleWorkerGetDrainStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).WorkerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	status, err := legacyFor(s).WorkerReadModels.GetDrainStatus(ctx, stringArg(args, "worker_pubkey"))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get worker drain status: %v", err)), nil
	}
	if status == nil {
		return errorResult("worker not found"), nil
	}
	return jsonResult(map[string]interface{}{"drain_status": status, "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerDrainTopic})
}

func (s *Server) handleWorkerListDrainStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).WorkerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	workers, err := legacyFor(s).Workers.List(ctx, "", 1000)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list worker drain statuses: %v", err)), nil
	}
	statuses := make([]domain.WorkerDrainStatus, 0, len(workers))
	for _, worker := range workers {
		status, err := legacyFor(s).WorkerReadModels.GetDrainStatus(ctx, worker.PubKey)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to get worker drain status: %v", err)), nil
		}
		if status != nil {
			statuses = append(statuses, *status)
		}
	}
	return jsonResult(map[string]interface{}{"drain_statuses": statuses, "total": len(statuses), "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerDrainTopic})
}

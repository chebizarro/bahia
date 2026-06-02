package mcp

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

const assistantAgentID = "bahia-operator-assistant"

func assistantAsyncToolDefinitions() []Tool {
	obj := func(props map[string]interface{}, required ...string) map[string]interface{} {
		s := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	id := map[string]interface{}{"type": "string"}
	return []Tool{
		{Name: "bahia_assistant_service_deploy", Description: "Assistant-safe async service deploy command (ContextVM service/deploy intent)", InputSchema: obj(map[string]interface{}{"service_id": id, "environment_id": id, "artifact_id": id, "idempotency_key": id}, "service_id", "environment_id", "artifact_id", "idempotency_key")},
		{Name: "bahia_assistant_service_rollback", Description: "Assistant-safe async service rollback command (ContextVM service/rollback intent)", InputSchema: obj(map[string]interface{}{"service_id": id, "environment_id": id, "idempotency_key": id}, "service_id", "environment_id", "idempotency_key")},
		{Name: "bahia_assistant_llm_deploy", Description: "Assistant-safe async LLM deploy command (ContextVM tools/call intent)", InputSchema: obj(map[string]interface{}{"route_id": id, "environment_id": id, "release_id": id, "requested_by": id, "idempotency_key": id}, "route_id", "environment_id", "release_id", "idempotency_key")},
		{Name: "bahia_assistant_llm_approve_deployment", Description: "Assistant-safe async LLM approval command (ContextVM approval/approve intent)", InputSchema: obj(map[string]interface{}{"intent_id": id, "decision": id, "idempotency_key": id}, "intent_id", "decision", "idempotency_key")},
		{Name: "bahia_assistant_llm_rollback", Description: "Assistant-safe async LLM rollback command (ContextVM tools/call intent)", InputSchema: obj(map[string]interface{}{"route_id": id, "environment_id": id, "requested_by": id, "idempotency_key": id}, "route_id", "environment_id", "idempotency_key")},
		{Name: "bahia_assistant_ml_deploy", Description: "Assistant-safe async ML deploy command (ContextVM tools/call intent)", InputSchema: obj(map[string]interface{}{"endpoint": id, "endpoint_id": id, "model_version": id, "model_version_id": id, "runtime_preference": id, "runtime": id, "placement": map[string]interface{}{"type": "object"}, "tags": map[string]interface{}{"type": "object"}, "idempotency_key": id}, "idempotency_key")},
		{Name: "bahia_assistant_ml_approve_deployment", Description: "Assistant-safe async ML approval command (ContextVM approval/approve intent)", InputSchema: obj(map[string]interface{}{"intent_id": id, "decision": id, "tags": map[string]interface{}{"type": "object"}, "idempotency_key": id}, "intent_id", "decision", "idempotency_key")},
		{Name: "bahia_assistant_ml_rollback", Description: "Assistant-safe async ML rollback command (ContextVM tools/call intent)", InputSchema: obj(map[string]interface{}{"endpoint": id, "endpoint_id": id, "requested_by": id, "tags": map[string]interface{}{"type": "object"}, "idempotency_key": id}, "idempotency_key")},
	}
}

func (s *Server) handleAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	receipt, err := s.InvokeAssistantAsyncTool(ctx, name, args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(receipt)
}

func (s *Server) InvokeAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	key, _ := args["idempotency_key"].(string)
	if key == "" {
		return nil, fmt.Errorf("idempotency_key is required")
	}
	switch name {
	case "bahia_assistant_service_deploy":
		if s.serviceCommands == nil {
			return nil, fmt.Errorf("service command publisher is not configured")
		}
		sid, err := parseRequiredUUIDArg(args, "service_id")
		if err != nil {
			return nil, err
		}
		eid, err := parseRequiredUUIDArg(args, "environment_id")
		if err != nil {
			return nil, err
		}
		aid, err := parseRequiredUUIDArg(args, "artifact_id")
		if err != nil {
			return nil, err
		}
		r, err := s.serviceCommands.PublishDeployRequest(ctx, controlplane.ServiceDeployCommand{ServiceID: sid, EnvironmentID: eid, ArtifactID: aid, IdempotencyKey: key, AgentID: assistantAgentID})
		if err != nil {
			return nil, err
		}
		return serviceReceipt(name, key, r), nil
	case "bahia_assistant_service_rollback":
		if s.serviceCommands == nil {
			return nil, fmt.Errorf("service command publisher is not configured")
		}
		sid, err := parseRequiredUUIDArg(args, "service_id")
		if err != nil {
			return nil, err
		}
		eid, err := parseRequiredUUIDArg(args, "environment_id")
		if err != nil {
			return nil, err
		}
		r, err := s.serviceCommands.PublishRollbackRequest(ctx, controlplane.ServiceRollbackCommand{ServiceID: sid, EnvironmentID: eid, IdempotencyKey: key, AgentID: assistantAgentID})
		if err != nil {
			return nil, err
		}
		return serviceReceipt(name, key, r), nil
	case "bahia_assistant_llm_deploy":
		if s.llmCommands == nil {
			return nil, fmt.Errorf("LLM command publisher is not configured")
		}
		routeID, err := parseRequiredUUIDArg(args, "route_id")
		if err != nil {
			return nil, err
		}
		envID, err := parseRequiredUUIDArg(args, "environment_id")
		if err != nil {
			return nil, err
		}
		relID, err := parseRequiredUUIDArg(args, "release_id")
		if err != nil {
			return nil, err
		}
		req, _ := args["requested_by"].(string)
		r, err := s.llmCommands.PublishLLMDeployRequest(ctx, controlplane.LLMDeployCommand{RouteID: routeID, EnvironmentID: envID, ReleaseID: relID, RequestedBy: req, IdempotencyKey: key, AgentID: assistantAgentID})
		if err != nil {
			return nil, err
		}
		return llmReceipt(name, key, r), nil
	case "bahia_assistant_llm_approve_deployment":
		return s.invokeAssistantLLMApproval(ctx, name, args, key)
	case "bahia_assistant_llm_rollback":
		if s.llmCommands == nil {
			return nil, fmt.Errorf("LLM command publisher is not configured")
		}
		routeID, err := parseRequiredUUIDArg(args, "route_id")
		if err != nil {
			return nil, err
		}
		envID, err := parseRequiredUUIDArg(args, "environment_id")
		if err != nil {
			return nil, err
		}
		req, _ := args["requested_by"].(string)
		r, err := s.llmCommands.PublishLLMRollbackRequest(ctx, controlplane.LLMRollbackCommand{RouteID: routeID, EnvironmentID: envID, RequestedBy: req, IdempotencyKey: key, AgentID: assistantAgentID})
		if err != nil {
			return nil, err
		}
		return llmReceipt(name, key, r), nil
	case "bahia_assistant_ml_deploy", "bahia_assistant_ml_approve_deployment", "bahia_assistant_ml_rollback":
		return s.invokeAssistantML(ctx, name, args, key)
	default:
		return nil, fmt.Errorf("assistant tool %q is not allowlisted", name)
	}
}

func (s *Server) invokeAssistantLLMApproval(ctx context.Context, name string, args map[string]interface{}, key string) (*domain.AsyncToolReceipt, error) {
	if s.llmCommands == nil {
		return nil, fmt.Errorf("LLM command publisher is not configured")
	}
	intentID, err := parseRequiredUUIDArg(args, "intent_id")
	if err != nil {
		return nil, err
	}
	decision, _ := args["decision"].(string)
	if decision == "" {
		decision = "approve"
	}
	r, err := s.llmCommands.PublishLLMApprovalRequest(ctx, controlplane.LLMApprovalCommand{IntentID: intentID, Decision: decision, IdempotencyKey: key, AgentID: assistantAgentID})
	if err != nil {
		return nil, err
	}
	return llmReceipt(name, key, r), nil
}

func (s *Server) invokeAssistantML(ctx context.Context, name string, args map[string]interface{}, key string) (*domain.AsyncToolReceipt, error) {
	if s.mlCommands == nil {
		return nil, fmt.Errorf("ML command publisher is not configured")
	}
	payload := mlPayloadFromArgs(args)
	payload.IdempotencyKey = key
	if payload.Tags == nil {
		payload.Tags = map[string]string{}
	}
	payload.Tags["agent"] = assistantAgentID
	var r *controlplane.MLCommandReceipt
	var err error
	switch name {
	case "bahia_assistant_ml_deploy":
		r, err = s.mlCommands.PublishMLInferenceDeployRequest(ctx, payload)
	case "bahia_assistant_ml_approve_deployment":
		publisher, ok := s.mlCommands.(interface {
			PublishMLInferenceApprovalRequest(context.Context, controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
		})
		if !ok {
			return nil, fmt.Errorf("ML approval command publisher is not configured")
		}
		r, err = publisher.PublishMLInferenceApprovalRequest(ctx, payload)
	case "bahia_assistant_ml_rollback":
		r, err = s.mlCommands.PublishMLInferenceRollbackRequest(ctx, payload)
	}
	if err != nil {
		return nil, err
	}
	return mlAsyncReceipt(name, key, r), nil
}

func serviceReceipt(tool, key string, r *controlplane.ServiceCommandReceipt) *domain.AsyncToolReceipt {
	return &domain.AsyncToolReceipt{ToolName: tool, RequestEventID: r.RequestEventID, RequestKind: r.RequestKind, StatusKinds: []int{r.StatusKind}, ResultKinds: []int{r.ResultKind}, ReadModelKinds: []int{controlplane.KindServiceState}, DTag: r.DTag, IdempotencyKey: key, PublishedRelays: []string{fmt.Sprint(r.PublishedRelays)}, ResourceTags: map[string]string{"service": r.ServiceID, "environment": r.EnvironmentID, "artifact": r.ArtifactID}}
}
func llmReceipt(tool, key string, r *controlplane.LLMCommandReceipt) *domain.AsyncToolReceipt {
	return &domain.AsyncToolReceipt{ToolName: tool, RequestEventID: r.RequestEventID, RequestKind: r.RequestKind, StatusKinds: []int{r.StatusKind}, ResultKinds: []int{r.ResultKind}, ReadModelKinds: []int{r.RegistryKind, r.StateKind}, DTag: key, IdempotencyKey: key, PublishedRelays: []string{fmt.Sprint(r.PublishedRelays)}, ResourceTags: map[string]string{"route": r.RouteID, "environment": r.EnvironmentID, "release": r.ReleaseID, "intent": r.IntentID}}
}
func mlAsyncReceipt(tool, key string, r *controlplane.MLCommandReceipt) *domain.AsyncToolReceipt {
	return &domain.AsyncToolReceipt{ToolName: tool, RequestEventID: r.RequestEventID, RequestKind: r.RequestKind, ResultKinds: []int{r.ResultKind}, ReadModelKinds: mapValues(r.ReadModelKinds), DTag: r.DTag, IdempotencyKey: key, PublishedRelays: []string{fmt.Sprint(r.PublishedRelays)}, ResourceTags: mlResourceTags(r)}
}

func mapValues(m map[string]int) []int {
	out := []int{}
	seen := map[int]bool{}
	for _, v := range m {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func mlResourceTags(r *controlplane.MLCommandReceipt) map[string]string {
	return map[string]string{"endpoint": r.Endpoint, "endpoint_id": r.EndpointID, "environment": r.Environment, "environment_id": r.EnvironmentID, "model_version": r.ModelVersion, "model_version_id": r.ModelVersionID, "model": r.Model, "runtime": r.Runtime}
}

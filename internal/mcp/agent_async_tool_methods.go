package mcp

import "github.com/openagentsinc/bahia/internal/controlplane"

// AssistantAsyncToolRequestMethods maps each assistant async tool to the
// ContextVM JSON-RPC methods its request events may carry. The assistant
// executor uses it to reconcile an uncertain dispatch: an operator-supplied
// request event only proves submission of a work item when its method is one
// the invoked tool publishes. Keep in step with InvokeAssistantAsyncTool.
func AssistantAsyncToolRequestMethods() map[string][]string {
	return map[string][]string{
		"bahia_assistant_service_deploy":         {controlplane.ContextVMMethodServiceDeploy},
		"bahia_assistant_service_rollback":       {controlplane.ContextVMMethodServiceRollback},
		"bahia_assistant_llm_deploy":             {"llm/deploy"},
		"bahia_assistant_llm_approve_deployment": {controlplane.ContextVMMethodLLMApprovalApprove, controlplane.ContextVMMethodLLMApprovalReject},
		"bahia_assistant_llm_rollback":           {"llm/rollback"},
		"bahia_assistant_ml_deploy":              {"ml/inference-deploy"},
		"bahia_assistant_ml_approve_deployment":  {"ml/inference-approval"},
		"bahia_assistant_ml_rollback":            {"ml/inference-rollback"},
	}
}

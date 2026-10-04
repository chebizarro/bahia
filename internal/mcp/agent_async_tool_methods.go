package mcp

// AssistantAsyncToolRequestMethods maps each assistant async tool to the
// ContextVM JSON-RPC methods its request events may carry. Intent-backed
// service and LLM calls are proven by processor markers instead. The assistant
// executor uses it to reconcile an uncertain dispatch: an operator-supplied
// request event only proves submission of a work item when its method is one
// the invoked tool publishes. Keep in step with InvokeAssistantAsyncTool.
func AssistantAsyncToolRequestMethods() map[string][]string {
	return map[string][]string{
		"bahia_assistant_ml_deploy":             {"ml/inference-deploy"},
		"bahia_assistant_ml_approve_deployment": {"ml/inference-approval"},
		"bahia_assistant_ml_rollback":           {"ml/inference-rollback"},
	}
}

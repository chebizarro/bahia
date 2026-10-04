package mcp

import "testing"

func TestAssistantAsyncToolRequestMethodsCoverEveryAsyncTool(t *testing.T) {
	methods := AssistantAsyncToolRequestMethods()
	defs := assistantAsyncToolDefinitions()
	if len(methods) != 3 {
		t.Fatalf("methods=%d async tools=%d", len(methods), len(defs))
	}
	for _, def := range defs {
		if isAssistantIntentTool(def.Name) {
			continue
		}
		if len(methods[def.Name]) == 0 {
			t.Fatalf("async tool %s has no reconcilable request method", def.Name)
		}
	}
}

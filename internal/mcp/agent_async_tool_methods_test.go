package mcp

import "testing"

func TestAssistantAsyncToolsUseIntentEvidence(t *testing.T) {
	if methods := AssistantAsyncToolRequestMethods(); len(methods) != 0 {
		t.Fatalf("legacy ContextVM methods remain: %v", methods)
	}
	for _, def := range assistantAsyncToolDefinitions() {
		if !isAssistantIntentTool(def.Name) {
			t.Fatalf("%s is not intent-backed", def.Name)
		}
	}
}

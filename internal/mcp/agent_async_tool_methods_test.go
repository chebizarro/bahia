package mcp

import "testing"

func TestAssistantAsyncToolsUseIntentEvidence(t *testing.T) {
	for _, def := range assistantAsyncToolDefinitions() {
		if !isAssistantIntentTool(def.Name) {
			t.Fatalf("%s is not intent-backed", def.Name)
		}
	}
}

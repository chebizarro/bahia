package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAnthropicAgentClientToolUseAndToolResultThreading(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if apiKey := r.Header.Get("x-api-key"); apiKey != "test-key" {
			t.Fatalf("x-api-key = %q", apiKey)
		}
		if version := r.Header.Get("anthropic-version"); version != anthropicVersion {
			t.Fatalf("anthropic-version = %q", version)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		assertAnthropicToolRequest(t, got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"stop_reason": "tool_use",
			"content": [
				{"type":"text","text":"Checking DNS."},
				{"type":"tool_use","id":"call_dns","name":"bahia_assistant_dns_list_records","input":{"zone":"example.test"}}
			]
		}`))
	}))
	defer server.Close()

	temp := 0.1
	client := NewAnthropicAgentClient(AnthropicAgentClientConfig{BaseURL: server.URL, Model: "configured-model", APIKey: "test-key"}, nil)
	var completed []AgentModelEvent
	resp, err := client.Next(context.Background(), AgentModelRequest{
		Model: "request-model",
		Messages: []domain.AssistantAgentMessage{
			{Role: domain.AssistantAgentMessageRoleSystem, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "You are Bahia."}}},
			{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "Check DNS"}}},
			{Role: domain.AssistantAgentMessageRoleAssistant, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "I will inspect DNS."}}, ToolCalls: []domain.AssistantAgentToolCall{{ID: "prior_dns", Name: "bahia_assistant_dns_list_records", Arguments: map[string]any{"zone": "example.test"}}}},
			{Role: domain.AssistantAgentMessageRoleTool, ToolCallID: "prior_dns", Name: "bahia_assistant_dns_list_records", Observation: &domain.AssistantToolObservation{ObservationID: "obs_dns", ToolCallID: "prior_dns", ToolName: "bahia_assistant_dns_list_records", Status: domain.AssistantToolObservationSucceeded, Summary: "Found records", Result: map[string]any{"count": float64(1)}}},
		},
		Tools:       []AgentToolSchema{{Name: "bahia_assistant_dns_list_records", Description: "List DNS records", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"zone": map[string]any{"type": "string"}}}}},
		ToolChoice:  AgentToolChoice{Mode: AgentToolChoiceRequired},
		MaxTokens:   77,
		Temperature: &temp,
	}, func(event AgentModelEvent) { completed = append(completed, event) })
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if resp.StopReason != AgentStopReasonToolCalls {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}
	if len(resp.Content) != 1 || !strings.Contains(resp.Content[0].Text, "Checking DNS") {
		t.Fatalf("content = %#v", resp.Content)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_dns" || resp.ToolCalls[0].Name != "bahia_assistant_dns_list_records" || resp.ToolCalls[0].Arguments["zone"] != "example.test" {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	if len(completed) != 1 || completed[0].Type != AgentModelEventCompleted || completed[0].StopReason != AgentStopReasonToolCalls {
		t.Fatalf("events = %#v", completed)
	}
}

func assertAnthropicToolRequest(t *testing.T, got map[string]any) {
	t.Helper()
	if got["model"] != "request-model" {
		t.Fatalf("model = %#v", got["model"])
	}
	if got["system"] != "You are Bahia." {
		t.Fatalf("system = %#v", got["system"])
	}
	if got["max_tokens"].(float64) != 77 {
		t.Fatalf("max_tokens = %#v", got["max_tokens"])
	}
	if got["temperature"].(float64) != 0.1 {
		t.Fatalf("temperature = %#v", got["temperature"])
	}
	choice := got["tool_choice"].(map[string]any)
	if choice["type"] != "any" {
		t.Fatalf("tool_choice = %#v", choice)
	}
	tools := got["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "bahia_assistant_dns_list_records" {
		t.Fatalf("tools = %#v", tools)
	}
	messages := got["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	assistant := messages[1].(map[string]any)
	assistantBlocks := assistant["content"].([]any)
	if assistant["role"] != "assistant" || len(assistantBlocks) != 2 {
		t.Fatalf("assistant message = %#v", assistant)
	}
	toolUse := assistantBlocks[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "prior_dns" || toolUse["name"] != "bahia_assistant_dns_list_records" {
		t.Fatalf("prior tool_use = %#v", toolUse)
	}
	toolResult := messages[2].(map[string]any)
	if toolResult["role"] != "user" {
		t.Fatalf("tool result role = %#v", toolResult)
	}
	resultBlocks := toolResult["content"].([]any)
	if len(resultBlocks) != 1 || resultBlocks[0].(map[string]any)["type"] != "tool_result" || resultBlocks[0].(map[string]any)["tool_use_id"] != "prior_dns" {
		t.Fatalf("tool_result blocks = %#v", resultBlocks)
	}
	var observation domain.AssistantToolObservation
	if err := json.Unmarshal([]byte(resultBlocks[0].(map[string]any)["content"].(string)), &observation); err != nil {
		t.Fatalf("tool_result content is not observation JSON: %v", err)
	}
	if observation.ObservationID != "obs_dns" || observation.Status != domain.AssistantToolObservationSucceeded {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestAnthropicAgentClientMalformedResponses(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
	}{
		{name: "empty", response: `{"content":[]}`, want: "no content or tool_use"},
		{name: "missing tool id", response: `{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"tool","input":{}}]}`, want: "missing id"},
		{name: "missing tool name", response: `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"call_1","input":{}}]}`, want: "missing name"},
		{name: "unsupported block", response: `{"content":[{"type":"image"}]}`, want: "unsupported content block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			client := NewAnthropicAgentClient(AnthropicAgentClientConfig{BaseURL: server.URL, Model: "claude-agent"}, nil)
			_, err := client.Next(context.Background(), AgentModelRequest{Messages: []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "hi"}}}}}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestAnthropicAgentClientContextTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":{"type":"request_too_large","message":"prompt is too long"}}`))
	}))
	defer server.Close()

	client := NewAnthropicAgentClient(AnthropicAgentClientConfig{BaseURL: server.URL, Model: "claude-agent"}, nil)
	_, err := client.Next(context.Background(), AgentModelRequest{Messages: []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "hi"}}}}}, nil)
	if !IsContextTooLarge(err) {
		t.Fatalf("IsContextTooLarge(%v) = false", err)
	}
}

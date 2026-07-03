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

func TestOpenAIAgentClientTextResponse(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("authorization header = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"I can help with that."}}]}`))
	}))
	defer server.Close()

	temp := 0.2
	client := NewOpenAIAgentClient(OpenAIAgentClientConfig{BaseURL: server.URL, Model: "configured-model", APIKey: "test-key"}, nil)
	var completed []AgentModelEvent
	resp, err := client.Next(context.Background(), AgentModelRequest{
		Model: "request-model",
		Messages: []domain.AssistantAgentMessage{
			{Role: domain.AssistantAgentMessageRoleSystem, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "You are Bahia."}}},
			{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "List services"}}},
		},
		MaxTokens:   99,
		Temperature: &temp,
	}, func(event AgentModelEvent) { completed = append(completed, event) })
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if resp.StopReason != AgentStopReasonEndTurn {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "I can help with that." {
		t.Fatalf("content = %#v", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("unexpected tool calls: %#v", resp.ToolCalls)
	}
	if got["model"] != "request-model" {
		t.Fatalf("request model = %#v", got["model"])
	}
	if got["max_tokens"].(float64) != 99 {
		t.Fatalf("max_tokens = %#v", got["max_tokens"])
	}
	if got["temperature"].(float64) != 0.2 {
		t.Fatalf("temperature = %#v", got["temperature"])
	}
	messages := got["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["content"] != "List services" {
		t.Fatalf("messages = %#v", messages)
	}
	if len(completed) != 1 || completed[0].Type != AgentModelEventCompleted || completed[0].StopReason != AgentStopReasonEndTurn {
		t.Fatalf("events = %#v", completed)
	}
}

func TestOpenAIAgentClientToolCallsAndCanonicalObservationThreading(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		assertNativeToolRequest(t, got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"finish_reason": "tool_calls",
				"message": {
					"content": "Checking DNS and services.",
					"tool_calls": [
						{"id":"call_dns","type":"function","function":{"name":"bahia_assistant_dns_list_records","arguments":"{\"zone\":\"example.test\"}"}},
						{"id":"call_services","type":"function","function":{"name":"bahia_assistant_service_list","arguments":"{\"environment_id\":\"env-1\"}"}}
					]
				}
			}]
		}`))
	}))
	defer server.Close()

	client := NewOpenAIAgentClient(OpenAIAgentClientConfig{BaseURL: server.URL, Model: "gpt-agent"}, nil)
	resp, err := client.Next(context.Background(), AgentModelRequest{
		Messages: []domain.AssistantAgentMessage{
			{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "Check DNS and services"}}},
			{Role: domain.AssistantAgentMessageRoleAssistant, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "I will call two tools."}}, ToolCalls: []domain.AssistantAgentToolCall{
				{ID: "prior_dns", Name: "bahia_assistant_dns_list_records", Arguments: map[string]any{"zone": "example.test"}},
				{ID: "prior_services", Name: "bahia_assistant_service_list", Arguments: map[string]any{"environment_id": "env-1"}},
			}},
			{Role: domain.AssistantAgentMessageRoleTool, Name: "bahia_assistant_dns_list_records", ToolCallID: "prior_dns", Observation: &domain.AssistantToolObservation{
				ObservationID: "obs_dns",
				ToolCallID:    "prior_dns",
				ToolName:      "bahia_assistant_dns_list_records",
				Status:        domain.AssistantToolObservationSucceeded,
				Summary:       "Found one record.",
				Result:        map[string]any{"records": []any{map[string]any{"name": "www", "type": "A"}}},
			}},
			{Role: domain.AssistantAgentMessageRoleTool, Name: "bahia_assistant_service_list", ToolCallID: "prior_services", Observation: &domain.AssistantToolObservation{
				ObservationID: "obs_services",
				ToolCallID:    "prior_services",
				ToolName:      "bahia_assistant_service_list",
				Status:        domain.AssistantToolObservationSucceeded,
				Summary:       "Found two services.",
				Result:        map[string]any{"count": float64(2)},
			}},
		},
		Tools: []AgentToolSchema{
			{Name: "bahia_assistant_dns_list_records", Description: "List DNS records", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"zone": map[string]any{"type": "string"}}}},
			{Name: "bahia_assistant_service_list", Description: "List services", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"environment_id": map[string]any{"type": "string"}}}},
		},
		ToolChoice: AgentToolChoice{Mode: AgentToolChoiceRequired},
	}, nil)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if resp.StopReason != AgentStopReasonToolCalls {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}
	if len(resp.Content) != 1 || !strings.Contains(resp.Content[0].Text, "Checking DNS") {
		t.Fatalf("content = %#v", resp.Content)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "call_dns" || resp.ToolCalls[0].Name != "bahia_assistant_dns_list_records" || resp.ToolCalls[0].Arguments["zone"] != "example.test" {
		t.Fatalf("first tool call = %#v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "call_services" || resp.ToolCalls[1].Name != "bahia_assistant_service_list" || resp.ToolCalls[1].Arguments["environment_id"] != "env-1" {
		t.Fatalf("second tool call = %#v", resp.ToolCalls[1])
	}
}

func assertNativeToolRequest(t *testing.T, got map[string]any) {
	t.Helper()
	if got["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %#v", got["tool_choice"])
	}
	tools := got["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v", tools)
	}
	firstTool := tools[0].(map[string]any)
	if firstTool["type"] != "function" {
		t.Fatalf("tool type = %#v", firstTool)
	}
	fn := firstTool["function"].(map[string]any)
	if fn["name"] != "bahia_assistant_dns_list_records" || fn["description"] != "List DNS records" {
		t.Fatalf("function tool = %#v", fn)
	}
	messages := got["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %#v", messages)
	}
	assistant := messages[1].(map[string]any)
	priorCalls := assistant["tool_calls"].([]any)
	if len(priorCalls) != 2 {
		t.Fatalf("prior tool calls = %#v", priorCalls)
	}
	priorCall := priorCalls[0].(map[string]any)
	if priorCall["id"] != "prior_dns" || priorCall["type"] != "function" {
		t.Fatalf("prior call = %#v", priorCall)
	}
	priorFn := priorCall["function"].(map[string]any)
	if priorFn["name"] != "bahia_assistant_dns_list_records" || !strings.Contains(priorFn["arguments"].(string), "example.test") {
		t.Fatalf("prior function call = %#v", priorFn)
	}
	dnsResult := messages[2].(map[string]any)
	if dnsResult["role"] != "tool" || dnsResult["tool_call_id"] != "prior_dns" || dnsResult["name"] != "bahia_assistant_dns_list_records" {
		t.Fatalf("dns tool result = %#v", dnsResult)
	}
	var observation domain.AssistantToolObservation
	if err := json.Unmarshal([]byte(dnsResult["content"].(string)), &observation); err != nil {
		t.Fatalf("tool content is not canonical observation JSON: %v", err)
	}
	if observation.ObservationID != "obs_dns" || observation.ToolCallID != "prior_dns" || observation.Status != domain.AssistantToolObservationSucceeded {
		t.Fatalf("observation = %#v", observation)
	}
	serviceResult := messages[3].(map[string]any)
	if serviceResult["role"] != "tool" || serviceResult["tool_call_id"] != "prior_services" {
		t.Fatalf("service tool result = %#v", serviceResult)
	}
}

func TestOpenAIAgentClientMalformedResponses(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
	}{
		{name: "missing choices", response: `{}`, want: "missing choices"},
		{name: "empty assistant turn", response: `{"choices":[{"message":{"content":""}}]}`, want: "no content or tool calls"},
		{name: "malformed arguments", response: `{"choices":[{"finish_reason":"tool_calls","message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bahia_tool","arguments":"not-json"}}]}}]}`, want: "unmarshal arguments"},
		{name: "missing tool name", response: `{"choices":[{"finish_reason":"tool_calls","message":{"tool_calls":[{"id":"call_1","type":"function","function":{"arguments":"{}"}}]}}]}`, want: "missing function name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			client := NewOpenAIAgentClient(OpenAIAgentClientConfig{BaseURL: server.URL, Model: "gpt-agent"}, nil)
			_, err := client.Next(context.Background(), AgentModelRequest{Messages: []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "hi"}}}}}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestOpenAIAgentClientContextTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded","type":"context_length_exceeded","code":"context_length_exceeded"}}`))
	}))
	defer server.Close()

	client := NewOpenAIAgentClient(OpenAIAgentClientConfig{BaseURL: server.URL, Model: "gpt-agent"}, nil)
	_, err := client.Next(context.Background(), AgentModelRequest{Messages: []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "hi"}}}}}, nil)
	if !IsContextTooLarge(err) {
		t.Fatalf("IsContextTooLarge(%v) = false", err)
	}
}

package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func chatCompletionResponse(t *testing.T, content string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"content": content},
		}},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

func TestPromptedAgentClientTextResponse(t *testing.T) {
	var got map[string]any
	client := NewPromptedAgentClient(PromptedAgentClientConfig{
		BaseURL: "https://llm.test",
		Model:   "configured-model",
		APIKey:  "test-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/v1/chat/completions" {
				t.Fatalf("path = %s", req.URL.Path)
			}
			if auth := req.Header.Get("Authorization"); auth != "Bearer test-key" {
				t.Fatalf("authorization header = %q", auth)
			}
			if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			return jsonHTTPResponse(http.StatusOK, `{"choices":[{"finish_reason":"stop","message":{"content":"I can help with that."}}]}`), nil
		})},
	}, nil)

	temp := 0.2
	var completed []AgentModelEvent
	resp, err := client.Next(context.Background(), AgentModelRequest{
		Model: "request-model",
		Messages: []domain.AssistantAgentMessage{
			{Role: domain.AssistantAgentMessageRoleSystem, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "You are Bahia."}}},
			{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "List services"}}},
		},
		Tools:       []AgentToolSchema{{Name: "bahia_assistant_service_list", Description: "List services", InputSchema: map[string]any{"type": "object"}}},
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
	if _, ok := got["tools"]; ok {
		t.Fatalf("prompted request sent native tools: %#v", got["tools"])
	}
	if _, ok := got["tool_choice"]; ok {
		t.Fatalf("prompted request sent native tool_choice: %#v", got["tool_choice"])
	}
	if got["model"] != "request-model" {
		t.Fatalf("request model = %#v", got["model"])
	}
	if got["max_tokens"].(float64) != 99 {
		t.Fatalf("max_tokens = %#v", got["max_tokens"])
	}
	messages := got["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	system := messages[0].(map[string]any)
	systemContent := system["content"].(string)
	if system["role"] != "system" || !strings.Contains(systemContent, "Bahia prompted tool-calling mode is active") || !strings.Contains(systemContent, "```tool_call") || !strings.Contains(systemContent, "bahia_assistant_service_list") {
		t.Fatalf("system prompt missing prompted tool instructions: %#v", system)
	}
	if len(completed) != 1 || completed[0].Type != AgentModelEventCompleted || completed[0].StopReason != AgentStopReasonEndTurn {
		t.Fatalf("events = %#v", completed)
	}
}

func TestPromptedAgentClientToolCallBlock(t *testing.T) {
	client := NewPromptedAgentClient(PromptedAgentClientConfig{
		BaseURL: "https://llm.test",
		Model:   "gemma",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var got map[string]any
			if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if _, ok := got["tools"]; ok {
				t.Fatalf("prompted request sent native tools: %#v", got["tools"])
			}
			return jsonHTTPResponse(http.StatusOK, chatCompletionResponse(t, "```tool_call\n{\"name\":\"bahia_assistant_dns_list_records\",\"arguments\":{\"zone\":\"example.test\"}}\n```")), nil
		})},
	}, nil)

	resp, err := client.Next(context.Background(), AgentModelRequest{
		Messages: []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "Check DNS"}}}},
		Tools:    []AgentToolSchema{{Name: "bahia_assistant_dns_list_records", Description: "List DNS records", InputSchema: map[string]any{"type": "object"}}},
	}, nil)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if resp.StopReason != AgentStopReasonToolCalls {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}
	if len(resp.Content) != 0 {
		t.Fatalf("content should be empty for tool-call response: %#v", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	call := resp.ToolCalls[0]
	if !strings.HasPrefix(call.ID, "prompted_call_") || call.Name != "bahia_assistant_dns_list_records" || call.Arguments["zone"] != "example.test" {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestPromptedAgentClientRendersToolHistoryAndParsesNextCall(t *testing.T) {
	var got map[string]any
	client := NewPromptedAgentClient(PromptedAgentClientConfig{
		BaseURL: "https://llm.test",
		Model:   "gemma",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			return jsonHTTPResponse(http.StatusOK, chatCompletionResponse(t, "```tool_call\n{\"name\":\"bahia_assistant_service_describe\",\"arguments\":{\"service_id\":\"svc-1\"}}\n```")), nil
		})},
	}, nil)

	resp, err := client.Next(context.Background(), AgentModelRequest{
		Messages: []domain.AssistantAgentMessage{
			{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "Find service details"}}},
			{Role: domain.AssistantAgentMessageRoleAssistant, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "I will list services first."}}, ToolCalls: []domain.AssistantAgentToolCall{
				{ID: "prior_services", Name: "bahia_assistant_service_list", Arguments: map[string]any{"environment_id": "env-1"}},
			}},
			{Role: domain.AssistantAgentMessageRoleTool, Name: "bahia_assistant_service_list", ToolCallID: "prior_services", Observation: &domain.AssistantToolObservation{
				ObservationID: "obs_services",
				ToolCallID:    "prior_services",
				ToolName:      "bahia_assistant_service_list",
				Status:        domain.AssistantToolObservationSucceeded,
				Summary:       "Found one service.",
				Result:        map[string]any{"services": []any{map[string]any{"id": "svc-1", "name": "api"}}},
			}},
		},
		Tools: []AgentToolSchema{
			{Name: "bahia_assistant_service_list", Description: "List services", InputSchema: map[string]any{"type": "object"}},
			{Name: "bahia_assistant_service_describe", Description: "Describe a service", InputSchema: map[string]any{"type": "object"}},
		},
		ToolChoice: AgentToolChoice{Mode: AgentToolChoiceAuto},
	}, nil)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %#v", messages)
	}
	for _, raw := range messages {
		msg := raw.(map[string]any)
		if msg["role"] == "tool" {
			t.Fatalf("prompted request used native tool role: %#v", msg)
		}
	}
	transcript := ""
	for _, raw := range messages {
		transcript += raw.(map[string]any)["content"].(string) + "\n"
	}
	for _, want := range []string{"Assistant requested tool call(s):", "Tool call ID: prior_services", "Tool result for call prior_services", "Found one service.", "svc-1"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("rendered transcript missing %q:\n%s", want, transcript)
		}
	}
	if resp.StopReason != AgentStopReasonToolCalls || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "bahia_assistant_service_describe" || resp.ToolCalls[0].Arguments["service_id"] != "svc-1" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestPromptedAgentClientMalformedToolCallBlockTreatedAsText(t *testing.T) {
	malformed := "```tool_call\n{\"name\":\"bahia_assistant_service_list\",\"arguments\":\n```"
	client := NewPromptedAgentClient(PromptedAgentClientConfig{
		BaseURL: "https://llm.test",
		Model:   "gemma",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return jsonHTTPResponse(http.StatusOK, chatCompletionResponse(t, malformed)), nil
		})},
	}, nil)

	resp, err := client.Next(context.Background(), AgentModelRequest{
		Messages: []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "List services"}}}},
		Tools:    []AgentToolSchema{{Name: "bahia_assistant_service_list"}},
	}, nil)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if resp.StopReason != AgentStopReasonEndTurn || len(resp.ToolCalls) != 0 || len(resp.Content) != 1 || resp.Content[0].Text != malformed {
		t.Fatalf("malformed block should be preserved as text response: %#v", resp)
	}
}

func TestPromptedAgentClientContextTooLarge(t *testing.T) {
	client := NewPromptedAgentClient(PromptedAgentClientConfig{
		BaseURL: "https://llm.test",
		Model:   "gemma",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return jsonHTTPResponse(http.StatusBadRequest, `{"error":{"message":"maximum context length exceeded","type":"context_length_exceeded","code":"context_length_exceeded"}}`), nil
		})},
	}, nil)
	_, err := client.Next(context.Background(), AgentModelRequest{Messages: []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "hi"}}}}}, nil)
	if !IsContextTooLarge(err) {
		t.Fatalf("IsContextTooLarge(%v) = false", err)
	}
}

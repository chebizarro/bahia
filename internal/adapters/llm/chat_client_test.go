package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func decodeJSON(t *testing.T, r *http.Request, v any) error {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return nil
}

const validPlanJSON = `{"summary":"List services","needs_clarification":false,"risk_level":"low","steps":[{"step_id":"s1","title":"List","description":"List services","tool_name":"bahia_assistant_service_list","tool_args":{"environment_id":"env-1"}}]}`

// streamedEmptyContentBody returns an SSE stream that carries role/finish
// frames but never any delta.content — mirroring providers that do not stream
// content deltas for stream+response_format(json_schema) requests.
func streamedEmptyContentBody() string {
	return "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
}

// streamedContentBody returns an SSE stream that carries the plan JSON across
// several delta.content chunks, like a well-behaved provider.
func streamedContentBody(content string) string {
	var b strings.Builder
	b.WriteString("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	// Split content into two chunks to exercise the accumulator.
	mid := len(content) / 2
	for _, part := range []string{content[:mid], content[mid:]} {
		b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":")
		b.WriteString(quoteJSON(part))
		b.WriteString("}}]}\n\n")
	}
	b.WriteString("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func quoteJSON(s string) string {
	// Minimal JSON string quoting for the content we control in tests.
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")
	return "\"" + replacer.Replace(s) + "\""
}

func writeSSE(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(body))
}

// TestPlanFromPromptStreamingFallsBackToNonStreaming verifies that when the
// streaming request yields no content deltas, the planner transparently falls
// back to the non-streaming completion and parses the plan from the full
// message content.
func TestPlanFromPromptStreamingFallsBackToNonStreaming(t *testing.T) {
	var streamCalls, nonStreamCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Stream bool `json:"stream"`
		}
		_ = decodeJSON(t, r, &got)
		if got.Stream {
			atomic.AddInt32(&streamCalls, 1)
			writeSSE(w, streamedEmptyContentBody())
			return
		}
		atomic.AddInt32(&nonStreamCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + quoteJSON(validPlanJSON) + `}}]}`))
	}))
	defer server.Close()

	client := NewChatClient(ChatClientConfig{BaseURL: server.URL, Model: "test-model"}, nil)
	var chunks []string
	plan, err := client.PlanFromPromptStreaming(context.Background(), "system", "user", func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("PlanFromPromptStreaming returned error: %v", err)
	}
	if plan == nil || plan.Summary != "List services" {
		t.Fatalf("plan = %#v", plan)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].ToolName != "bahia_assistant_service_list" {
		t.Fatalf("plan steps = %#v", plan.Steps)
	}
	if atomic.LoadInt32(&streamCalls) != 1 {
		t.Fatalf("streaming endpoint calls = %d, want 1", streamCalls)
	}
	if atomic.LoadInt32(&nonStreamCalls) != 1 {
		t.Fatalf("non-streaming fallback calls = %d, want 1", nonStreamCalls)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected no streamed chunks on empty stream, got %#v", chunks)
	}
}

// TestPlanFromPromptStreamingSurfacesRefusal verifies that a streamed
// delta.refusal (with no content) surfaces as a graceful clarification plan
// rather than a bare empty-response error, and that it does NOT fall back to
// non-streaming (a refusal is a genuine model response).
func TestPlanFromPromptStreamingSurfacesRefusal(t *testing.T) {
	var nonStreamCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Stream bool `json:"stream"`
		}
		_ = decodeJSON(t, r, &got)
		if !got.Stream {
			atomic.AddInt32(&nonStreamCalls, 1)
		}
		writeSSE(w, "data: {\"choices\":[{\"delta\":{\"refusal\":\"I cannot help with deleting production data.\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewChatClient(ChatClientConfig{BaseURL: server.URL, Model: "test-model"}, nil)
	plan, err := client.PlanFromPromptStreaming(context.Background(), "system", "user", nil)
	if err != nil {
		t.Fatalf("PlanFromPromptStreaming returned error: %v", err)
	}
	if plan == nil || !plan.NeedsClarification {
		t.Fatalf("expected clarification plan, got %#v", plan)
	}
	if !strings.Contains(plan.ClarifyingQuestion, "cannot help") {
		t.Fatalf("clarifying question = %q", plan.ClarifyingQuestion)
	}
	if atomic.LoadInt32(&nonStreamCalls) != 0 {
		t.Fatalf("refusal must not trigger non-streaming fallback, got %d calls", nonStreamCalls)
	}
}

// TestPlanFromPromptStreamingStreamsNormally verifies the happy path: when the
// provider streams content deltas, the plan is parsed from the accumulated
// stream and no non-streaming fallback occurs.
func TestPlanFromPromptStreamingStreamsNormally(t *testing.T) {
	var nonStreamCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Stream bool `json:"stream"`
		}
		_ = decodeJSON(t, r, &got)
		if !got.Stream {
			atomic.AddInt32(&nonStreamCalls, 1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeSSE(w, streamedContentBody(validPlanJSON))
	}))
	defer server.Close()

	client := NewChatClient(ChatClientConfig{BaseURL: server.URL, Model: "test-model"}, nil)
	var chunks []string
	plan, err := client.PlanFromPromptStreaming(context.Background(), "system", "user", func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("PlanFromPromptStreaming returned error: %v", err)
	}
	if plan == nil || plan.Summary != "List services" {
		t.Fatalf("plan = %#v", plan)
	}
	if atomic.LoadInt32(&nonStreamCalls) != 0 {
		t.Fatalf("successful stream must not fall back, got %d non-streaming calls", nonStreamCalls)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected streamed chunks to be delivered via onChunk")
	}
	if strings.Join(chunks, "") != validPlanJSON {
		t.Fatalf("streamed content = %q, want %q", strings.Join(chunks, ""), validPlanJSON)
	}
}

// TestPlanFromPromptStreamingEmptyEverywhereErrors verifies that when both the
// streaming and non-streaming paths return nothing (no content, no refusal),
// the planner returns a clear, actionable error.
func TestPlanFromPromptStreamingEmptyEverywhereErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Stream bool `json:"stream"`
		}
		_ = decodeJSON(t, r, &got)
		if got.Stream {
			writeSSE(w, streamedEmptyContentBody())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
	}))
	defer server.Close()

	client := NewChatClient(ChatClientConfig{BaseURL: server.URL, Model: "test-model"}, nil)
	_, err := client.PlanFromPromptStreaming(context.Background(), "system", "user", nil)
	if err == nil {
		t.Fatalf("expected error when both paths are empty")
	}
	if !strings.Contains(err.Error(), "no content and no refusal") {
		t.Fatalf("error = %v, want actionable empty-response message", err)
	}
}

// TestPlanFromPromptSurfacesNonStreamingRefusal verifies the non-streaming
// planner also surfaces message.refusal as a clarification.
func TestPlanFromPromptSurfacesNonStreamingRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","refusal":"I will not do that."}}]}`))
	}))
	defer server.Close()

	client := NewChatClient(ChatClientConfig{BaseURL: server.URL, Model: "test-model"}, nil)
	plan, err := client.PlanFromPrompt(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("PlanFromPrompt returned error: %v", err)
	}
	if plan == nil || !plan.NeedsClarification || !strings.Contains(plan.ClarifyingQuestion, "will not") {
		t.Fatalf("plan = %#v", plan)
	}
}

// TestPlanFromPromptStreamingContextTooLargePreserved verifies the
// ContextTooLargeError classification still works on the streaming path.
func TestPlanFromPromptStreamingContextTooLargePreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded","type":"context_length_exceeded","code":"context_length_exceeded"}}`))
	}))
	defer server.Close()

	client := NewChatClient(ChatClientConfig{BaseURL: server.URL, Model: "test-model"}, nil)
	_, err := client.PlanFromPromptStreaming(context.Background(), "system", "user", nil)
	if !IsContextTooLarge(err) {
		t.Fatalf("IsContextTooLarge(%v) = false", err)
	}
}

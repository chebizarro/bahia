package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/mcp"
)

func chatRequest(t *testing.T, planner bool, prompt string) []byte {
	t.Helper()
	body := map[string]any{
		"model":    "joined-e2e",
		"messages": []any{map[string]any{"role": "system", "content": "sys"}, map[string]any{"role": "user", "content": "context\n" + prompt}},
	}
	if planner {
		body["response_format"] = map[string]any{"type": "json_schema"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBatchPlanUsesExactlyThreeReadOnlyBackendTools(t *testing.T) {
	plan := BatchPlan()
	if len(plan.Steps) != 3 {
		t.Fatalf("batch plan has %d steps, the joined spec requires exactly 3", len(plan.Steps))
	}
	registry := mcp.NewAssistantToolRegistryForServer(mcp.NewServerWithOptions(nil, zap.NewNop(), mcp.ServerDeps{}))
	for _, step := range plan.Steps {
		descriptor, ok := registry.Get(step.ToolName)
		if !ok {
			t.Fatalf("step %s uses %q, which the backend MCP server does not register", step.StepID, step.ToolName)
		}
		if descriptor.Effect != domain.AssistantToolEffectRead || descriptor.ExecutionMode != domain.AssistantToolExecutionModeSync {
			t.Fatalf("step %s tool %q is %s/%s; the fixture must stay read-only and synchronous", step.StepID, step.ToolName, descriptor.Effect, descriptor.ExecutionMode)
		}
	}
	var edited map[string]any
	if err := json.Unmarshal([]byte(EditedFirstStepArgs), &edited); err != nil || len(edited) == 0 {
		t.Fatalf("edited args are not a JSON object: %v", err)
	}
}

func TestBatchPromptReturnsThePlan(t *testing.T) {
	server := httptest.NewServer(newProvider().routes())
	defer server.Close()
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(chatRequest(t, true, BatchPrompt)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK || len(out.Choices) != 1 {
		t.Fatalf("status=%d err=%v choices=%d", resp.StatusCode, err, len(out.Choices))
	}
	var plan domain.AssistantPlan
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &plan); err != nil || len(plan.Steps) != 3 {
		t.Fatalf("plan content = %q (%v)", out.Choices[0].Message.Content, err)
	}
}

func TestUnscriptedRequestsAreRefused(t *testing.T) {
	server := httptest.NewServer(newProvider().routes())
	defer server.Close()
	for name, body := range map[string][]byte{
		"planner with other prompt":   chatRequest(t, true, "something else"),
		"agent with other prompt":     chatRequest(t, false, "something else"),
		"agent with the batch prompt": chatRequest(t, false, BatchPrompt),
		"planner with iterative":      chatRequest(t, true, IterativePrompt),
	} {
		resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d, want 422", name, resp.StatusCode)
		}
	}
}

func TestIterativeTurnIsHeldUntilTheCallerGoesAway(t *testing.T) {
	held := make(chan struct{}, 1)
	p := newProvider()
	p.held = held
	server := httptest.NewServer(p.routes())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(chatRequest(t, false, IterativePrompt)))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("iterative turn completed instead of being held: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("held turn answered before the caller went away: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("held request ended with %v, want context.Canceled", err)
	}
}

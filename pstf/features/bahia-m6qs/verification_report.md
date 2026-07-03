# Verification Report: bahia-m6qs

## Observed behavior

- With agentic mode off (the default), `AssistantOrchestrator.planFromPrompt`
  (`internal/service/assistant_orchestrator.go:947`) routes through
  `ChatClient.PlanFromPromptStreaming`.
- `callChatCompletionsStreaming` accumulated **only** `choices[].delta.content`
  and hard-failed with `empty response from streaming chat completion API`
  whenever the configured OpenAI-compatible provider did not emit content deltas
  for a `stream:true` + `response_format(json_schema)` request (content arrives
  only in the final non-streaming message, or via `delta.refusal`).
- The assistant surfaced `assistant planning failed` at runtime.

## Intended behavior

- `callChatCompletionsStreaming` now returns a detectable sentinel
  (`errEmptyStreamingResponse`) when neither content nor refusal was streamed.
- `PlanFromPromptStreaming` detects that sentinel and transparently falls back
  to the non-streaming `callChatCompletions` path, which reads the full
  `choices[0].message.content`.
- Both paths capture `refusal` (`delta.refusal` streaming, `message.refusal`
  non-streaming). Empty content with a refusal surfaces as a graceful
  clarification `AssistantPlan` (mirroring the invalid-JSON handling) rather than
  a bare empty-response error.
- When both paths are genuinely empty with no refusal, a clear actionable error
  (`errEmptyCompletionResponse`: "provider returned no content and no refusal")
  is returned.
- Streaming is preserved unchanged when the provider emits content deltas.
- `ContextTooLargeError` classification is preserved on both paths.
- The request schema, the agentic `openai_agent_client.go`, and provider
  selection are untouched.

## Verification evidence

- `go build ./...` passed.
- `go test ./internal/adapters/llm` passed, including the six new
  `chat_client_test.go` cases:
  - `TestPlanFromPromptStreamingFallsBackToNonStreaming` (AC1)
  - `TestPlanFromPromptStreamingSurfacesRefusal` (AC2)
  - `TestPlanFromPromptSurfacesNonStreamingRefusal` (AC2)
  - `TestPlanFromPromptStreamingStreamsNormally` (AC3)
  - `TestPlanFromPromptStreamingEmptyEverywhereErrors` (AC4)
  - `TestPlanFromPromptStreamingContextTooLargePreserved` (AC5)
- `go test ./internal/service -run TestAssistantOrchestrator` passed (scoped
  legacy-path regression).
- `go vet ./internal/adapters/llm` reported no issues.

## Known boundaries

- Fix is scoped to the legacy (non-agentic) planning path in
  `internal/adapters/llm/chat_client.go`. The agentic loop and the
  `/v1/chat/completions` request schema were intentionally left unchanged.
- Tests use `net/http/httptest` fake transports (no network).

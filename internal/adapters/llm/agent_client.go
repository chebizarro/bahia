package llm

import (
	"context"
	"errors"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AgentModelClient is the provider-neutral model seam consumed by the
// assistant agent loop. Implementations translate Bahia's canonical agent
// messages, tool schemas, tool choices, and observations into provider-native
// request/response shapes without changing the domain datamodel.
type AgentModelClient interface {
	Next(ctx context.Context, req AgentModelRequest, onEvent AgentModelEventHandler) (*AgentModelResponse, error)
}

// AgentModelEventHandler receives optional provider progress events. The first
// OpenAI adapter is non-streaming, but the callback is part of the seam so the
// agent loop can consume streaming text/tool-call deltas from any provider
// without a second interface.
type AgentModelEventHandler func(AgentModelEvent)

// AgentModelRequest is the canonical cross-provider model request.
//
// Message threading contract:
//   - User/system/assistant text is represented by domain.AssistantAgentMessage
//     Content blocks using the domain.AssistantAgentContentText/JSON types.
//   - A model turn that asks for multiple tools is represented by one assistant
//     message with ToolCalls containing every domain.AssistantAgentToolCall in
//     that turn; adapters serialize this to provider-native multi-call fields.
//   - Each tool result is represented by a subsequent role=tool message whose
//     ToolCallID matches exactly one previous tool-call ID and whose Observation
//     is the canonical domain.AssistantToolObservation. This preserves ordering
//     and lets providers that require one tool-result message per call serialize
//     the same transcript deterministically.
//   - Adapters must not invent provider-specific message structs outside their
//     transport layer; durable transcript storage uses the domain types above.
type AgentModelRequest struct {
	Model       string
	Messages    []domain.AssistantAgentMessage
	Tools       []AgentToolSchema
	ToolChoice  AgentToolChoice
	MaxTokens   int
	Temperature *float64
	Metadata    map[string]any
}

// AgentToolSchema is the provider-neutral native-tool schema injected into the
// model request. InputSchema is a JSON Schema object compatible with MCP tool
// descriptors and OpenAI/Anthropic native tool definitions.
type AgentToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// AgentToolChoiceMode is the provider-neutral tool-choice selector.
type AgentToolChoiceMode string

const (
	AgentToolChoiceAuto     AgentToolChoiceMode = "auto"
	AgentToolChoiceNone     AgentToolChoiceMode = "none"
	AgentToolChoiceRequired AgentToolChoiceMode = "required"
	AgentToolChoiceTool     AgentToolChoiceMode = "tool"
)

// AgentToolChoice controls whether the model may, must, or must not call tools.
// When Mode is AgentToolChoiceTool, Name selects the required tool.
type AgentToolChoice struct {
	Mode AgentToolChoiceMode `json:"mode,omitempty"`
	Name string              `json:"name,omitempty"`
}

// AgentStopReason is a provider-neutral finish reason.
type AgentStopReason string

const (
	AgentStopReasonEndTurn      AgentStopReason = "end_turn"
	AgentStopReasonToolCalls    AgentStopReason = "tool_calls"
	AgentStopReasonMaxTokens    AgentStopReason = "max_tokens"
	AgentStopReasonContentGuard AgentStopReason = "content_guard"
	AgentStopReasonUnknown      AgentStopReason = "unknown"
)

// AgentModelResponse is the provider-neutral model result. ToolCalls may contain
// multiple calls from one assistant turn; callers persist them as a single
// assistant message before appending one canonical tool observation per result.
type AgentModelResponse struct {
	Content    []domain.AssistantAgentContentBlock `json:"content,omitempty"`
	ToolCalls  []domain.AssistantAgentToolCall     `json:"tool_calls,omitempty"`
	StopReason AgentStopReason                     `json:"stop_reason"`
	Raw        map[string]any                      `json:"raw,omitempty"`
}

// AgentModelEventType identifies optional progress events from model clients.
type AgentModelEventType string

const (
	AgentModelEventContentDelta  AgentModelEventType = "content_delta"
	AgentModelEventToolCallDelta AgentModelEventType = "tool_call_delta"
	AgentModelEventCompleted     AgentModelEventType = "completed"
)

// AgentModelEvent is a provider-neutral streaming/progress event. Non-streaming
// adapters may emit only AgentModelEventCompleted.
type AgentModelEvent struct {
	Type       AgentModelEventType                 `json:"type"`
	Text       string                              `json:"text,omitempty"`
	ToolCall   *domain.AssistantAgentToolCall      `json:"tool_call,omitempty"`
	Content    []domain.AssistantAgentContentBlock `json:"content,omitempty"`
	StopReason AgentStopReason                     `json:"stop_reason,omitempty"`
	Metadata   map[string]any                      `json:"metadata,omitempty"`
}

// ErrAgentModelClientNotImplemented is returned by provider seams whose Bead is
// intentionally deferred and not wired into production configuration yet.
var ErrAgentModelClientNotImplemented = errors.New("agent model client provider not implemented")

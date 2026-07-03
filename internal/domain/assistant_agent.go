package domain

import "time"

// AssistantAgentLoopState is the persisted state machine value stored under the
// assistant session agent-loop metadata.
type AssistantAgentLoopState string

const (
	AssistantAgentLoopStateIdle             AssistantAgentLoopState = "idle"
	AssistantAgentLoopStateRunning          AssistantAgentLoopState = "running"
	AssistantAgentLoopStateWaitingAsync     AssistantAgentLoopState = "waiting_async"
	AssistantAgentLoopStateAwaitingApproval AssistantAgentLoopState = "awaiting_approval"
	AssistantAgentLoopStateBlocked          AssistantAgentLoopState = "blocked"
	AssistantAgentLoopStateCompleted        AssistantAgentLoopState = "completed"
	AssistantAgentLoopStateFailed           AssistantAgentLoopState = "failed"
)

// Terminal reports whether the loop state is terminal for the current run.
func (s AssistantAgentLoopState) Terminal() bool {
	switch s {
	case AssistantAgentLoopStateBlocked, AssistantAgentLoopStateCompleted, AssistantAgentLoopStateFailed:
		return true
	default:
		return false
	}
}

// AssistantAgentLoopMetadata is the JSON shape stored in
// AssistantSession.Metadata["agent_loop"] by later loop/runtime items.
type AssistantAgentLoopMetadata struct {
	RunID                      string                  `json:"run_id"`
	Iteration                  int                     `json:"iteration"`
	State                      AssistantAgentLoopState `json:"state"`
	PendingActionID            string                  `json:"pending_action_id,omitempty"`
	PendingToolCallID          string                  `json:"pending_tool_call_id,omitempty"`
	WaitingReceipt             *AsyncToolReceipt       `json:"waiting_receipt,omitempty"`
	ConsecutiveToolFailures    int                     `json:"consecutive_tool_failures,omitempty"`
	MaxIterations              int                     `json:"max_iterations,omitempty"`
	MaxConsecutiveToolFailures int                     `json:"max_consecutive_tool_failures,omitempty"`
	LastObservationID          string                  `json:"last_observation_id,omitempty"`
	TranscriptCursor           string                  `json:"transcript_cursor,omitempty"`
	UpdatedAt                  time.Time               `json:"updated_at,omitempty"`
}

// AssistantAgentMessageRole is provider-neutral; model adapters translate these
// values to OpenAI, Anthropic, or other provider-native role names.
type AssistantAgentMessageRole string

const (
	AssistantAgentMessageRoleSystem    AssistantAgentMessageRole = "system"
	AssistantAgentMessageRoleUser      AssistantAgentMessageRole = "user"
	AssistantAgentMessageRoleAssistant AssistantAgentMessageRole = "assistant"
	AssistantAgentMessageRoleTool      AssistantAgentMessageRole = "tool"
)

// AssistantAgentContentType identifies one provider-neutral message content
// block. Adapters may serialize text, tool calls, and tool observations
// differently while preserving this canonical in-process shape.
type AssistantAgentContentType string

const (
	AssistantAgentContentText        AssistantAgentContentType = "text"
	AssistantAgentContentToolCall    AssistantAgentContentType = "tool_call"
	AssistantAgentContentObservation AssistantAgentContentType = "tool_observation"
	AssistantAgentContentJSON        AssistantAgentContentType = "json"
)

// AssistantAgentMessage is the canonical cross-provider message shape consumed
// by the future agent model clients. It supports multiple tool calls in one
// assistant turn and provider-neutral tool observations in subsequent messages.
type AssistantAgentMessage struct {
	ID          string                       `json:"id,omitempty"`
	Role        AssistantAgentMessageRole    `json:"role"`
	Name        string                       `json:"name,omitempty"`
	Content     []AssistantAgentContentBlock `json:"content,omitempty"`
	ToolCalls   []AssistantAgentToolCall     `json:"tool_calls,omitempty"`
	ToolCallID  string                       `json:"tool_call_id,omitempty"`
	Observation *AssistantToolObservation    `json:"observation,omitempty"`
	Metadata    map[string]any               `json:"metadata,omitempty"`
}

// AssistantAgentContentBlock is one provider-neutral content block in an agent
// message.
type AssistantAgentContentBlock struct {
	Type        AssistantAgentContentType `json:"type"`
	Text        string                    `json:"text,omitempty"`
	JSON        map[string]any            `json:"json,omitempty"`
	ToolCall    *AssistantAgentToolCall   `json:"tool_call,omitempty"`
	Observation *AssistantToolObservation `json:"observation,omitempty"`
	Metadata    map[string]any            `json:"metadata,omitempty"`
}

// AssistantAgentToolCall is the canonical tool-call request shape returned by a
// model adapter before the permission/runtime layers decide whether to execute,
// defer, deny, or suspend for async observation.
type AssistantAgentToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// AssistantToolObservationStatus is the normalized status fed back to the model
// after a tool request is evaluated by permission and runtime layers.
type AssistantToolObservationStatus string

const (
	AssistantToolObservationSucceeded    AssistantToolObservationStatus = "succeeded"
	AssistantToolObservationFailed       AssistantToolObservationStatus = "failed"
	AssistantToolObservationDenied       AssistantToolObservationStatus = "denied"
	AssistantToolObservationDeferred     AssistantToolObservationStatus = "deferred"
	AssistantToolObservationWaitingAsync AssistantToolObservationStatus = "waiting_async"
	AssistantToolObservationCancelled    AssistantToolObservationStatus = "cancelled"
)

// AssistantToolObservation hides the sync/async split from the model: sync tool
// results, async receipts, terminal observed events, permission denials, and
// deferred approvals all become one canonical observation shape.
type AssistantToolObservation struct {
	ObservationID string                         `json:"observation_id"`
	ToolCallID    string                         `json:"tool_call_id,omitempty"`
	ToolName      string                         `json:"tool_name"`
	Status        AssistantToolObservationStatus `json:"status"`
	Effect        AssistantToolEffect            `json:"effect,omitempty"`
	Risk          AssistantToolRisk              `json:"risk,omitempty"`
	ExecutionMode AssistantToolExecutionMode     `json:"execution_mode,omitempty"`
	Summary       string                         `json:"summary,omitempty"`
	Content       []AssistantAgentContentBlock   `json:"content,omitempty"`
	Result        map[string]any                 `json:"result,omitempty"`
	Error         string                         `json:"error,omitempty"`
	Receipt       *AsyncToolReceipt              `json:"receipt,omitempty"`
	Deferred      *AssistantDeferredAction       `json:"deferred_action,omitempty"`
	EventID       string                         `json:"event_id,omitempty"`
	ObservedAt    time.Time                      `json:"observed_at,omitempty"`
	Metadata      map[string]any                 `json:"metadata,omitempty"`
}

// AssistantDeferredAction is persisted when a permission decision requires a
// human approval before the runtime can execute a tool call.
type AssistantDeferredAction struct {
	ActionID       string                    `json:"action_id"`
	SessionID      string                    `json:"session_id"`
	RunID          string                    `json:"run_id,omitempty"`
	TurnID         string                    `json:"turn_id,omitempty"`
	ToolCallID     string                    `json:"tool_call_id,omitempty"`
	ToolName       string                    `json:"tool_name"`
	ToolArgs       map[string]any            `json:"tool_args,omitempty"`
	PlanHash       string                    `json:"plan_hash,omitempty"`
	CancelScope    string                    `json:"cancel_scope,omitempty"`
	Permission     AssistantPermissionResult `json:"permission"`
	ApprovalPrompt string                    `json:"approval_prompt,omitempty"`
	CreatedAt      time.Time                 `json:"created_at,omitempty"`
	ExpiresAt      time.Time                 `json:"expires_at,omitempty"`
	Metadata       map[string]any            `json:"metadata,omitempty"`
}

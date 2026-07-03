package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Operator assistant Nostr event kinds and schemas. Prompt and approval
// mutation intents are carried by ContextVM JSON-RPC (kind 25910, optionally
// wrapped by 1059/21059); durable assistant observables are projected here.
const (
	KindAssistantSessionState = 30900
	KindAssistantStatus       = 30315
	KindAssistantTranscript   = 30316

	AssistantSessionSchema    = "bahia.assistant-session.v1"
	AssistantStatusSchema     = "bahia.assistant-status.v1"
	AssistantTranscriptSchema = "bahia.assistant-transcript.v1"

	AssistantContextVMMethodPrompt   = "assistant/prompt"
	AssistantContextVMMethodApproval = "assistant/approval"
)

const (
	AssistantDomain = "assistant"

	AssistantTranscriptDTagPrefix = AssistantTranscriptSchema + ":"

	AssistantTranscriptTagDomain      = "domain"
	AssistantTranscriptTagSchema      = "schema"
	AssistantTranscriptTagSession     = "session"
	AssistantTranscriptTagTurn        = "turn"
	AssistantTranscriptTagRole        = "role"
	AssistantTranscriptTagSequence    = "seq"
	AssistantTranscriptTagKeyRef      = "key_ref"
	AssistantTranscriptTagKeyVersion  = "key_version"
	AssistantTranscriptTagKeyRotation = "key_rotation"
	AssistantTranscriptTagEnvelope    = "envelope"

	AssistantTranscriptEnvelopeServiceHeldAEAD = "service-held-symmetric-key-aead"
	AssistantTranscriptAEADAlgorithmXChaCha20  = "XChaCha20-Poly1305"
)

// AssistantSessionState describes the canonical per-session lifecycle state.
type AssistantSessionState string

const (
	AssistantSessionStateIdle             AssistantSessionState = "idle"
	AssistantSessionStatePlanning         AssistantSessionState = "planning"
	AssistantSessionStateAwaitingApproval AssistantSessionState = "awaiting_approval"
	AssistantSessionStateExecuting        AssistantSessionState = "executing"
	AssistantSessionStateBlocked          AssistantSessionState = "blocked"
	AssistantSessionStateCompleted        AssistantSessionState = "completed"
	AssistantSessionStateFailed           AssistantSessionState = "failed"
)

// Terminal reports whether the state is terminal for a session turn.
func (s AssistantSessionState) Terminal() bool {
	switch s {
	case AssistantSessionStateCompleted, AssistantSessionStateBlocked, AssistantSessionStateFailed:
		return true
	default:
		return false
	}
}

// AssistantSession is the content contract for canonical assistant session
// state projections. The event is a kind 30900 CAS read model with schema,
// d=bahia.assistant-session.v1:<session_id>, session=<session_id>,
// p=<operator pubkey>, agent=<assistant id>, and status=<state> tags.
type AssistantSession struct {
	SessionID         string                `json:"session_id"`
	State             AssistantSessionState `json:"state"`
	OperatorPubkey    string                `json:"operator_pubkey"`
	Participants      []string              `json:"participants,omitempty"`
	AssistantID       string                `json:"assistant_id"`
	AssistantPubkey   string                `json:"assistant_pubkey,omitempty"`
	CurrentTurnID     string                `json:"current_turn_id,omitempty"`
	CurrentRequestID  string                `json:"current_request_id,omitempty"`
	LastPlanHash      string                `json:"last_plan_hash,omitempty"`
	CurrentPlan       *AssistantPlan        `json:"current_plan,omitempty"`
	PendingSteps      []AssistantPlanStep   `json:"pending_steps,omitempty"`
	TranscriptSummary string                `json:"transcript_summary,omitempty"`
	LastResultID      string                `json:"last_result_id,omitempty"`
	Metadata          map[string]any        `json:"metadata,omitempty"`
}

// AssistantPlan is the JSON-schema-constrained plan contract shared by the LLM,
// backend, and frontend.
type AssistantPlan struct {
	Summary            string              `json:"summary"`
	NeedsClarification bool                `json:"needs_clarification"`
	ClarifyingQuestion string              `json:"clarifying_question,omitempty"`
	RiskLevel          string              `json:"risk_level"`
	ContextRefs        []string            `json:"context_refs,omitempty"`
	Steps              []AssistantPlanStep `json:"steps"`
}

// AssistantPlanStep is one ordered executable step in an assistant plan.
type AssistantPlanStep struct {
	StepID         string         `json:"step_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	ToolName       string         `json:"tool_name"`
	ToolArgs       map[string]any `json:"tool_args"`
	ArgsPreview    map[string]any `json:"args_preview,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

// AssistantPromptRequest is the ContextVM params contract for assistant prompt
// intents authored by the operator browser key.
type AssistantPromptRequest struct {
	SessionID    string         `json:"session_id"`
	TurnID       string         `json:"turn_id"`
	Prompt       string         `json:"prompt"`
	RouteContext map[string]any `json:"route_context,omitempty"`
	SelectedRefs []string       `json:"selected_refs,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// AssistantApprovalRequest is the ContextVM params contract for approving,
// rejecting, or canceling the latest assistant plan for a session.
type AssistantApprovalRequest struct {
	SessionID    string         `json:"session_id"`
	PlanHash     string         `json:"plan_hash"`
	ActionID     string         `json:"action_id,omitempty"`
	CancelScope  string         `json:"cancel_scope,omitempty"`
	Decision     string         `json:"decision"`
	Reason       string         `json:"reason,omitempty"`
	Message      string         `json:"message,omitempty"`
	ModifiedPlan *AssistantPlan `json:"modified_plan,omitempty"`
}

// AssistantTranscriptAEADEnvelope is the JSON content shape for kind 30316
// transcript events. The ciphertext is produced with a service-held symmetric
// key; key lookup and rotation metadata are mirrored in tags so clients can
// scope subscriptions without decrypting content.
type AssistantTranscriptAEADEnvelope struct {
	Schema         string            `json:"schema"`
	Envelope       string            `json:"envelope"`
	Algorithm      string            `json:"algorithm"`
	KeyRef         string            `json:"key_ref"`
	KeyVersion     string            `json:"key_version,omitempty"`
	KeyRotation    string            `json:"key_rotation,omitempty"`
	Nonce          string            `json:"nonce"`
	Ciphertext     string            `json:"ciphertext"`
	AssociatedData map[string]string `json:"associated_data,omitempty"`
	Compression    string            `json:"compression,omitempty"`
}

// AssistantTranscriptPayload is the plaintext schema encrypted inside
// AssistantTranscriptAEADEnvelope. It is defined now so transcript publishers,
// replayers, and model adapters share one canonical shape when item 6 adds the
// store and crypto.
type AssistantTranscriptPayload struct {
	SessionID string                `json:"session_id"`
	TurnID    string                `json:"turn_id,omitempty"`
	RunID     string                `json:"run_id,omitempty"`
	Sequence  int                   `json:"seq"`
	Message   AssistantAgentMessage `json:"message"`
	Metadata  map[string]any        `json:"metadata,omitempty"`
}

// AsyncToolReceipt normalizes event-native downstream tool dispatch metadata for
// assistant-safe tools.
type AsyncToolReceipt struct {
	ToolName        string            `json:"tool_name"`
	RequestEventID  string            `json:"request_event_id"`
	RequestKind     int               `json:"request_kind"`
	StatusKinds     []int             `json:"status_kinds"`
	ResultKinds     []int             `json:"result_kinds"`
	ReadModelKinds  []int             `json:"read_model_kinds,omitempty"`
	DTag            string            `json:"d_tag,omitempty"`
	ResourceTags    map[string]string `json:"resource_tags,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key"`
	PublishedRelays []string          `json:"published_relays,omitempty"`
}

type assistantPlanHashEnvelope struct {
	SessionID string        `json:"session_id"`
	Plan      AssistantPlan `json:"plan"`
}

// ComputePlanHash returns sha256(canonical_json({session_id, plan})) as a
// lowercase hex string. encoding/json emits deterministic struct field order and
// sorted map keys for JSON object maps, with HTML escaping disabled, which is
// sufficient for the Phase 1 plan hash contract. If the plan contains
// non-JSON-marshalable values, an empty string is returned so callers can reject
// the invalid plan.
func ComputePlanHash(plan AssistantPlan, sessionID string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(assistantPlanHashEnvelope{
		SessionID: sessionID,
		Plan:      plan,
	}); err != nil {
		return ""
	}
	payload := strings.TrimSuffix(buf.String(), "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	cascadia "git.sharegap.net/cascadia/cascadia-go"
)

// AssistantExecutionVersion is the checkpoint and session lifecycle contract.
const AssistantExecutionVersion = 2

const (
	AssistantSessionSchemaV2           = "bahia.assistant-session.v2"
	AssistantExecutionCheckpointSchema = "bahia.audit.assistant-execution-checkpoint.v1"
	AssistantExecutionCheckpointKind   = cascadia.CAS_AUDIT
	AssistantContextVMMethodCancel     = "assistant/cancel"
	AssistantContextVMMethodReconcile  = "assistant/reconcile"

	AssistantCheckpointTypeExecution = "execution-checkpoint"
	AssistantCheckpointTagDomain     = "domain"
	AssistantCheckpointTagType       = "type"
	AssistantCheckpointTagSchema     = "schema"
	AssistantCheckpointTagSession    = "session"
	AssistantCheckpointTagRun        = "run"
	AssistantCheckpointTagRevision   = "revision"
	AssistantCheckpointTagPrevious   = "prev"
)

type AssistantWorkflow string

const (
	AssistantWorkflowBatch     AssistantWorkflow = "batch"
	AssistantWorkflowIterative AssistantWorkflow = "iterative"
)

func (w AssistantWorkflow) Valid() bool {
	return w == AssistantWorkflowBatch || w == AssistantWorkflowIterative
}

func (w *AssistantWorkflow) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	parsed := AssistantWorkflow(value)
	if !parsed.Valid() {
		return fmt.Errorf("invalid assistant workflow %q", value)
	}
	*w = parsed
	return nil
}

type AssistantExecutionPhase string

const (
	AssistantExecutionProposing        AssistantExecutionPhase = "proposing"
	AssistantExecutionAwaitingApproval AssistantExecutionPhase = "awaiting_approval"
	AssistantExecutionExecuting        AssistantExecutionPhase = "executing"
	AssistantExecutionWaitingAsync     AssistantExecutionPhase = "waiting_async"
	AssistantExecutionBlocked          AssistantExecutionPhase = "blocked"
	AssistantExecutionCancelling       AssistantExecutionPhase = "cancelling"
	AssistantExecutionCompleted        AssistantExecutionPhase = "completed"
	AssistantExecutionFailed           AssistantExecutionPhase = "failed"
	AssistantExecutionCancelled        AssistantExecutionPhase = "cancelled"
)

func (p AssistantExecutionPhase) Valid() bool {
	switch p {
	case AssistantExecutionProposing, AssistantExecutionAwaitingApproval, AssistantExecutionExecuting,
		AssistantExecutionWaitingAsync, AssistantExecutionBlocked, AssistantExecutionCancelling,
		AssistantExecutionCompleted, AssistantExecutionFailed, AssistantExecutionCancelled:
		return true
	}
	return false
}

func (p *AssistantExecutionPhase) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	parsed := AssistantExecutionPhase(value)
	if !parsed.Valid() {
		return fmt.Errorf("invalid assistant phase %q", value)
	}
	*p = parsed
	return nil
}

type AssistantWorkState string

const (
	AssistantWorkPending          AssistantWorkState = "pending"
	AssistantWorkAwaitingApproval AssistantWorkState = "awaiting_approval"
	AssistantWorkReady            AssistantWorkState = "ready"
	AssistantWorkDispatching      AssistantWorkState = "dispatching"
	AssistantWorkWaitingAsync     AssistantWorkState = "waiting_async"
	AssistantWorkObserved         AssistantWorkState = "observed"
	AssistantWorkSucceeded        AssistantWorkState = "succeeded"
	AssistantWorkFailed           AssistantWorkState = "failed"
	AssistantWorkDenied           AssistantWorkState = "denied"
	AssistantWorkSkipped          AssistantWorkState = "skipped"
	AssistantWorkUncertain        AssistantWorkState = "uncertain"
)

func (s AssistantWorkState) Valid() bool {
	switch s {
	case AssistantWorkPending, AssistantWorkAwaitingApproval, AssistantWorkReady, AssistantWorkDispatching,
		AssistantWorkWaitingAsync, AssistantWorkObserved, AssistantWorkSucceeded, AssistantWorkFailed,
		AssistantWorkDenied, AssistantWorkSkipped, AssistantWorkUncertain:
		return true
	}
	return false
}

func (s *AssistantWorkState) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	parsed := AssistantWorkState(value)
	if !parsed.Valid() {
		return fmt.Errorf("invalid assistant work state %q", value)
	}
	*s = parsed
	return nil
}

// AssistantCommandScope is persisted before either proposer runs. The field is
// deliberately not omitempty: null means unrestricted, [] means no tools.
type AssistantCommandScope struct {
	CommandName  string         `json:"command_name,omitempty"`
	AllowedTools []string       `json:"allowed_tools"`
	SelectedRefs []string       `json:"selected_refs,omitempty"`
	Arguments    map[string]any `json:"arguments,omitempty"`
}

// AssistantApprovalScope is the public, hashable scope commitment. Private
// command arguments stay in the encrypted execution checkpoint; the digest
// binds them without exposing them in the session projection.
type AssistantApprovalScope struct {
	CommandName     string   `json:"command_name,omitempty"`
	AllowedTools    []string `json:"allowed_tools"`
	SelectedRefs    []string `json:"selected_refs,omitempty"`
	ArgumentsDigest string   `json:"arguments_digest,omitempty"`
}

func (s AssistantCommandScope) ApprovalScope() (AssistantApprovalScope, error) {
	out := AssistantApprovalScope{CommandName: s.CommandName, SelectedRefs: append([]string(nil), s.SelectedRefs...)}
	if s.AllowedTools != nil {
		out.AllowedTools = append([]string{}, s.AllowedTools...)
	}
	if s.Arguments != nil {
		digest, err := ComputeAssistantArgumentsDigest(s.Arguments)
		if err != nil {
			return AssistantApprovalScope{}, err
		}
		out.ArgumentsDigest = digest
	}
	return out, nil
}

// AssistantProposalRevision identifies one immutable draft. Plan contains only
// executable input; previews and caller-supplied dispatch keys are excluded.
type AssistantProposalRevision struct {
	ProposalID       string        `json:"proposal_id"`
	Revision         uint64        `json:"revision"`
	Plan             AssistantPlan `json:"plan"`
	Hash             string        `json:"hash"`
	RequestID        string        `json:"request_id"`
	PreviousRevision uint64        `json:"previous_revision,omitempty"`
	PreviousHash     string        `json:"previous_hash,omitempty"`
}

type AssistantAuthorizationBinding struct {
	OperatorPubkey    string                    `json:"operator_pubkey"`
	DecisionRequestID string                    `json:"decision_request_id"`
	ProposalID        string                    `json:"proposal_id,omitempty"`
	ProposalRevision  uint64                    `json:"proposal_revision,omitempty"`
	ActionID          string                    `json:"action_id,omitempty"`
	ArgumentsDigest   string                    `json:"arguments_digest"`
	Scope             AssistantCommandScope     `json:"scope"`
	Permission        AssistantPermissionResult `json:"permission"`
}

type AssistantWorkItem struct {
	WorkID          string                         `json:"work_id"`
	OriginID        string                         `json:"origin_id"`
	Ordinal         int                            `json:"ordinal"`
	ToolName        string                         `json:"tool_name"`
	Arguments       map[string]any                 `json:"arguments"`
	ArgumentsDigest string                         `json:"arguments_digest"`
	Authorization   *AssistantAuthorizationBinding `json:"authorization,omitempty"`
	IdempotencyKey  string                         `json:"idempotency_key,omitempty"`
	State           AssistantWorkState             `json:"state"`
	Receipt         *AsyncToolReceipt              `json:"receipt,omitempty"`
	Observation     *AssistantToolObservation      `json:"observation,omitempty"`
}

type AssistantExecutionCancellation struct {
	Scope          string    `json:"scope"`
	RunID          string    `json:"run_id"`
	OperatorPubkey string    `json:"operator_pubkey"`
	RequestID      string    `json:"request_id"`
	Reason         string    `json:"reason,omitempty"`
	RecordedAt     time.Time `json:"recorded_at"`
}

type AssistantExecutionMigration struct {
	SourceSchema   string `json:"source_schema"`
	SourceEventID  string `json:"source_event_id"`
	Classification string `json:"classification"`
	LegacyPlanHash string `json:"legacy_plan_hash,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type AssistantExecution struct {
	Version      int                             `json:"version"`
	SessionID    string                          `json:"session_id"`
	RunID        string                          `json:"run_id"`
	TurnID       string                          `json:"turn_id"`
	RequestID    string                          `json:"request_id"`
	Workflow     AssistantWorkflow               `json:"workflow"`
	Revision     uint64                          `json:"revision"`
	Phase        AssistantExecutionPhase         `json:"phase"`
	Proposal     *AssistantProposalRevision      `json:"proposal,omitempty"`
	Work         []AssistantWorkItem             `json:"work"`
	Cursor       int                             `json:"cursor"`
	Scope        AssistantCommandScope           `json:"scope"`
	Cancellation *AssistantExecutionCancellation `json:"cancellation,omitempty"`
	Migration    *AssistantExecutionMigration    `json:"migration,omitempty"`
}

// AssistantExecutionCheckpoint is encrypted before publication. Public 4903
// tags contain only domain/type/schema and non-sensitive correlation IDs.
type AssistantExecutionCheckpoint struct {
	Execution       AssistantExecution `json:"execution"`
	PreviousEventID string             `json:"previous_event_id,omitempty"`
}

// AssistantExecutionCheckpointAEADEnvelope is the public 4903 content shape.
// Its authenticated plaintext is AssistantExecutionCheckpoint; neither work
// arguments nor transcript text appear in public content or tags.
type AssistantExecutionCheckpointAEADEnvelope struct {
	Schema         string            `json:"schema"`
	Envelope       string            `json:"envelope"`
	Algorithm      string            `json:"algorithm"`
	KeyRef         string            `json:"key_ref"`
	KeyVersion     string            `json:"key_version,omitempty"`
	Nonce          string            `json:"nonce"`
	Ciphertext     string            `json:"ciphertext"`
	AssociatedData map[string]string `json:"associated_data,omitempty"`
}

// AssistantSessionV2 is a public read model; it is not the dispatch journal.
// It never embeds the encrypted work arguments or approval bindings.
type AssistantSessionV2 struct {
	Schema            string                     `json:"schema"`
	SessionID         string                     `json:"session_id"`
	State             AssistantSessionState      `json:"state"`
	OperatorPubkey    string                     `json:"operator_pubkey"`
	Participants      []string                   `json:"participants,omitempty"`
	AssistantID       string                     `json:"assistant_id"`
	AssistantPubkey   string                     `json:"assistant_pubkey,omitempty"`
	CurrentTurnID     string                     `json:"current_turn_id,omitempty"`
	CurrentRequestID  string                     `json:"current_request_id,omitempty"`
	TranscriptSummary string                     `json:"transcript_summary,omitempty"`
	LastResultID      string                     `json:"last_result_id,omitempty"`
	ExecutionVersion  int                        `json:"execution_version"`
	Workflow          AssistantWorkflow          `json:"workflow"`
	CurrentRunID      string                     `json:"current_run_id,omitempty"`
	ExecutionRevision uint64                     `json:"execution_revision"`
	Phase             AssistantExecutionPhase    `json:"phase"`
	Scope             AssistantApprovalScope     `json:"scope"`
	Proposal          *AssistantProposalRevision `json:"proposal,omitempty"`
	PendingApprovals  []string                   `json:"pending_approvals,omitempty"`
	SubmittedEffects  int                        `json:"submitted_effects"`
	UncertainEffects  int                        `json:"uncertain_effects"`
	CheckpointEventID string                     `json:"checkpoint_event_id,omitempty"`
	// Closed reports that a session-scope cancellation closed the session to
	// new turns; ClosedAt is when it was recorded. Both are additive to the v2
	// schema (omitted when open). The cancellation reason is operator text kept
	// only in the encrypted checkpoint and is deliberately not projected.
	Closed   bool       `json:"closed,omitempty"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
}

// AssistantCancellationRequest is distinct from rejecting a draft or action.
type AssistantCancellationRequest struct {
	ContractVersion int    `json:"contract_version"`
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	Scope           string `json:"scope"` // run or session
	Reason          string `json:"reason,omitempty"`
}

// AssistantReconciliationRequest names one exact submitted request event;
// absence of evidence never authorizes a replay or a synthetic failure.
type AssistantReconciliationRequest struct {
	ContractVersion int    `json:"contract_version"`
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	WorkID          string `json:"work_id"`
	RequestEventID  string `json:"request_event_id"`
}

// AssistantBatchApprovalHashInput is the complete v2 hash envelope. The
// canonical bytes are RFC 8785 JSON over this exact field set, including null
// AllowedTools and an empty steps array. Hash is lowercase SHA-256 hex.
type AssistantBatchApprovalHashInput struct {
	Version    int                    `json:"version"`
	SessionID  string                 `json:"session_id"`
	RunID      string                 `json:"run_id"`
	Workflow   AssistantWorkflow      `json:"workflow"`
	ProposalID string                 `json:"proposal_id"`
	Revision   uint64                 `json:"revision"`
	Scope      AssistantApprovalScope `json:"scope"`
	Plan       AssistantPlan          `json:"plan"`
}

func (in AssistantBatchApprovalHashInput) CanonicalBytes() ([]byte, error) {
	if in.Version != AssistantExecutionVersion || in.Workflow != AssistantWorkflowBatch ||
		strings.TrimSpace(in.SessionID) == "" || strings.TrimSpace(in.RunID) == "" ||
		strings.TrimSpace(in.ProposalID) == "" || in.Revision == 0 {
		return nil, errors.New("invalid batch approval envelope identity")
	}
	plan, err := NormalizeAssistantExecutablePlan(in.Plan)
	if err != nil {
		return nil, err
	}
	in.Plan = plan
	return assistantCanonicalJSON(in)
}

func ComputeAssistantArgumentsDigest(arguments map[string]any) (string, error) {
	if arguments == nil {
		return "", errors.New("arguments must be an object")
	}
	b, err := assistantCanonicalJSON(arguments)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func ComputeAssistantBatchApprovalHash(in AssistantBatchApprovalHashInput) (string, error) {
	b, err := in.CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeAssistantExecutablePlan removes presentation/dispatch fields and
// refuses duplicate IDs or non-object arguments. Step order is significant.
func NormalizeAssistantExecutablePlan(plan AssistantPlan) (AssistantPlan, error) {
	out := plan
	out.ContextRefs = append([]string(nil), plan.ContextRefs...)
	out.Steps = make([]AssistantPlanStep, len(plan.Steps))
	seen := make(map[string]struct{}, len(plan.Steps))
	for i, step := range plan.Steps {
		if strings.TrimSpace(step.StepID) == "" || strings.TrimSpace(step.ToolName) == "" {
			return AssistantPlan{}, fmt.Errorf("step %d lacks identity or tool", i)
		}
		if _, exists := seen[step.StepID]; exists {
			return AssistantPlan{}, fmt.Errorf("duplicate step_id %q", step.StepID)
		}
		seen[step.StepID] = struct{}{}
		if step.ToolArgs == nil {
			return AssistantPlan{}, fmt.Errorf("step %s tool_args must be an object", step.StepID)
		}
		args, err := DeepCopyAssistantJSONMap(step.ToolArgs)
		if err != nil {
			return AssistantPlan{}, fmt.Errorf("step %s arguments: %w", step.StepID, err)
		}
		out.Steps[i] = AssistantPlanStep{StepID: step.StepID, Title: step.Title, Description: step.Description, ToolName: step.ToolName, ToolArgs: args}
	}
	return out, nil
}

// DecodeAssistantExecutablePlan rejects unknown structural fields before
// normalizing. map-valued tool_args remain arbitrary JSON objects.
func DecodeAssistantExecutablePlan(raw []byte) (AssistantPlan, error) {
	if err := ValidateAssistantIJSON(raw); err != nil {
		return AssistantPlan{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var plan AssistantPlan
	if err := decoder.Decode(&plan); err != nil {
		return AssistantPlan{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return AssistantPlan{}, errors.New("trailing JSON value")
		}
		return AssistantPlan{}, err
	}
	return NormalizeAssistantExecutablePlan(plan)
}

// DeepCopyAssistantJSONMap clones nested objects and arrays through strict JSON
// serialization so approval snapshots cannot alias producer-owned maps.
func DeepCopyAssistantJSONMap(in map[string]any) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	if err := validateAssistantUnicode(reflect.ValueOf(in)); err != nil {
		return nil, err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s AssistantCommandScope) Clone() (AssistantCommandScope, error) {
	out := s
	if s.AllowedTools != nil {
		out.AllowedTools = append([]string{}, s.AllowedTools...)
	}
	out.SelectedRefs = append([]string(nil), s.SelectedRefs...)
	var err error
	out.Arguments, err = DeepCopyAssistantJSONMap(s.Arguments)
	return out, err
}

func (e AssistantExecution) Clone() (AssistantExecution, error) {
	if err := validateAssistantUnicode(reflect.ValueOf(e)); err != nil {
		return AssistantExecution{}, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return AssistantExecution{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var out AssistantExecution
	if err := decoder.Decode(&out); err != nil {
		return AssistantExecution{}, err
	}
	return out, nil
}

// ValidateAssistantIJSON rejects duplicate keys and invalid escaped Unicode
// before Go's JSON decoder can silently normalize either one.
func ValidateAssistantIJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8 JSON")
	}
	if err := validateAssistantEscapedUnicode(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateAssistantJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateAssistantJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("non-string JSON key")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := validateAssistantJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := validateAssistantJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func validateAssistantEscapedUnicode(raw []byte) error {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return errors.New("truncated JSON escape")
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return errors.New("truncated Unicode escape")
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return err
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return errors.New("lone low surrogate")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return errors.New("lone high surrogate")
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errors.New("high surrogate without low surrogate")
			}
			i += 6
		}
	}
	return nil
}

func validateAssistantUnicode(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return validateAssistantUnicode(value.Elem())
		}
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("invalid UTF-8 string")
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateAssistantUnicode(iterator.Key()); err != nil {
				return err
			}
			if err := validateAssistantUnicode(iterator.Value()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateAssistantUnicode(value.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() {
				if err := validateAssistantUnicode(value.Field(i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func assistantCanonicalJSON(v any) ([]byte, error) {
	if err := validateAssistantUnicode(reflect.ValueOf(v)); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeAssistantJCS(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeAssistantJCS(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		writeAssistantJCSString(out, v)
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("invalid JSON number %q", v)
		}
		if f == 0 {
			out.WriteByte('0')
			return nil
		}
		if math.Abs(f) >= 1e-6 && math.Abs(f) < 1e21 {
			out.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
		} else {
			s := strconv.FormatFloat(f, 'e', -1, 64)
			parts := strings.SplitN(s, "e", 2)
			exponent, _ := strconv.Atoi(parts[1])
			if exponent >= 0 {
				out.WriteString(parts[0] + "e+" + strconv.Itoa(exponent))
			} else {
				out.WriteString(parts[0] + "e" + strconv.Itoa(exponent))
			}
		}
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeAssistantJCS(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return assistantUTF16Less(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeAssistantJCSString(out, key)
			out.WriteByte(':')
			if err := writeAssistantJCS(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", v)
	}
	return nil
}

func assistantUTF16Less(a, b string) bool {
	aa, bb := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return len(aa) < len(bb)
}

func writeAssistantJCSString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, "\\u%04x", r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

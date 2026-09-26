package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

const defaultAssistantAgentID = "bahia-operator-assistant"

const assistantPublishTimeout = 10 * time.Second

// Stable refusal codes returned as {status:"failed", step:<code>} results. The
// browser classifies these as refusals of the request, never as downstream
// execution outcomes.
const (
	AssistantRefusalValidation              = "validation_error"
	AssistantRefusalUnauthorized            = "unauthorized_participant"
	AssistantRefusalUnavailable             = "assistant_unavailable"
	AssistantRefusalRunInProgress           = "run_in_progress"
	AssistantRefusalSessionClosed           = "session_closed"
	AssistantRefusalCheckpointUnconfirmed   = "checkpoint_unconfirmed"
	AssistantRefusalProposalChanged         = "proposal_changed_requires_review"
	AssistantRefusalStaleApproval           = "stale_approval"
	AssistantRefusalStaleTarget             = "stale_target"
	AssistantRefusalInvalidState            = "invalid_state"
	AssistantRefusalApprovalDenied          = "approval_denied"
	AssistantRefusalReconciliation          = "reconciliation_rejected"
	AssistantRefusalContractUpgradeRequired = "approval_contract_upgrade_required"
	AssistantRefusalLegacyReadOnly          = "legacy_session_read_only"
	AssistantRefusalSessionLookup           = "session_lookup_unavailable"
	AssistantRefusalUnknownSession          = "unknown_session"
	AssistantRefusalPlanValidation          = "plan_validation_error"
	AssistantRefusalExecution               = "execution_error"
	// AssistantRefusalWorkflowUnavailable refuses a new turn or batch approval
	// in a workflow this deployment does not offer (batch without
	// assistant.llm_model). The request is never run in another workflow.
	AssistantRefusalWorkflowUnavailable = "workflow_unavailable"
)

// AssistantEventPublisher publishes signed assistant events to relays.
type AssistantEventPublisher interface {
	Publish(ctx context.Context, ev nostr.Event) (int, error)
}

type AssistantRelaySubscriber interface {
	SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (AssistantMergedSubscription, error)
}

type AssistantMergedSubscription interface {
	EventChan() <-chan *nostr.Event
	ClosedChan() <-chan AssistantRelayClosed
	EOSEChan() <-chan struct{}
	Close()
}

type AssistantRelayClosed struct {
	RelayURL string
	Reason   string
}

// AssistantIdentity describes the managed assistant identity used for attribution.
type AssistantIdentity struct {
	AgentID string
	Pubkey  string
	Npub    string
}

// AssistantRequestSource describes the canonical ContextVM request that initiated
// assistant orchestration.
type AssistantRequestSource struct {
	Event          *nostr.Event
	OperatorPubkey string
	RequestID      string
	DedupKey       string
}

// AssistantOperationResult is returned as the ContextVM JSON-RPC result payload
// for prompt/approval/cancel/reconcile operations.
type AssistantOperationResult map[string]any

// AssistantExecutionReader exposes engine-owned read models. The concrete
// AssistantExecutionEngine implements it.
type AssistantExecutionReader interface {
	Snapshot(sessionID string) (domain.AssistantExecution, bool)
	Projection(sessionID string) (domain.AssistantSessionV2, bool)
}

// AssistantOrchestratorConfig contains dependencies and startup state.
type AssistantOrchestratorConfig struct {
	// Engine is the only execution owner. The orchestrator routes requests to
	// it and never dispatches, observes or persists execution itself.
	Engine AssistantTurnEngine
	// Executions defaults to Engine when the engine implements it.
	Executions      AssistantExecutionReader
	DefaultWorkflow domain.AssistantWorkflow
	Publisher       AssistantEventPublisher
	Subscriber      AssistantRelaySubscriber
	Signer          nostr.Signer
	Identity        AssistantIdentity
	// InitialSessions is the bounded startup cache of historical v1 sessions.
	// v1 history is read-only: it answers participant checks and refuses new
	// turns; it never authorizes an unknown session as new.
	InitialSessions []domain.AssistantSession
	Logger          *slog.Logger
}

// AssistantOrchestrator is request routing for the operator assistant: it
// validates requests, selects the workflow inputs and delegates every
// execution decision to the unified executor.
type AssistantOrchestrator struct {
	engine          AssistantTurnEngine
	executions      AssistantExecutionReader
	defaultWorkflow domain.AssistantWorkflow
	subscriber      AssistantRelaySubscriber
	signer          nostr.Signer
	identity        AssistantIdentity
	status          *AssistantStatusEventPublisher
	logger          *slog.Logger

	mu     sync.Mutex
	legacy map[string]domain.AssistantSession
}

// NewAssistantOrchestrator creates the request router.
func NewAssistantOrchestrator(config AssistantOrchestratorConfig) *AssistantOrchestrator {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	identity := config.Identity
	if strings.TrimSpace(identity.AgentID) == "" {
		identity.AgentID = defaultAssistantAgentID
	}
	executions := config.Executions
	if executions == nil {
		executions, _ = config.Engine.(AssistantExecutionReader)
	}
	o := &AssistantOrchestrator{
		engine:          config.Engine,
		executions:      executions,
		defaultWorkflow: config.DefaultWorkflow,
		subscriber:      config.Subscriber,
		signer:          config.Signer,
		identity:        identity,
		status:          NewAssistantStatusEventPublisher(config.Publisher, config.Signer, identity),
		logger:          logger.With("component", "assistant_orchestrator"),
		legacy:          make(map[string]domain.AssistantSession),
	}
	for _, session := range config.InitialSessions {
		copySession := session
		normalizeSessionParticipants(&copySession)
		if copySession.SessionID != "" {
			o.legacy[copySession.SessionID] = copySession
		}
	}
	return o
}

// HandlePromptRequest starts a turn. Workflow selection: an explicit
// per-request workflow wins, then the session's persisted workflow, then the
// configured default. A second prompt against an unfinished run is refused
// with run_in_progress by the engine.
func (o *AssistantOrchestrator) HandlePromptRequest(ctx context.Context, source AssistantRequestSource, req domain.AssistantPromptRequest) (AssistantOperationResult, error) {
	event, source, err := o.normalizeSource(source)
	if err != nil {
		return nil, err
	}
	if err := validatePromptRequest(req); err != nil {
		return o.refusal(event, req.SessionID, AssistantRefusalValidation, err.Error(), nil), nil
	}
	if req.ContractVersion != 0 && req.ContractVersion != domain.AssistantExecutionVersion {
		return o.refusal(event, req.SessionID, AssistantRefusalValidation, fmt.Sprintf("unsupported assistant contract_version %d", req.ContractVersion), nil), nil
	}
	if req.Workflow != "" && !req.Workflow.Valid() {
		return o.refusal(event, req.SessionID, AssistantRefusalValidation, fmt.Sprintf("invalid assistant workflow %q", req.Workflow), nil), nil
	}
	if o.engine == nil {
		return o.refusal(event, req.SessionID, AssistantRefusalUnavailable, "assistant executor is not configured", nil), nil
	}
	existing, code, err := o.resolveSession(ctx, req.SessionID, source.OperatorPubkey)
	if code != "" {
		msg := "assistant session cannot accept a new turn"
		if err != nil {
			msg = err.Error()
		}
		return o.refusal(event, req.SessionID, code, msg, nil), nil
	}
	o.logger.Info("assistant prompt received", "session_id", req.SessionID, "turn_id", req.TurnID, "request_event_id", source.RequestID, "workflow", string(req.Workflow))
	result, err := o.engine.StartTurn(ctx, AssistantTurnStartRequest{Prompt: req, OperatorPubkey: source.OperatorPubkey, RequestEventID: source.RequestID, ExistingSession: existing, DefaultWorkflow: o.defaultWorkflow})
	return o.engineResult(event, req.SessionID, result, err), nil
}

// HandleApprovalRequest routes a v2 batch or action decision. Unversioned v1
// requests are translated by the transport's compatibility decoder before
// they reach this method; anything still unversioned is refused.
func (o *AssistantOrchestrator) HandleApprovalRequest(ctx context.Context, source AssistantRequestSource, req domain.AssistantApprovalRequest) (AssistantOperationResult, error) {
	event, source, err := o.normalizeSource(source)
	if err != nil {
		return nil, err
	}
	if req.ContractVersion != domain.AssistantExecutionVersion {
		return o.refusal(event, req.SessionID, AssistantRefusalContractUpgradeRequired, "assistant approvals require contract_version 2", nil), nil
	}
	req.Decision = strings.ToLower(strings.TrimSpace(req.Decision))
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.RunID) == "" || (req.Decision != "approve" && req.Decision != "reject") {
		return o.refusal(event, req.SessionID, AssistantRefusalValidation, "v2 approval requires session_id, run_id and decision approve or reject", nil), nil
	}
	if req.ModifiedPlan != nil && req.Decision != "approve" {
		return o.refusal(event, req.SessionID, AssistantRefusalValidation, "modified_plan is only valid for approve decisions", nil), nil
	}
	if o.engine == nil {
		return o.refusal(event, req.SessionID, AssistantRefusalUnavailable, "assistant executor is not configured", nil), nil
	}
	requestID := firstNonEmptyString(strings.TrimSpace(req.RequestID), source.RequestID)
	result, err := o.engine.Decide(ctx, AssistantTurnDecisionRequest{Approval: req, OperatorPubkey: source.OperatorPubkey, RequestEventID: requestID})
	return o.engineResult(event, req.SessionID, result, err), nil
}

// HandleCancellationRequest routes an explicit run or session cancellation.
func (o *AssistantOrchestrator) HandleCancellationRequest(ctx context.Context, source AssistantRequestSource, req domain.AssistantCancellationRequest) (AssistantOperationResult, error) {
	event, source, err := o.normalizeSource(source)
	if err != nil {
		return nil, err
	}
	if req.ContractVersion != domain.AssistantExecutionVersion || strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.RunID) == "" || (req.Scope != "run" && req.Scope != "session") {
		return o.refusal(event, req.SessionID, AssistantRefusalValidation, "cancellation requires contract_version 2, session_id, run_id and scope run or session", nil), nil
	}
	if o.engine == nil {
		return o.refusal(event, req.SessionID, AssistantRefusalUnavailable, "assistant executor is not configured", nil), nil
	}
	result, err := o.engine.Cancel(ctx, AssistantTurnCancellationRequest{Cancellation: req, OperatorPubkey: source.OperatorPubkey, RequestEventID: source.RequestID})
	return o.engineResult(event, req.SessionID, result, err), nil
}

// HandleReconciliationRequest attaches verified evidence for uncertain work.
func (o *AssistantOrchestrator) HandleReconciliationRequest(ctx context.Context, source AssistantRequestSource, req domain.AssistantReconciliationRequest) (AssistantOperationResult, error) {
	event, source, err := o.normalizeSource(source)
	if err != nil {
		return nil, err
	}
	if req.ContractVersion != domain.AssistantExecutionVersion || strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.WorkID) == "" || strings.TrimSpace(req.RequestEventID) == "" {
		return o.refusal(event, req.SessionID, AssistantRefusalValidation, "reconciliation requires contract_version 2, session_id, run_id, work_id and request_event_id", nil), nil
	}
	if o.engine == nil {
		return o.refusal(event, req.SessionID, AssistantRefusalUnavailable, "assistant executor is not configured", nil), nil
	}
	result, err := o.engine.Reconcile(ctx, AssistantTurnReconciliationRequest{Reconciliation: req, OperatorPubkey: source.OperatorPubkey, RequestEventID: source.RequestID})
	return o.engineResult(event, req.SessionID, result, err), nil
}

// ExecutionSnapshot returns the engine's confirmed execution for a session.
// The transport's v1 compatibility decoder uses it to recognize migrated
// targets; it is a copy and grants no authority by itself.
func (o *AssistantOrchestrator) ExecutionSnapshot(sessionID string) (domain.AssistantExecution, bool) {
	if o == nil || o.executions == nil {
		return domain.AssistantExecution{}, false
	}
	return o.executions.Snapshot(sessionID)
}

// IsSessionParticipant reports whether operator may interact with a known
// session. Unknown sessions return true so a new session can be created; the
// engine still authorizes every operation against the session projection.
func (o *AssistantOrchestrator) IsSessionParticipant(sessionID, operator string) bool {
	sessionID = strings.TrimSpace(sessionID)
	operator = strings.ToLower(strings.TrimSpace(operator))
	if sessionID == "" || operator == "" {
		return false
	}
	if o.executions != nil {
		if projection, ok := o.executions.Projection(sessionID); ok {
			return assistantProjectionAuthorizes(projection, operator)
		}
	}
	o.mu.Lock()
	session, known := o.legacy[sessionID]
	o.mu.Unlock()
	if !known {
		return true
	}
	return sessionHasParticipant(&session, operator)
}

// PublishAssistantStatus publishes an informational status event.
func (o *AssistantOrchestrator) PublishAssistantStatus(ctx context.Context, sessionID, status string, content map[string]any) error {
	return o.status.PublishAssistantStatus(ctx, sessionID, status, content)
}

// resolveSession returns the session's v2 projection when one exists. A
// caller-supplied session ID unknown to this process is checked with a scoped
// coordinate lookup through EOSE before it may be created: the startup cache
// is bounded and cannot prove a session is new. v1-only history is read-only.
func (o *AssistantOrchestrator) resolveSession(ctx context.Context, sessionID, operator string) (*domain.AssistantSessionV2, string, error) {
	if o.executions != nil {
		if projection, ok := o.executions.Projection(sessionID); ok {
			return o.loadFinishedRun(ctx, projection, operator)
		}
	}
	o.mu.Lock()
	_, legacy := o.legacy[sessionID]
	o.mu.Unlock()
	if legacy {
		return nil, AssistantRefusalLegacyReadOnly, errors.New("historical v1 assistant sessions are read-only; start a new session")
	}
	found, err := o.lookupSession(ctx, sessionID)
	if err != nil {
		return nil, AssistantRefusalSessionLookup, fmt.Errorf("assistant session lookup incomplete: %w", err)
	}
	switch {
	case found.v2 != nil:
		if hydrator, ok := o.engine.(AssistantExecutionProjectionHydrator); ok {
			hydrator.HydrateProjection(*found.v2, found.v2At)
		}
		return o.loadFinishedRun(ctx, *found.v2, operator)
	case found.v1 != nil:
		o.mu.Lock()
		o.legacy[sessionID] = *found.v1
		o.mu.Unlock()
		return nil, AssistantRefusalLegacyReadOnly, errors.New("historical v1 assistant sessions are read-only; start a new session")
	}
	return nil, "", nil
}

// loadFinishedRun makes the engine load a finished run's checkpoint chain the
// first time this process starts a new turn after it. Startup recovery only
// hydrates finished projections, and the projection has no "closed" field, so
// a session-scope cancellation recorded on a finished run is only enforced
// once its checkpoint is loaded.
func (o *AssistantOrchestrator) loadFinishedRun(ctx context.Context, projection domain.AssistantSessionV2, operator string) (*domain.AssistantSessionV2, string, error) {
	if !assistantProjectionAuthorizes(projection, operator) {
		return nil, AssistantRefusalUnauthorized, errors.New("requester is not a participant in this assistant session")
	}
	if projection.CurrentRunID == "" || !assistantPhaseFinished(projection.Phase) {
		return &projection, "", nil
	}
	if x, ok := o.ExecutionSnapshot(projection.SessionID); ok && x.RunID == projection.CurrentRunID {
		return &projection, "", nil
	}
	if err := o.engine.Recover(ctx, AssistantExecutionReference{SessionID: projection.SessionID, RunID: projection.CurrentRunID, CheckpointEventID: projection.CheckpointEventID}); err != nil {
		return nil, AssistantRefusalSessionLookup, fmt.Errorf("assistant session history could not be loaded: %w", err)
	}
	return &projection, "", nil
}

type assistantSessionLookup struct {
	v2   *domain.AssistantSessionV2
	v2At nostr.Timestamp
	v1   *domain.AssistantSession
}

// lookupSession backfills this service's v1 and v2 projection coordinates for
// one session. Only a complete backfill (EOSE) proves absence; CLOSED or a
// stream ending early is an error.
func (o *AssistantOrchestrator) lookupSession(ctx context.Context, sessionID string) (assistantSessionLookup, error) {
	if o.subscriber == nil || o.signer == nil {
		return assistantSessionLookup{}, errors.New("assistant relay subscriber or signer not configured")
	}
	author, err := o.signer.GetPublicKey(ctx)
	if err != nil {
		return assistantSessionLookup{}, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{domain.KindAssistantSessionState}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"d": []string{domain.AssistantSessionSchema + ":" + sessionID, domain.AssistantSessionSchemaV2 + ":" + sessionID}}}
	sub, err := o.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return assistantSessionLookup{}, err
	}
	defer sub.Close()
	var out assistantSessionLookup
	var v1At nostr.Timestamp
	events, closed, eose := sub.EventChan(), sub.ClosedChan(), sub.EOSEChan()
	for {
		select {
		case <-ctx.Done():
			return assistantSessionLookup{}, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return assistantSessionLookup{}, fmt.Errorf("closed by %s: %s", c.RelayURL, c.Reason)
		case ev, ok := <-events:
			if !ok {
				if assistantEOSEReached(eose) {
					return out, nil
				}
				return assistantSessionLookup{}, errors.New("session lookup ended before EOSE")
			}
			if ev == nil || ev.PubKey != author || !ev.CheckID() || !ev.VerifySignature() || tagValue(ev.Tags, "session") != sessionID {
				continue
			}
			switch tagValue(ev.Tags, domain.AssistantSessionTagSchema) {
			case domain.AssistantSessionSchemaV2:
				var p domain.AssistantSessionV2
				if json.Unmarshal([]byte(ev.Content), &p) != nil || p.SessionID != sessionID || ev.CreatedAt < out.v2At {
					continue
				}
				out.v2, out.v2At = &p, ev.CreatedAt
			case domain.AssistantSessionSchema:
				var s domain.AssistantSession
				if json.Unmarshal([]byte(ev.Content), &s) != nil || s.SessionID != sessionID || ev.CreatedAt < v1At {
					continue
				}
				normalizeSessionParticipants(&s)
				out.v1, v1At = &s, ev.CreatedAt
			}
		case <-eose:
			return out, nil
		}
	}
}

func (o *AssistantOrchestrator) normalizeSource(source AssistantRequestSource) (*nostr.Event, AssistantRequestSource, error) {
	event := source.Event
	if event == nil {
		return nil, source, fmt.Errorf("assistant request event is nil")
	}
	if strings.TrimSpace(source.OperatorPubkey) == "" {
		source.OperatorPubkey = event.PubKey.Hex()
	}
	if strings.TrimSpace(source.RequestID) == "" {
		source.RequestID = event.ID.Hex()
	}
	return event, source, nil
}

// engineResult maps an engine outcome onto the stable ContextVM result shape.
// Accepted requests report status "accepted" plus the execution phase, so a
// run that later fails is never mistaken for a refused request.
func (o *AssistantOrchestrator) engineResult(event *nostr.Event, sessionID string, result AssistantTurnResult, err error) AssistantOperationResult {
	if err != nil {
		code, message := assistantRefusalFromEngineError(err)
		var extra map[string]any
		if result.Session.SessionID != "" {
			extra = map[string]any{"run_id": result.Session.CurrentRunID, "phase": string(result.Session.Phase), "session": result.Session}
		}
		o.logger.Info("assistant request refused", "session_id", sessionID, "code", code, "error", err)
		return o.refusal(event, sessionID, code, message, extra)
	}
	p := result.Session
	return o.resultPayload(event, sessionID, "accepted", result.Acknowledgment, map[string]any{
		"summary":            "assistant request accepted: " + result.Acknowledgment,
		"acknowledgment":     result.Acknowledgment,
		"run_id":             p.CurrentRunID,
		"workflow":           string(p.Workflow),
		"phase":              string(p.Phase),
		"execution_revision": result.ExecutionRevision,
		"pending_effects":    result.PendingEffects,
		"session":            p,
	})
}

func assistantRefusalFromEngineError(err error) (string, string) {
	var denial *AssistantWorkDenial
	switch {
	case errors.Is(err, ErrAssistantRunInProgress):
		return AssistantRefusalRunInProgress, "an assistant run is already in progress for this session"
	case errors.Is(err, ErrAssistantSessionClosed):
		return AssistantRefusalSessionClosed, "assistant session was closed by a session-scope cancellation"
	case errors.Is(err, ErrAssistantCheckpointUnconfirmed):
		return AssistantRefusalCheckpointUnconfirmed, err.Error()
	case errors.Is(err, ErrAssistantProposalChangedRequiresReview):
		return AssistantRefusalProposalChanged, "preparation changed the approved input; review the replacement proposal"
	case errors.Is(err, ErrAssistantStaleTarget):
		return AssistantRefusalStaleApproval, err.Error()
	case errors.Is(err, ErrAssistantNotAwaitingApproval):
		return AssistantRefusalInvalidState, "assistant run is not awaiting approval"
	case errors.Is(err, ErrAssistantOperatorMismatch):
		return AssistantRefusalUnauthorized, "requester is not a participant in this assistant session"
	case errors.Is(err, ErrAssistantReconciliationRejected):
		return AssistantRefusalReconciliation, err.Error()
	case errors.Is(err, ErrAssistantWorkflowUnavailable):
		return AssistantRefusalWorkflowUnavailable, err.Error()
	case errors.As(err, &denial):
		return AssistantRefusalApprovalDenied, err.Error()
	case strings.HasPrefix(err.Error(), "modified plan invalid"):
		return AssistantRefusalPlanValidation, err.Error()
	default:
		return AssistantRefusalExecution, err.Error()
	}
}

func (o *AssistantOrchestrator) refusal(event *nostr.Event, sessionID, code, message string, extra map[string]any) AssistantOperationResult {
	content := map[string]any{"summary": message, "error": message}
	for k, v := range extra {
		content[k] = v
	}
	return o.resultPayload(event, sessionID, "failed", code, content)
}

func (o *AssistantOrchestrator) resultPayload(requestEvent *nostr.Event, sessionID, status, step string, content map[string]any) AssistantOperationResult {
	payload := AssistantOperationResult{}
	for k, v := range content {
		payload[k] = v
	}
	payload["session_id"] = sessionID
	payload["status"] = status
	payload["agent"] = o.identity.AgentID
	if step != "" {
		payload["step"] = step
	}
	if requestEvent != nil {
		payload["request_event_id"] = requestEvent.ID.Hex()
	}
	return payload
}

// AssistantStatusEventPublisher signs and publishes kind-30315 assistant status
// events for operator visibility (planner stream, final answers).
type AssistantStatusEventPublisher struct {
	publisher AssistantEventPublisher
	signer    nostr.Signer
	identity  AssistantIdentity
}

var _ AssistantStatusPublisher = (*AssistantStatusEventPublisher)(nil)

func NewAssistantStatusEventPublisher(publisher AssistantEventPublisher, signer nostr.Signer, identity AssistantIdentity) *AssistantStatusEventPublisher {
	if strings.TrimSpace(identity.AgentID) == "" {
		identity.AgentID = defaultAssistantAgentID
	}
	return &AssistantStatusEventPublisher{publisher: publisher, signer: signer, identity: identity}
}

func (p *AssistantStatusEventPublisher) PublishAssistantStatus(ctx context.Context, sessionID, status string, content map[string]any) error {
	if p == nil || p.publisher == nil || p.signer == nil {
		return fmt.Errorf("assistant status publisher is not configured")
	}
	body := map[string]any{}
	for k, v := range content {
		body[k] = v
	}
	body["status"] = status
	contentJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal assistant status: %w", err)
	}
	dTag := fmt.Sprintf("%s:%s:%s:%d", domain.AssistantStatusSchema, sessionID, status, time.Now().UnixNano())
	tags := nostr.Tags{{"d", dTag}, {"schema", domain.AssistantStatusSchema}, {"session", sessionID}, {"agent", p.identity.AgentID}, {"status", status}}
	if runID := stringFromMap(body, "run_id"); runID != "" {
		tags = append(tags, nostr.Tag{"run", runID})
	}
	if streaming, _ := body["streaming"].(bool); streaming {
		tags = append(tags, nostr.Tag{"streaming", "true"})
	}
	ev := &nostr.Event{Kind: domain.KindAssistantStatus, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if err := signGoNostrEvent(ctx, p.signer, ev); err != nil {
		return fmt.Errorf("sign assistant status: %w", err)
	}
	publishCtx, cancel := context.WithTimeout(ctx, assistantPublishTimeout)
	defer cancel()
	published, err := p.publisher.Publish(publishCtx, *ev)
	if err != nil {
		return fmt.Errorf("publish assistant status: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("publish assistant status: no relay accepted event")
	}
	return nil
}

func normalizeSessionParticipants(session *domain.AssistantSession) {
	if session == nil {
		return
	}
	if strings.TrimSpace(session.OperatorPubkey) == "" && len(session.Participants) > 0 {
		session.OperatorPubkey = session.Participants[0]
	}
	participants := make([]string, 0, len(session.Participants)+1)
	seen := map[string]struct{}{}
	for _, participant := range append([]string{session.OperatorPubkey}, session.Participants...) {
		clean := strings.ToLower(strings.TrimSpace(participant))
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		participants = append(participants, clean)
	}
	session.Participants = participants
}

func sessionHasParticipant(session *domain.AssistantSession, operator string) bool {
	if session == nil {
		return false
	}
	operator = strings.ToLower(strings.TrimSpace(operator))
	if operator == "" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(session.OperatorPubkey)) == operator {
		return true
	}
	for _, participant := range session.Participants {
		if strings.ToLower(strings.TrimSpace(participant)) == operator {
			return true
		}
	}
	return false
}

func validatePromptRequest(req domain.AssistantPromptRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(req.TurnID) == "" {
		return fmt.Errorf("turn_id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	return nil
}

func tagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func tagContainsValue(tags nostr.Tags, key string, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

func routeContextStrings(routeContext map[string]any) map[string]string {
	out := make(map[string]string, len(routeContext))
	for k, v := range routeContext {
		if strings.TrimSpace(k) == "" || v == nil {
			continue
		}
		switch vv := v.(type) {
		case string:
			out[k] = vv
		default:
			b, _ := json.Marshal(vv)
			out[k] = string(b)
		}
	}
	return out
}

func stringFromMap(m map[string]any, key string) string {
	v := m[key]
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func signGoNostrEvent(ctx context.Context, signer nostr.Signer, ev *nostr.Event) error {
	if ev == nil {
		return fmt.Errorf("nostr event is nil")
	}
	if signer == nil {
		return fmt.Errorf("assistant signer is not configured")
	}
	return signer.SignEvent(ctx, ev)
}

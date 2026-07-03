package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantSessionRecoveryConfig configures startup recovery of assistant sessions.
type AssistantSessionRecoveryConfig struct {
	RecentLimit   int
	ServicePubkey string
	Logger        *slog.Logger
	AgentRuntime  *AssistantToolRuntime
}

// AssistantSessionRecoveryRunner resumes observation of pending assistant steps after restart.
type AssistantSessionRecoveryRunner struct {
	orchestrator  *AssistantOrchestrator
	agentRuntime  *AssistantToolRuntime
	limit         int
	servicePubkey string
	logger        *slog.Logger
}

type recoveredSessionEvent struct {
	session domain.AssistantSession
	created nostr.Timestamp
}

func NewAssistantSessionRecoveryRunner(orchestrator *AssistantOrchestrator, cfg AssistantSessionRecoveryConfig) *AssistantSessionRecoveryRunner {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	limit := cfg.RecentLimit
	if limit <= 0 {
		limit = 500
	}
	return &AssistantSessionRecoveryRunner{orchestrator: orchestrator, agentRuntime: cfg.AgentRuntime, limit: limit, servicePubkey: strings.TrimSpace(cfg.ServicePubkey), logger: logger.With("component", "assistant_session_recovery")}
}

func (r *AssistantSessionRecoveryRunner) Name() string { return "assistant-session-recovery" }

// Run performs one EOSE-aware startup pass. It does not periodically poll.
func (r *AssistantSessionRecoveryRunner) Run(ctx context.Context) error {
	if r == nil || r.orchestrator == nil {
		return nil
	}
	o := r.orchestrator
	if o.subscriber == nil {
		r.logger.Warn("assistant session recovery skipped: relay subscriber not configured")
		return nil
	}
	servicePubkey := r.servicePubkey
	if servicePubkey == "" {
		servicePubkey = strings.TrimSpace(o.identity.Pubkey)
	}
	if servicePubkey == "" {
		r.logger.Warn("assistant session recovery skipped: service pubkey not configured")
		return nil
	}

	r.logger.Info("assistant session recovery started", "limit", r.limit, "service_pubkey", servicePubkey)
	sessions, err := r.queryRecentSessions(ctx, servicePubkey)
	if err != nil {
		r.logger.Warn("assistant session recovery query failed", "error", err)
		return nil
	}
	for i := range sessions {
		if ctx.Err() != nil {
			return nil
		}
		r.recoverSession(ctx, &sessions[i])
	}
	r.logger.Info("assistant session recovery completed", "sessions_checked", len(sessions))
	return nil
}

func (r *AssistantSessionRecoveryRunner) queryRecentSessions(ctx context.Context, servicePubkey string) ([]domain.AssistantSession, error) {
	serviceAuthor, err := nostr.PubKeyFromHex(servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("decode service pubkey: %w", err)
	}
	merged, err := r.orchestrator.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{domain.KindAssistantSessionState}, Authors: []nostr.PubKey{serviceAuthor}, Tags: nostr.TagMap{"schema": []string{domain.AssistantSessionSchema}}, Limit: r.limit}})
	if err != nil {
		return nil, err
	}
	defer merged.Close()

	latest := map[string]recoveredSessionEvent{}
	seenEvents := map[string]struct{}{}
	eventsCh := merged.EventChan()
	closedCh := merged.ClosedChan()
	eoseCh := merged.EOSEChan()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case closed, ok := <-closedCh:
			if !ok {
				closedCh = nil
				continue
			}
			r.logger.Warn("assistant session recovery relay subscription closed", "relay", closed.RelayURL, "reason", closed.Reason)
		case ev, ok := <-eventsCh:
			if !ok {
				return sessionEventsToSlice(latest), nil
			}
			if ev == nil {
				continue
			}
			eventID := ev.ID.Hex()
			if _, dup := seenEvents[eventID]; dup {
				continue
			}
			seenEvents[eventID] = struct{}{}
			var session domain.AssistantSession
			if err := json.Unmarshal([]byte(ev.Content), &session); err != nil {
				r.logger.Warn("failed to parse recovered assistant session", "event_id", eventID, "error", err)
				continue
			}
			if session.SessionID == "" {
				continue
			}
			if current, ok := latest[session.SessionID]; !ok || ev.CreatedAt > current.created {
				latest[session.SessionID] = recoveredSessionEvent{session: session, created: ev.CreatedAt}
			}
		case <-eoseCh:
			return sessionEventsToSlice(latest), nil
		}
	}
}

func sessionEventsToSlice(latest map[string]recoveredSessionEvent) []domain.AssistantSession {
	sessions := make([]domain.AssistantSession, 0, len(latest))
	for _, item := range latest {
		sessions = append(sessions, item.session)
	}
	return sessions
}

func (r *AssistantSessionRecoveryRunner) recoverSession(ctx context.Context, recovered *domain.AssistantSession) {
	if recovered == nil {
		return
	}
	if r.recoverAgentLoop(ctx, recovered) {
		return
	}
	if len(recovered.PendingSteps) == 0 {
		return
	}
	if recovered.State != domain.AssistantSessionStateExecuting && recovered.State != domain.AssistantSessionStateBlocked {
		return
	}
	lock := r.orchestrator.lockForSession(recovered.SessionID)
	lock.Lock()
	session := r.orchestrator.loadOrCreateSession(recovered.SessionID, recovered.OperatorPubkey)
	*session = *recovered
	lock.Unlock()

	r.logger.Info("recovering assistant session", "session_id", recovered.SessionID, "state", recovered.State, "pending_steps", len(recovered.PendingSteps))
	for {
		lock.Lock()
		if session.State != domain.AssistantSessionStateExecuting && session.State != domain.AssistantSessionStateBlocked {
			lock.Unlock()
			return
		}
		if len(session.PendingSteps) == 0 {
			session.State = domain.AssistantSessionStateCompleted
			_ = r.orchestrator.publishSession(ctx, session)
			lock.Unlock()
			_ = r.orchestrator.publishStatus(ctx, nil, recovered.SessionID, "completed", map[string]any{"summary": "assistant plan completed during startup recovery", "step": "completed", "plan_hash": session.LastPlanHash})
			return
		}
		step := session.PendingSteps[0]
		receipt := r.receiptForStep(session, step)
		if receipt == nil {
			r.logger.Warn("pending assistant step has no async receipt; leaving session blocked", "session_id", session.SessionID, "step_id", step.StepID)
			session.State = domain.AssistantSessionStateBlocked
			_ = r.orchestrator.publishSession(ctx, session)
			lock.Unlock()
			return
		}
		session.State = domain.AssistantSessionStateExecuting
		_ = r.orchestrator.publishSession(ctx, session)
		lock.Unlock()

		r.logger.Info("recovering pending assistant step", "session_id", recovered.SessionID, "step_id", step.StepID, "downstream_request", receipt.RequestEventID)
		outcome, err := r.findTerminalResult(ctx, receipt)
		if err == nil && outcome.Status == "" {
			outcome, err = r.orchestrator.observeDownstreamResult(ctx, recovered.SessionID, step, receipt)
		}

		lock.Lock()
		if err != nil || outcome.Status == "blocked" {
			session.State = domain.AssistantSessionStateBlocked
			_ = r.orchestrator.publishSession(ctx, session)
			lock.Unlock()
			r.logger.Warn("assistant session recovery blocked", "session_id", recovered.SessionID, "step_id", step.StepID, "error", err)
			_ = r.orchestrator.publishStatus(ctx, nil, recovered.SessionID, "blocked", map[string]any{"summary": "downstream observation blocked during startup recovery", "step": "blocked", "plan_hash": session.LastPlanHash, "step_id": step.StepID, "tool_name": step.ToolName})
			return
		}
		if outcome.Status == "failed" {
			session.State = domain.AssistantSessionStateFailed
			_ = r.orchestrator.publishSession(ctx, session)
			lock.Unlock()
			r.logger.Info("assistant step recovered as failed", "session_id", recovered.SessionID, "step_id", step.StepID)
			_ = r.orchestrator.publishStatus(ctx, nil, recovered.SessionID, "failed", map[string]any{"summary": "downstream step failed before/during startup recovery", "step": "downstream_failed", "plan_hash": session.LastPlanHash, "step_id": step.StepID, "tool_name": step.ToolName, "downstream_result": outcome.Event})
			return
		}
		r.orchestrator.clearPendingReceipt(session, receipt.IdempotencyKey)
		removePendingStep(session, step.StepID)
		_ = r.orchestrator.publishSession(ctx, session)
		lock.Unlock()
		r.logger.Info("assistant step recovered as completed", "session_id", recovered.SessionID, "step_id", step.StepID)
	}
}

func (r *AssistantSessionRecoveryRunner) recoverAgentLoop(ctx context.Context, recovered *domain.AssistantSession) bool {
	metadata := assistantAgentLoopMetadata(recovered)
	if metadata.State != domain.AssistantAgentLoopStateWaitingAsync || metadata.WaitingReceipt == nil {
		return false
	}
	if r.agentRuntime == nil {
		r.logger.Warn("agentic assistant session is waiting_async but runtime recovery is not configured", "session_id", recovered.SessionID, "run_id", metadata.RunID, "tool_call_id", metadata.PendingToolCallID)
		r.blockAgentLoopRecovery(ctx, recovered, metadata, "agentic assistant async recovery runtime is not configured")
		return true
	}
	if recovered.State != domain.AssistantSessionStateExecuting && recovered.State != domain.AssistantSessionStateBlocked {
		return true
	}
	lock := r.orchestrator.lockForSession(recovered.SessionID)
	lock.Lock()
	session := r.orchestrator.loadOrCreateSession(recovered.SessionID, recovered.OperatorPubkey)
	*session = *recovered
	lock.Unlock()

	r.logger.Info("recovering agentic assistant async tool", "session_id", recovered.SessionID, "run_id", metadata.RunID, "tool_call_id", metadata.PendingToolCallID, "downstream_request", metadata.WaitingReceipt.RequestEventID)
	obs, err := r.agentRuntime.ResumeAsync(ctx, AssistantToolResumeRequest{Session: session, RunID: metadata.RunID})
	if err != nil {
		r.logger.Warn("agentic assistant async recovery failed", "session_id", recovered.SessionID, "run_id", metadata.RunID, "tool_call_id", metadata.PendingToolCallID, "error", err)
		return true
	}
	if obs != nil && obs.Status == domain.AssistantToolObservationFailed {
		r.logger.Warn("agentic assistant async recovery produced failed observation", "session_id", recovered.SessionID, "run_id", metadata.RunID, "tool_call_id", metadata.PendingToolCallID, "observation_id", obs.ObservationID, "blocked", obs.Metadata["blocked"])
		return true
	}
	if obs != nil {
		r.logger.Info("agentic assistant async recovery resumed", "session_id", recovered.SessionID, "run_id", metadata.RunID, "tool_call_id", metadata.PendingToolCallID, "observation_id", obs.ObservationID)
	}
	return true
}

func (r *AssistantSessionRecoveryRunner) blockAgentLoopRecovery(ctx context.Context, recovered *domain.AssistantSession, metadata domain.AssistantAgentLoopMetadata, reason string) {
	if recovered == nil {
		return
	}
	lock := r.orchestrator.lockForSession(recovered.SessionID)
	lock.Lock()
	session := r.orchestrator.loadOrCreateSession(recovered.SessionID, recovered.OperatorPubkey)
	*session = *recovered
	metadata.State = domain.AssistantAgentLoopStateBlocked
	metadata.UpdatedAt = time.Now().UTC()
	setAssistantAgentLoopMetadata(session, metadata)
	session.State = domain.AssistantSessionStateBlocked
	_ = r.orchestrator.publishSession(ctx, session)
	lock.Unlock()
	_ = r.orchestrator.publishStatus(ctx, nil, recovered.SessionID, "blocked", map[string]any{"phase": "tool_observation_blocked", "summary": "agentic assistant async recovery blocked", "error": reason, "tool_call_id": metadata.PendingToolCallID})
}

func (r *AssistantSessionRecoveryRunner) receiptForStep(session *domain.AssistantSession, step domain.AssistantPlanStep) *domain.AsyncToolReceipt {
	if step.IdempotencyKey != "" {
		return r.orchestrator.pendingReceipt(session, step.IdempotencyKey)
	}
	if session == nil || session.Metadata == nil {
		return nil
	}
	receipts, _ := session.Metadata["pending_receipts"].(map[string]any)
	for key := range receipts {
		receipt := r.orchestrator.pendingReceipt(session, key)
		if receipt != nil && (receipt.ToolName == step.ToolName || step.ToolName == "") {
			return receipt
		}
	}
	return nil
}

func (r *AssistantSessionRecoveryRunner) findTerminalResult(ctx context.Context, receipt *domain.AsyncToolReceipt) (downstreamOutcome, error) {
	if receipt == nil || receipt.RequestEventID == "" || len(receipt.ResultKinds) == 0 {
		return downstreamOutcome{Status: "blocked"}, fmt.Errorf("downstream receipt is missing observable result metadata")
	}
	resultKinds := make([]nostr.Kind, 0, len(receipt.ResultKinds))
	for _, kind := range receipt.ResultKinds {
		resultKinds = append(resultKinds, nostr.Kind(kind))
	}
	merged, err := r.orchestrator.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: resultKinds, Tags: nostr.TagMap{"e": []string{receipt.RequestEventID}}}})
	if err != nil {
		return downstreamOutcome{Status: "blocked"}, err
	}
	defer merged.Close()
	seen := map[string]struct{}{}
	eventsCh := merged.EventChan()
	closedCh := merged.ClosedChan()
	eoseCh := merged.EOSEChan()
	for {
		select {
		case <-ctx.Done():
			return downstreamOutcome{Status: "blocked"}, ctx.Err()
		case closed, ok := <-closedCh:
			if !ok {
				closedCh = nil
				continue
			}
			return downstreamOutcome{Status: "blocked"}, fmt.Errorf("relay subscription closed during recovery backfill: relay=%s reason=%s", closed.RelayURL, closed.Reason)
		case ev, ok := <-eventsCh:
			if !ok {
				return downstreamOutcome{}, nil
			}
			if ev == nil {
				continue
			}
			eventID := ev.ID.Hex()
			if _, dup := seen[eventID]; dup {
				continue
			}
			seen[eventID] = struct{}{}
			status := terminalStatus(ev)
			if status == "completed" || status == "failed" {
				return downstreamOutcome{Status: status, Event: ev}, nil
			}
		case <-eoseCh:
			return downstreamOutcome{}, nil
		}
	}
}

func removePendingStep(session *domain.AssistantSession, stepID string) {
	if session == nil || len(session.PendingSteps) == 0 {
		return
	}
	for i := range session.PendingSteps {
		if session.PendingSteps[i].StepID == stepID {
			session.PendingSteps = append(session.PendingSteps[:i], session.PendingSteps[i+1:]...)
			return
		}
	}
	session.PendingSteps = session.PendingSteps[1:]
}

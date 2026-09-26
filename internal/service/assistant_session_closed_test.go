package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantProjectionClosedOnlyForSessionScopeCancellation(t *testing.T) {
	at := time.Date(2026, time.September, 26, 12, 0, 0, 0, time.FixedZone("x", 3600))
	base := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: "s", RunID: "r", Workflow: domain.AssistantWorkflowIterative, Phase: domain.AssistantExecutionCancelled}

	open := projectAssistantExecution(base, "cp", domain.AssistantSessionV2{})
	if open.Closed || open.ClosedAt != nil {
		t.Fatalf("uncancelled projection closed: %+v", open)
	}

	run := base
	run.Cancellation = &domain.AssistantExecutionCancellation{Scope: "run", RunID: "r", RecordedAt: at}
	if p := projectAssistantExecution(run, "cp", domain.AssistantSessionV2{}); p.Closed || p.ClosedAt != nil {
		t.Fatalf("run-scope cancellation closed the session: %+v", p)
	}

	session := base
	session.Cancellation = &domain.AssistantExecutionCancellation{Scope: "session", RunID: "r", Reason: "operator private note", RecordedAt: at}
	closed := projectAssistantExecution(session, "cp", domain.AssistantSessionV2{})
	if !closed.Closed || closed.ClosedAt == nil || !closed.ClosedAt.Equal(at) || closed.ClosedAt.Location() != time.UTC {
		t.Fatalf("session-scope cancellation projection = closed:%v closed_at:%v", closed.Closed, closed.ClosedAt)
	}
	raw, err := json.Marshal(closed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"closed":true`) || !strings.Contains(string(raw), `"closed_at":"2026-09-26T11:00:00Z"`) || strings.Contains(string(raw), "operator private note") {
		t.Fatalf("closed projection JSON = %s", raw)
	}

	// A projection carried over from hydration never keeps a stale closed flag:
	// it is re-derived from the execution on every refresh.
	if p := projectAssistantExecution(base, "cp", closed); p.Closed || p.ClosedAt != nil {
		t.Fatalf("stale closed flag survived re-projection: %+v", p)
	}
}

// Old readers see byte-identical projections for open sessions: the additive
// fields are omitted, so the schema string is unchanged.
func TestAssistantProjectionOpenSessionOmitsClosedFields(t *testing.T) {
	p := projectAssistantExecution(domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: "s", RunID: "r", Workflow: domain.AssistantWorkflowBatch, Phase: domain.AssistantExecutionCompleted}, "cp", domain.AssistantSessionV2{})
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "closed") || p.Schema != domain.AssistantSessionSchemaV2 {
		t.Fatalf("open projection = %s", raw)
	}
}

// Closing a session while its run is still active projects closed immediately,
// alongside the cancelling/cancelled phase, and refuses the next turn.
func TestAssistantSessionCloseOnActiveRunProjectsClosed(t *testing.T) {
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowBatch, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantSyncAsyncSyncPlan()}})
	draft := requireAccepted(t, f.prompt(t, "s-active", ""))
	if draft.Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatalf("phase = %s", draft.Phase)
	}
	res, err := f.router.HandleCancellationRequest(t.Context(), f.source("close"), domain.AssistantCancellationRequest{ContractVersion: 2, SessionID: "s-active", RunID: draft.CurrentRunID, Scope: "session"})
	if err != nil || res["status"] != "accepted" {
		t.Fatalf("close = %#v err=%v", res, err)
	}
	p := assistantJoinedLatestProjection(t, f.relay, "s-active")
	if !p.Closed || p.ClosedAt == nil {
		t.Fatalf("active-run close projection = %+v", p)
	}
	requireRefusal(t, f.prompt(t, "s-active", ""), AssistantRefusalSessionClosed)
}

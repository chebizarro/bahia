package controlplane

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// The attested abandon resolution rides the assistant/reconcile surface:
// misuse is refused with the stable abandonment_refused code before or by the
// executor and records nothing; only uncertain work can be abandoned, and the
// accepted decision is a checkpoint that finishes the run without completing
// the work.
func TestAssistantHandlerAbandonThroughReconcileSurface(t *testing.T) {
	f := newHandlerFixture(t)
	ctx := context.Background()
	digest, err := domain.ComputeAssistantArgumentsDigest(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	item := func(id, tool string, ordinal int, state domain.AssistantWorkState) domain.AssistantWorkItem {
		return domain.AssistantWorkItem{WorkID: id, OriginID: id, Ordinal: ordinal, ToolName: tool, Arguments: map[string]any{}, ArgumentsDigest: digest, State: state}
	}
	x := domain.AssistantExecution{Version: 2, SessionID: "s-abandon", RunID: "run-abandon", TurnID: "t", RequestID: "r", Workflow: domain.AssistantWorkflowIterative, Revision: 1, Phase: domain.AssistantExecutionBlocked,
		Work: []domain.AssistantWorkItem{item("done", "read-one", 0, domain.AssistantWorkSucceeded), item("lost", "mutate", 1, domain.AssistantWorkUncertain), item("later", "read-one", 2, domain.AssistantWorkPending)}}
	if _, err := f.store.Append(ctx, x, ""); err != nil {
		t.Fatal(err)
	}
	f.engine.HydrateProjection(domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: x.SessionID, OperatorPubkey: legacyOperatorPubkey().Hex(), CurrentRunID: x.RunID, Workflow: x.Workflow}, 0)
	if err := f.engine.Recover(ctx, service.AssistantExecutionReference{SessionID: x.SessionID, RunID: x.RunID}); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t, "s-abandon").Revision
	params := func(workID string, mutate func(map[string]any)) map[string]any {
		p := map[string]any{"contract_version": 2, "session_id": "s-abandon", "run_id": "run-abandon", "work_id": workID, "resolution": "abandon", "reason": "downstream history pruned", "attestation": domain.AssistantAbandonmentAttestation}
		if mutate != nil {
			mutate(p)
		}
		return p
	}
	refused := []struct {
		name   string
		params map[string]any
		code   string
	}{
		{"missing reason", params("lost", func(p map[string]any) { delete(p, "reason") }), service.AssistantRefusalAbandonmentRefused},
		{"blank reason", params("lost", func(p map[string]any) { p["reason"] = "   " }), service.AssistantRefusalAbandonmentRefused},
		{"missing attestation", params("lost", func(p map[string]any) { delete(p, "attestation") }), service.AssistantRefusalAbandonmentRefused},
		{"wrong attestation", params("lost", func(p map[string]any) { p["attestation"] = "completed" }), service.AssistantRefusalAbandonmentRefused},
		{"with request event", params("lost", func(p map[string]any) { p["request_event_id"] = "ab" }), service.AssistantRefusalAbandonmentRefused},
		{"succeeded work", params("done", nil), service.AssistantRefusalAbandonmentRefused},
		{"pending work", params("later", nil), service.AssistantRefusalAbandonmentRefused},
		{"unknown resolution", params("lost", func(p map[string]any) { p["resolution"] = "complete" }), service.AssistantRefusalValidation},
		{"evidence without event", params("lost", func(p map[string]any) { p["resolution"] = "evidence" }), service.AssistantRefusalValidation},
	}
	for _, tc := range refused {
		res := mustResult(t)(f.adapter.handleReconcile(ctx, f.request(t, tc.params, "")))
		if res["status"] != "failed" || res["step"] != tc.code {
			t.Fatalf("%s = %#v", tc.name, res)
		}
	}
	if after := f.snapshot(t, "s-abandon"); after.Revision != before || f.tools.count() != 0 {
		t.Fatalf("refused abandonments changed state: rev %d->%d calls=%d", before, after.Revision, f.tools.count())
	}

	res := mustResult(t)(f.adapter.handleReconcile(ctx, f.request(t, params("lost", nil), "abandon-lost")))
	if res["status"] != "accepted" || res["step"] != "abandoned" || res["phase"] != string(domain.AssistantExecutionFailed) || res["pending_effects"] != 0 {
		t.Fatalf("abandon uncertain work = %#v", res)
	}
	got := f.snapshot(t, "s-abandon")
	if got.Work[1].State != domain.AssistantWorkAbandoned || got.Work[1].Abandonment == nil || got.Work[1].Abandonment.OperatorPubkey != legacyOperatorPubkey().Hex() || got.Work[2].State != domain.AssistantWorkSkipped || got.Work[0].State != domain.AssistantWorkSucceeded {
		t.Fatalf("work after abandonment = %+v", got.Work)
	}
	if p, ok := f.engine.Projection("s-abandon"); !ok || p.AbandonedEffects != 1 || p.UncertainEffects != 0 {
		t.Fatalf("projection = %+v", p)
	}
	res = mustResult(t)(f.adapter.handleReconcile(ctx, f.request(t, params("lost", nil), "abandon-lost")))
	if res["status"] != "accepted" || res["step"] != "already_abandoned" || f.tools.count() != 0 {
		t.Fatalf("redelivered abandonment = %#v", res)
	}
}

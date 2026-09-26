package service

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

// The requesting operator is persisted in every checkpoint of the run and can
// never change; approval-bound work acts as its approver.
func TestAssistantExecutionPersistsRequestingOperator(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantOneReadPlan()}})
	start := st.startBatch(t, "s-operator")
	st.approve(t, "s-operator", start)
	relay.waitFor(t, "completed", func() bool { return st.snapshot("s-operator").Phase == domain.AssistantExecutionCompleted })
	for _, x := range assistantDecodedCheckpoints(t, st, relay, "s-operator", start.Session.CurrentRunID) {
		if x.OperatorPubkey != "operator" {
			t.Fatalf("revision %d operator %q", x.Revision, x.OperatorPubkey)
		}
	}
	x := st.snapshot("s-operator")
	if got := assistantWorkOperator(x, x.Work[0]); got != x.Work[0].Authorization.OperatorPubkey {
		t.Fatalf("approved work acts as %q", got)
	}
	changed := x
	changed.OperatorPubkey = "someone-else"
	if err := validateAssistantExecutionTransition(x, changed); err == nil {
		t.Fatal("operator change accepted by the journal validator")
	}
	p := auth.GetPrincipal(assistantOperatorContext(auth.ContextWithPrincipal(context.Background(), auth.SystemPrincipal("x")), " ABC "))
	if p == nil || p.Method != auth.MethodNIP98 || p.PubKey != "abc" || len(p.Roles) != 0 {
		t.Fatalf("operator principal %+v", p)
	}
	if p := auth.GetPrincipal(assistantOperatorContext(auth.ContextWithPrincipal(context.Background(), auth.SystemPrincipal("x")), "")); p.IsAuthenticated() {
		t.Fatalf("empty operator kept an ambient principal: %+v", p)
	}
}

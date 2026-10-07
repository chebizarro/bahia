package soulfactory

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Tests for: operator abandon, late result after abandon,
// unauthorized abandon rejection, async deferred fleet work ordering,
// restart-rebuild durability, and rejection when the operation is not parked.

// TestAbandonReleasesStuckSoulAndLaterRollbackProceeds confirms that an
// operator abandon of a stuck awaiting_terminal operation releases the soul's
// serialization lock so a subsequent rollback action proceeds.
func TestAbandonReleasesStuckSoulAndLaterRollbackProceeds(t *testing.T) {
	f := relayBackedLifecycleFixture(t, "timeout")

	// Start a hot-reload that will time out, parking the soul.
	hotReload := f.action(t, "stuck-hot-reload", domain.SoulActionHotReload)
	if err := f.handler.HandleAction(t.Context(), hotReload); err != nil {
		t.Fatalf("HandleAction(hot-reload) error = %v, want parked", err)
	}
	if held, _ := f.reactor.soulOperations().held("scout"); !held {
		t.Fatal("soul should be held after a parked hot-reload")
	}

	// Abandon the stuck operation.
	abandon := buildActionEvent(t, f.signer, "abandon-stuck",
		nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", "abandon"}, {"reason", "test: stuck hot-reload"}}, "")
	if err := f.handler.HandleAction(t.Context(), abandon); err != nil {
		t.Fatalf("HandleAction(abandon) error = %v", err)
	}

	// The soul should be free now.
	if held, _ := f.reactor.soulOperations().held("scout"); held {
		t.Fatal("soul should be free after abandon")
	}

	// Verify the abandon produced a terminal result for the abandon action.
	results := f.capture.byKind(domain.KindProvisioningResult)
	var abandonResult *nostr.Event
	for _, r := range results {
		if tagValue(r.Tags, tagEvent) == abandon.ID.Hex() {
			abandonResult = r
		}
	}
	if abandonResult == nil {
		t.Fatal("no terminal result for the abandon action")
	}
	if tagValue(abandonResult.Tags, tagStatus) != "completed" {
		t.Fatalf("abandon result status = %q, want completed", tagValue(abandonResult.Tags, tagStatus))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(abandonResult.Content), &payload); err != nil {
		t.Fatalf("decode abandon payload: %v", err)
	}
	if payload["reason"] != "test: stuck hot-reload" {
		t.Fatalf("abandon payload reason = %v, want 'test: stuck hot-reload'", payload["reason"])
	}

	// Verify the abandon also produced a terminal result for the abandoned
	// request (the hot-reload), so the restart rebuild sees it as finished.
	var abandonedRequestResult *nostr.Event
	for _, r := range results {
		if tagValue(r.Tags, tagEvent) == hotReload.ID.Hex() && tagValue(r.Tags, tagStatus) == "abandoned" {
			abandonedRequestResult = r
		}
	}
	if abandonedRequestResult == nil {
		t.Fatal("no terminal result for the abandoned request (the hot-reload)")
	}
	if got := tagValue(abandonedRequestResult.Tags, tagRequestKind); got != strconv.Itoa(domain.KindSoulAction) {
		t.Fatalf("abandoned request result request-kind = %q, want %d", got, domain.KindSoulAction)
	}

	// A subsequent rollback now proceeds (it's not deferred forever).
	rollback := buildActionEvent(t, f.signer, "after-abandon-rollback",
		nostr.Tags{
			{"soul", buildSoulRefForTest(f.soul)}, {"action", string(domain.SoulActionRollback)},
			{"draft", "31952:" + f.signer.pubkey + ":scout"}, {"draft-event", f.currentDraft.EventID},
			{"spec-hash", "sha256:old"}, {"previous-spec-hash", "sha256:new"},
		}, "")
	if err := f.handler.HandleAction(t.Context(), rollback); err != nil {
		t.Fatalf("HandleAction(rollback after abandon) error = %v", err)
	}

	// The rollback should produce a terminal result.
	var rollbackResult *nostr.Event
	for _, r := range f.capture.byKind(domain.KindProvisioningResult) {
		if tagValue(r.Tags, tagEvent) == rollback.ID.Hex() {
			rollbackResult = r
		}
	}
	if rollbackResult == nil {
		t.Fatal("no terminal result for the rollback after abandon")
	}
}

// TestLateResultAfterAbandonIsRecordedNotApplied confirms that a late runtime
// result for an abandoned operation is recorded (as progress) but the soul is
// NOT updated.
func TestLateResultAfterAbandonIsRecordedNotApplied(t *testing.T) {
	f := newLateLifecycleFixture(t, "timeout")

	// Start a hot-reload that will time out, parking the soul.
	hotReload := f.action(t, "late-after-abandon", domain.SoulActionHotReload)
	if err := f.handler.HandleAction(t.Context(), hotReload); err != nil {
		t.Fatalf("HandleAction(hot-reload) error = %v, want parked", err)
	}

	_, pending := f.runtime.snapshot()
	if len(pending) != 1 {
		t.Fatalf("pending runtime calls = %d, want 1", len(pending))
	}
	soulsBefore := len(f.capture.byKind(domain.KindAgentSoul))

	// Abandon the stuck operation.
	abandon := buildActionEvent(t, f.signer, "abandon-for-late",
		nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", "abandon"}, {"reason", "test"}}, "")
	if err := f.handler.HandleAction(t.Context(), abandon); err != nil {
		t.Fatalf("HandleAction(abandon) error = %v", err)
	}
	soulsAfterAbandon := len(f.capture.byKind(domain.KindAgentSoul))
	if soulsAfterAbandon != soulsBefore {
		t.Fatalf("soul publishes after abandon = %d, want %d (abandon does not republish)", soulsAfterAbandon, soulsBefore)
	}

	// Deliver the late result for the abandoned operation.
	late := lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "success", nostr.Now())
	f.reactor.handleEvent(t.Context(), late)

	// The soul must NOT have been updated by the late result.
	soulsAfterLate := len(f.capture.byKind(domain.KindAgentSoul))
	if soulsAfterLate != soulsBefore {
		t.Fatalf("soul publishes after late result = %d, want %d (late result after abandon is not applied)", soulsAfterLate, soulsBefore)
	}
	if f.soul.SpecHash != "sha256:old" {
		t.Fatalf("soul spec hash = %q, want unchanged sha256:old", f.soul.SpecHash)
	}

	// But the late result should have been recorded as progress.
	lateProgress := progressWithStatus(f.capture.byKind(domain.KindProvisioningStatus), "abandoned_late_result")
	if len(lateProgress) != 1 {
		t.Fatalf("abandoned_late_result progress events = %d, want 1", len(lateProgress))
	}
	if !strings.Contains(lateProgress[0].Content, "not applied") {
		t.Fatalf("late result progress = %q, want to contain 'not applied'", lateProgress[0].Content)
	}
}

// TestUnauthorizedAbandonIsRejected confirms that a pubkey not in
// AuthorizedPubkeys cannot abandon a soul.
func TestUnauthorizedAbandonIsRejected(t *testing.T) {
	f := newLateLifecycleFixture(t, "timeout")

	hotReload := f.action(t, "unauth-stuck", domain.SoulActionHotReload)
	if err := f.handler.HandleAction(t.Context(), hotReload); err != nil {
		t.Fatalf("HandleAction(hot-reload) error = %v, want parked", err)
	}

	unauthorized := newFakeSigner(t)
	abandon := buildActionEvent(t, unauthorized, "unauth-abandon",
		nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", "abandon"}}, "")
	err := f.handler.HandleAction(t.Context(), abandon)
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("HandleAction(unauthorized abandon) error = %v, want unauthorized", err)
	}

	// The soul should still be held.
	if held, _ := f.reactor.soulOperations().held("scout"); !held {
		t.Fatal("soul should still be held after rejected abandon")
	}

	// There should be an error result for the unauthorized abandon.
	var errorResult *nostr.Event
	for _, r := range f.capture.byKind(domain.KindProvisioningResult) {
		if tagValue(r.Tags, tagEvent) == abandon.ID.Hex() && tagValue(r.Tags, tagStatus) == "error" {
			errorResult = r
		}
	}
	if errorResult == nil {
		t.Fatal("no error result for the unauthorized abandon")
	}
}

// TestAbandonRejectedWhenOperationStillExecuting confirms that an abandon is
// rejected when the soul is held but its operation is still executing (not yet
// parked on an awaiting_terminal result). Force-releasing the hold in that
// state would break per-soul serialization.
func TestAbandonRejectedWhenOperationStillExecuting(t *testing.T) {
	f := newLateLifecycleFixture(t)

	// Manually hold the soul: acquire starts an operation but we never run
	// it, simulating a run func mid-execution.
	gate := f.reactor.soulOperations()
	_, outcome := gate.acquire("scout",
		soulOperation{key: "test:executing", run: func(context.Context, *soulHold) bool { return false }},
		soulOperation{key: "test:executing", run: func(context.Context, *soulHold) bool { return false }})
	if outcome != soulOperationStarted {
		t.Fatalf("acquire outcome = %v, want started", outcome)
	}

	// Try to abandon — should be rejected because nothing is parked.
	abandon := buildActionEvent(t, f.signer, "reject-abandon",
		nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", "abandon"}}, "")
	err := f.handler.HandleAction(t.Context(), abandon)
	if err == nil || !strings.Contains(err.Error(), "still executing") {
		t.Fatalf("HandleAction(abandon while executing) error = %v, want 'still executing'", err)
	}

	// The soul should still be held — not force-released.
	if held, _ := gate.held("scout"); !held {
		t.Fatal("soul should still be held after rejected abandon")
	}

	// An error result should have been published.
	var errorResult *nostr.Event
	for _, r := range f.capture.byKind(domain.KindProvisioningResult) {
		if tagValue(r.Tags, tagEvent) == abandon.ID.Hex() && tagValue(r.Tags, tagStatus) == "error" {
			errorResult = r
		}
	}
	if errorResult == nil {
		t.Fatal("no error result for the rejected abandon")
	}
	if !strings.Contains(errorResult.Content, "still executing") {
		t.Fatalf("error result = %q, want to contain 'still executing'", errorResult.Content)
	}
}

// TestRestartAfterAbandonDoesNotReDrive confirms that after an abandon the
// restart rebuild (rebuildParkedOperations -> withoutTerminalResults) sees the
// abandoned request as finished and does NOT re-hold the soul or re-drive the
// operation. A later rollback on the free soul proceeds.
func TestRestartAfterAbandonDoesNotReDrive(t *testing.T) {
	f := relayBackedLifecycleFixture(t, "timeout")

	// Park a hot-reload.
	hotReload := f.action(t, "abandoned-rebuild", domain.SoulActionHotReload)
	if err := f.handler.HandleAction(t.Context(), hotReload); err != nil {
		t.Fatalf("HandleAction(hot-reload) error = %v", err)
	}

	// Confirm an awaiting_terminal progress event was published.
	awaitingEvents := progressWithStatus(f.capture.byKind(domain.KindProvisioningStatus), actionStatusAwaitingTerminal)
	if len(awaitingEvents) != 1 {
		t.Fatalf("awaiting_terminal events = %d, want 1", len(awaitingEvents))
	}

	// Abandon it.
	abandon := buildActionEvent(t, f.signer, "rebuild-abandon",
		nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", "abandon"}, {"reason", "test: rebuild"}}, "")
	if err := f.handler.HandleAction(t.Context(), abandon); err != nil {
		t.Fatalf("HandleAction(abandon) error = %v", err)
	}

	// Simulate a restart rebuild: feed the published events through the
	// same logic rebuildParkedOperations uses.
	factoryKey, err := nostr.PubKeyFromHex(f.signer.pubkey)
	if err != nil {
		t.Fatalf("factory key: %v", err)
	}
	outstanding := outstandingFromAwaiting(awaitingEvents, factoryKey)
	if len(outstanding) != 1 {
		t.Fatalf("outstanding before terminal check = %d, want 1", len(outstanding))
	}
	results := f.capture.byKind(domain.KindProvisioningResult)
	outstanding = withoutTerminalResults(outstanding, results, factoryKey)
	if len(outstanding) != 0 {
		var keys []string
		for _, op := range outstanding {
			keys = append(keys, op.key())
		}
		t.Fatalf("outstanding after abandon = %v, want none (rebuild should skip the abandoned operation)", keys)
	}

	// The soul is free: a later rollback proceeds.
	if held, _ := f.reactor.soulOperations().held("scout"); held {
		t.Fatal("soul should be free after abandon")
	}
	rollback := buildActionEvent(t, f.signer, "after-rebuild-rollback",
		nostr.Tags{
			{"soul", buildSoulRefForTest(f.soul)}, {"action", string(domain.SoulActionRollback)},
			{"draft", "31952:" + f.signer.pubkey + ":scout"}, {"draft-event", f.currentDraft.EventID},
			{"spec-hash", "sha256:old"}, {"previous-spec-hash", "sha256:new"},
		}, "")
	if err := f.handler.HandleAction(t.Context(), rollback); err != nil {
		t.Fatalf("HandleAction(rollback after abandon) error = %v", err)
	}
	var rollbackResult *nostr.Event
	for _, r := range f.capture.byKind(domain.KindProvisioningResult) {
		if tagValue(r.Tags, tagEvent) == rollback.ID.Hex() {
			rollbackResult = r
		}
	}
	if rollbackResult == nil {
		t.Fatal("no terminal result for the rollback after restart rebuild")
	}
}

// TestAsyncDeferredWorkKeepsPerSoulOrderAndDoesNotHoldFleetLock confirms that
// per-soul ordering is preserved when a fleet revision and a lifecycle action
// target the same soul, and that the fleet revision lock is not held while
// deferred lifecycle work executes.
func TestAsyncDeferredWorkKeepsPerSoulOrderAndDoesNotHoldFleetLock(t *testing.T) {
	f := relayBackedLifecycleFixture(t, "success", "success", "success", "success", "success")
	_, revision := fleetReconcileSnapshots(t)

	// Track whether the fleet lock is held during lifecycle action execution.
	type lockProbe struct {
		mu   sync.Mutex
		held bool
	}
	probe := &lockProbe{}

	// Wrap the publish function to detect when a lifecycle action's terminal
	// result is published and check whether the fleet lock is still held.
	originalPublish := f.reactor.publishFn
	f.reactor.publishFn = func(ctx context.Context, event *nostr.Event, relays []string) error {
		if event.Kind == nostr.Kind(domain.KindProvisioningResult) &&
			tagValue(event.Tags, tagRequestKind) == "1950" {
			// Try to acquire the fleet reconciler's mu; if it's still held, we
			// detect it. Since we're not actually contending, we use TryLock.
			locked := f.reactor.fleetReconciler().mu.TryLock()
			probe.mu.Lock()
			if locked {
				f.reactor.fleetReconciler().mu.Unlock()
			} else {
				probe.held = true
			}
			probe.mu.Unlock()
		}
		return originalPublish(ctx, event, relays)
	}

	// Start a fleet reconciliation that applies to the soul.
	if err := f.reactor.fleetReconciler().Reconcile(t.Context(), revision); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Queue a lifecycle suspend behind the fleet apply (it should be deferred
	// by the soul gate).
	suspend := buildActionEvent(t, f.signer, "fleet-then-suspend",
		nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", string(domain.SoulActionSuspend)}}, "")
	if err := f.handler.HandleAction(t.Context(), suspend); err != nil {
		t.Fatalf("HandleAction(suspend) error = %v", err)
	}

	// Verify ordering: fleet result should come before suspend result.
	results := f.capture.byKind(domain.KindProvisioningResult)
	var order []string
	for _, r := range results {
		kind := tagValue(r.Tags, tagRequestKind)
		action := tagValue(r.Tags, tagAction)
		switch {
		case kind == "31953":
			order = append(order, "fleet")
		case action == "suspend":
			order = append(order, "suspend")
		}
	}
	if !slices.Contains(order, "fleet") || !slices.Contains(order, "suspend") {
		t.Fatalf("result order = %v, want both fleet and suspend", order)
	}
	fleetIdx, suspendIdx := -1, -1
	for i, v := range order {
		if v == "fleet" && fleetIdx == -1 {
			fleetIdx = i
		}
		if v == "suspend" && suspendIdx == -1 {
			suspendIdx = i
		}
	}
	if fleetIdx > suspendIdx {
		t.Fatalf("fleet result (index %d) should come before suspend (index %d): per-soul order violated", fleetIdx, suspendIdx)
	}

	// Verify the soul has both the fleet revision and the suspended status.
	soul := f.capture.latestSoul(f.soul.AgentID)
	if soul == nil {
		t.Fatal("no soul published")
	}
	if soul.Status != domain.SoulStatusSuspended {
		t.Fatalf("soul status = %s, want suspended", soul.Status)
	}
	if soul.AppliedFleetConfigRevision != revision.EventID {
		t.Fatalf("soul fleet revision = %q, want %q", soul.AppliedFleetConfigRevision, revision.EventID)
	}

	// Verify the fleet lock was NOT held during the lifecycle action.
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.held {
		t.Fatal("fleet revision lock was held during deferred lifecycle action execution")
	}
}

package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
)

// scriptedLateRuntime answers each Execute from a script. "timeout" returns
// the outcome-unknown error a real adapter returns when its wait for the
// kind:38386 ended first (the request was published, no result observed yet);
// "failure" is an observed terminal failure; anything else is success. No
// test waits on a clock: the late result is delivered as an event.
type scriptedLateRuntime struct {
	t          *testing.T
	controller fakeSigner
	runtime    fakeSigner

	mu       sync.Mutex
	script   []string
	requests []RuntimeAdapterRequest
	pending  []*runtimeResultPending
}

func (a *scriptedLateRuntime) Runtime() domain.RuntimeTarget { return domain.RuntimeTargetOpenClaw }

func (a *scriptedLateRuntime) DiscoverCapabilities(context.Context, domain.SoulRelayPolicySpec) ([]RuntimeCapability, error) {
	return nil, nil
}

func (a *scriptedLateRuntime) Execute(ctx context.Context, req RuntimeAdapterRequest) (*RuntimeControlResultEnvelope, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	index := len(a.requests)
	a.requests = append(a.requests, req)
	outcome := "success"
	if index < len(a.script) && a.script[index] != "" {
		outcome = a.script[index]
	}
	switch outcome {
	case "timeout":
		pending := newScriptedRuntimePending(a.t, a.controller, a.runtime, req, index)
		a.pending = append(a.pending, pending)
		return nil, pending
	case "failure":
		result := &RuntimeControlResultEnvelope{
			Schema: domain.SoulFactoryRuntimeControlSchema, Method: req.Method, Status: "error",
			Error: &RuntimeControlError{Code: "runtime_error", Message: "runtime rejected the request"},
		}
		return result, runtimeResultFailure(result)
	default:
		return &RuntimeControlResultEnvelope{
			Schema: domain.SoulFactoryRuntimeControlSchema, Method: req.Method, Status: "success",
			Result: map[string]interface{}{"state": "running"},
		}, nil
	}
}

func (a *scriptedLateRuntime) snapshot() ([]RuntimeAdapterRequest, []*runtimeResultPending) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]RuntimeAdapterRequest(nil), a.requests...), append([]*runtimeResultPending(nil), a.pending...)
}

func (a *scriptedLateRuntime) rollbacks() int {
	requests, _ := a.snapshot()
	count := 0
	for _, request := range requests {
		if request.Action == domain.SoulActionRollback {
			count++
		}
	}
	return count
}

// newScriptedRuntimePending builds what runtimeControlAdapter.Execute returns
// on a timeout: the signed kind:38384 it published and the resolved request.
func newScriptedRuntimePending(t *testing.T, controller, runtime fakeSigner, req RuntimeAdapterRequest, index int) *runtimeResultPending {
	t.Helper()
	req.Target.RuntimePubkey = runtime.pubkey
	if req.Target.AgentID == "" {
		req.Target.AgentID = req.Soul.ID
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = runtimeIdempotencyKey(controller.pubkey, req.Method, req.Operator.RequestEvent, runtime.pubkey, req.Target.AgentID, req.Soul.SpecHash)
	}
	request := &nostr.Event{
		Kind:      nostr.Kind(domain.KindRuntimeControlRequest),
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{tagPubkey, runtime.pubkey}, {tagMethod, req.Method}, {tagIdempotencyKey, req.IdempotencyKey}},
		Content:   fmt.Sprintf(`{"call":%d}`, index),
	}
	if err := controller.Sign(t.Context(), request); err != nil {
		t.Fatalf("sign control request: %v", err)
	}
	return &runtimeResultPending{
		noResult:         &NoTerminalResultError{RequestID: request.ID.Hex(), Timeout: time.Minute, Cause: context.DeadlineExceeded},
		requestEvent:     request,
		req:              req,
		controllerPubkey: controller.pubkey,
	}
}

// lateRuntimeResultEvent is the runtime's signed kind:38386 answer to pending,
// published after the controller's wait ended.
func lateRuntimeResultEvent(t *testing.T, runtime fakeSigner, pending *runtimeResultPending, status string, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	envelope := RuntimeControlResultEnvelope{
		Schema:               domain.SoulFactoryRuntimeControlSchema,
		Method:               pending.req.Method,
		IdempotencyKey:       pending.req.IdempotencyKey,
		RequestEvent:         pending.requestID(),
		OperatorRequestEvent: pending.req.Operator.RequestEvent,
		Status:               status,
		Result:               map[string]interface{}{"state": "running"},
	}
	if status != "success" {
		envelope.Error = &RuntimeControlError{Code: "runtime_error", Message: "runtime rejected the request"}
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	event := &nostr.Event{
		Kind:      nostr.Kind(domain.KindRuntimeControlResult),
		CreatedAt: createdAt,
		Tags:      nostr.Tags{{tagPubkey, pending.controllerPubkey}, {tagEvent, pending.requestID()}, {tagStatus, status}},
		Content:   string(content),
	}
	if err := runtime.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign result: %v", err)
	}
	return event
}

func statusEventsWith(events []*nostr.Event, status string) int {
	count := 0
	for _, event := range events {
		if tagValue(event.Tags, tagStatus) == status {
			count++
		}
	}
	return count
}

func decodeResultPayload(t *testing.T, event *nostr.Event) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(event.Content), &payload); err != nil {
		t.Fatalf("decode result payload: %v", err)
	}
	return payload
}

type lateLifecycleFixture struct {
	signer        fakeSigner
	runtime       *scriptedLateRuntime
	reactor       *Reactor
	handler       *LifecycleHandler
	capture       *fleetReconcilePublishCapture
	soul          *domain.AgentSoul
	currentDraft  *domain.SoulDraft
	proposedDraft *domain.SoulDraft
}

type backfillSignalHandler struct {
	slog.Handler
	complete chan<- struct{}
}

func (h *backfillSignalHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "soul factory request backfill complete; processing realtime events" {
		select {
		case h.complete <- struct{}{}:
		default:
		}
	}
	return h.Handler.Handle(ctx, record)
}

func (h *backfillSignalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &backfillSignalHandler{Handler: h.Handler.WithAttrs(attrs), complete: h.complete}
}

func (h *backfillSignalHandler) WithGroup(name string) slog.Handler {
	return &backfillSignalHandler{Handler: h.Handler.WithGroup(name), complete: h.complete}
}

// newLateLifecycleFixture wires a lifecycle handler whose soul changes voice,
// memory and persona between its current and proposed drafts.
func newLateLifecycleFixture(t *testing.T, script ...string) *lateLifecycleFixture {
	t.Helper()
	signer := newFakeSigner(t)
	runtimeKey := soulTestPubKeyHex("late-runtime")
	currentDraft := &domain.SoulDraft{EventID: "late-current-draft", AgentID: "scout", CreatedBy: signer.pubkey, Content: domain.SoulDraftContent{
		Schema:   domain.SoulFactoryDraftSchemaV2,
		Identity: domain.SoulIdentitySpec{Name: "Scout", Purpose: "Research", Tier: domain.SoulTierStandard},
		Persona:  domain.SoulPersonaSpec{Traits: []string{"careful"}},
		Voice:    domain.SoulVoiceSpec{Provider: "elevenlabs", PersonaID: "old-voice"},
		Memory:   domain.SoulMemorySpec{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small"},
		Runtime:  domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: runtimeKey},
		SpecHash: "sha256:old",
	}}
	proposedDraft := &domain.SoulDraft{EventID: "late-proposed-draft", AgentID: "scout", CreatedBy: signer.pubkey, Content: domain.SoulDraftContent{
		Schema:           domain.SoulFactoryDraftSchemaV2,
		Identity:         domain.SoulIdentitySpec{Name: "Scout", Purpose: "Research deeply", Tier: domain.SoulTierStandard},
		Persona:          domain.SoulPersonaSpec{Traits: []string{"careful", "curious"}},
		Voice:            domain.SoulVoiceSpec{Provider: "elevenlabs", PersonaID: "new-voice"},
		Memory:           domain.SoulMemorySpec{EmbeddingProvider: "voyage", EmbeddingModel: "voyage-3"},
		Runtime:          domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: runtimeKey},
		SpecHash:         "sha256:new",
		PreviousSpecHash: "sha256:old",
	}}
	soul := &domain.AgentSoul{
		ID: uuid.New(), AgentID: "scout", Name: "Scout", Purpose: "Research", Tier: domain.SoulTierStandard,
		Status: domain.SoulStatusActive, DraftRef: "31952:" + signer.pubkey + ":scout", DraftEventID: currentDraft.EventID,
		SpecHash: "sha256:old", Runtime: currentDraft.Content.Runtime, CreatedAt: time.Now().UTC(),
	}
	reactor := NewReactor(Config{
		Relays:            []string{"wss://public.example"},
		AuthorizedPubkeys: []string{signer.pubkey},
		SoulFactoryPubkey: signer.pubkey,
	}, scriptedGenerator{}, signer, slog.Default())
	reactor.relayClient = newEOSEOnlyRelayClient(t)
	capture := &fleetReconcilePublishCapture{}
	reactor.publishFn = capture.publish
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) { return soul, nil }
	reactor.findLifecycleResultFn = func(context.Context, string) (*nostr.Event, error) { return nil, nil }
	reactor.getDraftFn = func(_ context.Context, _ string, eventID string) (*domain.SoulDraft, error) {
		switch eventID {
		case currentDraft.EventID:
			return currentDraft, nil
		case proposedDraft.EventID:
			return proposedDraft, nil
		}
		return nil, nil
	}
	runtime := &scriptedLateRuntime{t: t, controller: signer, runtime: newFakeSigner(t), script: script}
	handler := NewLifecycleHandler(reactor, nil, nil, slog.Default())
	handler.SetRuntimeAdapters(map[domain.RuntimeTarget]RuntimeAdapter{domain.RuntimeTargetOpenClaw: runtime})
	reactor.lifecycleHandler = handler
	return &lateLifecycleFixture{signer: signer, runtime: runtime, reactor: reactor, handler: handler, capture: capture, soul: soul, currentDraft: currentDraft, proposedDraft: proposedDraft}
}

func (f *lateLifecycleFixture) action(t *testing.T, id string, action domain.SoulActionType) *nostr.Event {
	t.Helper()
	return buildActionEvent(t, f.signer, id, nostr.Tags{
		{"soul", buildSoulRefForTest(f.soul)}, {"action", string(action)},
		{"draft", "31952:" + f.signer.pubkey + ":scout"}, {"draft-event", f.proposedDraft.EventID},
		{"spec-hash", "sha256:new"}, {"previous-spec-hash", "sha256:old"},
	}, "")
}

// A hot-reload whose second runtime call times out is parked, not rolled back.
// Its late success resumes the remaining calls and publishes the same terminal
// result and soul a timely success would have.
func TestLifecycleHotReloadLateSuccessAfterTimeoutIsReconciled(t *testing.T) {
	f := newLateLifecycleFixture(t, "success", "timeout")
	if err := f.handler.HandleAction(t.Context(), f.action(t, "late-hot-reload", domain.SoulActionHotReload)); err != nil {
		t.Fatalf("HandleAction() error = %v, want the action parked", err)
	}

	requests, pending := f.runtime.snapshot()
	if len(requests) != 2 || len(pending) != 1 {
		t.Fatalf("runtime requests = %d pending = %d, want 2 calls with the second awaiting", len(requests), len(pending))
	}
	if f.runtime.rollbacks() != 0 {
		t.Fatal("a runtime timeout triggered a rollback")
	}
	if got := len(f.capture.byKind(domain.KindProvisioningResult)); got != 0 {
		t.Fatalf("terminal results while awaiting = %d, want 0", got)
	}
	if got := len(f.capture.byKind(domain.KindAgentSoul)); got != 0 {
		t.Fatalf("soul publishes while awaiting = %d, want 0", got)
	}
	if got := statusEventsWith(f.capture.byKind(domain.KindProvisioningStatus), actionStatusAwaitingTerminal); got != 1 {
		t.Fatalf("awaiting_terminal progress events = %d, want 1", got)
	}
	if f.soul.SpecHash != "sha256:old" {
		t.Fatalf("soul spec hash while awaiting = %q, want unchanged", f.soul.SpecHash)
	}

	late := lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "success", nostr.Now())
	f.reactor.handleEvent(t.Context(), late)

	requests, _ = f.runtime.snapshot()
	if len(requests) != 3 || f.runtime.rollbacks() != 0 {
		t.Fatalf("runtime requests after late success = %d (rollbacks %d), want the remaining call and no rollback", len(requests), f.runtime.rollbacks())
	}
	results := f.capture.byKind(domain.KindProvisioningResult)
	if len(results) != 1 || tagValue(results[0].Tags, tagStatus) != "completed" {
		t.Fatalf("terminal results after late success = %+v, want one completed", results)
	}
	if payload := decodeResultPayload(t, results[0]); payload["applied_change_count"] != float64(3) || payload["hot_reload"] != true {
		t.Fatalf("terminal payload = %#v, want 3 applied changes", payload)
	}
	if got := len(f.capture.byKind(domain.KindAgentSoul)); got != 1 {
		t.Fatalf("soul publishes after late success = %d, want 1", got)
	}
	if f.soul.SpecHash != "sha256:new" || f.soul.DraftEventID != f.proposedDraft.EventID {
		t.Fatalf("soul after late success = spec %q draft %q, want the proposed revision", f.soul.SpecHash, f.soul.DraftEventID)
	}

	// A second copy of the same result resumes nothing.
	f.reactor.handleEvent(t.Context(), late)
	if got := len(f.capture.byKind(domain.KindProvisioningResult)); got != 1 {
		t.Fatalf("terminal results after a duplicate late result = %d, want 1", got)
	}
}

// An update whose runtime call times out is parked; the late observed failure
// rolls it back exactly as a timely failure would have.
func TestLifecycleUpdateLateFailureAfterTimeoutRollsBack(t *testing.T) {
	f := newLateLifecycleFixture(t, "timeout")
	if err := f.handler.HandleAction(t.Context(), f.action(t, "late-update", domain.SoulActionUpdate)); err != nil {
		t.Fatalf("HandleAction() error = %v, want the action parked", err)
	}
	requests, pending := f.runtime.snapshot()
	if len(requests) != 1 || len(pending) != 1 || f.runtime.rollbacks() != 0 {
		t.Fatalf("requests = %d pending = %d rollbacks = %d, want one awaiting update and no rollback", len(requests), len(pending), f.runtime.rollbacks())
	}

	f.reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "error", nostr.Now()))

	requests, _ = f.runtime.snapshot()
	if f.runtime.rollbacks() != 1 || requests[len(requests)-1].Method != RuntimeMethodUpdate {
		t.Fatalf("runtime requests after late failure = %+v, want one update rollback", requests)
	}
	results := f.capture.byKind(domain.KindProvisioningResult)
	if len(results) != 1 || tagValue(results[0].Tags, tagStatus) != "error" {
		t.Fatalf("terminal results after late failure = %+v, want one error", results)
	}
	if got := len(f.capture.byKind(domain.KindAgentSoul)); got != 0 {
		t.Fatalf("soul publishes after late failure = %d, want 0", got)
	}
	if f.soul.SpecHash != "sha256:old" {
		t.Fatalf("soul spec hash after late failure = %q, want unchanged", f.soul.SpecHash)
	}
}

// A late result that arrives before the operation parks (between the wait's
// timeout and the park) is remembered and handed back by park.
func TestRuntimeResultWaitersHandBackResultObservedBeforePark(t *testing.T) {
	controller, runtime, forger := newFakeSigner(t), newFakeSigner(t), newFakeSigner(t)
	req := RuntimeAdapterRequest{
		Method:   RuntimeMethodConfigReload,
		Operator: RuntimeOperatorRef{Pubkey: controller.pubkey, RequestEvent: soulTestID("operator").Hex()},
		Soul:     RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
	}
	pending := newScriptedRuntimePending(t, controller, runtime, req, 0)
	waiters := newRuntimeResultWaiters(slog.Default())

	// A result for the same request signed by another key never correlates.
	if waiters.deliver(t.Context(), lateRuntimeResultEvent(t, forger, pending, "success", nostr.Now())) {
		t.Fatal("deliver resumed an operation that has not parked")
	}
	late := lateRuntimeResultEvent(t, runtime, pending, "success", nostr.Now())
	if waiters.deliver(t.Context(), late) {
		t.Fatal("deliver resumed an operation that has not parked")
	}
	got, observed := waiters.park(pending, parkedOperation{shardKey: "scout", resume: func(context.Context, *RuntimeControlResultEnvelope) {
		t.Fatal("park registered a continuation for a result it already had")
	}})
	if !observed || got == nil || got.Event.ID != late.ID {
		t.Fatalf("park() = %v, %v; want the runtime's result, not the forged one", got, observed)
	}
	if _, parked := waiters.shardKey(pending.requestID()); parked {
		t.Fatal("park registered a waiter after handing back the observed result")
	}
}

func newLateFleetFixture(t *testing.T, script ...string) (*Reactor, *scriptedLateRuntime, *fleetReconcilePublishCapture, *domain.AgentSoul, *FleetConfigSnapshot, *FleetConfigSnapshot) {
	t.Helper()
	oldRevision, newRevision := fleetReconcileSnapshots(t)
	soul := fleetReconcileSoul("alpha", oldRevision.EventID)
	signer := newFakeSigner(t)
	runtime := &scriptedLateRuntime{t: t, controller: signer, runtime: newFakeSigner(t), script: script}
	reactor, capture := newFleetReconcileTestReactorWithRuntime(t, signer, []*domain.AgentSoul{soul}, nil, runtime)
	reactor.getFleetConfigRevisionFn = func(_ context.Context, eventID string) (*FleetConfigSnapshot, error) {
		switch eventID {
		case oldRevision.EventID:
			return oldRevision, nil
		case newRevision.EventID:
			return newRevision, nil
		}
		return nil, nil
	}
	return reactor, runtime, capture, soul, oldRevision, newRevision
}

// A fleet apply that times out is not rolled back: the soul is awaiting its
// terminal result, and the late success records the revision and completes the
// operation exactly as a timely success would have.
func TestFleetApplyLateSuccessAfterTimeoutDoesNotRollBack(t *testing.T) {
	reactor, runtime, capture, _, oldRevision, newRevision := newLateFleetFixture(t, "timeout")
	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err != nil {
		t.Fatalf("Reconcile() error = %v, want the apply awaiting its result", err)
	}
	requests, pending := runtime.snapshot()
	if len(requests) != 1 || len(pending) != 1 || runtime.rollbacks() != 0 {
		t.Fatalf("requests = %d pending = %d rollbacks = %d, want one awaiting apply and no rollback", len(requests), len(pending), runtime.rollbacks())
	}
	if got := len(capture.byKind(domain.KindProvisioningResult)); got != 0 {
		t.Fatalf("terminal results while awaiting = %d, want 0", got)
	}
	if got := len(capture.byKind(domain.KindAgentSoul)); got != 0 {
		t.Fatalf("soul publishes while awaiting = %d, want 0", got)
	}
	if got := statusEventsWith(capture.byKind(domain.KindProvisioningStatus), actionStatusAwaitingTerminal); got != 1 {
		t.Fatalf("awaiting_terminal progress events = %d, want 1", got)
	}

	reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, runtime.runtime, pending[0], "success", nostr.Now()))

	if requests, _ := runtime.snapshot(); len(requests) != 1 || runtime.rollbacks() != 0 {
		t.Fatalf("runtime requests after late success = %d (rollbacks %d), want no rollback", len(requests), runtime.rollbacks())
	}
	souls := capture.byKind(domain.KindAgentSoul)
	if len(souls) != 1 || ParseAgentSoulEvent(souls[0]).AppliedFleetConfigRevision != newRevision.EventID {
		t.Fatalf("soul publishes after late success = %d, want one recording %s (was %s)", len(souls), newRevision.EventID, oldRevision.EventID)
	}
	results := capture.byKind(domain.KindProvisioningResult)
	if len(results) != 1 || tagValue(results[0].Tags, tagStatus) != "completed" {
		t.Fatalf("terminal results after late success = %+v, want one completed", results)
	}
	if payload := decodeResultPayload(t, results[0]); payload["fleet_status"] != "applied" {
		t.Fatalf("terminal payload = %#v, want fleet_status applied", payload)
	}
}

// An observed terminal failure, even a late one, rolls the fleet apply back.
func TestFleetApplyLateFailureAfterTimeoutRollsBack(t *testing.T) {
	reactor, runtime, capture, _, oldRevision, newRevision := newLateFleetFixture(t, "timeout")
	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	_, pending := runtime.snapshot()
	if len(pending) != 1 || runtime.rollbacks() != 0 {
		t.Fatalf("pending = %d rollbacks = %d, want one awaiting apply and no rollback", len(pending), runtime.rollbacks())
	}

	reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, runtime.runtime, pending[0], "error", nostr.Now()))

	requests, _ := runtime.snapshot()
	if len(requests) != 2 || requests[1].Action != domain.SoulActionRollback {
		t.Fatalf("runtime requests after late failure = %+v, want apply then rollback", requests)
	}
	rolledBack := requests[1].Params["patch"].(map[string]interface{})["fleet_config"].(*FleetConfigSnapshot)
	if rolledBack.EventID != oldRevision.EventID {
		t.Fatalf("rollback revision = %s, want %s", rolledBack.EventID, oldRevision.EventID)
	}
	if got := len(capture.byKind(domain.KindAgentSoul)); got != 0 {
		t.Fatalf("soul publishes after late failure = %d, want 0", got)
	}
	results := capture.byKind(domain.KindProvisioningResult)
	if len(results) != 1 || tagValue(results[0].Tags, tagStatus) != "error" {
		t.Fatalf("terminal results after late failure = %+v, want one error", results)
	}
	if payload := decodeResultPayload(t, results[0]); payload["fleet_status"] != "failed" || payload["rollback_status"] != "completed" {
		t.Fatalf("terminal payload = %#v, want failed with a completed rollback", payload)
	}
}

// A rollback whose own result is not observed is reported as outcome_unknown,
// not as completed or failed.
func TestFleetRollbackTimeoutReportsOutcomeUnknown(t *testing.T) {
	reactor, _, capture, _, _, newRevision := newLateFleetFixture(t, "failure", "timeout")
	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err == nil {
		t.Fatal("Reconcile() error = nil, want the observed apply failure")
	}
	results := capture.byKind(domain.KindProvisioningResult)
	if len(results) != 1 {
		t.Fatalf("terminal results = %d, want 1", len(results))
	}
	if payload := decodeResultPayload(t, results[0]); payload["rollback_status"] != rollbackStatusOutcomeUnknown {
		t.Fatalf("terminal payload = %#v, want rollback_status %s", payload, rollbackStatusOutcomeUnknown)
	}
}

// A newer revision defers a soul whose apply is awaiting its result; the late
// result is reconciled first and the soul is then re-driven to the newer
// revision, so revisions reach the runtime in order.
func TestFleetNewerRevisionDefersAwaitingSoulThenRedrives(t *testing.T) {
	reactor, runtime, capture, soul, _, newRevision := newLateFleetFixture(t, "timeout")
	newest := *newRevision
	newest.EventID = soulTestID("fleet-newest").Hex()
	newest.CreatedAt = newRevision.CreatedAt + 10
	newest.Document.Template = map[string]interface{}{"logging": map[string]interface{}{"level": "warn"}}
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) {
		current := *soul
		return &current, nil
	}

	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err != nil {
		t.Fatalf("Reconcile(new) error = %v", err)
	}
	if err := reactor.fleetReconciler().Reconcile(t.Context(), &newest); err != nil {
		t.Fatalf("Reconcile(newest) error = %v", err)
	}
	requests, pending := runtime.snapshot()
	if len(requests) != 1 || len(pending) != 1 {
		t.Fatalf("requests = %d pending = %d, want the newest revision deferred behind the awaiting apply", len(requests), len(pending))
	}

	reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, runtime.runtime, pending[0], "success", nostr.Now()))

	requests, _ = runtime.snapshot()
	if len(requests) != 2 || runtime.rollbacks() != 0 {
		t.Fatalf("runtime requests = %+v, want the deferred newest apply and no rollback", requests)
	}
	applied := requests[1].Params["patch"].(map[string]interface{})["fleet_config"].(*FleetConfigSnapshot)
	if applied.EventID != newest.EventID {
		t.Fatalf("re-driven revision = %s, want %s", applied.EventID, newest.EventID)
	}
	var recorded []string
	for _, event := range capture.byKind(domain.KindAgentSoul) {
		recorded = append(recorded, ParseAgentSoulEvent(event).AppliedFleetConfigRevision)
	}
	if len(recorded) != 2 || recorded[0] != newRevision.EventID || recorded[1] != newest.EventID {
		t.Fatalf("recorded revisions = %v, want %s then %s", recorded, newRevision.EventID, newest.EventID)
	}
	if got := len(capture.byKind(domain.KindProvisioningResult)); got != 2 {
		t.Fatalf("terminal results = %d, want one per revision", got)
	}
}

// The resumable subscription's reissued REQ starts from the newest event the
// relay delivered, less the overlap, but only once that relay's EOSE proved the
// backfill below it complete; future-dated events cannot push it past now.
func TestRelayClientResumableSubscriptionResumesFromCursorAfterEOSE(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	generations := make([]*fakeRelaySubscription, 4)
	for i := range generations {
		generations[i] = newFakeRelaySubscription()
		endpoint.subscribeQueue <- generations[i]
	}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const overlap = 10 * time.Minute
	overlapSeconds := nostr.Timestamp(overlap / time.Second)
	kind := domain.KindRuntimeControlResult
	sub, err := bus.subscribeResumable(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(kind)}, Limit: 50}}, overlap)
	if err != nil {
		t.Fatalf("subscribeResumable() error = %v", err)
	}
	if got := mustReceiveFilters(t, endpoint.subscribeCalls); got[0].Since != 0 {
		t.Fatalf("initial REQ since = %d, want the full backfill", got[0].Since)
	}

	base := nostr.Now() - 3600
	generations[0].events <- signedSoulFactoryEventAt(t, signer, kind, base, nil, "stored")
	close(generations[0].eose)
	mustReceiveRelayEvent(t, sub.Events)
	<-sub.EndOfStoredEvents
	generations[0].events <- signedSoulFactoryEventAt(t, signer, kind, base+60, nil, "live")
	mustReceiveRelayEvent(t, sub.Events)
	close(generations[0].events) // the connection drops

	want := base + 60 - overlapSeconds
	if got := mustReceiveFilters(t, endpoint.subscribeCalls); got[0].Since != want || got[0].Limit != 50 {
		t.Fatalf("reissued REQ = since %d limit %d, want since %d limit 50", got[0].Since, got[0].Limit, want)
	}

	// Dropped before EOSE: the generation's stored events may be incomplete, so
	// they do not advance the cursor.
	generations[1].events <- signedSoulFactoryEventAt(t, signer, kind, base+600, nil, "unconfirmed")
	mustReceiveRelayEvent(t, sub.Events)
	close(generations[1].events)
	if got := mustReceiveFilters(t, endpoint.subscribeCalls); got[0].Since != want {
		t.Fatalf("REQ after a generation without EOSE = since %d, want the committed %d", got[0].Since, want)
	}

	before := nostr.Now()
	close(generations[2].eose)
	generations[2].events <- signedSoulFactoryEventAt(t, signer, kind, before+300, nil, "future")
	mustReceiveRelayEvent(t, sub.Events)
	close(generations[2].events)
	got := mustReceiveFilters(t, endpoint.subscribeCalls)
	if got[0].Since < before-overlapSeconds || got[0].Since > nostr.Now()-overlapSeconds {
		t.Fatalf("REQ after a future-dated event = since %d, want clamped to now less the overlap", got[0].Since)
	}
}

// End to end through Reactor.Run: an action parks on a runtime timeout, the
// relay connection drops, the runtime publishes its result while the reactor is
// disconnected, and the resumed REQ's backfill delivers it and completes the
// action.
func TestReactorReconnectDeliversResultPublishedWhileDisconnected(t *testing.T) {
	f := newLateLifecycleFixture(t, "timeout")
	backfillComplete := make(chan struct{}, 1)
	f.reactor.logger = slog.New(&backfillSignalHandler{
		Handler:  slog.NewTextHandler(io.Discard, nil),
		complete: backfillComplete,
	})
	completed := make(chan struct{}, 1)
	f.reactor.publishFn = func(ctx context.Context, event *nostr.Event, relays []string) error {
		if err := f.capture.publish(ctx, event, relays); err != nil {
			return err
		}
		if event.Kind == nostr.Kind(domain.KindProvisioningResult) {
			select {
			case completed <- struct{}{}:
			default:
			}
		}
		return nil
	}
	endpoint := newFakeRelayEndpoint(t)
	first, second := newFakeRelaySubscription(), newFakeRelaySubscription()
	// The pool sends one REQ per filter: the runtime-result REQ is scripted,
	// the others (re)answer with EOSE.
	resultScripts := make(chan *fakeRelaySubscription, 2)
	resultScripts <- first
	resultScripts <- second
	endpoint.scriptFor = func(filters []nostr.Filter) *fakeRelaySubscription {
		if len(filters) == 1 && slices.Contains(filters[0].Kinds, nostr.Kind(domain.KindRuntimeControlResult)) {
			return <-resultScripts
		}
		return eoseScript()
	}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	f.reactor.relayClient = bus

	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- f.reactor.Run(ctx) }()

	initial := receiveREQFor(t, endpoint, nostr.Kind(domain.KindRuntimeControlResult))
	if got := initial.Tags[tagPubkey]; len(got) != 1 || got[0] != f.signer.pubkey || initial.Since != 0 {
		t.Fatalf("initial result filter = %+v, want #p the controller and a full backfill", initial)
	}
	// Stored history: an older, unrelated failed result, then EOSE.
	base := nostr.Now() - 120
	other := newScriptedRuntimePending(t, f.signer, f.runtime.runtime, RuntimeAdapterRequest{
		Method: RuntimeMethodVoiceConfigure, Operator: RuntimeOperatorRef{RequestEvent: soulTestID("older-action").Hex()},
		Soul: RuntimeSoulRef{ID: "scout", SpecHash: "sha256:old"},
	}, 99)
	first.events <- lateRuntimeResultEvent(t, f.runtime.runtime, other, "error", base)
	close(first.eose)
	select {
	case <-backfillComplete:
	case <-time.After(10 * time.Second):
		t.Fatal("initial result backfill did not reach EOSE")
	}

	if err := f.handler.HandleAction(t.Context(), f.action(t, "reconnect-hot-reload", domain.SoulActionHotReload)); err != nil {
		t.Fatalf("HandleAction() error = %v", err)
	}
	_, pending := f.runtime.snapshot()
	if len(pending) != 1 {
		t.Fatalf("pending runtime calls = %d, want 1", len(pending))
	}

	close(first.events) // the relay connection drops
	resumed := receiveREQFor(t, endpoint, nostr.Kind(domain.KindRuntimeControlResult))
	if want := base - nostr.Timestamp(reactorResumeOverlap/time.Second); resumed.Since != want {
		t.Fatalf("resumed result filter since = %d, want %d", resumed.Since, want)
	}
	// Published while the reactor was disconnected: only the resumed REQ's
	// backfill carries it.
	second.events <- lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "success", base+30)
	close(second.eose)

	select {
	case <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("late result delivered after reconnect never completed the action")
	}
	cancel()
	if err := <-runDone; err == nil {
		t.Fatal("Run() returned nil after cancellation")
	}
	results := f.capture.byKind(domain.KindProvisioningResult)
	if len(results) != 1 || tagValue(results[0].Tags, tagStatus) != "completed" {
		t.Fatalf("terminal results = %+v, want one completed", results)
	}
	if requests, _ := f.runtime.snapshot(); len(requests) != 3 || f.runtime.rollbacks() != 0 {
		t.Fatalf("runtime requests = %d rollbacks = %d, want all 3 sections and no rollback", len(requests), f.runtime.rollbacks())
	}
}

// Reactor.GetSoul reads through the reactor's own relay bus (Relays plus
// AdditionalRelays) rather than a temporary bus over Relays: a lifecycle action
// finds its soul on the bus even though config.Relays names another relay.
func TestLifecycleActionReadsSoulThroughReactorRelayBus(t *testing.T) {
	factory, operator := newFakeSigner(t), newFakeSigner(t)
	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: "Scout", Tier: domain.SoulTierStandard, Status: domain.SoulStatusActive, CreatedAt: time.Now().UTC()}
	soulEvent := BuildAgentSoulEvent(soul)
	if err := factory.Sign(t.Context(), soulEvent); err != nil {
		t.Fatalf("sign soul: %v", err)
	}
	endpoint := newFakeRelayEndpoint(t)
	lookup, terminal := newFakeRelaySubscription(), newFakeRelaySubscription()
	lookup.events <- soulEvent
	close(lookup.eose)
	close(terminal.eose)
	endpoint.subscribeQueue <- lookup
	endpoint.subscribeQueue <- terminal
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	reactor := NewReactor(Config{
		Relays:            []string{"wss://not-the-bus.invalid"},
		AuthorizedPubkeys: []string{operator.pubkey},
		SoulFactoryPubkey: factory.pubkey,
	}, scriptedGenerator{}, factory, slog.Default())
	reactor.relayClient = bus
	capture := attachPublishCapture(reactor)

	event := buildActionEvent(t, operator, "bus-suspend", nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionSuspend)}}, "")
	if err := reactor.lifecycle().HandleAction(t.Context(), event); err != nil {
		t.Fatalf("HandleAction() error = %v", err)
	}
	filters := mustReceiveFilters(t, endpoint.subscribeCalls)
	if len(filters) != 1 || len(filters[0].Kinds) != 1 || filters[0].Kinds[0] != nostr.Kind(domain.KindAgentSoul) ||
		len(filters[0].Authors) != 1 || filters[0].Authors[0].Hex() != factory.pubkey || filters[0].Tags[tagParameterizedD][0] != "scout" {
		t.Fatalf("soul lookup REQ on the reactor bus = %+v", filters)
	}
	souls := capture.eventsByKind(domain.KindAgentSoul)
	if len(souls) != 1 || ParseAgentSoulEvent(souls[0]).Status != domain.SoulStatusSuspended {
		t.Fatalf("published souls = %d, want the soul read through the bus, suspended", len(souls))
	}
}

// A later action for a soul whose action is awaiting a runtime result waits
// behind it, as it would have waited on the handler shard for a timely result,
// and runs once the late result finishes the first action.
func TestLifecycleActionDefersBehindAwaitingActionUntilLateResult(t *testing.T) {
	f := newLateLifecycleFixture(t, "timeout")
	update := f.action(t, "awaiting-update", domain.SoulActionUpdate)
	if err := f.handler.HandleAction(t.Context(), update); err != nil {
		t.Fatalf("HandleAction(update) error = %v", err)
	}
	suspend := buildActionEvent(t, f.signer, "deferred-suspend", nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", string(domain.SoulActionSuspend)}}, "")
	if err := f.handler.HandleAction(t.Context(), suspend); err != nil {
		t.Fatalf("HandleAction(suspend) error = %v", err)
	}
	if f.soul.Status != domain.SoulStatusActive || len(f.capture.byKind(domain.KindProvisioningResult)) != 0 {
		t.Fatalf("deferred suspend ran early: status %s, results %d", f.soul.Status, len(f.capture.byKind(domain.KindProvisioningResult)))
	}

	_, pending := f.runtime.snapshot()
	f.reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "success", nostr.Now()))

	results := f.capture.byKind(domain.KindProvisioningResult)
	if len(results) != 2 {
		t.Fatalf("terminal results = %d, want the update then the deferred suspend", len(results))
	}
	for i, want := range []*nostr.Event{update, suspend} {
		if got := tagValue(results[i].Tags, tagEvent); got != want.ID.Hex() || tagValue(results[i].Tags, tagStatus) != "completed" {
			t.Fatalf("terminal result %d = request %s status %s, want %s completed", i, got, tagValue(results[i].Tags, tagStatus), want.ID.Hex())
		}
	}
	if f.soul.SpecHash != "sha256:new" || f.soul.Status != domain.SoulStatusSuspended {
		t.Fatalf("soul = spec %q status %s, want the update applied, then suspended", f.soul.SpecHash, f.soul.Status)
	}
}

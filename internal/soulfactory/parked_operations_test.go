package soulfactory

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Tests for parked-operation rebuild after a restart, overflow
// that never strands a soul, late rollback follow-up progress, and per-soul
// serialization of lifecycle actions and fleet reloads. None waits on a clock:
// late results are delivered as events, and Run-based waits end on a publish.

// The gate runs deferred work in arrival order, collapses duplicates, and keeps
// the soul for work that parks until its continuation releases it.
func TestSoulOperationGateRunsDeferredWorkInArrivalOrderAcrossParks(t *testing.T) {
	gate := newSoulOperationGate()
	var ran []string
	holds := map[string]*soulHold{}
	op := func(key string, parks bool) soulOperation {
		return soulOperation{key: key, run: func(_ context.Context, hold *soulHold) bool {
			ran = append(ran, key)
			holds[key] = hold
			return parks
		}}
	}

	if got := gate.do(t.Context(), "scout", op("first", true), op("first", true)); got != soulOperationStarted {
		t.Fatalf("first = %v, want started", got)
	}
	if got := gate.do(t.Context(), "scout", op("second", true), op("second", true)); got != soulOperationDeferred {
		t.Fatalf("second = %v, want deferred behind the parked first", got)
	}
	if got := gate.do(t.Context(), "scout", op("third", false), op("third", false)); got != soulOperationDeferred {
		t.Fatalf("third = %v, want deferred", got)
	}
	for _, key := range []string{"first", "second"} {
		if got := gate.do(t.Context(), "scout", op(key, false), op(key, false)); got != soulOperationDuplicate {
			t.Fatalf("repeat %s = %v, want duplicate", key, got)
		}
	}
	if got := gate.do(t.Context(), "other", op("elsewhere", false), op("elsewhere", false)); got != soulOperationStarted {
		t.Fatalf("other soul = %v, want started: the gate is per soul", got)
	}

	gate.release(t.Context(), holds["first"])
	if want := []string{"first", "elsewhere", "second"}; !slices.Equal(ran, want) {
		t.Fatalf("ran = %v, want %v: second parks and keeps the soul", ran, want)
	}
	gate.release(t.Context(), holds["first"]) // stale release: no effect
	if held, deferred := gate.held("scout"); !held || deferred != 1 {
		t.Fatalf("held = %v deferred = %d, want second holding with third waiting", held, deferred)
	}
	gate.release(t.Context(), holds["second"])
	if want := []string{"first", "elsewhere", "second", "third"}; !slices.Equal(ran, want) {
		t.Fatalf("ran = %v, want %v", ran, want)
	}
	if held, deferred := gate.held("scout"); held || deferred != 0 {
		t.Fatalf("held = %v deferred = %d after all work, want the soul free", held, deferred)
	}
}

// A parked operation's late result can release the soul on another goroutine
// before the operation's run has returned. Work arriving in that window waits
// for the run to return instead of overlapping it, and is then started.
func TestSoulOperationGateDoesNotOverlapRunUnwindingAfterEarlyRelease(t *testing.T) {
	gate := newSoulOperationGate()
	var events []string
	first := soulOperation{key: "first", run: func(ctx context.Context, hold *soulHold) bool {
		events = append(events, "first:parked")
		gate.release(ctx, hold) // the late result arrives before run returns
		if got := gate.do(ctx, "scout", soulOperation{key: "second", run: func(context.Context, *soulHold) bool {
			events = append(events, "second")
			return false
		}}, soulOperation{key: "second", run: func(context.Context, *soulHold) bool {
			events = append(events, "second")
			return false
		}}); got != soulOperationDeferred {
			t.Errorf("work arriving while first's run unwinds = %v, want deferred", got)
		}
		events = append(events, "first:returned")
		return true
	}}
	gate.do(t.Context(), "scout", first, first)
	if want := []string{"first:parked", "first:returned", "second"}; !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if held, deferred := gate.held("scout"); held || deferred != 0 {
		t.Fatalf("held = %v deferred = %d, want the soul free", held, deferred)
	}
}

// Overflow evicts only late rollback follow-ups, which hold no soul; an
// operation holding its soul is never dropped, so its late result still
// resumes it.
func TestRuntimeResultWaitersOverflowNeverEvictsSoulHoldingOperation(t *testing.T) {
	controller, runtime := newFakeSigner(t), newFakeSigner(t)
	pendingFor := func(i int) *runtimeResultPending {
		return newScriptedRuntimePending(t, controller, runtime, RuntimeAdapterRequest{
			Method:   RuntimeMethodConfigReload,
			Operator: RuntimeOperatorRef{Pubkey: controller.pubkey, RequestEvent: soulTestID("overflow-operator").Hex()},
			Soul:     RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
		}, i)
	}
	waiters := newRuntimeResultWaiters(slog.Default())
	waiters.limit = 1
	resumed := map[int]bool{}
	park := func(i int, holdsSoul bool) *runtimeResultPending {
		pending := pendingFor(i)
		if _, observed := waiters.park(pending, parkedOperation{shardKey: "scout", holdsSoul: holdsSoul, resume: func(context.Context, *RuntimeControlResultEnvelope) {
			resumed[i] = true
		}}); observed {
			t.Fatalf("park(%d) observed a result that was never delivered", i)
		}
		return pending
	}

	holding := park(0, true)
	firstFollowUp := park(1, false)
	secondFollowUp := park(2, false) // over the limit: the oldest follow-up goes
	secondHolding := park(3, true)   // holding entries do not count against it

	for i, pending := range []*runtimeResultPending{holding, firstFollowUp, secondFollowUp, secondHolding} {
		waiters.deliver(t.Context(), lateRuntimeResultEvent(t, runtime, pending, "success", nostr.Now()))
		_ = i
	}
	if want := map[int]bool{0: true, 2: true, 3: true}; len(resumed) != len(want) || !resumed[0] || !resumed[2] || !resumed[3] {
		t.Fatalf("resumed = %v, want %v: only the oldest follow-up is evicted", resumed, want)
	}
}

// End to end: a fleet apply awaiting its result holds its soul while a burst of
// late rollback follow-ups overflows the waiters. The soul is not stranded:
// its late result completes the apply and the newer revision deferred behind
// it is applied.
func TestParkedOverflowDoesNotStrandSoul(t *testing.T) {
	oldRevision, newRevision := fleetReconcileSnapshots(t)
	newest := *newRevision
	newest.EventID = soulTestID("overflow-newest").Hex()
	newest.CreatedAt = newRevision.CreatedAt + 10
	newest.Document.Template = map[string]interface{}{"logging": map[string]interface{}{"level": "warn"}}
	souls := []*domain.AgentSoul{fleetReconcileSoul("alpha", oldRevision.EventID), fleetReconcileSoul("beta", oldRevision.EventID)}
	signer := newFakeSigner(t)
	// alpha apply times out (parks, holding alpha); beta apply fails and its
	// rollback times out (a follow-up that holds nothing); then the newest
	// revision applies to beta, and to alpha once alpha is released.
	runtime := &scriptedLateRuntime{t: t, controller: signer, runtime: newFakeSigner(t), script: []string{"timeout", "failure", "timeout", "success", "success"}}
	reactor, capture := newFleetReconcileTestReactorWithRuntime(t, signer, souls, nil, runtime)
	reactor.fleetConfigReconciler = NewFleetConfigReconciler(reactor, 1) // alpha, then beta
	reactor.runtimeResults.limit = 0
	revisions := map[string]*FleetConfigSnapshot{oldRevision.EventID: oldRevision, newRevision.EventID: newRevision, newest.EventID: &newest}
	reactor.getFleetConfigRevisionFn = func(_ context.Context, eventID string) (*FleetConfigSnapshot, error) {
		return revisions[eventID], nil
	}

	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("Reconcile(new) error = %v, want beta's observed failure", err)
	}
	_, pending := runtime.snapshot()
	if len(pending) != 2 {
		t.Fatalf("pending runtime calls = %d, want alpha's apply and beta's rollback", len(pending))
	}
	if _, parked := reactor.runtimeResults.shardKey(pending[0].requestID()); !parked {
		t.Fatal("overflow evicted alpha's apply, which holds the soul")
	}
	if _, parked := reactor.runtimeResults.shardKey(pending[1].requestID()); parked {
		t.Fatal("beta's rollback follow-up survived an overflow past a limit of 0")
	}

	if err := reactor.fleetReconciler().Reconcile(t.Context(), &newest); err != nil {
		t.Fatalf("Reconcile(newest) error = %v", err)
	}
	if held, deferred := reactor.soulOperations().held("alpha"); !held || deferred != 1 {
		t.Fatalf("alpha held = %v deferred = %d, want the newest revision deferred behind the awaiting apply", held, deferred)
	}

	reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, runtime.runtime, pending[0], "success", nostr.Now()))

	for _, agentID := range []string{"alpha", "beta"} {
		if soul := capture.latestSoul(agentID); soul == nil || soul.AppliedFleetConfigRevision != newest.EventID {
			t.Fatalf("%s recorded revision = %+v, want %s", agentID, soul, newest.EventID)
		}
	}
	if held, _ := reactor.soulOperations().held("alpha"); held {
		t.Fatal("alpha is still held after its late result and the deferred re-drive")
	}
}

func progressWithStatus(events []*nostr.Event, status string) []*nostr.Event {
	var out []*nostr.Event
	for _, event := range events {
		if tagValue(event.Tags, tagStatus) == status {
			out = append(out, event)
		}
	}
	return out
}

// A lifecycle rollback whose result times out is reported outcome_unknown; its
// late result is then published as rollback_resolved progress on the action.
func TestLifecycleLateRollbackResultPublishesFollowUpProgress(t *testing.T) {
	f := newLateLifecycleFixture(t, "failure", "timeout")
	update := f.action(t, "late-rollback-update", domain.SoulActionUpdate)
	if err := f.handler.HandleAction(t.Context(), update); err == nil || !strings.Contains(err.Error(), rollbackStatusOutcomeUnknown) {
		t.Fatalf("HandleAction() error = %v, want the failure with an outcome_unknown rollback", err)
	}
	_, pending := f.runtime.snapshot()
	if len(pending) != 1 || pending[0].req.Action != domain.SoulActionRollback {
		t.Fatalf("pending runtime calls = %+v, want the timed-out rollback", pending)
	}
	if got := progressWithStatus(f.capture.byKind(domain.KindProvisioningStatus), actionStatusRollbackResolved); len(got) != 0 {
		t.Fatalf("rollback_resolved progress before the late result = %d, want 0", len(got))
	}

	late := lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "success", nostr.Now())
	f.reactor.handleEvent(t.Context(), late)
	f.reactor.handleEvent(t.Context(), late) // a duplicate delivery reports nothing new

	followUps := progressWithStatus(f.capture.byKind(domain.KindProvisioningStatus), actionStatusRollbackResolved)
	if len(followUps) != 1 {
		t.Fatalf("rollback_resolved progress = %d, want 1", len(followUps))
	}
	if got := tagValue(followUps[0].Tags, tagEvent); got != update.ID.Hex() {
		t.Fatalf("follow-up references %s, want the action %s", got, update.ID.Hex())
	}
	if got := tagValue(followUps[0].Tags, tagRollbackStatus); got != "completed" {
		t.Fatalf("follow-up rollback-status = %q, want completed", got)
	}
	if got := len(f.capture.byKind(domain.KindProvisioningResult)); got != 1 {
		t.Fatalf("terminal results = %d, want only the original error", got)
	}
}

// The same for a fleet rollback: the late failure is published as progress for
// the soul's fleet revision.
func TestFleetLateRollbackResultPublishesFollowUpProgress(t *testing.T) {
	reactor, runtime, capture, soul, _, newRevision := newLateFleetFixture(t, "failure", "timeout")
	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err == nil {
		t.Fatal("Reconcile() error = nil, want the observed apply failure")
	}
	_, pending := runtime.snapshot()
	if len(pending) != 1 {
		t.Fatalf("pending runtime calls = %d, want the timed-out rollback", len(pending))
	}

	reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, runtime.runtime, pending[0], "error", nostr.Now()))

	followUps := progressWithStatus(capture.byKind(domain.KindProvisioningStatus), actionStatusRollbackResolved)
	if len(followUps) != 1 {
		t.Fatalf("rollback_resolved progress = %d, want 1", len(followUps))
	}
	tags := followUps[0].Tags
	if tagValue(tags, tagRollbackStatus) != "failed" || tagValue(tags, tagFleetRevision) != newRevision.EventID || tagValue(tags, tagAgentID) != soul.AgentID {
		t.Fatalf("follow-up tags = %v, want rollback-status failed for %s on %s", tags, soul.AgentID, newRevision.EventID)
	}
	if !strings.Contains(followUps[0].Content, "runtime rejected the request") {
		t.Fatalf("follow-up message = %q, want the runtime's error", followUps[0].Content)
	}
}

// relayBackedLifecycleFixture is a lateLifecycleFixture whose soul reads return
// what the reactor last published (as relays would), with fleet reconciliation
// over the same soul.
func relayBackedLifecycleFixture(t *testing.T, script ...string) *lateLifecycleFixture {
	t.Helper()
	f := newLateLifecycleFixture(t, script...)
	f.reactor.getSoulFn = relayBackedSoulLookup(f.capture, func() []*domain.AgentSoul { return []*domain.AgentSoul{f.soul} })
	f.reactor.listSoulsFn = func(ctx context.Context) ([]*domain.AgentSoul, error) {
		soul, err := f.reactor.getSoulFn(ctx, f.soul.AgentID)
		return []*domain.AgentSoul{soul}, err
	}
	return f
}

func methods(requests []RuntimeAdapterRequest) []string {
	out := make([]string, 0, len(requests))
	for _, request := range requests {
		out = append(out, request.Method)
	}
	return out
}

// A fleet reload for a soul whose lifecycle update awaits its runtime result
// waits behind it, then applies on top of the update: no runtime request
// overlaps, and the final soul keeps both changes.
func TestFleetReloadWaitsBehindAwaitingLifecycleUpdateForSameSoul(t *testing.T) {
	f := relayBackedLifecycleFixture(t, "timeout")
	_, revision := fleetReconcileSnapshots(t)
	if err := f.handler.HandleAction(t.Context(), f.action(t, "serialized-update", domain.SoulActionUpdate)); err != nil {
		t.Fatalf("HandleAction(update) error = %v", err)
	}
	awaiting := progressWithStatus(f.capture.byKind(domain.KindProvisioningStatus), actionStatusAwaitingTerminal)
	if len(awaiting) != 1 || tagValue(awaiting[0].Tags, tagTopic) != awaitingTerminalTopic {
		t.Fatalf("awaiting_terminal progress = %+v, want one tagged t=%s", awaiting, awaitingTerminalTopic)
	}

	if err := f.reactor.fleetReconciler().Reconcile(t.Context(), revision); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if requests, _ := f.runtime.snapshot(); len(requests) != 1 {
		t.Fatalf("runtime requests = %v, want the fleet reload held back while the update awaits", methods(requests))
	}

	_, pending := f.runtime.snapshot()
	f.reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "success", nostr.Now()))

	requests, _ := f.runtime.snapshot()
	if want := []string{RuntimeMethodUpdate, RuntimeMethodPersonaUpdate, RuntimeMethodConfigReload}; !slices.Equal(methods(requests), want) {
		t.Fatalf("runtime requests = %v, want %v", methods(requests), want)
	}
	soul := f.capture.latestSoul(f.soul.AgentID)
	if soul == nil || soul.SpecHash != "sha256:new" || soul.AppliedFleetConfigRevision != revision.EventID {
		t.Fatalf("final soul = %+v, want the update's spec and the fleet revision", soul)
	}
}

// A lifecycle action for a soul whose fleet apply awaits its runtime result
// waits behind it, then runs on the soul the fleet apply recorded.
func TestLifecycleActionWaitsBehindAwaitingFleetApplyForSameSoul(t *testing.T) {
	f := relayBackedLifecycleFixture(t, "timeout")
	_, revision := fleetReconcileSnapshots(t)
	if err := f.reactor.fleetReconciler().Reconcile(t.Context(), revision); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	suspend := buildActionEvent(t, f.signer, "serialized-suspend", nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", string(domain.SoulActionSuspend)}}, "")
	if err := f.handler.HandleAction(t.Context(), suspend); err != nil {
		t.Fatalf("HandleAction(suspend) error = %v", err)
	}
	if got := len(f.capture.byKind(domain.KindAgentSoul)); got != 0 {
		t.Fatalf("soul publishes while the fleet apply awaits = %d, want 0: the suspend ran early", got)
	}

	_, pending := f.runtime.snapshot()
	f.reactor.handleEvent(t.Context(), lateRuntimeResultEvent(t, f.runtime.runtime, pending[0], "success", nostr.Now()))

	results := f.capture.byKind(domain.KindProvisioningResult)
	if len(results) != 2 || tagValue(results[0].Tags, tagRequestKind) != "31953" || tagValue(results[1].Tags, tagEvent) != suspend.ID.Hex() {
		t.Fatalf("terminal results = %d, want the fleet apply's then the suspend's", len(results))
	}
	soul := f.capture.latestSoul(f.soul.AgentID)
	if soul == nil || soul.Status != domain.SoulStatusSuspended || soul.AppliedFleetConfigRevision != revision.EventID {
		t.Fatalf("final soul = %+v, want suspended with the fleet revision kept", soul)
	}
}

// After a restart, an action a previous run left awaiting_terminal is rebuilt
// from relay events (its 6950 without a matching 7950) and re-driven before the
// backlog is handled, even though it is outside the backlog; the soul's later
// action waits behind it.
func TestReactorRestartRebuildsAwaitingActionFromRelayEvents(t *testing.T) {
	f := newLateLifecycleFixture(t)
	results := make(chan *nostr.Event, 4)
	f.reactor.publishFn = func(ctx context.Context, event *nostr.Event, relays []string) error {
		if err := f.capture.publish(ctx, event, relays); err != nil {
			return err
		}
		if event.Kind == nostr.Kind(domain.KindProvisioningResult) {
			results <- event
		}
		return nil
	}

	// Relay-served events must be validly signed.
	signed := func(unsigned *nostr.Event, createdAt nostr.Timestamp) *nostr.Event {
		event := &nostr.Event{Kind: unsigned.Kind, CreatedAt: createdAt, Tags: unsigned.Tags, Content: unsigned.Content}
		if err := f.signer.Sign(t.Context(), event); err != nil {
			t.Fatalf("sign action: %v", err)
		}
		return event
	}
	base := nostr.Now() - 60
	parkedAction := signed(f.action(t, "rebuilt-hot-reload", domain.SoulActionHotReload), base)
	parsed, err := ParseSoulActionEvent(parkedAction)
	if err != nil {
		t.Fatalf("parse action: %v", err)
	}
	awaiting := BuildActionStatusEvent(parsed, actionStatusAwaitingTerminal, "awaiting_terminal: runtime result not observed", f.soul.AgentID)
	awaiting.Tags = append(awaiting.Tags, awaitingTerminalTags()...)
	awaiting.CreatedAt = parkedAction.CreatedAt + 1
	if err := f.signer.Sign(t.Context(), awaiting); err != nil {
		t.Fatalf("sign awaiting progress: %v", err)
	}
	later := signed(buildActionEvent(t, f.signer, "after-restart-suspend", nostr.Tags{{"soul", buildSoulRefForTest(f.soul)}, {"action", string(domain.SoulActionSuspend)}}, ""), base+10)

	endpoint := newFakeRelayEndpoint(t)
	var mu sync.Mutex
	var rebuildFilter *nostr.Filter
	endpoint.scriptFor = func(filters []nostr.Filter) *fakeRelaySubscription {
		filter := filters[0]
		script := newFakeRelaySubscription()
		switch {
		case slices.Contains(filter.Kinds, nostr.Kind(domain.KindProvisioningStatus)):
			mu.Lock()
			rebuildFilter = &filter
			mu.Unlock()
			script.events <- awaiting
		case slices.Contains(filter.Kinds, nostr.Kind(domain.KindSoulAction)) && len(filter.IDs) > 0:
			script.events <- parkedAction
		case slices.Contains(filter.Kinds, nostr.Kind(domain.KindSoulAction)):
			// The backlog holds only the later action: the parked one is
			// outside its limit.
			script.events <- later
		}
		close(script.eose)
		return script
	}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	f.reactor.relayClient = bus

	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- f.reactor.Run(ctx) }()

	var got []string
	for range 2 {
		select {
		case result := <-results:
			if status := tagValue(result.Tags, tagStatus); status != "completed" {
				t.Fatalf("terminal result for %s = %s, want completed", tagValue(result.Tags, tagEvent), status)
			}
			got = append(got, tagValue(result.Tags, tagEvent))
		case <-time.After(10 * time.Second):
			t.Fatalf("terminal results = %v, want the rebuilt action and then the later one", got)
		}
	}
	cancel()
	<-runDone

	if want := []string{parkedAction.ID.Hex(), later.ID.Hex()}; !slices.Equal(got, want) {
		t.Fatalf("terminal results = %v, want the rebuilt action before the later one %v", got, want)
	}
	requests, _ := f.runtime.snapshot()
	for _, request := range requests {
		if request.Operator.RequestEvent != parkedAction.ID.Hex() {
			t.Fatalf("runtime request for %s, want only the re-driven action's", request.Operator.RequestEvent)
		}
	}
	if len(requests) != 3 {
		t.Fatalf("runtime requests = %d, want the re-driven hot reload's 3 sections", len(requests))
	}
	mu.Lock()
	defer mu.Unlock()
	if rebuildFilter == nil || len(rebuildFilter.Authors) != 1 || rebuildFilter.Authors[0].Hex() != f.signer.pubkey ||
		!slices.Equal(rebuildFilter.Tags[tagTopic], []string{awaitingTerminalTopic}) {
		t.Fatalf("rebuild REQ = %+v, want the factory's 6950s tagged t=%s", rebuildFilter, awaitingTerminalTopic)
	}
}

// An awaiting_terminal operation with a matching terminal result is finished:
// the rebuild skips it. Fleet operations are matched per soul.
func TestOutstandingAwaitingOperationsSkipFinishedOnes(t *testing.T) {
	factory := newFakeSigner(t)
	factoryKey, err := nostr.PubKeyFromHex(factory.pubkey)
	if err != nil {
		t.Fatalf("factory key: %v", err)
	}
	sign := func(event *nostr.Event) *nostr.Event {
		if err := factory.Sign(t.Context(), event); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return event
	}
	action := func(id string) *domain.SoulAction {
		return &domain.SoulAction{EventID: soulTestID(id).Hex(), SoulRef: "31951:" + factory.pubkey + ":scout", Action: domain.SoulActionUpdate, Initiator: factory.pubkey}
	}
	awaitingFor := func(a *domain.SoulAction, agentID string, at nostr.Timestamp, fleet bool) *nostr.Event {
		event := BuildActionStatusEvent(a, actionStatusAwaitingTerminal, "awaiting", agentID)
		if fleet {
			setTagValue(&event.Tags, tagRequestKind, "31953")
		}
		event.Tags = append(event.Tags, awaitingTerminalTags()...)
		event.CreatedAt = at
		return sign(event)
	}
	resultFor := func(a *domain.SoulAction, agentID string, fleet bool) *nostr.Event {
		event, err := BuildActionResultEvent(a, "completed", nil, ActionResultCanonical, agentID)
		if err != nil {
			t.Fatalf("build result: %v", err)
		}
		if fleet {
			setTagValue(&event.Tags, tagRequestKind, "31953")
		}
		return sign(event)
	}
	done, open, fleet := action("done"), action("open"), action("fleet-revision")
	awaiting := []*nostr.Event{
		awaitingFor(open, "scout", 30, false),
		awaitingFor(done, "scout", 10, false),
		awaitingFor(open, "scout", 40, false), // a later park of the same action
		awaitingFor(fleet, "alpha", 20, true),
		awaitingFor(fleet, "beta", 25, true),
	}
	outstanding := withoutTerminalResults(outstandingFromAwaiting(awaiting, factoryKey),
		[]*nostr.Event{resultFor(done, "scout", false), resultFor(fleet, "alpha", true)}, factoryKey)

	var got []string
	for _, op := range outstanding {
		got = append(got, op.key())
	}
	want := []string{"fleet:" + fleet.EventID + ":beta", "action:" + open.EventID}
	if !slices.Equal(got, want) {
		t.Fatalf("outstanding = %v, want %v (oldest first)", got, want)
	}
}

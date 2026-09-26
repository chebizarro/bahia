package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// assistantTestRelay is a deterministic single relay. Publish answers OK only
// when the event is stored; subscriptions deliver stored matches, then EOSE,
// then live matches, in order. Tests inject rejection, CLOSED and stream end.
// Every state change broadcasts on a channel so tests wait on barriers, never
// on elapsed time.

type assistantTestRelay struct {
	mu      sync.Mutex
	events  []nostr.Event
	log     []nostr.Event // every accepted publication, in order
	subs    map[int]*assistantTestRelaySub
	nextSub int
	changed chan struct{}
	reject  func(nostr.Event) error
	// ambiguous stores the event but reports an error, like an OK lost in
	// transit.
	ambiguous func(nostr.Event) bool
}

type assistantTestRelayItem struct {
	ev   *nostr.Event
	eose bool
}

type assistantTestRelaySub struct {
	id      int
	filters []nostr.Filter
	events  chan *nostr.Event
	closed  chan AssistantRelayClosed
	eose    chan struct{}
	done    chan struct{}
	wake    chan struct{}
	mu      sync.Mutex
	queue   []assistantTestRelayItem
	ended   bool
	once    sync.Once
}

func newAssistantTestRelay() *assistantTestRelay {
	return &assistantTestRelay{subs: map[int]*assistantTestRelaySub{}, changed: make(chan struct{})}
}

func (r *assistantTestRelay) touchLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *assistantTestRelay) touch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchLocked()
}

func assistantReplaceable(kind nostr.Kind) (bool, bool) {
	k := int(kind)
	switch {
	case k == 0 || k == 3 || (k >= 10000 && k < 20000):
		return true, false
	case k >= 30000 && k < 40000:
		return true, true
	}
	return false, false
}

func (r *assistantTestRelay) Publish(_ context.Context, ev nostr.Event) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.touchLocked()
	if !ev.CheckID() || !ev.VerifySignature() {
		return 0, errors.New("invalid: bad id or signature")
	}
	if r.reject != nil {
		if err := r.reject(ev); err != nil {
			return 0, err
		}
	}
	ambiguous := r.ambiguous != nil && r.ambiguous(ev)
	r.log = append(r.log, ev)
	if replaceable, addressable := assistantReplaceable(ev.Kind); replaceable {
		d := tagValue(ev.Tags, "d")
		for i := range r.events {
			old := r.events[i]
			if old.Kind != ev.Kind || old.PubKey != ev.PubKey || (addressable && tagValue(old.Tags, "d") != d) {
				continue
			}
			if old.CreatedAt > ev.CreatedAt || (old.CreatedAt == ev.CreatedAt && old.ID.Hex() <= ev.ID.Hex()) {
				if ambiguous {
					return 0, errors.New("connection lost before OK")
				}
				return 1, nil // NIP-01: the newer stored event wins
			}
			r.events = append(r.events[:i], r.events[i+1:]...)
			break
		}
	}
	r.events = append(r.events, ev)
	for _, sub := range r.subs {
		if assistantFiltersMatch(sub.filters, ev) {
			copyEv := ev
			sub.push(assistantTestRelayItem{ev: &copyEv})
		}
	}
	if ambiguous {
		return 0, errors.New("connection lost before OK")
	}
	return 1, nil
}

func assistantFiltersMatch(filters []nostr.Filter, ev nostr.Event) bool {
	for _, f := range filters {
		if f.Matches(ev) {
			return true
		}
	}
	return false
}

func (r *assistantTestRelay) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (AssistantMergedSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.touchLocked()
	r.nextSub++
	sub := &assistantTestRelaySub{id: r.nextSub, filters: append([]nostr.Filter(nil), filters...), events: make(chan *nostr.Event), closed: make(chan AssistantRelayClosed, 4), eose: make(chan struct{}), done: make(chan struct{}), wake: make(chan struct{}, 1)}
	backfill := []nostr.Event{}
	for _, ev := range r.events {
		if assistantFiltersMatch(filters, ev) {
			backfill = append(backfill, ev)
		}
	}
	sort.SliceStable(backfill, func(i, j int) bool { return backfill[i].CreatedAt > backfill[j].CreatedAt })
	for i := range backfill {
		ev := backfill[i]
		sub.queue = append(sub.queue, assistantTestRelayItem{ev: &ev})
	}
	sub.queue = append(sub.queue, assistantTestRelayItem{eose: true})
	r.subs[sub.id] = sub
	go sub.pump(r)
	return sub, nil
}

func (s *assistantTestRelaySub) push(item assistantTestRelayItem) {
	s.mu.Lock()
	s.queue = append(s.queue, item)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *assistantTestRelaySub) pump(r *assistantTestRelay) {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			ended := s.ended
			s.mu.Unlock()
			if ended {
				close(s.events)
				r.touch()
				return
			}
			select {
			case <-s.wake:
				continue
			case <-s.done:
				return
			}
		}
		item := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		if item.eose {
			close(s.eose)
			r.touch()
			continue
		}
		select {
		case s.events <- item.ev:
		case <-s.done:
			return
		}
	}
}

func (s *assistantTestRelaySub) EventChan() <-chan *nostr.Event          { return s.events }
func (s *assistantTestRelaySub) ClosedChan() <-chan AssistantRelayClosed { return s.closed }
func (s *assistantTestRelaySub) EOSEChan() <-chan struct{}               { return s.eose }
func (s *assistantTestRelaySub) Close()                                  { s.once.Do(func() { close(s.done) }) }

// closeMatching sends CLOSED to live subscriptions whose filters match kind
// and ends their streams, as a relay that drops a subscription does.
func (r *assistantTestRelay) closeMatching(kind nostr.Kind, reason string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.touchLocked()
	n := 0
	for id, sub := range r.subs {
		if !assistantSubHasKind(sub, kind) {
			continue
		}
		sub.closed <- AssistantRelayClosed{RelayURL: "wss://relay.test", Reason: reason}
		sub.mu.Lock()
		sub.ended = true
		sub.mu.Unlock()
		select {
		case sub.wake <- struct{}{}:
		default:
		}
		delete(r.subs, id)
		n++
	}
	return n
}

func assistantSubHasKind(sub *assistantTestRelaySub, kind nostr.Kind) bool {
	for _, f := range sub.filters {
		for _, k := range f.Kinds {
			if k == kind {
				return true
			}
		}
	}
	return false
}

// liveSubs counts open subscriptions for a kind.
func (r *assistantTestRelay) liveSubs(kind nostr.Kind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, sub := range r.subs {
		select {
		case <-sub.done:
			continue
		default:
		}
		if assistantSubHasKind(sub, kind) {
			n++
		}
	}
	return n
}

func (r *assistantTestRelay) eventsOfKind(kind nostr.Kind) []nostr.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []nostr.Event{}
	for _, ev := range r.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// published returns every accepted publication of a kind in publish order,
// including replaceable events later superseded in storage.
func (r *assistantTestRelay) published(kind nostr.Kind) []nostr.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []nostr.Event{}
	for _, ev := range r.log {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (r *assistantTestRelay) setReject(fn func(nostr.Event) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reject = fn
}

// waitFor re-evaluates cond after every relay publish, subscribe or stream
// change. The deadline only turns a hang into a failure.
func (r *assistantTestRelay) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		ch := r.changed
		r.mu.Unlock()
		if cond() {
			return
		}
		select {
		case <-ch:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// ---------------------------------------------------------------------------
// assistantTestToolServer counts provider invocations per tool and can hold an
// invocation at a gate until the test releases it.

type assistantTestToolServer struct {
	onCall  func(tool string)
	mu      sync.Mutex
	calls   map[string]int
	keys    map[string][]string
	gates   map[string]chan struct{}
	entered chan string
	notify  func()
}

func newAssistantTestToolServer(notify func()) *assistantTestToolServer {
	return &assistantTestToolServer{calls: map[string]int{}, keys: map[string][]string{}, gates: map[string]chan struct{}{}, entered: make(chan string, 64), notify: notify}
}

func (s *assistantTestToolServer) gate(tool string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{})
	s.gates[tool] = ch
	return ch
}

func (s *assistantTestToolServer) enter(ctx context.Context, tool string, args map[string]interface{}) error {
	s.mu.Lock()
	s.calls[tool]++
	if key, _ := args["idempotency_key"].(string); key != "" {
		s.keys[tool] = append(s.keys[tool], key)
	}
	gate := s.gates[tool]
	onCall := s.onCall
	s.mu.Unlock()
	if onCall != nil {
		onCall(tool)
	}
	select {
	case s.entered <- tool:
	default:
	}
	if s.notify != nil {
		s.notify()
	}
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *assistantTestToolServer) CallTool(ctx context.Context, name string, args map[string]interface{}) (*AssistantToolRuntimeToolResult, error) {
	if err := s.enter(ctx, name, args); err != nil {
		return nil, err
	}
	return &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true,"tool":"` + name + `"}`}}}, nil
}

func (s *assistantTestToolServer) InvokeAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	if err := s.enter(ctx, name, args); err != nil {
		return nil, err
	}
	key, _ := args["idempotency_key"].(string)
	return &domain.AsyncToolReceipt{ToolName: name, RequestEventID: assistantTestRequestID(key), RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: key}, nil
}

func assistantTestRequestID(key string) string { return "request:" + key }

func (s *assistantTestToolServer) count(tool string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[tool]
}

func (s *assistantTestToolServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		n += c
	}
	return n
}

func (s *assistantTestToolServer) keysFor(tool string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys[tool]...)
}

func assistantTestRegistry() assistantRuntimeRegistry {
	schema := map[string]any{"type": "object"}
	return assistantRuntimeRegistry{
		"read-one": {Name: "read-one", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: schema},
		"mutate":   {Name: "mutate", ExecutionMode: domain.AssistantToolExecutionModeAsync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: schema},
		"read-two": {Name: "read-two", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: schema},
	}
}

func assistantTestRuntime(server AssistantToolRuntimeMCPServer, hooks *AssistantHookRunner, rules []AssistantPermissionRule) *AssistantToolRuntime {
	return NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: assistantTestRegistry(), Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, rules), Hooks: hooks})
}

// ---------------------------------------------------------------------------
// Proposers

type assistantTestBatchProposer struct{ plan domain.AssistantPlan }

func (p assistantTestBatchProposer) ProposeBatch(context.Context, AssistantProposalRequest) (AssistantProposal, error) {
	plan := p.plan
	return AssistantProposal{Kind: AssistantProposalBatch, Batch: &plan}, nil
}

// assistantScriptedProposer returns one scripted response per model call. Its
// state is shared across restarts to stand in for transcript-derived history.
type assistantScriptedProposer struct {
	mu        sync.Mutex
	responses []AssistantProposal
	requests  []AssistantProposalRequest
	gate      chan struct{}
	entered   chan struct{}
}

func (p *assistantScriptedProposer) ProposeIterative(ctx context.Context, req AssistantProposalRequest) (AssistantProposal, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	idx := len(p.requests) - 1
	gate := p.gate
	entered := p.entered
	p.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			// A cancelled model call may still return a (late) response.
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < len(p.responses) {
		return p.responses[idx], nil
	}
	return AssistantProposal{Kind: AssistantProposalFinal, Text: "done"}, nil
}

func (p *assistantScriptedProposer) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

type assistantScopeResolverFunc func(context.Context, AssistantTurnStartRequest) (domain.AssistantCommandScope, error)

func (f assistantScopeResolverFunc) ResolveAssistantScope(ctx context.Context, req AssistantTurnStartRequest) (domain.AssistantCommandScope, error) {
	return f(ctx, req)
}

// assistantMutableHookEvaluator is a prompt-hook evaluator whose outcome the
// test changes between approval and dispatch.
type assistantMutableHookEvaluator struct {
	mu       sync.Mutex
	outcome  AssistantHookOutcome
	sequence []AssistantHookOutcome // consumed one per evaluation, then outcome
	calls    int
}

func (h *assistantMutableHookEvaluator) set(outcome AssistantHookOutcome) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outcome = outcome
}

func (h *assistantMutableHookEvaluator) EvaluateHookPrompt(context.Context, AssistantHookPromptRequest) (AssistantHookOutcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	out := h.outcome
	if h.calls <= len(h.sequence) {
		out = h.sequence[h.calls-1]
	}
	if out.UpdatedInput != nil {
		copied := map[string]any{}
		for k, v := range out.UpdatedInput {
			copied[k] = v
		}
		out.UpdatedInput = copied
	}
	return out, nil
}

func newAssistantTestHooks(t *testing.T, evaluator *assistantMutableHookEvaluator) *AssistantHookRunner {
	t.Helper()
	set, err := ParseAssistantHookDocument([]byte(`{"PreToolUse":[{"matcher":"*","hooks":[{"type":"prompt","prompt":"inspect tool input"}]}]}`), "test-hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	return NewAssistantHookRunner(AssistantHookRunnerConfig{Set: set, Prompt: evaluator})
}

// ---------------------------------------------------------------------------
// Stack: every service a restart reconstructs.

type assistantStackOptions struct {
	batch     AssistantBatchProposer
	iterative AssistantIterativeProposer
	// iterativeFactory builds a proposer over this stack's transcript store.
	iterativeFactory func(*AssistantTranscriptStore) AssistantIterativeProposer
	// runtime replaces the default test runtime (hooks/rules are then unused).
	runtime  *AssistantToolRuntime
	scope    AssistantExecutionScopeResolver
	evidence AssistantRequestEvidenceResolver
	hooks    *AssistantHookRunner
	rules    []AssistantPermissionRule
}

type assistantStack struct {
	engine     *AssistantExecutionEngine
	store      *AssistantExecutionStore
	transcript *AssistantTranscriptStore
	observer   *AssistantExecutionObserver
	reissue    chan struct{}
	cancel     context.CancelFunc
	pubkey     string
}

var assistantTestClock = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }

func newAssistantStack(t *testing.T, relay *assistantTestRelay, signer nostr.Signer, server AssistantToolRuntimeMCPServer, opts assistantStackOptions) *assistantStack {
	t.Helper()
	pk, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}
	lifecycle, cancel := context.WithCancel(context.Background())
	st := &assistantStack{cancel: cancel, pubkey: pk.Hex(), reissue: make(chan struct{})}
	st.store = NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, KeyProvider: keys, Now: assistantTestClock})
	st.transcript = NewAssistantTranscriptStore(AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, Identity: AssistantIdentity{AgentID: "assistant-test", Pubkey: pk.Hex()}, KeyProvider: keys, Now: assistantTestClock})
	st.observer = &AssistantExecutionObserver{Subscriber: relay, ReissueWait: func(ctx context.Context, _ int) error {
		select {
		case <-st.reissue:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	runtime := opts.runtime
	if runtime == nil {
		runtime = assistantTestRuntime(server, opts.hooks, opts.rules)
	}
	iterative := opts.iterative
	if opts.iterativeFactory != nil {
		iterative = opts.iterativeFactory(st.transcript)
	}
	st.engine = NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: st.store, Runtime: runtime, Observer: st.observer, Batch: opts.batch, Iterative: iterative, Transcript: st.transcript, ScopeResolver: opts.scope, Evidence: opts.evidence, Publisher: relay, Signer: signer, Subscriber: relay, Identity: AssistantIdentity{AgentID: "assistant-test", Pubkey: pk.Hex()}, Lifecycle: lifecycle, Now: assistantTestClock})
	t.Cleanup(st.crash)
	return st
}

// crash stops every goroutine of this stack without recording anything.
func (st *assistantStack) crash() {
	st.cancel()
	st.engine.Wait()
}

func (st *assistantStack) recover(t *testing.T, relay *assistantTestRelay) {
	t.Helper()
	runner := NewAssistantSessionRecoveryRunner(nil, AssistantSessionRecoveryConfig{Engine: st.engine, Store: st.store, Subscriber: relay, ServicePubkey: st.pubkey})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (st *assistantStack) faulted(sessionID string) bool {
	s := st.engine.session(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fault != nil
}

func (st *assistantStack) projection(sessionID string) domain.AssistantSessionV2 {
	s := st.engine.session(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projection
}

func (st *assistantStack) snapshot(sessionID string) domain.AssistantExecution {
	x, _ := st.engine.Snapshot(sessionID)
	return x
}

func (st *assistantStack) startBatch(t *testing.T, sessionID string) AssistantTurnResult {
	t.Helper()
	res, err := st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: sessionID, TurnID: "turn-1", Prompt: "do it"}, OperatorPubkey: "operator", RequestEventID: "prompt-" + sessionID, DefaultWorkflow: domain.AssistantWorkflowBatch})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (st *assistantStack) startIterative(t *testing.T, sessionID string) AssistantTurnResult {
	t.Helper()
	res, err := st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: sessionID, TurnID: "turn-1", Prompt: "act"}, OperatorPubkey: "operator", RequestEventID: "prompt-" + sessionID, DefaultWorkflow: domain.AssistantWorkflowIterative})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func assistantApproveUnchanged(sessionID string, res AssistantTurnResult, requestID string) AssistantTurnDecisionRequest {
	p := res.Session.Proposal
	return AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: sessionID, RunID: res.Session.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: requestID}
}

func (st *assistantStack) approve(t *testing.T, sessionID string, res AssistantTurnResult) AssistantTurnResult {
	t.Helper()
	out, err := st.engine.Decide(context.Background(), assistantApproveUnchanged(sessionID, res, "approval-"+sessionID))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assistantCancelRequest(sessionID, runID, requestID string) AssistantTurnCancellationRequest {
	return AssistantTurnCancellationRequest{Cancellation: domain.AssistantCancellationRequest{ContractVersion: 2, SessionID: sessionID, RunID: runID, Scope: "run"}, OperatorPubkey: "operator", RequestEventID: requestID}
}

func publishAssistantResult(t *testing.T, relay *assistantTestRelay, requestID, status string) *nostr.Event {
	t.Helper()
	ev := assistantSignedResultEvent(t, "result-"+requestID, 7961, requestID, status)
	if _, err := relay.Publish(context.Background(), *ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func assistantWorkState(x domain.AssistantExecution, i int) domain.AssistantWorkState {
	if i >= len(x.Work) {
		return ""
	}
	return x.Work[i].State
}

// transcriptObservations returns tool observation messages in replay order.
func (st *assistantStack) transcriptObservations(t *testing.T, sessionID string) []AssistantTranscriptRecord {
	t.Helper()
	records, err := st.transcript.Replay(context.Background(), AssistantTranscriptReplayQuery{SessionID: sessionID, Roles: []domain.AssistantAgentMessageRole{domain.AssistantAgentMessageRoleTool}})
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func assistantCheckpointRevisions(relay *assistantTestRelay, runID string) []uint64 {
	out := []uint64{}
	for _, ev := range relay.eventsOfKind(nostr.Kind(domain.AssistantExecutionCheckpointKind)) {
		if tagValue(ev.Tags, domain.AssistantCheckpointTagRun) != runID {
			continue
		}
		rev, _ := strconv.ParseUint(tagValue(ev.Tags, domain.AssistantCheckpointTagRevision), 10, 64)
		out = append(out, rev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

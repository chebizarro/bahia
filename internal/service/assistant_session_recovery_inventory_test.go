package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// recoveryProbeEngine records which runs startup recovery hands to Recover.
type recoveryProbeEngine struct {
	mu       sync.Mutex
	recover  []AssistantExecutionReference
	hydrated []string
}

func (e *recoveryProbeEngine) StartTurn(context.Context, AssistantTurnStartRequest) (AssistantTurnResult, error) {
	return AssistantTurnResult{}, errors.New("not in recovery")
}
func (e *recoveryProbeEngine) Decide(context.Context, AssistantTurnDecisionRequest) (AssistantTurnResult, error) {
	return AssistantTurnResult{}, errors.New("not in recovery")
}
func (e *recoveryProbeEngine) Cancel(context.Context, AssistantTurnCancellationRequest) (AssistantTurnResult, error) {
	return AssistantTurnResult{}, errors.New("not in recovery")
}
func (e *recoveryProbeEngine) Reconcile(context.Context, AssistantTurnReconciliationRequest) (AssistantTurnResult, error) {
	return AssistantTurnResult{}, errors.New("not in recovery")
}
func (e *recoveryProbeEngine) Recover(_ context.Context, ref AssistantExecutionReference) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.recover = append(e.recover, ref)
	return nil
}
func (e *recoveryProbeEngine) HydrateProjection(p domain.AssistantSessionV2, _ nostr.Timestamp) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hydrated = append(e.hydrated, p.SessionID)
}

// recovered returns the session ids handed to Recover, with duplicates
// counted.
func (e *recoveryProbeEngine) recovered() (map[string]int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]int{}
	for _, ref := range e.recover {
		out[ref.SessionID]++
	}
	return out, len(e.recover)
}

func (e *recoveryProbeEngine) hydratedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.hydrated)
}

// recoveryProbeStore is a checkpoint store no v2 recovery consults.
type recoveryProbeStore struct{}

func (recoveryProbeStore) Append(context.Context, domain.AssistantExecution, string) (string, error) {
	return "", errors.New("recovery must not append v2 checkpoints")
}
func (recoveryProbeStore) Load(context.Context, string, string) (AssistantCheckpointHead, error) {
	return AssistantCheckpointHead{}, ErrAssistantCheckpointNotFound
}

// assistantSessionRecord is one signed v2 session projection of the service.
func assistantSessionRecord(t *testing.T, signer nostr.Signer, sessionID string, phase domain.AssistantExecutionPhase, created nostr.Timestamp) nostr.Event {
	t.Helper()
	p := domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: sessionID, OperatorPubkey: "operator", CurrentRunID: "run-" + sessionID, Phase: phase, Workflow: domain.AssistantWorkflowBatch, ExecutionVersion: 2, CheckpointEventID: assistantTestID("checkpoint-" + sessionID).Hex()}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{Kind: nostr.Kind(domain.KindAssistantSessionState), CreatedAt: created, Tags: nostr.Tags{{"d", domain.AssistantSessionSchemaV2 + ":" + sessionID}, {domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2}, {"t", kinds.AssistantSessionTopic}, {"session", sessionID}, {"status", string(phase)}}, Content: string(raw)}
	if err := signer.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// recoveryInventoryFixture is 650 sessions: the 100 oldest are still
// executing, the newest 550 are finished, so a single 500-event REQ would
// never return a running session.
type recoveryInventoryFixture struct {
	signer  nostr.Signer
	pubkey  string
	events  []nostr.Event
	running map[string]bool
}

const (
	recoveryFixtureSessions = 650
	recoveryFixtureRunning  = 100
)

func newRecoveryInventoryFixture(t *testing.T) *recoveryInventoryFixture {
	t.Helper()
	signer := testAssistantSigner(t)
	pk, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryInventoryFixture{signer: signer, pubkey: pk.Hex(), running: map[string]bool{}}
	base := nostr.Timestamp(assistantTestClock().Unix()) - recoveryFixtureSessions*2
	for i := 0; i < recoveryFixtureSessions; i++ {
		id := fmt.Sprintf("s-%04d", i)
		phase := domain.AssistantExecutionCompleted
		if i < recoveryFixtureRunning {
			phase = domain.AssistantExecutionExecuting
			f.running[id] = true
		}
		f.events = append(f.events, assistantSessionRecord(t, signer, id, phase, base+nostr.Timestamp(i)))
	}
	// A running session whose projection was replaced: the local store and
	// the relay both keep only the newest version.
	f.events = append(f.events, assistantSessionRecord(t, signer, "s-0000", domain.AssistantExecutionExecuting, base+1))
	return f
}

func (f *recoveryInventoryFixture) assertRecovered(t *testing.T, engine *recoveryProbeEngine, passes int) {
	t.Helper()
	recovered, total := engine.recovered()
	if total != passes*recoveryFixtureRunning {
		t.Fatalf("Recover calls = %d, want %d running sessions x %d passes", total, recoveryFixtureRunning, passes)
	}
	for id := range f.running {
		if recovered[id] != passes {
			t.Fatalf("running session %s recovered %d times, want %d", id, recovered[id], passes)
		}
	}
	for id := range recovered {
		if !f.running[id] {
			t.Fatalf("finished session %s was re-run", id)
		}
	}
	if n := engine.hydratedCount(); n != passes*recoveryFixtureSessions {
		t.Fatalf("hydrated %d projections, want %d", n, passes*recoveryFixtureSessions)
	}
}

func TestAssistantRecoveryEnumeratesEverySessionFromLocalStore(t *testing.T) {
	f := newRecoveryInventoryFixture(t)
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, ev := range f.events {
		if _, err := store.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	engine := &recoveryProbeEngine{}
	gate := newReadinessGate()
	// The relay would answer, but a configured local store is the inventory.
	relay := newAssistantTestRelay()
	runner := NewAssistantSessionRecoveryRunner(nil, AssistantSessionRecoveryConfig{PageLimit: 500, Engine: engine, Store: recoveryProbeStore{}, Subscriber: relay, LocalStore: store, Readiness: gate, ServicePubkey: f.pubkey})

	// Recovery waits for the first relay catch-up before reading the store.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, total := engine.recovered(); total != 0 {
		t.Fatalf("recovered %d sessions before the local store was synced", total)
	}
	gate.open()
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertRecovered(t, engine, 1)
	if relay.liveSubs(nostr.Kind(domain.KindAssistantSessionState)) != 0 {
		t.Fatal("local-store recovery opened a relay subscription")
	}
	// A second pass is deterministic and idempotent: the same running
	// sessions, once more each, nothing finished re-run.
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertRecovered(t, engine, 2)
}

func TestAssistantRecoveryPagesRelayInventoryToCompletion(t *testing.T) {
	f := newRecoveryInventoryFixture(t)
	relay := newAssistantTestRelay()
	for _, ev := range f.events {
		if _, err := relay.Publish(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	engine := &recoveryProbeEngine{}
	runner := NewAssistantSessionRecoveryRunner(nil, AssistantSessionRecoveryConfig{PageLimit: 500, Engine: engine, Store: recoveryProbeStore{}, Subscriber: relay, ServicePubkey: f.pubkey})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertRecovered(t, engine, 1)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertRecovered(t, engine, 2)
}

// A page whose events all share one created_at cannot be paged past with
// until; recovery parks instead of treating the truncated inventory as
// complete.
func TestAssistantRecoveryParksWhenRelayPageCannotAdvance(t *testing.T) {
	signer := testAssistantSigner(t)
	pk, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	relay := newAssistantTestRelay()
	at := nostr.Timestamp(assistantTestClock().Unix())
	for i := 0; i < 6; i++ {
		if _, err := relay.Publish(context.Background(), assistantSessionRecord(t, signer, fmt.Sprintf("same-%d", i), domain.AssistantExecutionExecuting, at)); err != nil {
			t.Fatal(err)
		}
	}
	engine := &recoveryProbeEngine{}
	runner := NewAssistantSessionRecoveryRunner(nil, AssistantSessionRecoveryConfig{PageLimit: 3, Engine: engine, Store: recoveryProbeStore{}, Subscriber: relay, ServicePubkey: pk.Hex()})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, total := engine.recovered(); total != 0 {
		t.Fatalf("recovered %d sessions from an inventory that could not be completed", total)
	}
}

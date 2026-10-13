package service

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func checkpointTestExecution(rev uint64) domain.AssistantExecution {
	return domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: "session-chain", RunID: "run-chain", TurnID: "turn", RequestID: "request", Workflow: domain.AssistantWorkflowBatch, Revision: rev, Phase: domain.AssistantExecutionExecuting, Work: []domain.AssistantWorkItem{{WorkID: "work", ToolName: "secret-tool", Arguments: map[string]any{"password": "private-checkpoint-secret"}, State: domain.AssistantWorkReady}}}
}

func TestAssistantExecutionStoreEncryptedChainIgnoresArrivalAndTimestamp(t *testing.T) {
	pub := &assistantTestPublisher{}
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: testAssistantSigner(t), KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}, Now: func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) }})
	previous := ""
	for rev := uint64(1); rev <= 3; rev++ {
		var err error
		previous, err = store.Append(context.Background(), checkpointTestExecution(rev), previous)
		if err != nil {
			t.Fatal(err)
		}
	}
	events := pub.eventsOfKind(domain.AssistantExecutionCheckpointKind)
	if len(events) != 3 {
		t.Fatalf("events=%d", len(events))
	}
	for _, ev := range events {
		if strings.Contains(ev.Content, "private-checkpoint-secret") || tagValue(ev.Tags, "d") != "" {
			t.Fatalf("checkpoint leaked private work or used d tag: %+v", ev)
		}
	}
	store.subscriber = newReplayTranscriptSubscriber([]nostr.Event{events[2], events[0], events[1], events[2]})
	head, err := store.Load(context.Background(), "session-chain", "run-chain")
	if err != nil {
		t.Fatal(err)
	}
	if head.Execution.Revision != 3 || head.EventID != events[2].ID.Hex() || len(head.Chain) != 3 || !head.Contains(events[0].ID.Hex()) || head.Contains("unknown") {
		t.Fatalf("head=%d id=%s chain=%v", head.Execution.Revision, head.EventID, head.Chain)
	}
}

func TestAssistantCheckpointChainRejectsForkGapAndDisconnectedRoot(t *testing.T) {
	node := func(id, prev string, rev uint64) assistantCheckpointNode {
		return assistantCheckpointNode{id: id, cp: domain.AssistantExecutionCheckpoint{Execution: checkpointTestExecution(rev), PreviousEventID: prev}}
	}
	cases := map[string][]assistantCheckpointNode{
		"fork":         {node("a", "", 1), node("b", "a", 2), node("c", "a", 2)},
		"gap":          {node("a", "", 1), node("b", "a", 3)},
		"disconnected": {node("a", "", 1), node("b", "unknown", 2)},
		"two roots":    {node("a", "", 1), node("b", "", 1)},
	}
	for name, nodes := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newestAssistantCheckpoint(nodes); err == nil {
				t.Fatal("accepted ambiguous chain")
			}
		})
	}
}

type assistantRejectingCheckpointPublisher struct {
	events []nostr.Event
	reject bool
}

func (p *assistantRejectingCheckpointPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.events = append(p.events, ev)
	if p.reject {
		return 0, errors.New("relay rejected")
	}
	return 1, nil
}

func TestAssistantCheckpointPublicationRetriesSameSignedEvent(t *testing.T) {
	pub := &assistantRejectingCheckpointPublisher{reject: true}
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: testAssistantSigner(t), KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}})
	x := checkpointTestExecution(1)
	if _, err := store.Append(context.Background(), x, ""); err == nil {
		t.Fatal("rejected checkpoint was committed")
	}
	pub.reject = false
	id, err := store.Append(context.Background(), x, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 2 || pub.events[0].ID != pub.events[1].ID || id != pub.events[0].ID.Hex() {
		t.Fatal("retry did not use the same signed event")
	}
}

func TestAssistantCheckpointRetryWithDifferentPayloadIsRefused(t *testing.T) {
	pub := &assistantRejectingCheckpointPublisher{reject: true}
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: testAssistantSigner(t), KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}})
	x := checkpointTestExecution(1)
	if _, err := store.Append(context.Background(), x, ""); err == nil {
		t.Fatal("rejected checkpoint was committed")
	}
	pub.reject = false
	different := checkpointTestExecution(1)
	different.Phase = domain.AssistantExecutionCancelled
	if _, err := store.Append(context.Background(), different, ""); !errors.Is(err, ErrAssistantCheckpointConflict) {
		t.Fatalf("a different logical checkpoint replaced an unconfirmed one: %v", err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("conflicting checkpoint was published: %d", len(pub.events))
	}
	if _, err := store.Append(context.Background(), x, ""); err != nil {
		t.Fatalf("identical retry refused: %v", err)
	}
}

func TestAssistantCheckpointLoadNotFoundAndForgedAuthor(t *testing.T) {
	signer := testAssistantSigner(t)
	pub := &assistantTestPublisher{}
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: signer, KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}})
	store.subscriber = newReplayTranscriptSubscriber(nil)
	if _, err := store.Load(context.Background(), "session-chain", "run-chain"); !errors.Is(err, ErrAssistantCheckpointNotFound) {
		t.Fatalf("empty backfill: %v", err)
	}
	// A checkpoint signed by another key with identical tags parks the run.
	forger := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: testAssistantSigner(t), KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}})
	if _, err := forger.Append(context.Background(), checkpointTestExecution(1), ""); err != nil {
		t.Fatal(err)
	}
	store.subscriber = newReplayTranscriptSubscriber(pub.eventsOfKind(domain.AssistantExecutionCheckpointKind))
	if _, err := store.Load(context.Background(), "session-chain", "run-chain"); err == nil || !strings.Contains(err.Error(), "author") {
		t.Fatalf("forged checkpoint accepted: %v", err)
	}
}

func TestAssistantCheckpointPreservesNilVersusEmptyAllowedTools(t *testing.T) {
	for name, allowed := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			pub := &assistantTestPublisher{}
			store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: testAssistantSigner(t), KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}})
			x := checkpointTestExecution(1)
			x.Scope.AllowedTools = allowed
			if _, err := store.Append(context.Background(), x, ""); err != nil {
				t.Fatal(err)
			}
			store.subscriber = newReplayTranscriptSubscriber(pub.eventsOfKind(domain.AssistantExecutionCheckpointKind))
			head, err := store.Load(context.Background(), "session-chain", "run-chain")
			if err != nil {
				t.Fatal(err)
			}
			got := head.Execution.Scope.AllowedTools
			if (allowed == nil) != (got == nil) || len(got) != 0 {
				t.Fatalf("allowed_tools %v round-tripped as %#v", allowed, got)
			}
		})
	}
}

func TestAssistantCheckpointLoadUsesIndexedSessionTopic(t *testing.T) {
	signer := testAssistantSigner(t)
	relay := newAssistantTestRelay()
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}})
	root, err := store.Append(context.Background(), checkpointTestExecution(1), "")
	if err != nil {
		t.Fatal(err)
	}
	other := checkpointTestExecution(1)
	other.SessionID = "other-session"
	if _, err := store.Append(context.Background(), other, ""); err != nil {
		t.Fatal(err)
	}
	head, err := store.Load(context.Background(), "session-chain", "run-chain")
	if err != nil || head.EventID != root {
		t.Fatalf("head = %+v, %v", head, err)
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.nextSub != 1 {
		t.Fatalf("opened %d REQs for indexed history", relay.nextSub)
	}
	for _, sub := range relay.subs {
		for _, filter := range sub.filters {
			if len(filter.Authors) != 1 || len(filter.Kinds) != 1 || filter.Kinds[0] != nostr.Kind(domain.AssistantExecutionCheckpointKind) {
				t.Fatalf("unscoped checkpoint filter: %+v", filter)
			}
			if got := filter.Tags["t"]; len(got) != 1 || got[0] != kinds.AssistantCheckpointSessionTopic("session-chain") {
				t.Fatalf("session topic = %v", got)
			}
			for tag := range filter.Tags {
				if len(tag) != 1 {
					t.Fatalf("non-indexed REQ tag %q", tag)
				}
			}
		}
	}
}

func TestAssistantCheckpointLoadFallsBackForPreTopicEvents(t *testing.T) {
	signer := testAssistantSigner(t)
	relay := newAssistantTestRelay()
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}})
	previous := ""
	for rev := uint64(1); rev <= 2; rev++ {
		execution := checkpointTestExecution(rev)
		plain, err := json.Marshal(domain.AssistantExecutionCheckpoint{Execution: execution, PreviousEventID: previous})
		if err != nil {
			t.Fatal(err)
		}
		ev, err := store.sealCheckpoint(context.Background(), execution, previous, plain)
		if err != nil {
			t.Fatal(err)
		}
		var tags nostr.Tags
		for _, tag := range ev.Tags {
			if len(tag) == 0 || tag[0] != "t" {
				tags = append(tags, tag)
			}
		}
		ev.Tags = tags
		if err := signGoNostrEvent(context.Background(), signer, &ev); err != nil {
			t.Fatal(err)
		}
		if _, err := relay.Publish(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
		previous = ev.ID.Hex()
	}
	other := checkpointTestExecution(1)
	other.SessionID = "another-session"
	if _, err := store.Append(context.Background(), other, ""); err != nil {
		t.Fatal(err)
	}
	head, err := store.Load(context.Background(), "session-chain", "run-chain")
	if err != nil || head.EventID != previous || len(head.Chain) != 2 {
		t.Fatalf("legacy head = %+v, %v", head, err)
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.nextSub != 2 {
		t.Fatalf("legacy load opened %d REQs, want scoped then author/kind", relay.nextSub)
	}
	for _, sub := range relay.subs {
		for _, filter := range sub.filters {
			for tag := range filter.Tags {
				if len(tag) != 1 {
					t.Fatalf("non-indexed REQ tag %q", tag)
				}
			}
		}
	}
}

type assistantRetentionProbe struct {
	fail     bool
	markFail bool
	event    *nostr.Event
	accepted bool
}

func (p *assistantRetentionProbe) SaveEvent(ev nostr.Event) (bool, error) {
	if p.fail {
		return false, errors.New("bbolt write failed")
	}
	p.event = &ev
	return true, nil
}

func (p *assistantRetentionProbe) MarkAssistantCheckpointAccepted(id nostr.ID) error {
	if p.markFail {
		return errors.New("bbolt acceptance marker failed")
	}
	if p.event == nil || p.event.ID != id {
		return errors.New("checkpoint not retained")
	}
	p.accepted = true
	return nil
}

func (p *assistantRetentionProbe) AssistantCheckpointAccepted(id nostr.ID) (bool, error) {
	return p.accepted && p.event != nil && p.event.ID == id, nil
}

func (p *assistantRetentionProbe) QueryEvents(filter nostr.Filter) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		if p.event != nil && filter.Matches(*p.event) {
			yield(*p.event)
		}
	}
}

func TestAssistantCheckpointLocalRetentionFailureKeepsSignedPendingEvent(t *testing.T) {
	for _, failMark := range []bool{false, true} {
		name := "event_write"
		if failMark {
			name = "acceptance_marker"
		}
		t.Run(name, func(t *testing.T) {
			publisher := &assistantTestPublisher{}
			local := &assistantRetentionProbe{fail: !failMark, markFail: failMark}
			store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: publisher, Signer: testAssistantSigner(t), KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}, LocalStore: local})
			execution := checkpointTestExecution(1)
			if _, err := store.Append(context.Background(), execution, ""); err == nil {
				t.Fatal("local retention failure committed checkpoint")
			}
			first := publisher.eventsOfKind(domain.AssistantExecutionCheckpointKind)
			if len(first) != 1 {
				t.Fatalf("published %d events", len(first))
			}
			local.fail, local.markFail = false, false
			id, err := store.Append(context.Background(), execution, "")
			if err != nil {
				t.Fatal(err)
			}
			second := publisher.eventsOfKind(domain.AssistantExecutionCheckpointKind)
			if len(second) != 2 || second[1].ID != first[0].ID || id != first[0].ID.Hex() || !local.accepted {
				t.Fatal("retry changed the signed event or failed to retain its acceptance")
			}
		})
	}
}

func TestAssistantCheckpointLoadIgnoresPreOKLocalCacheCopy(t *testing.T) {
	local, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	signer := testAssistantSigner(t)
	relay := newAssistantTestRelay()
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}, LocalStore: local})
	execution := checkpointTestExecution(1)
	plain, err := json.Marshal(domain.AssistantExecutionCheckpoint{Execution: execution})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := store.sealCheckpoint(context.Background(), execution, "", plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.SaveEvent(ev); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), execution.SessionID, execution.RunID); !errors.Is(err, ErrAssistantCheckpointNotFound) {
		t.Fatalf("pre-OK local event used as authoritative checkpoint: %v", err)
	}
}

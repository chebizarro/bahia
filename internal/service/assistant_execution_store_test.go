package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
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
	latest, id, err := store.Load(context.Background(), "session-chain", "run-chain")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision != 3 || id != events[2].ID.Hex() {
		t.Fatalf("latest=%d id=%s", latest.Revision, id)
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
			if _, _, err := newestAssistantCheckpoint(nodes); err == nil {
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

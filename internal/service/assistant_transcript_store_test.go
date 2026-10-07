package service

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestAssistantTranscriptStorePublishesEncrypted30316Envelope(t *testing.T) {
	publisher := &assistantTestPublisher{}
	store := newTestAssistantTranscriptStore(t, publisher, nil)

	record, err := store.AppendMessage(context.Background(), AssistantTranscriptAppend{
		SessionID:      "session-1",
		TurnID:         "turn-1",
		Sequence:       1,
		OperatorPubkey: strings.Repeat("a", 64),
		Message:        textAssistantMessage(domain.AssistantAgentMessageRoleUser, "deploy the api service"),
	})
	if err != nil {
		t.Fatalf("AppendMessage returned error: %v", err)
	}
	if record.EventID == "" || record.Payload.Sequence != 1 {
		t.Fatalf("unexpected append record: %+v", record)
	}

	events := publisher.eventsOfKind(domain.KindAssistantTranscript)
	if len(events) != 1 {
		t.Fatalf("expected one transcript event, got %d", len(events))
	}
	ev := events[0]
	if ev.Kind != nostr.Kind(domain.KindAssistantTranscript) {
		t.Fatalf("event kind = %d", ev.Kind)
	}
	if strings.Contains(ev.Content, "deploy the api service") {
		t.Fatalf("transcript plaintext leaked into encrypted content: %s", ev.Content)
	}
	for _, want := range [][2]string{
		{domain.AssistantTranscriptTagSchema, domain.AssistantTranscriptSchema},
		{domain.AssistantTranscriptTagDomain, domain.AssistantDomain},
		{domain.AssistantTranscriptTagSession, "session-1"},
		{domain.AssistantTranscriptTagTurn, "turn-1"},
		{domain.AssistantTranscriptTagRole, string(domain.AssistantAgentMessageRoleUser)},
		{domain.AssistantTranscriptTagSequence, "1"},
		{domain.AssistantTranscriptTagEnvelope, domain.AssistantTranscriptEnvelopeServiceHeldAEAD},
	} {
		if got := tagValue(ev.Tags, want[0]); got != want[1] {
			t.Fatalf("tag %s = %q, want %q", want[0], got, want[1])
		}
	}
	topics := 0
	for range ev.Tags.FindAll("t") {
		topics++
	}
	if topics != 2 {
		t.Fatalf("transcript t topics = %v, want family and session topics", ev.Tags)
	}
	for _, topic := range []string{kinds.AssistantTranscriptTopic, kinds.AssistantTranscriptSessionTopic("session-1")} {
		if !ev.Tags.ContainsAny("t", []string{topic}) {
			t.Fatalf("transcript lacks t=%s: %v", topic, ev.Tags)
		}
	}
	var envelope domain.AssistantTranscriptAEADEnvelope
	mustUnmarshalEventContent(t, &ev, &envelope)
	if envelope.Envelope != domain.AssistantTranscriptEnvelopeServiceHeldAEAD || envelope.Algorithm != domain.AssistantTranscriptAEADAlgorithmXChaCha20 || envelope.KeyRef != testAssistantTranscriptKey().Ref {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
}

func TestAssistantTranscriptStoreReplayOrdersAndDedupes(t *testing.T) {
	publisher := &assistantTestPublisher{}
	store := newTestAssistantTranscriptStore(t, publisher, nil)
	appendTranscriptForTest(t, store, "session-1", "turn-1", 2, domain.AssistantAgentMessageRoleAssistant, "second")
	appendTranscriptForTest(t, store, "session-1", "turn-1", 1, domain.AssistantAgentMessageRoleUser, "first")

	published := publisher.eventsOfKind(domain.KindAssistantTranscript)
	if len(published) != 2 {
		t.Fatalf("published events = %d", len(published))
	}
	subscriber := newReplayTranscriptSubscriber([]nostr.Event{published[0], published[1], published[1]})
	freshStore := newTestAssistantTranscriptStore(t, nil, subscriber)

	records, err := freshStore.Replay(context.Background(), AssistantTranscriptReplayQuery{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 deduped records, got %d", len(records))
	}
	if records[0].Payload.Sequence != 1 || messageText(records[0].Payload.Message) != "first" {
		t.Fatalf("first replay record = %+v", records[0].Payload)
	}
	if records[1].Payload.Sequence != 2 || messageText(records[1].Payload.Message) != "second" {
		t.Fatalf("second replay record = %+v", records[1].Payload)
	}
	// The replay REQ scopes the session by its single-letter topic only;
	// relays do not index #schema/#domain/#session.
	filter := subscriber.lastFilter(t)
	for key := range filter.Tags {
		if len(key) != 1 {
			t.Fatalf("replay filter sends multi-letter tag #%s: %#v", key, filter.Tags)
		}
	}
	if got := filter.Tags["t"]; len(got) != 1 || got[0] != kinds.AssistantTranscriptSessionTopic("session-1") {
		t.Fatalf("replay filter #t = %#v, want [%s]", got, kinds.AssistantTranscriptSessionTopic("session-1"))
	}
	for _, ev := range published {
		if !filter.Matches(ev) {
			t.Fatalf("producer-shaped transcript event does not match the replay filter: tags=%v", ev.Tags)
		}
	}
	appendTranscriptForTest(t, store, "session-2", "turn-1", 1, domain.AssistantAgentMessageRoleUser, "other session")
	other := publisher.eventsOfKind(domain.KindAssistantTranscript)
	if filter.Matches(other[len(other)-1]) {
		t.Fatalf("another session's transcript matches the session-1 replay filter: tags=%v", other[len(other)-1].Tags)
	}
}

func TestAssistantTranscriptStoreReplayTruncatesToMostRecentMessages(t *testing.T) {
	publisher := &assistantTestPublisher{}
	store := newTestAssistantTranscriptStore(t, publisher, nil)
	appendTranscriptForTest(t, store, "session-1", "turn-1", 1, domain.AssistantAgentMessageRoleUser, "one")
	appendTranscriptForTest(t, store, "session-1", "turn-1", 2, domain.AssistantAgentMessageRoleAssistant, "two")
	appendTranscriptForTest(t, store, "session-1", "turn-2", 3, domain.AssistantAgentMessageRoleUser, "three")

	subscriber := newReplayTranscriptSubscriber(publisher.eventsOfKind(domain.KindAssistantTranscript))
	freshStore := newTestAssistantTranscriptStore(t, nil, subscriber)
	records, err := freshStore.Replay(context.Background(), AssistantTranscriptReplayQuery{SessionID: "session-1", Limit: 2})
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 truncated records, got %d", len(records))
	}
	if got := []string{messageText(records[0].Payload.Message), messageText(records[1].Payload.Message)}; got[0] != "two" || got[1] != "three" {
		t.Fatalf("truncated history = %#v, want two/three", got)
	}
}

func TestAssistantTranscriptStoreDecryptsWithServiceHeldKeyOnFreshSession(t *testing.T) {
	publisher := &assistantTestPublisher{}
	firstStore := newTestAssistantTranscriptStore(t, publisher, nil)
	appendTranscriptForTest(t, firstStore, "fresh-session", "turn-1", 1, domain.AssistantAgentMessageRoleUser, "remember me")

	freshSubscriber := newReplayTranscriptSubscriber(publisher.eventsOfKind(domain.KindAssistantTranscript))
	freshStore := newTestAssistantTranscriptStore(t, nil, freshSubscriber)
	history, err := freshStore.BuildModelHistory(context.Background(), "fresh-session", 20)
	if err != nil {
		t.Fatalf("BuildModelHistory returned error: %v", err)
	}
	if len(history) != 1 || history[0].Role != domain.AssistantAgentMessageRoleUser || messageText(history[0]) != "remember me" {
		t.Fatalf("fresh-session history = %+v", history)
	}
}

func TestAssistantContextBuilderUsesTranscriptReplayForModelHistory(t *testing.T) {
	history := &assistantTranscriptHistoryFixture{messages: []domain.AssistantAgentMessage{
		textAssistantMessage(domain.AssistantAgentMessageRoleUser, "first turn"),
		textAssistantMessage(domain.AssistantAgentMessageRoleAssistant, "first answer"),
	}}
	builder := NewAssistantContextBuilder(nil, nil, nil, nil, nil, nil, AssistantContextBuilderConfig{TranscriptHistory: history, TranscriptLimit: 2})

	messages, err := builder.BuildModelHistory(context.Background(), "session-1", map[string]string{"session_id": "session-1"}, nil, "next question")
	if err != nil {
		t.Fatalf("BuildModelHistory returned error: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("model history length = %d, want system + 2 history + current", len(messages))
	}
	if messages[0].Role != domain.AssistantAgentMessageRoleSystem || messages[1].Role != domain.AssistantAgentMessageRoleUser || messages[2].Role != domain.AssistantAgentMessageRoleAssistant || messages[3].Role != domain.AssistantAgentMessageRoleUser {
		t.Fatalf("unexpected model history roles: %+v", messages)
	}
	if history.lastSessionID != "session-1" || history.lastLimit != 2 {
		t.Fatalf("history replay args session=%q limit=%d", history.lastSessionID, history.lastLimit)
	}
	if strings.Contains(messageText(messages[0]), "first turn") {
		t.Fatalf("system context duplicated transcript history: %s", messageText(messages[0]))
	}

	contextBlock, err := builder.BuildContext(context.Background(), map[string]string{"session_id": "session-1"}, nil, "old summary should not be used")
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}
	if !strings.Contains(contextBlock, "Transcript History") || !strings.Contains(contextBlock, "first answer") {
		t.Fatalf("context block did not include replayed transcript history:\n%s", contextBlock)
	}
	if strings.Contains(contextBlock, "old summary should not be used") || strings.Contains(contextBlock, "Transcript Summary") {
		t.Fatalf("context block used legacy TranscriptSummary despite replay provider:\n%s", contextBlock)
	}
}

func newTestAssistantTranscriptStore(t *testing.T, publisher AssistantEventPublisher, subscriber AssistantRelaySubscriber) *AssistantTranscriptStore {
	t.Helper()
	return NewAssistantTranscriptStore(AssistantTranscriptStoreConfig{
		Publisher:   publisher,
		Subscriber:  subscriber,
		Signer:      testAssistantSigner(t),
		Identity:    AssistantIdentity{AgentID: "assistant-test"},
		KeyProvider: StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()},
		Now:         func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) },
	})
}

func testAssistantTranscriptKey() AssistantTranscriptKey {
	return AssistantTranscriptKey{
		Ref:      "assistant-transcript/default",
		Version:  "v1",
		Rotation: "2026-07",
		Key:      []byte("0123456789abcdef0123456789abcdef"),
	}
}

func appendTranscriptForTest(t *testing.T, store *AssistantTranscriptStore, sessionID, turnID string, seq int, role domain.AssistantAgentMessageRole, text string) {
	t.Helper()
	_, err := store.AppendMessage(context.Background(), AssistantTranscriptAppend{SessionID: sessionID, TurnID: turnID, Sequence: seq, Message: textAssistantMessage(role, text)})
	if err != nil {
		t.Fatalf("append transcript seq=%d: %v", seq, err)
	}
}

func textAssistantMessage(role domain.AssistantAgentMessageRole, text string) domain.AssistantAgentMessage {
	return domain.AssistantAgentMessage{
		Role: role,
		Content: []domain.AssistantAgentContentBlock{{
			Type: domain.AssistantAgentContentText,
			Text: text,
		}},
	}
}

func messageText(message domain.AssistantAgentMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		if block.Type == domain.AssistantAgentContentText {
			parts = append(parts, block.Text)
		}
		if block.Type == domain.AssistantAgentContentJSON && len(block.JSON) > 0 {
			encoded, _ := json.Marshal(block.JSON)
			parts = append(parts, string(encoded))
		}
	}
	return strings.Join(parts, " ")
}

type replayTranscriptSubscriber struct {
	mu      sync.Mutex
	events  []nostr.Event
	filters []nostr.Filter
}

func newReplayTranscriptSubscriber(events []nostr.Event) *replayTranscriptSubscriber {
	return &replayTranscriptSubscriber{events: append([]nostr.Event(nil), events...)}
}

func (s *replayTranscriptSubscriber) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (AssistantMergedSubscription, error) {
	s.mu.Lock()
	s.filters = append(s.filters, filters...)
	events := append([]nostr.Event(nil), s.events...)
	s.mu.Unlock()

	sub := &replayTranscriptMergedSubscription{events: make(chan *nostr.Event), closed: make(chan AssistantRelayClosed), eose: make(chan struct{})}
	go func() {
		defer close(sub.events)
		for i := range events {
			ev := events[i]
			sub.events <- &ev
		}
		close(sub.eose)
		close(sub.closed)
	}()
	return sub, nil
}

func (s *replayTranscriptSubscriber) lastFilter(t *testing.T) nostr.Filter {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.filters) == 0 {
		t.Fatal("no subscription filters captured")
	}
	return s.filters[len(s.filters)-1]
}

type replayTranscriptMergedSubscription struct {
	events chan *nostr.Event
	closed chan AssistantRelayClosed
	eose   chan struct{}
	once   sync.Once
}

func (s *replayTranscriptMergedSubscription) EventChan() <-chan *nostr.Event { return s.events }
func (s *replayTranscriptMergedSubscription) ClosedChan() <-chan AssistantRelayClosed {
	return s.closed
}
func (s *replayTranscriptMergedSubscription) EOSEChan() <-chan struct{} { return s.eose }
func (s *replayTranscriptMergedSubscription) Close()                    { s.once.Do(func() {}) }

type assistantTranscriptHistoryFixture struct {
	messages      []domain.AssistantAgentMessage
	lastSessionID string
	lastLimit     int
}

func (f *assistantTranscriptHistoryFixture) BuildModelHistory(_ context.Context, sessionID string, limit int) ([]domain.AssistantAgentMessage, error) {
	f.lastSessionID = sessionID
	f.lastLimit = limit
	out := append([]domain.AssistantAgentMessage(nil), f.messages...)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func TestAssistantTranscriptLogicalObservationIdentityDedupesReplay(t *testing.T) {
	pub := &assistantTestPublisher{}
	store := newTestAssistantTranscriptStore(t, pub, nil)
	req := AssistantTranscriptAppend{SessionID: "logical-session", TurnID: "turn", RunID: "run", Sequence: 1, LogicalID: "run:work:observation", Message: textAssistantMessage(domain.AssistantAgentMessageRoleTool, "observed")}
	if _, err := store.AppendMessage(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	events := pub.eventsOfKind(domain.KindAssistantTranscript)
	if len(events) != 2 || tagValue(events[0].Tags, "d") != tagValue(events[1].Tags, "d") {
		t.Fatal("logical identity was not stable")
	}
	replay := newTestAssistantTranscriptStore(t, nil, newReplayTranscriptSubscriber(events))
	records, err := replay.Replay(context.Background(), AssistantTranscriptReplayQuery{SessionID: "logical-session"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("replayed logical observations=%d", len(records))
	}
}

func TestAssistantTranscriptAppendOnceIsIdempotentAndDedupeIsOrderIndependent(t *testing.T) {
	relay := newAssistantTestRelay()
	store := newTestAssistantTranscriptStore(t, relay, relay)
	appendTranscriptForTest(t, store, "once", "turn", 0, domain.AssistantAgentMessageRoleUser, "hello")
	req := AssistantTranscriptAppend{SessionID: "once", TurnID: "turn", RunID: "run", LogicalID: "run:work:observation", Message: textAssistantMessage(domain.AssistantAgentMessageRoleTool, "observed")}
	first, err := store.AppendMessageOnce(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AppendMessageOnce(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Payload.Sequence != 1 || second.EventID != first.EventID || len(relay.published(nostr.Kind(domain.KindAssistantTranscript))) != 2 {
		t.Fatalf("append once: first=%+v second=%+v", first.Payload.Sequence, second.EventID)
	}
	a := AssistantTranscriptRecord{EventID: "b", Payload: domain.AssistantTranscriptPayload{Sequence: 3, Metadata: map[string]any{"logical_id": "x"}}}
	b := AssistantTranscriptRecord{EventID: "a", Payload: domain.AssistantTranscriptPayload{Sequence: 5, Metadata: map[string]any{"logical_id": "x"}}}
	left := dedupeAssistantLogicalRecords([]AssistantTranscriptRecord{a, b})
	right := dedupeAssistantLogicalRecords([]AssistantTranscriptRecord{b, a})
	if len(left) != 1 || len(right) != 1 || left[0].EventID != right[0].EventID || left[0].Payload.Sequence != 3 {
		t.Fatalf("dedupe depends on arrival order: %+v %+v", left, right)
	}
}

// A retried publish of one message targets the same addressable coordinate,
// so the relay keeps a single copy: the d tag is derived from
// (session, logical id), or (session, sequence) without a logical id, and
// never from a random value.
func TestAssistantTranscriptRetryReusesDeterministicCoordinate(t *testing.T) {
	pub := &assistantTestPublisher{}
	store := newTestAssistantTranscriptStore(t, pub, nil)
	ctx := context.Background()

	logical := AssistantTranscriptAppend{SessionID: "s1", TurnID: "t1", Sequence: 4, LogicalID: "msg-7", Message: textAssistantMessage(domain.AssistantAgentMessageRoleAssistant, "done")}
	if _, err := store.AppendMessage(ctx, logical); err != nil {
		t.Fatal(err)
	}
	// A retry through AppendMessageOnce may compute a different sequence.
	logical.Sequence = 5
	if _, err := store.AppendMessage(ctx, logical); err != nil {
		t.Fatal(err)
	}
	unkeyed := AssistantTranscriptAppend{SessionID: "s1", TurnID: "t1", Sequence: 9, Message: textAssistantMessage(domain.AssistantAgentMessageRoleUser, "hi")}
	for i := 0; i < 2; i++ {
		if _, err := store.AppendMessage(ctx, unkeyed); err != nil {
			t.Fatal(err)
		}
	}

	events := pub.eventsOfKind(domain.KindAssistantTranscript)
	if len(events) != 4 {
		t.Fatalf("published %d events, want 4", len(events))
	}
	wantLogical := domain.AssistantTranscriptDTagPrefix + "s1:msg:msg-7"
	wantSeq := domain.AssistantTranscriptDTagPrefix + "s1:seq:00000000000000000009"
	for i, want := range []string{wantLogical, wantLogical, wantSeq, wantSeq} {
		if got := tagValue(events[i].Tags, "d"); got != want {
			t.Fatalf("event %d d = %q, want %q", i, got, want)
		}
	}

	// Replay of every copy (a relay that has not applied the replacement yet)
	// still yields one record per message.
	replay := newTestAssistantTranscriptStore(t, nil, newReplayTranscriptSubscriber(events))
	records, err := replay.Replay(ctx, AssistantTranscriptReplayQuery{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("replayed %d records, want 2", len(records))
	}
}

func TestAssistantTranscriptCoordinateDedupeKeepsNIP01Winner(t *testing.T) {
	now := time.Unix(1710000000, 0).UTC()
	older := AssistantTranscriptRecord{EventID: "a", DTag: "x", CreatedAt: now}
	newer := AssistantTranscriptRecord{EventID: "c", DTag: "x", CreatedAt: now.Add(time.Second)}
	tie := AssistantTranscriptRecord{EventID: "b", DTag: "x", CreatedAt: now.Add(time.Second)}
	for _, order := range [][]AssistantTranscriptRecord{{older, newer, tie}, {tie, newer, older}} {
		got := dedupeAssistantCoordinateRecords(order)
		if len(got) != 1 || got[0].EventID != "b" {
			t.Fatalf("coordinate dedupe of %v = %+v, want event b", order, got)
		}
	}
}

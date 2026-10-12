package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// legacyHistory is an assistant history sealed with the nsec-derived v1 key
// and signed by the retired identity: one checkpoint chain of three
// revisions, two transcript messages, the session's v2 projection pointing at
// the chain head, and a v1-only projection of another session.
type legacyHistory struct {
	oldSigner nostr.Signer
	oldAuthor nostr.PubKey
	oldKeys   StaticAssistantTranscriptKeyProvider
	events    []nostr.Event
	chain     []string
}

func buildLegacyHistory(t *testing.T) legacyHistory {
	t.Helper()
	ctx := context.Background()
	h := legacyHistory{oldSigner: testAssistantSigner(t), oldKeys: StaticAssistantTranscriptKeyProvider{Key: AssistantTranscriptKey{
		Ref: "assistant-transcript/service-nostr-key", Version: "v1", Rotation: "service-nostr-key", Key: []byte("legacy-nsec-derived-key-32-bytes"),
	}}}
	h.oldAuthor, _ = h.oldSigner.GetPublicKey(ctx)
	pub := &assistantTestPublisher{}
	checkpoints := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: h.oldSigner, KeyProvider: h.oldKeys})
	previous := ""
	for rev := uint64(1); rev <= 3; rev++ {
		id, err := checkpoints.Append(ctx, checkpointTestExecution(rev), previous)
		if err != nil {
			t.Fatal(err)
		}
		h.chain = append(h.chain, id)
		previous = id
	}
	transcripts := NewAssistantTranscriptStore(AssistantTranscriptStoreConfig{Publisher: pub, Signer: h.oldSigner, Identity: AssistantIdentity{AgentID: "assistant"}, KeyProvider: h.oldKeys, ServicePubkey: h.oldAuthor.Hex()})
	appendTranscriptForTest(t, transcripts, "session-chain", "turn", 0, domain.AssistantAgentMessageRoleUser, "private question")
	appendTranscriptForTest(t, transcripts, "session-chain", "turn", 1, domain.AssistantAgentMessageRoleAssistant, "private answer")
	h.events = append(h.events, pub.events...)
	h.events = append(h.events,
		h.projection(t, domain.AssistantSessionSchemaV2, "session-chain", map[string]any{"schema": domain.AssistantSessionSchemaV2, "session_id": "session-chain", "current_run_id": "run-chain", "checkpoint_event_id": previous, "phase": "executing", "future_field": "kept"}),
		h.projection(t, domain.AssistantSessionSchema, "session-chain", map[string]any{"schema": domain.AssistantSessionSchema, "session_id": "session-chain"}),
		h.projection(t, domain.AssistantSessionSchema, "session-v1", map[string]any{"schema": domain.AssistantSessionSchema, "session_id": "session-v1"}),
	)
	return h
}

func (h legacyHistory) projection(t *testing.T, schema, session string, content map[string]any) nostr.Event {
	t.Helper()
	body, _ := json.Marshal(content)
	ev := nostr.Event{Kind: nostr.Kind(domain.KindAssistantSessionState), CreatedAt: nostr.Now(), Content: string(body), Tags: nostr.Tags{
		{"d", schema + ":" + session}, {domain.AssistantSessionTagSchema, schema}, {"t", kinds.AssistantSessionTopic}, {"session", session},
	}}
	if err := h.oldSigner.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func ofKind(events []nostr.Event, kind int) []nostr.Event {
	var out []nostr.Event
	for _, ev := range events {
		if ev.Kind == nostr.Kind(kind) {
			out = append(out, ev)
		}
	}
	return out
}

func TestAssistantReencryptionReestablishesHistoryUnderTheNewIdentityAndKey(t *testing.T) {
	ctx := context.Background()
	h := buildLegacyHistory(t)
	newSigner := testAssistantSigner(t)
	newAuthor, _ := newSigner.GetPublicKey(ctx)
	newKeys := keyringKeys(t, "k1")

	plan, err := PlanAssistantReencryption(ctx, AssistantReencryptInput{OldAuthor: h.oldAuthor, OldKeys: h.oldKeys, NewKeys: newKeys, Signer: newSigner, OldEvents: h.events})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Checkpoints != 3 || plan.Transcripts != 2 || plan.Sessions != 2 || plan.Present != 0 || len(plan.Failures) != 0 {
		t.Fatalf("plan = %d checkpoints, %d transcripts, %d sessions, %d present, failures %v", plan.Checkpoints, plan.Transcripts, plan.Sessions, plan.Present, plan.Failures)
	}
	for _, ev := range plan.Events {
		if ev.PubKey != newAuthor || !ev.VerifySignature() {
			t.Fatal("a re-established event is not signed by the current identity")
		}
		if strings.Contains(ev.Content, "private") || strings.Contains(ev.Content, "service-nostr-key") || tagValue(ev.Tags, domain.AssistantTranscriptTagKeyRotation) != "" {
			t.Fatalf("event %s still carries plaintext or the legacy key reference", ev.ID.Hex())
		}
	}

	// The re-sealed chain loads under the new identity and key alone.
	reader := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Signer: newSigner, KeyProvider: newKeys, ServicePubkey: newAuthor.Hex(),
		Subscriber: newReplayTranscriptSubscriber(ofKind(plan.Events, domain.AssistantExecutionCheckpointKind))})
	head, err := reader.Load(ctx, "session-chain", "run-chain")
	if err != nil || head.Execution.Revision != 3 || len(head.Chain) != 3 {
		t.Fatalf("re-established chain = %+v, %v", head, err)
	}
	if ofKind(plan.Events, domain.AssistantExecutionCheckpointKind)[0].CreatedAt != ofKind(h.events, domain.AssistantExecutionCheckpointKind)[0].CreatedAt {
		t.Fatal("created_at not preserved")
	}
	transcripts := NewAssistantTranscriptStore(AssistantTranscriptStoreConfig{Subscriber: newReplayTranscriptSubscriber(plan.Events), KeyProvider: newKeys, ServicePubkey: newAuthor.Hex()})
	records, err := transcripts.Replay(ctx, AssistantTranscriptReplayQuery{SessionID: "session-chain"})
	if err != nil || len(records) != 2 || records[1].Payload.Message.Content[0].Text != "private answer" {
		t.Fatalf("re-established transcript = %+v, %v", records, err)
	}
	bySchema := map[string][]nostr.Event{}
	for _, ev := range ofKind(plan.Events, domain.KindAssistantSessionState) {
		schema := tagValue(ev.Tags, domain.AssistantSessionTagSchema)
		bySchema[schema] = append(bySchema[schema], ev)
	}
	var projection map[string]any
	if len(bySchema[domain.AssistantSessionSchemaV2]) != 1 {
		t.Fatalf("v2 projections = %d", len(bySchema[domain.AssistantSessionSchemaV2]))
	}
	if err := json.Unmarshal([]byte(bySchema[domain.AssistantSessionSchemaV2][0].Content), &projection); err != nil {
		t.Fatal(err)
	}
	if projection["checkpoint_event_id"] != head.EventID || projection["future_field"] != "kept" {
		t.Fatalf("v2 projection = %v, want checkpoint %s", projection, head.EventID)
	}
	if v1 := bySchema[domain.AssistantSessionSchema]; len(v1) != 1 || tagValue(v1[0].Tags, "session") != "session-v1" {
		t.Fatal("only the v1 projection of a session without a v2 projection is republished")
	}

	// A rerun finds everything present and publishes nothing.
	again, err := PlanAssistantReencryption(ctx, AssistantReencryptInput{OldAuthor: h.oldAuthor, OldKeys: h.oldKeys, NewKeys: newKeys, Signer: newSigner, OldEvents: h.events, NewEvents: plan.Events})
	if err != nil || len(again.Events) != 0 || again.Present != 3+2+2 || len(again.Failures) != 0 {
		t.Fatalf("rerun = %d events, %d present, %v, %v", len(again.Events), again.Present, again.Failures, err)
	}

	// An interrupted run that published two revisions resumes on them.
	partial := ofKind(plan.Events, domain.AssistantExecutionCheckpointKind)[:2]
	resumed, err := PlanAssistantReencryption(ctx, AssistantReencryptInput{OldAuthor: h.oldAuthor, OldKeys: h.oldKeys, NewKeys: newKeys, Signer: newSigner, OldEvents: h.events, NewEvents: partial})
	if err != nil || resumed.Checkpoints != 1 {
		t.Fatalf("resume = %d checkpoints, %v", resumed.Checkpoints, err)
	}
	reader.subscriber = newReplayTranscriptSubscriber(append(append([]nostr.Event(nil), partial...), ofKind(resumed.Events, domain.AssistantExecutionCheckpointKind)...))
	if head, err := reader.Load(ctx, "session-chain", "run-chain"); err != nil || head.Execution.Revision != 3 {
		t.Fatalf("resumed chain = %+v, %v", head, err)
	}
}

func TestAssistantReencryptionRefusesAndReports(t *testing.T) {
	ctx := context.Background()
	h := buildLegacyHistory(t)
	newKeys := keyringKeys(t, "k1")
	if _, err := PlanAssistantReencryption(ctx, AssistantReencryptInput{OldAuthor: h.oldAuthor, OldKeys: h.oldKeys, NewKeys: newKeys, Signer: h.oldSigner, OldEvents: h.events}); err == nil || !strings.Contains(err.Error(), "rotate it first") {
		t.Fatalf("same identity = %v", err)
	}
	newSigner := testAssistantSigner(t)

	// A wrong old key opens nothing: the run and its projection are
	// reported, never re-sealed from guesses.
	wrong := StaticAssistantTranscriptKeyProvider{Key: AssistantTranscriptKey{Ref: "assistant-transcript/service-nostr-key", Version: "v1", Rotation: "service-nostr-key", Key: []byte("not-the-legacy-key-32-bytes-long")}}
	plan, err := PlanAssistantReencryption(ctx, AssistantReencryptInput{OldAuthor: h.oldAuthor, OldKeys: wrong, NewKeys: newKeys, Signer: newSigner, OldEvents: h.events})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Checkpoints != 0 || plan.Transcripts != 0 || plan.Sessions != 1 || len(plan.Failures) != 1+2+1 {
		t.Fatalf("wrong key plan = %d/%d/%d, failures %v", plan.Checkpoints, plan.Transcripts, plan.Sessions, plan.Failures)
	}
	for _, failure := range plan.Failures {
		if strings.Contains(failure, "private") {
			t.Fatalf("failure leaks plaintext: %s", failure)
		}
	}

	// A forged event in the old history is reported, not trusted.
	forged := h.events[0]
	forged.Content += " "
	plan, err = PlanAssistantReencryption(ctx, AssistantReencryptInput{OldAuthor: h.oldAuthor, OldKeys: h.oldKeys, NewKeys: newKeys, Signer: newSigner, OldEvents: append([]nostr.Event{forged}, h.events[1:]...)})
	if err != nil || len(plan.Failures) == 0 || plan.Checkpoints != 0 {
		t.Fatalf("forged history = %d checkpoints, %v, %v", plan.Checkpoints, plan.Failures, err)
	}
}

type countingSigner struct {
	nostr.Signer
	signs int
}

func (c *countingSigner) SignEvent(ctx context.Context, ev *nostr.Event) error {
	c.signs++
	return c.Signer.SignEvent(ctx, ev)
}

func TestAssistantReencryptionDryRunSignsNothing(t *testing.T) {
	ctx := context.Background()
	h := buildLegacyHistory(t)
	signer := &countingSigner{Signer: testAssistantSigner(t)}
	author, _ := signer.GetPublicKey(ctx)
	plan, err := PlanAssistantReencryption(ctx, AssistantReencryptInput{OldAuthor: h.oldAuthor, OldKeys: h.oldKeys, NewKeys: keyringKeys(t, "k1"), Signer: signer, OldEvents: h.events, DryRun: true})
	if err != nil || signer.signs != 0 || plan.Checkpoints != 3 || plan.Transcripts != 2 || plan.Sessions != 2 {
		t.Fatalf("dry run = %d signs, %d/%d/%d, %v", signer.signs, plan.Checkpoints, plan.Transcripts, plan.Sessions, err)
	}
	for _, ev := range plan.Events {
		if ev.PubKey != author || !ev.CheckID() || ev.VerifySignature() {
			t.Fatal("a dry-run event must carry the current pubkey and its id, unsigned")
		}
	}
}

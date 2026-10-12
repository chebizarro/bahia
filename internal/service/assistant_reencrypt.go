package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// AssistantReencryptInput is one retired service identity's assistant
// history and what to re-establish it under: the current service identity
// (Signer) and the active assistant-transcript key (NewKeys). The operator
// tool cmd/bahia-assistant-reencrypt gathers both event sets from relays.
type AssistantReencryptInput struct {
	// OldAuthor signed the history; OldKeys opens it.
	OldAuthor nostr.PubKey
	OldKeys   AssistantTranscriptKeyProvider
	// NewKeys seals the re-encrypted events; Signer is the current service
	// identity and must not be OldAuthor.
	NewKeys AssistantTranscriptKeyProvider
	Signer  nostr.Signer
	// OldEvents are OldAuthor's checkpoints (4903), transcripts (30316) and
	// assistant session projections; NewEvents are the current identity's,
	// so a rerun skips what is already re-established.
	OldEvents []nostr.Event
	NewEvents []nostr.Event
	// DryRun plans without signing: events carry the current identity's
	// pubkey and their ids, but no signature, and Signer signs nothing.
	DryRun bool
}

// AssistantReencryptPlan is the signed events that re-establish the history,
// in publish order: every checkpoint chain root first, then transcripts, then
// session projections (which name checkpoints by id).
type AssistantReencryptPlan struct {
	Events      []nostr.Event
	Checkpoints int
	Transcripts int
	Sessions    int
	// Present counts items the current identity already holds.
	Present int
	// Failures name what cannot be re-established and why; they never
	// contain plaintext.
	Failures []string
}

// PlanAssistantReencryption decrypts the history with the old key and
// re-seals it under the new one, signed by the current identity:
//
//   - a checkpoint chain is re-sealed root to head with each predecessor
//     link re-pointed at the re-sealed predecessor (event ids change), and
//     reuses any revision the current identity already holds with the same
//     predecessor, so an interrupted run resumes without forking the chain;
//   - a transcript keeps its d coordinate, payload and created_at;
//   - a v2 session projection is republished with its checkpoint reference
//     re-pointed; a v1 projection is republished only for a session without
//     a v2 projection, because recovery derives a v1 session's conversion
//     run from the projection's event id.
//
// Events keep their created_at. A run whose chain does not verify, or whose
// re-established chain conflicts, is reported and skipped with its session
// projection.
func PlanAssistantReencryption(ctx context.Context, in AssistantReencryptInput) (AssistantReencryptPlan, error) {
	var plan AssistantReencryptPlan
	if in.Signer == nil || in.OldKeys == nil || in.NewKeys == nil {
		return plan, errors.New("assistant re-encryption needs the old key, the new key and the current service signer")
	}
	newAuthor, err := in.Signer.GetPublicKey(ctx)
	if err != nil {
		return plan, fmt.Errorf("read the current service pubkey: %w", err)
	}
	if newAuthor == in.OldAuthor {
		return plan, errors.New("the current service identity is the retired one: re-encryption republishes under the rotated service key, so rotate it first")
	}
	signer := in.Signer
	if in.DryRun {
		signer = idOnlySigner{pubkey: newAuthor}
	}
	if _, err := in.NewKeys.ActiveTranscriptKey(ctx); err != nil {
		return plan, fmt.Errorf("resolve the new assistant transcript key: %w", err)
	}
	old := partitionAssistantEvents(in.OldEvents, in.OldAuthor, &plan)
	current := partitionAssistantEvents(in.NewEvents, newAuthor, &plan)

	oldStore := &AssistantExecutionStore{keys: in.OldKeys}
	newStore := &AssistantExecutionStore{keys: in.NewKeys, signer: signer}
	reSealed := map[string]map[string]string{} // session/run -> old id -> new id
	for _, run := range sortedKeys(old.checkpoints) {
		mapping, err := planCheckpointChain(ctx, &plan, oldStore, newStore, in.OldAuthor, newAuthor, old.checkpoints[run], current.checkpoints[run])
		if err != nil {
			plan.Failures = append(plan.Failures, fmt.Sprintf("checkpoint run %s: %v", run, err))
			continue
		}
		reSealed[run] = mapping
	}

	transcripts := &AssistantTranscriptStore{keys: in.OldKeys, now: nowUTC}
	newKey, err := in.NewKeys.ActiveTranscriptKey(ctx)
	if err != nil {
		return plan, err
	}
	for _, d := range sortedKeys(old.transcripts) {
		if _, ok := current.transcripts[d]; ok {
			plan.Present++
			continue
		}
		ev, err := reSealTranscript(ctx, transcripts, newKey, signer, old.transcripts[d])
		if err != nil {
			plan.Failures = append(plan.Failures, fmt.Sprintf("transcript %s: %v", old.transcripts[d].ID.Hex(), err))
			continue
		}
		plan.Events = append(plan.Events, ev)
		plan.Transcripts++
	}

	for _, d := range sortedKeys(old.sessions) {
		if _, ok := current.sessions[d]; ok {
			plan.Present++
			continue
		}
		ev := old.sessions[d]
		content := ev.Content
		switch tagValue(ev.Tags, domain.AssistantSessionTagSchema) {
		case domain.AssistantSessionSchemaV2:
			content, err = repointSessionProjection(ev.Content, reSealed)
			if err != nil {
				plan.Failures = append(plan.Failures, fmt.Sprintf("session projection %s: %v", ev.ID.Hex(), err))
				continue
			}
		case domain.AssistantSessionSchema:
			if _, hasV2 := old.sessions[domain.AssistantSessionSchemaV2+":"+tagValue(ev.Tags, "session")]; hasV2 {
				continue
			}
		default:
			continue
		}
		next := nostr.Event{Kind: ev.Kind, CreatedAt: ev.CreatedAt, Tags: cloneTags(ev.Tags), Content: content}
		if err := signGoNostrEvent(ctx, signer, &next); err != nil {
			return plan, fmt.Errorf("sign session projection: %w", err)
		}
		plan.Events = append(plan.Events, next)
		plan.Sessions++
	}
	return plan, nil
}

func nowUTC() time.Time { return time.Now().UTC() }

// idOnlySigner gives a dry-run plan the pubkey and ids of the real one (ids do
// not depend on the signature, so checkpoint links are as published) without
// asking the service signer to sign anything.
type idOnlySigner struct{ pubkey nostr.PubKey }

func (s idOnlySigner) GetPublicKey(context.Context) (nostr.PubKey, error) { return s.pubkey, nil }

func (s idOnlySigner) SignEvent(_ context.Context, ev *nostr.Event) error {
	ev.PubKey = s.pubkey
	ev.ID = ev.GetID()
	return nil
}

type assistantHistory struct {
	checkpoints map[string][]nostr.Event // session/run
	transcripts map[string]nostr.Event   // d, NIP-01 winner
	sessions    map[string]nostr.Event   // d, NIP-01 winner
}

func partitionAssistantEvents(events []nostr.Event, author nostr.PubKey, plan *AssistantReencryptPlan) assistantHistory {
	h := assistantHistory{checkpoints: map[string][]nostr.Event{}, transcripts: map[string]nostr.Event{}, sessions: map[string]nostr.Event{}}
	seen := map[nostr.ID]bool{}
	for _, ev := range events {
		if seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		if ev.PubKey != author || !ev.CheckID() || !ev.VerifySignature() {
			plan.Failures = append(plan.Failures, fmt.Sprintf("event %s: not a valid event of %s", ev.ID.Hex(), author.Hex()))
			continue
		}
		switch {
		case ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind):
			run := tagValue(ev.Tags, domain.AssistantCheckpointTagSession) + "/" + tagValue(ev.Tags, domain.AssistantCheckpointTagRun)
			h.checkpoints[run] = append(h.checkpoints[run], ev)
		case ev.Kind == nostr.Kind(domain.KindAssistantTranscript):
			keepNewest(h.transcripts, ev)
		case ev.Kind == nostr.Kind(domain.KindAssistantSessionState) && hasTagValue(ev.Tags, "t", kinds.AssistantSessionTopic):
			keepNewest(h.sessions, ev)
		}
	}
	return h
}

// keepNewest keeps the NIP-01 winner per d: newest created_at, lowest id on
// a tie.
func keepNewest(byD map[string]nostr.Event, ev nostr.Event) {
	d := tagValue(ev.Tags, "d")
	if d == "" {
		return
	}
	cur, ok := byD[d]
	if !ok || ev.CreatedAt > cur.CreatedAt || (ev.CreatedAt == cur.CreatedAt && ev.ID.Hex() < cur.ID.Hex()) {
		byD[d] = ev
	}
}

func planCheckpointChain(ctx context.Context, plan *AssistantReencryptPlan, oldStore, newStore *AssistantExecutionStore, oldAuthor, newAuthor nostr.PubKey, oldEvents, newEvents []nostr.Event) (map[string]string, error) {
	if len(oldEvents) == 0 {
		return nil, errors.New("no checkpoints")
	}
	session, runID := tagValue(oldEvents[0].Tags, domain.AssistantCheckpointTagSession), tagValue(oldEvents[0].Tags, domain.AssistantCheckpointTagRun)
	nodes := make([]assistantCheckpointNode, 0, len(oldEvents))
	createdAt := map[string]nostr.Timestamp{}
	for i := range oldEvents {
		cp, err := oldStore.decode(ctx, &oldEvents[i], oldAuthor, session, runID)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %s does not open under the old key: %w", oldEvents[i].ID.Hex(), err)
		}
		nodes = append(nodes, assistantCheckpointNode{id: oldEvents[i].ID.Hex(), cp: cp})
		createdAt[oldEvents[i].ID.Hex()] = oldEvents[i].CreatedAt
	}
	head, err := newestAssistantCheckpoint(nodes)
	if err != nil {
		return nil, err
	}
	byID := map[string]assistantCheckpointNode{}
	for _, node := range nodes {
		byID[node.id] = node
	}
	existing := map[uint64]assistantCheckpointNode{}
	for i := range newEvents {
		cp, err := newStore.decode(ctx, &newEvents[i], newAuthor, session, runID)
		if err != nil {
			return nil, fmt.Errorf("re-established checkpoint %s does not open under the new keys: %w", newEvents[i].ID.Hex(), err)
		}
		rev := cp.Execution.Revision
		if prior, ok := existing[rev]; ok && prior.id != newEvents[i].ID.Hex() {
			return nil, fmt.Errorf("the current identity holds two checkpoints of revision %d", rev)
		}
		existing[rev] = assistantCheckpointNode{id: newEvents[i].ID.Hex(), cp: cp}
	}
	mapping := map[string]string{}
	var sealed []nostr.Event
	present := 0
	previous := ""
	for _, oldID := range head.Chain {
		execution := byID[oldID].cp.Execution
		if have, ok := existing[execution.Revision]; ok {
			if have.cp.PreviousEventID != previous {
				return nil, fmt.Errorf("the current identity's revision %d does not follow the re-established chain", execution.Revision)
			}
			mapping[oldID], previous = have.id, have.id
			present++
			continue
		}
		plaintext, err := json.Marshal(domain.AssistantExecutionCheckpoint{Execution: execution, PreviousEventID: previous})
		if err != nil {
			return nil, err
		}
		ev, err := newStore.sealCheckpointAt(ctx, execution, previous, plaintext, createdAt[oldID])
		if err != nil {
			return nil, fmt.Errorf("re-seal revision %d: %w", execution.Revision, err)
		}
		sealed = append(sealed, ev)
		mapping[oldID], previous = ev.ID.Hex(), ev.ID.Hex()
	}
	plan.Events = append(plan.Events, sealed...)
	plan.Checkpoints += len(sealed)
	plan.Present += present
	return mapping, nil
}

func reSealTranscript(ctx context.Context, store *AssistantTranscriptStore, key AssistantTranscriptKey, signer nostr.Signer, ev nostr.Event) (nostr.Event, error) {
	record, err := store.decryptEvent(ctx, &ev, AssistantTranscriptReplayQuery{SessionID: tagValue(ev.Tags, domain.AssistantTranscriptTagSession)})
	if err != nil {
		return nostr.Event{}, err
	}
	if record == nil {
		return nostr.Event{}, errors.New("not an assistant transcript event")
	}
	envelope, err := encryptAssistantTranscriptPayload(ctx, key, record.Payload, assistantTranscriptAssociatedData(record.Payload, key))
	if err != nil {
		return nostr.Event{}, err
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return nostr.Event{}, err
	}
	tags := make(nostr.Tags, 0, len(ev.Tags)+1)
	for _, tag := range ev.Tags {
		switch tag[0] {
		case domain.AssistantTranscriptTagKeyRef, domain.AssistantTranscriptTagKeyVersion, domain.AssistantTranscriptTagKeyRotation:
			continue
		}
		tags = append(tags, append(nostr.Tag(nil), tag...))
	}
	tags = append(tags, nostr.Tag{domain.AssistantTranscriptTagKeyRef, key.Ref})
	if key.Version != "" {
		tags = append(tags, nostr.Tag{domain.AssistantTranscriptTagKeyVersion, key.Version})
	}
	if key.Rotation != "" {
		tags = append(tags, nostr.Tag{domain.AssistantTranscriptTagKeyRotation, key.Rotation})
	}
	next := nostr.Event{Kind: ev.Kind, CreatedAt: ev.CreatedAt, Tags: tags, Content: string(content)}
	if err := signGoNostrEvent(ctx, signer, &next); err != nil {
		return nostr.Event{}, fmt.Errorf("sign transcript: %w", err)
	}
	return next, nil
}

// repointSessionProjection replaces a v2 projection's checkpoint reference
// with its re-sealed id, keeping every other field as published.
func repointSessionProjection(content string, reSealed map[string]map[string]string) (string, error) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(content), &fields); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	checkpoint, _ := fields["checkpoint_event_id"].(string)
	if checkpoint == "" {
		return content, nil
	}
	session, _ := fields["session_id"].(string)
	run, _ := fields["current_run_id"].(string)
	next, ok := reSealed[session+"/"+run][checkpoint]
	if !ok {
		return "", fmt.Errorf("its checkpoint chain %s/%s is not re-established", session, run)
	}
	fields["checkpoint_event_id"] = next
	out, err := json.Marshal(fields)
	return string(out), err
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func hasTagValue(tags nostr.Tags, name, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}

func cloneTags(tags nostr.Tags) nostr.Tags {
	out := make(nostr.Tags, len(tags))
	for i, tag := range tags {
		out[i] = append(nostr.Tag(nil), tag...)
	}
	return out
}

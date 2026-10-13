package service

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// ErrAssistantCheckpointNotFound means a complete EOSE-bounded backfill found
// no checkpoint for the run. It is not evidence about downstream effects.
var ErrAssistantCheckpointNotFound = errors.New("no assistant checkpoint")

// ErrAssistantCheckpointConflict means a different logical checkpoint was
// offered for a revision whose signed event is still unconfirmed. Publishing
// it could fork the chain, so the caller must retry the pending event instead.
var ErrAssistantCheckpointConflict = errors.New("assistant checkpoint conflicts with an unconfirmed pending checkpoint")

// AssistantCheckpointStore is the append-only dispatch journal. A returned ID
// means a relay accepted the exact signed event, not merely that it was queued.
// Retrying Append with an identical execution and predecessor republishes the
// same signed event rather than creating another logical checkpoint.
type AssistantCheckpointStore interface {
	Append(ctx context.Context, execution domain.AssistantExecution, previousEventID string) (string, error)
	Load(ctx context.Context, sessionID, runID string) (AssistantCheckpointHead, error)
}

// AssistantCheckpointHead is the newest checkpoint of a single validated
// predecessor chain. Chain lists event IDs from root to head.
type AssistantCheckpointHead struct {
	Execution domain.AssistantExecution
	EventID   string
	Chain     []string
}

// Contains reports whether an event ID is part of the validated chain.
func (h AssistantCheckpointHead) Contains(eventID string) bool {
	for _, id := range h.Chain {
		if id == eventID {
			return true
		}
	}
	return false
}

// AssistantLocalEventStore retains relay-accepted checkpoints separately
// from the publisher's speculative pre-OK event cache.
type AssistantLocalEventStore interface {
	SupervisionEventStore
	SaveEvent(nostr.Event) (bool, error)
	MarkAssistantCheckpointAccepted(nostr.ID) error
	AssistantCheckpointAccepted(nostr.ID) (bool, error)
}

type AssistantExecutionStoreConfig struct {
	Publisher     AssistantEventPublisher
	Subscriber    AssistantRelaySubscriber
	Signer        nostr.Signer
	KeyProvider   AssistantTranscriptKeyProvider
	ServicePubkey string
	LocalStore    AssistantLocalEventStore
	Now           func() time.Time
}

type AssistantExecutionStore struct {
	publisher     AssistantEventPublisher
	subscriber    AssistantRelaySubscriber
	signer        nostr.Signer
	keys          AssistantTranscriptKeyProvider
	servicePubkey string
	local         AssistantLocalEventStore
	now           func() time.Time
	mu            sync.Mutex
	pending       map[string]assistantPendingCheckpoint
}

// assistantPendingCheckpoint is a signed checkpoint whose acceptance has not
// been confirmed. plaintext identifies the logical checkpoint for retries.
type assistantPendingCheckpoint struct {
	event     nostr.Event
	plaintext []byte
	revision  uint64
}

func NewAssistantExecutionStore(c AssistantExecutionStoreConfig) *AssistantExecutionStore {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return &AssistantExecutionStore{publisher: c.Publisher, subscriber: c.Subscriber, signer: c.Signer, keys: c.KeyProvider, servicePubkey: strings.TrimSpace(c.ServicePubkey), local: c.LocalStore, now: now, pending: make(map[string]assistantPendingCheckpoint)}
}

func checkpointTags(e domain.AssistantExecution, prev string, key AssistantTranscriptKeyRef) nostr.Tags {
	tags := nostr.Tags{
		{domain.AssistantCheckpointTagDomain, domain.AssistantDomain},
		{domain.AssistantCheckpointTagType, domain.AssistantCheckpointTypeExecution},
		{domain.AssistantCheckpointTagSchema, domain.AssistantExecutionCheckpointSchema},
		{domain.AssistantCheckpointTagSession, e.SessionID},
		{domain.AssistantCheckpointTagRun, e.RunID},
		{"t", kinds.AssistantCheckpointSessionTopic(e.SessionID)},
		{domain.AssistantCheckpointTagRevision, strconv.FormatUint(e.Revision, 10)},
		{domain.AssistantTranscriptTagKeyRef, key.Ref},
		{domain.AssistantTranscriptTagKeyVersion, key.Version},
	}
	if prev != "" {
		tags = append(tags, nostr.Tag{domain.AssistantCheckpointTagPrevious, prev})
	}
	return tags
}

// checkpointAD is the associated data a checkpoint is sealed with: its chain
// position and the key that seals it. Checkpoints sealed under the retired
// nsec-derived key predate the key binding (checkpointADKeyBound).
func checkpointAD(e domain.AssistantExecution, prev string, key AssistantTranscriptKeyRef) map[string]string {
	ad := map[string]string{"schema": domain.AssistantExecutionCheckpointSchema, "session": e.SessionID, "run": e.RunID, "revision": strconv.FormatUint(e.Revision, 10), "prev": prev}
	if checkpointADKeyBound(key.Ref) {
		ad[domain.AssistantTranscriptTagKeyRef] = key.Ref
		ad[domain.AssistantTranscriptTagKeyVersion] = key.Version
	}
	return ad
}

// checkpointADKeyBound reports whether a checkpoint sealed under keyRef binds
// its key reference into the associated data: every Signet system key does.
func checkpointADKeyBound(keyRef string) bool {
	return strings.HasPrefix(keyRef, secretstore.SystemLabelNamespace+"/")
}

func (s *AssistantExecutionStore) Append(ctx context.Context, execution domain.AssistantExecution, previous string) (string, error) {
	if s == nil || s.publisher == nil || s.signer == nil || s.keys == nil {
		return "", errors.New("assistant checkpoint store is not fully configured")
	}
	if execution.Version != domain.AssistantExecutionVersion || execution.SessionID == "" || execution.RunID == "" || execution.Revision == 0 || !execution.Workflow.Valid() || !execution.Phase.Valid() {
		return "", errors.New("invalid assistant checkpoint identity")
	}
	if (execution.Revision == 1) != (previous == "") {
		return "", errors.New("assistant checkpoint predecessor/revision mismatch")
	}
	plaintext, err := json.Marshal(domain.AssistantExecutionCheckpoint{Execution: execution, PreviousEventID: previous})
	if err != nil {
		return "", err
	}
	runKey := execution.SessionID + "\x00" + execution.RunID
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, exists := s.pending[runKey]
	switch {
	case exists && pending.revision == execution.Revision:
		// A failed or ambiguous publication may only be retried with the
		// identical signed event; a different payload would fork the chain.
		if !bytes.Equal(pending.plaintext, plaintext) {
			return "", fmt.Errorf("%w: revision %d", ErrAssistantCheckpointConflict, execution.Revision)
		}
	case exists && pending.revision+1 == execution.Revision && previous == pending.event.ID.Hex():
		// The caller observed the pending event in the validated chain.
		delete(s.pending, runKey)
		exists = false
	case exists:
		return "", fmt.Errorf("%w: pending revision %d, offered %d", ErrAssistantCheckpointConflict, pending.revision, execution.Revision)
	}
	if !exists {
		ev, signErr := s.sealCheckpoint(ctx, execution, previous, plaintext)
		if signErr != nil {
			return "", signErr
		}
		pending = assistantPendingCheckpoint{event: ev, plaintext: plaintext, revision: execution.Revision}
		s.pending[runKey] = pending
	}
	accepted, err := s.publisher.Publish(ctx, pending.event)
	if err != nil {
		return "", fmt.Errorf("publish assistant checkpoint %s: %w", pending.event.ID.Hex(), err)
	}
	if accepted < 1 {
		return "", fmt.Errorf("publish assistant checkpoint %s: no relay accepted event", pending.event.ID.Hex())
	}
	if s.local != nil {
		if _, err := s.local.SaveEvent(pending.event); err != nil {
			return "", fmt.Errorf("retain assistant checkpoint %s: %w", pending.event.ID.Hex(), err)
		}
		retained := false
		for range s.local.QueryEvents(nostr.Filter{IDs: []nostr.ID{pending.event.ID}}) {
			retained = true
		}
		if !retained {
			return "", fmt.Errorf("assistant checkpoint %s was not retained locally", pending.event.ID.Hex())
		}
		if err := s.local.MarkAssistantCheckpointAccepted(pending.event.ID); err != nil {
			return "", fmt.Errorf("mark assistant checkpoint %s accepted locally: %w", pending.event.ID.Hex(), err)
		}
	}
	delete(s.pending, runKey)
	return pending.event.ID.Hex(), nil
}

func (s *AssistantExecutionStore) sealCheckpoint(ctx context.Context, execution domain.AssistantExecution, previous string, plaintext []byte) (nostr.Event, error) {
	return s.sealCheckpointAt(ctx, execution, previous, plaintext, nostr.Timestamp(s.now().UTC().Unix()))
}

func (s *AssistantExecutionStore) sealCheckpointAt(ctx context.Context, execution domain.AssistantExecution, previous string, plaintext []byte, createdAt nostr.Timestamp) (nostr.Event, error) {
	var envelope domain.AssistantExecutionCheckpointAEADEnvelope
	var key AssistantTranscriptKeyRef
	if err := s.keys.WithActiveKey(ctx, func(ref AssistantTranscriptKeyRef, aead cipher.AEAD) error {
		key = ref
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		ad := checkpointAD(execution, previous, ref)
		adBytes, err := json.Marshal(ad)
		if err != nil {
			return err
		}
		envelope = domain.AssistantExecutionCheckpointAEADEnvelope{Schema: domain.AssistantExecutionCheckpointSchema, Envelope: domain.AssistantTranscriptEnvelopeServiceHeldAEAD, Algorithm: domain.AssistantTranscriptAEADAlgorithmXChaCha20, KeyRef: ref.Ref, KeyVersion: ref.Version, Nonce: base64.RawStdEncoding.EncodeToString(nonce), Ciphertext: base64.RawStdEncoding.EncodeToString(aead.Seal(nil, nonce, plaintext, adBytes)), AssociatedData: ad}
		return nil
	}); err != nil {
		return nostr.Event{}, fmt.Errorf("seal assistant checkpoint: %w", err)
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return nostr.Event{}, err
	}
	ev := nostr.Event{Kind: nostr.Kind(domain.AssistantExecutionCheckpointKind), CreatedAt: createdAt, Tags: checkpointTags(execution, previous, key), Content: string(content)}
	if err = signGoNostrEvent(ctx, s.signer, &ev); err != nil {
		return nostr.Event{}, fmt.Errorf("sign assistant checkpoint: %w", err)
	}
	return ev, nil
}

func (s *AssistantExecutionStore) decode(ctx context.Context, ev *nostr.Event, author nostr.PubKey, sessionID, runID string) (domain.AssistantExecutionCheckpoint, error) {
	if ev == nil || ev.Kind != nostr.Kind(domain.AssistantExecutionCheckpointKind) || !ev.CheckID() || !ev.VerifySignature() {
		return domain.AssistantExecutionCheckpoint{}, errors.New("invalid assistant checkpoint event signature or kind")
	}
	if ev.PubKey != author {
		return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint author mismatch")
	}
	if tagValue(ev.Tags, "d") != "" || tagValue(ev.Tags, domain.AssistantCheckpointTagDomain) != domain.AssistantDomain || tagValue(ev.Tags, domain.AssistantCheckpointTagType) != domain.AssistantCheckpointTypeExecution || tagValue(ev.Tags, domain.AssistantCheckpointTagSchema) != domain.AssistantExecutionCheckpointSchema || tagValue(ev.Tags, domain.AssistantCheckpointTagSession) != sessionID || tagValue(ev.Tags, domain.AssistantCheckpointTagRun) != runID {
		return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint tags mismatch")
	}
	var env domain.AssistantExecutionCheckpointAEADEnvelope
	if err := json.Unmarshal([]byte(ev.Content), &env); err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	if env.Schema != domain.AssistantExecutionCheckpointSchema || env.Envelope != domain.AssistantTranscriptEnvelopeServiceHeldAEAD || env.Algorithm != domain.AssistantTranscriptAEADAlgorithmXChaCha20 || env.KeyRef != tagValue(ev.Tags, domain.AssistantTranscriptTagKeyRef) || env.KeyVersion != tagValue(ev.Tags, domain.AssistantTranscriptTagKeyVersion) {
		return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint envelope mismatch")
	}
	if s.keys == nil {
		return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint key provider missing")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	sealed, err := base64.RawStdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	adBytes, err := json.Marshal(env.AssociatedData)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	var plain []byte
	if err := s.keys.WithKey(ctx, env.KeyRef, env.KeyVersion, func(aead cipher.AEAD) error {
		var err error
		plain, err = aead.Open(nil, nonce, sealed, adBytes)
		return err
	}); err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	var cp domain.AssistantExecutionCheckpoint
	if err = json.Unmarshal(plain, &cp); err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	e := cp.Execution
	if e.Version != domain.AssistantExecutionVersion || e.SessionID != sessionID || e.RunID != runID || e.Revision == 0 || !e.Workflow.Valid() || !e.Phase.Valid() || tagValue(ev.Tags, domain.AssistantCheckpointTagRevision) != strconv.FormatUint(e.Revision, 10) || tagValue(ev.Tags, domain.AssistantCheckpointTagPrevious) != cp.PreviousEventID {
		return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint payload/tags mismatch")
	}
	expected := checkpointAD(e, cp.PreviousEventID, AssistantTranscriptKeyRef{Ref: env.KeyRef, Version: env.KeyVersion})
	if len(env.AssociatedData) != len(expected) {
		return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint associated data mismatch")
	}
	for k, v := range expected {
		if env.AssociatedData[k] != v {
			return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint associated data mismatch")
		}
	}
	if (e.Revision == 1) != (cp.PreviousEventID == "") {
		return domain.AssistantExecutionCheckpoint{}, errors.New("assistant checkpoint invalid root")
	}
	return cp, nil
}

type assistantCheckpointNode struct {
	id string
	cp domain.AssistantExecutionCheckpoint
}

// newestAssistantCheckpoint follows revision and predecessor links only. Neither
// relay arrival order nor CreatedAt participates in the result.
func newestAssistantCheckpoint(nodes []assistantCheckpointNode) (AssistantCheckpointHead, error) {
	if len(nodes) == 0 {
		return AssistantCheckpointHead{}, ErrAssistantCheckpointNotFound
	}
	byID := make(map[string]assistantCheckpointNode, len(nodes))
	successors := map[string]string{}
	for _, node := range nodes {
		if _, ok := byID[node.id]; ok {
			continue
		}
		byID[node.id] = node
		pred := node.cp.PreviousEventID
		if prior, ok := successors[pred]; ok && prior != node.id {
			return AssistantCheckpointHead{}, errors.New("conflicting assistant checkpoint successors")
		}
		successors[pred] = node.id
	}
	rootID := successors[""]
	if rootID == "" {
		return AssistantCheckpointHead{}, errors.New("assistant checkpoint root missing")
	}
	id := rootID
	visited := map[string]bool{}
	chain := []string{}
	for {
		if visited[id] {
			return AssistantCheckpointHead{}, errors.New("assistant checkpoint cycle")
		}
		visited[id] = true
		chain = append(chain, id)
		node := byID[id]
		if nextID, ok := successors[id]; ok {
			next := byID[nextID]
			if next.cp.Execution.Revision != node.cp.Execution.Revision+1 || next.cp.Execution.SessionID != node.cp.Execution.SessionID || next.cp.Execution.RunID != node.cp.Execution.RunID {
				return AssistantCheckpointHead{}, errors.New("assistant checkpoint revision gap")
			}
			id = nextID
			continue
		}
		if len(visited) != len(byID) {
			return AssistantCheckpointHead{}, errors.New("assistant checkpoint disconnected chain")
		}
		return AssistantCheckpointHead{Execution: node.cp.Execution, EventID: id, Chain: chain}, nil
	}
}

// assistantEOSEReached reports whether EOSE is already readable. Adapters close
// the event stream and EOSE together when a subscription finishes, so a
// closed event channel observed first must not be mistaken for a stream that
// ended before historical catch-up completed.
func assistantEOSEReached(eose <-chan struct{}) bool {
	if eose == nil {
		return false
	}
	select {
	case <-eose:
		return true
	default:
		return false
	}
}

// author resolves the only pubkey whose checkpoints are trusted. Without an
// explicit service pubkey the signing identity is used; checkpoints are never
// loaded without an author constraint.
func (s *AssistantExecutionStore) author(ctx context.Context) (nostr.PubKey, error) {
	if s.servicePubkey != "" {
		return nostr.PubKeyFromHex(s.servicePubkey)
	}
	if s.signer == nil {
		return nostr.PubKey{}, errors.New("assistant checkpoint author is not configured")
	}
	return s.signer.GetPublicKey(ctx)
}

// Load backfills through EOSE, merges the service-signed local replica, and
// validates the complete predecessor chain. An author/kind-only second REQ
// recovers checkpoints written before the indexed session topic existed.
func (s *AssistantExecutionStore) Load(ctx context.Context, sessionID, runID string) (AssistantCheckpointHead, error) {
	if s == nil || s.subscriber == nil || sessionID == "" || runID == "" {
		return AssistantCheckpointHead{}, errors.New("assistant checkpoint query not configured")
	}
	author, err := s.author(ctx)
	if err != nil {
		return AssistantCheckpointHead{}, err
	}
	base := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(domain.AssistantExecutionCheckpointKind)}, Authors: []nostr.PubKey{author}}
	scoped := base
	scoped.Tags = nostr.TagMap{"t": {kinds.AssistantCheckpointSessionTopic(sessionID)}}
	nodes, err := s.backfill(ctx, scoped, author, sessionID, runID)
	if err != nil {
		return AssistantCheckpointHead{}, err
	}
	if s.local != nil {
		for ev := range s.local.QueryEvents(scoped) {
			if !assistantCheckpointMatchesRun(ev, sessionID, runID) {
				continue
			}
			accepted, err := s.local.AssistantCheckpointAccepted(ev.ID)
			if err != nil {
				return AssistantCheckpointHead{}, fmt.Errorf("read local checkpoint acceptance %s: %w", ev.ID.Hex(), err)
			}
			if !accepted {
				continue
			}
			cp, err := s.decode(ctx, &ev, author, sessionID, runID)
			if err != nil {
				return AssistantCheckpointHead{}, fmt.Errorf("local checkpoint %s: %w", ev.ID.Hex(), err)
			}
			nodes = append(nodes, assistantCheckpointNode{id: ev.ID.Hex(), cp: cp})
		}
	}
	if head, err := newestAssistantCheckpoint(nodes); err == nil {
		return head, nil
	}
	legacy, err := s.backfill(ctx, base, author, sessionID, runID)
	if err != nil {
		return AssistantCheckpointHead{}, err
	}
	return newestAssistantCheckpoint(append(nodes, legacy...))
}

func assistantCheckpointMatchesRun(ev nostr.Event, sessionID, runID string) bool {
	return tagValue(ev.Tags, domain.AssistantCheckpointTagSession) == sessionID && tagValue(ev.Tags, domain.AssistantCheckpointTagRun) == runID
}

func (s *AssistantExecutionStore) backfill(ctx context.Context, filter nostr.Filter, author nostr.PubKey, sessionID, runID string) ([]assistantCheckpointNode, error) {
	sub, err := s.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	events, closed, eose := sub.EventChan(), sub.ClosedChan(), sub.EOSEChan()
	nodes := []assistantCheckpointNode{}
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return nil, fmt.Errorf("assistant checkpoint subscription closed: %s %s", c.RelayURL, c.Reason)
		case ev, ok := <-events:
			if !ok {
				if assistantEOSEReached(eose) {
					return nodes, nil
				}
				return nil, errors.New("assistant checkpoint subscription ended before EOSE")
			}
			if ev == nil || seen[ev.ID.Hex()] || !assistantCheckpointMatchesRun(*ev, sessionID, runID) {
				continue
			}
			seen[ev.ID.Hex()] = true
			cp, err := s.decode(ctx, ev, author, sessionID, runID)
			if err != nil {
				return nil, fmt.Errorf("checkpoint %s: %w", ev.ID.Hex(), err)
			}
			nodes = append(nodes, assistantCheckpointNode{id: ev.ID.Hex(), cp: cp})
		case <-eose:
			return nodes, nil
		}
	}
}

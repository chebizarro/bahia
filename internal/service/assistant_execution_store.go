package service

import (
	"context"
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
)

// AssistantCheckpointStore is the append-only dispatch journal. A returned ID
// means a relay accepted the exact signed event, not merely that it was queued.
type AssistantCheckpointStore interface {
	Append(context.Context, domain.AssistantExecution, string) (string, error)
	Load(context.Context, string, string) (domain.AssistantExecution, string, error)
}

type AssistantExecutionStoreConfig struct {
	Publisher     AssistantEventPublisher
	Subscriber    AssistantRelaySubscriber
	Signer        nostr.Signer
	KeyProvider   AssistantTranscriptKeyProvider
	ServicePubkey string
	Now           func() time.Time
}

type AssistantExecutionStore struct {
	publisher     AssistantEventPublisher
	subscriber    AssistantRelaySubscriber
	signer        nostr.Signer
	keys          AssistantTranscriptKeyProvider
	servicePubkey string
	now           func() time.Time
	mu            sync.Mutex
	pending       map[string]nostr.Event
}

func NewAssistantExecutionStore(c AssistantExecutionStoreConfig) *AssistantExecutionStore {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return &AssistantExecutionStore{publisher: c.Publisher, subscriber: c.Subscriber, signer: c.Signer, keys: c.KeyProvider, servicePubkey: strings.TrimSpace(c.ServicePubkey), now: now, pending: make(map[string]nostr.Event)}
}

func checkpointTags(e domain.AssistantExecution, prev string, key AssistantTranscriptKey) nostr.Tags {
	tags := nostr.Tags{
		{domain.AssistantCheckpointTagDomain, domain.AssistantDomain},
		{domain.AssistantCheckpointTagType, domain.AssistantCheckpointTypeExecution},
		{domain.AssistantCheckpointTagSchema, domain.AssistantExecutionCheckpointSchema},
		{domain.AssistantCheckpointTagSession, e.SessionID},
		{domain.AssistantCheckpointTagRun, e.RunID},
		{domain.AssistantCheckpointTagRevision, strconv.FormatUint(e.Revision, 10)},
		{domain.AssistantTranscriptTagKeyRef, key.Ref},
		{domain.AssistantTranscriptTagKeyVersion, key.Version},
	}
	if prev != "" {
		tags = append(tags, nostr.Tag{domain.AssistantCheckpointTagPrevious, prev})
	}
	return tags
}

func checkpointAD(e domain.AssistantExecution, prev string) map[string]string {
	return map[string]string{"schema": domain.AssistantExecutionCheckpointSchema, "session": e.SessionID, "run": e.RunID, "revision": strconv.FormatUint(e.Revision, 10), "prev": prev}
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
	keyID := execution.SessionID + "\x00" + execution.RunID + "\x00" + strconv.FormatUint(execution.Revision, 10)
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, exists := s.pending[keyID]
	if !exists {
		key, err := s.keys.ActiveTranscriptKey(ctx)
		if err != nil {
			return "", err
		}
		key, err = validateAssistantTranscriptKey(key)
		if err != nil {
			return "", err
		}
		plaintext, err := json.Marshal(domain.AssistantExecutionCheckpoint{Execution: execution, PreviousEventID: previous})
		if err != nil {
			return "", err
		}
		aead, err := chacha20poly1305.NewX(key.Key)
		if err != nil {
			return "", err
		}
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		if _, err = rand.Read(nonce); err != nil {
			return "", err
		}
		ad := checkpointAD(execution, previous)
		adBytes, err := json.Marshal(ad)
		if err != nil {
			return "", err
		}
		envelope := domain.AssistantExecutionCheckpointAEADEnvelope{Schema: domain.AssistantExecutionCheckpointSchema, Envelope: domain.AssistantTranscriptEnvelopeServiceHeldAEAD, Algorithm: domain.AssistantTranscriptAEADAlgorithmXChaCha20, KeyRef: key.Ref, KeyVersion: key.Version, Nonce: base64.RawStdEncoding.EncodeToString(nonce), Ciphertext: base64.RawStdEncoding.EncodeToString(aead.Seal(nil, nonce, plaintext, adBytes)), AssociatedData: ad}
		content, err := json.Marshal(envelope)
		if err != nil {
			return "", err
		}
		ev = nostr.Event{Kind: nostr.Kind(domain.AssistantExecutionCheckpointKind), CreatedAt: nostr.Timestamp(s.now().UTC().Unix()), Tags: checkpointTags(execution, previous, key), Content: string(content)}
		if err = signGoNostrEvent(ctx, s.signer, &ev); err != nil {
			return "", fmt.Errorf("sign assistant checkpoint: %w", err)
		}
		s.pending[keyID] = ev
	} else {
		// A failed/ambiguous publication can only retry the same signed event.
		if tagValue(ev.Tags, domain.AssistantCheckpointTagPrevious) != previous {
			return "", errors.New("checkpoint retry changed predecessor")
		}
	}
	accepted, err := s.publisher.Publish(ctx, ev)
	if err != nil {
		return "", fmt.Errorf("publish assistant checkpoint %s: %w", ev.ID.Hex(), err)
	}
	if accepted < 1 {
		return "", fmt.Errorf("publish assistant checkpoint %s: no relay accepted event", ev.ID.Hex())
	}
	delete(s.pending, keyID)
	return ev.ID.Hex(), nil
}

func (s *AssistantExecutionStore) decode(ctx context.Context, ev *nostr.Event, sessionID, runID string) (domain.AssistantExecutionCheckpoint, error) {
	if ev == nil || ev.Kind != nostr.Kind(domain.AssistantExecutionCheckpointKind) || !ev.CheckID() || !ev.VerifySignature() {
		return domain.AssistantExecutionCheckpoint{}, errors.New("invalid assistant checkpoint event signature or kind")
	}
	if s.servicePubkey != "" && ev.PubKey.Hex() != s.servicePubkey {
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
	key, err := s.keys.TranscriptKey(ctx, env.KeyRef, env.KeyVersion)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	key, err = validateAssistantTranscriptKey(key)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	nonce, err := base64.RawStdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	cipher, err := base64.RawStdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	aead, err := chacha20poly1305.NewX(key.Key)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	adBytes, err := json.Marshal(env.AssociatedData)
	if err != nil {
		return domain.AssistantExecutionCheckpoint{}, err
	}
	plain, err := aead.Open(nil, nonce, cipher, adBytes)
	if err != nil {
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
	expected := checkpointAD(e, cp.PreviousEventID)
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
func newestAssistantCheckpoint(nodes []assistantCheckpointNode) (domain.AssistantExecution, string, error) {
	if len(nodes) == 0 {
		return domain.AssistantExecution{}, "", errors.New("no assistant checkpoint")
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
			return domain.AssistantExecution{}, "", errors.New("conflicting assistant checkpoint successors")
		}
		successors[pred] = node.id
	}
	rootID := successors[""]
	if rootID == "" {
		return domain.AssistantExecution{}, "", errors.New("assistant checkpoint root missing")
	}
	id := rootID
	visited := map[string]bool{}
	for {
		if visited[id] {
			return domain.AssistantExecution{}, "", errors.New("assistant checkpoint cycle")
		}
		visited[id] = true
		node := byID[id]
		if nextID, ok := successors[id]; ok {
			next := byID[nextID]
			if next.cp.Execution.Revision != node.cp.Execution.Revision+1 || next.cp.Execution.SessionID != node.cp.Execution.SessionID || next.cp.Execution.RunID != node.cp.Execution.RunID {
				return domain.AssistantExecution{}, "", errors.New("assistant checkpoint revision gap")
			}
			id = nextID
			continue
		}
		if len(visited) != len(byID) {
			return domain.AssistantExecution{}, "", errors.New("assistant checkpoint disconnected chain")
		}
		return node.cp.Execution, id, nil
	}
}

func (s *AssistantExecutionStore) Load(ctx context.Context, sessionID, runID string) (domain.AssistantExecution, string, error) {
	if s == nil || s.subscriber == nil || sessionID == "" || runID == "" {
		return domain.AssistantExecution{}, "", errors.New("assistant checkpoint query not configured")
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(domain.AssistantExecutionCheckpointKind)}, Tags: nostr.TagMap{domain.AssistantCheckpointTagDomain: []string{domain.AssistantDomain}, domain.AssistantCheckpointTagType: []string{domain.AssistantCheckpointTypeExecution}, domain.AssistantCheckpointTagSchema: []string{domain.AssistantExecutionCheckpointSchema}, domain.AssistantCheckpointTagSession: []string{sessionID}, domain.AssistantCheckpointTagRun: []string{runID}}}
	if s.servicePubkey != "" {
		author, err := nostr.PubKeyFromHex(s.servicePubkey)
		if err != nil {
			return domain.AssistantExecution{}, "", err
		}
		filter.Authors = []nostr.PubKey{author}
	}
	sub, err := s.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return domain.AssistantExecution{}, "", err
	}
	defer sub.Close()
	events := sub.EventChan()
	closed := sub.ClosedChan()
	eose := sub.EOSEChan()
	nodes := []assistantCheckpointNode{}
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return domain.AssistantExecution{}, "", ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return domain.AssistantExecution{}, "", fmt.Errorf("assistant checkpoint subscription closed: %s %s", c.RelayURL, c.Reason)
		case ev, ok := <-events:
			if !ok {
				return domain.AssistantExecution{}, "", errors.New("assistant checkpoint subscription ended before EOSE")
			}
			if ev == nil || seen[ev.ID.Hex()] {
				continue
			}
			seen[ev.ID.Hex()] = true
			cp, err := s.decode(ctx, ev, sessionID, runID)
			if err != nil {
				return domain.AssistantExecution{}, "", fmt.Errorf("checkpoint %s: %w", ev.ID.Hex(), err)
			}
			nodes = append(nodes, assistantCheckpointNode{id: ev.ID.Hex(), cp: cp})
		case <-eose:
			return newestAssistantCheckpoint(nodes)
		}
	}
}

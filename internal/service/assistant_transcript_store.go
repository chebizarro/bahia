package service

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	defaultAssistantTranscriptPublishTimeout = 10 * time.Second
	defaultAssistantTranscriptReplayLimit    = 200
	assistantTranscriptMaxFutureSkew         = 10 * time.Minute
	assistantTranscriptMaxPastAge            = 365 * 24 * time.Hour
)

// AssistantTranscriptKeyRef is how ciphertext names the key that sealed it;
// it is relay-visible (tags) and bound into the AEAD associated data.
type AssistantTranscriptKeyRef struct {
	Ref      string
	Version  string
	Rotation string
}

// AssistantTranscriptKeyProvider lends the cipher of a transcript key. The key
// bytes never leave the provider: fn receives an XChaCha20-Poly1305 AEAD built
// from them and must not retain it past the call.
type AssistantTranscriptKeyProvider interface {
	// WithActiveKey lends the key that seals new events.
	WithActiveKey(ctx context.Context, fn func(AssistantTranscriptKeyRef, cipher.AEAD) error) error
	// WithKey lends the key an event names by key_ref and key_version.
	WithKey(ctx context.Context, keyRef, keyVersion string, fn func(cipher.AEAD) error) error
}

// AssistantTranscriptKey is one key held in memory by a
// StaticAssistantTranscriptKeyProvider. Its holder clears Key when done.
type AssistantTranscriptKey struct {
	Ref      string
	Version  string
	Rotation string
	Key      []byte
}

// StaticAssistantTranscriptKeyProvider serves one key its holder loaded, such
// as the retired nsec-derived key the offline re-encryption tool opens history
// with.
type StaticAssistantTranscriptKeyProvider struct {
	Key AssistantTranscriptKey
}

func (p StaticAssistantTranscriptKeyProvider) WithActiveKey(_ context.Context, fn func(AssistantTranscriptKeyRef, cipher.AEAD) error) error {
	aead, err := p.cipher()
	if err != nil {
		return err
	}
	return fn(AssistantTranscriptKeyRef{Ref: p.Key.Ref, Version: p.Key.Version, Rotation: p.Key.Rotation}, aead)
}

func (p StaticAssistantTranscriptKeyProvider) WithKey(_ context.Context, keyRef, keyVersion string, fn func(cipher.AEAD) error) error {
	if p.Key.Ref != strings.TrimSpace(keyRef) {
		return fmt.Errorf("assistant transcript key %q is not available", keyRef)
	}
	if strings.TrimSpace(keyVersion) != "" && p.Key.Version != strings.TrimSpace(keyVersion) {
		return fmt.Errorf("assistant transcript key %q version %q is not available", keyRef, keyVersion)
	}
	aead, err := p.cipher()
	if err != nil {
		return err
	}
	return fn(aead)
}

func (p StaticAssistantTranscriptKeyProvider) cipher() (cipher.AEAD, error) {
	if strings.TrimSpace(p.Key.Ref) == "" {
		return nil, fmt.Errorf("assistant transcript key_ref is required")
	}
	if len(p.Key.Key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("assistant transcript key must be %d bytes", chacha20poly1305.KeySize)
	}
	return chacha20poly1305.NewX(p.Key.Key)
}

// AssistantTranscriptAppend describes one append-only assistant transcript
// message to encrypt and publish as kind 30316 on the deterministic coordinate
// assistantTranscriptDTag derives from it.
type AssistantTranscriptAppend struct {
	LogicalID      string
	SessionID      string
	TurnID         string
	RunID          string
	Sequence       int
	Message        domain.AssistantAgentMessage
	OperatorPubkey string
	Metadata       map[string]any
}

// AssistantTranscriptReplayQuery scopes historical transcript replay. SessionID
// is required; other fields narrow the subscription while preserving event-native
// backfill semantics through EOSE.
type AssistantTranscriptReplayQuery struct {
	SessionID string
	TurnID    string
	Roles     []domain.AssistantAgentMessageRole
	Limit     int
	Since     *time.Time
	Until     *time.Time
}

// AssistantTranscriptRecord is the decrypted transcript payload with the source
// relay event metadata used for ordering, cursors, and dedupe.
type AssistantTranscriptRecord struct {
	Payload   domain.AssistantTranscriptPayload
	EventID   string
	CreatedAt time.Time
	Pubkey    string
	DTag      string
}

// AssistantTranscriptStoreConfig wires the transcript store to the assistant's
// existing Nostr publisher/subscriber and signing identity.
type AssistantTranscriptStoreConfig struct {
	Publisher      AssistantEventPublisher
	Subscriber     AssistantRelaySubscriber
	Signer         nostr.Signer
	Identity       AssistantIdentity
	KeyProvider    AssistantTranscriptKeyProvider
	ServicePubkey  string
	ReplayLimit    int
	PublishTimeout time.Duration
	Now            func() time.Time
}

// AssistantTranscriptStore publishes and replays encrypted assistant transcript
// events. It is intentionally independent of app wiring so the agent loop can
// consume it without changing startup integration in item 6.
type AssistantTranscriptStore struct {
	publisher      AssistantEventPublisher
	subscriber     AssistantRelaySubscriber
	signer         nostr.Signer
	identity       AssistantIdentity
	keys           AssistantTranscriptKeyProvider
	servicePubkey  string
	replayLimit    int
	publishTimeout time.Duration
	now            func() time.Time
}

func NewAssistantTranscriptStore(config AssistantTranscriptStoreConfig) *AssistantTranscriptStore {
	replayLimit := config.ReplayLimit
	if replayLimit <= 0 {
		replayLimit = defaultAssistantTranscriptReplayLimit
	}
	publishTimeout := config.PublishTimeout
	if publishTimeout <= 0 {
		publishTimeout = defaultAssistantTranscriptPublishTimeout
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	servicePubkey := strings.TrimSpace(config.ServicePubkey)
	if servicePubkey == "" {
		servicePubkey = strings.TrimSpace(config.Identity.Pubkey)
	}
	return &AssistantTranscriptStore{
		publisher:      config.Publisher,
		subscriber:     config.Subscriber,
		signer:         config.Signer,
		identity:       config.Identity,
		keys:           config.KeyProvider,
		servicePubkey:  servicePubkey,
		replayLimit:    replayLimit,
		publishTimeout: publishTimeout,
		now:            now,
	}
}

func (s *AssistantTranscriptStore) AppendMessage(ctx context.Context, appendReq AssistantTranscriptAppend) (*AssistantTranscriptRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("assistant transcript store is nil")
	}
	if s.publisher == nil {
		return nil, fmt.Errorf("assistant transcript publisher is not configured")
	}
	if strings.TrimSpace(appendReq.SessionID) == "" {
		return nil, fmt.Errorf("assistant transcript session_id is required")
	}
	if appendReq.Sequence < 0 {
		return nil, fmt.Errorf("assistant transcript seq must not be negative")
	}
	if appendReq.Message.Role == "" {
		return nil, fmt.Errorf("assistant transcript message role is required")
	}
	if s.keys == nil {
		return nil, fmt.Errorf("assistant transcript key provider is not configured")
	}
	payload := domain.AssistantTranscriptPayload{
		SessionID: strings.TrimSpace(appendReq.SessionID),
		TurnID:    strings.TrimSpace(appendReq.TurnID),
		RunID:     strings.TrimSpace(appendReq.RunID),
		Sequence:  appendReq.Sequence,
		Message:   appendReq.Message,
		Metadata:  cloneAnyMap(appendReq.Metadata),
	}
	if appendReq.LogicalID != "" {
		if payload.Metadata == nil {
			payload.Metadata = map[string]any{}
		}
		payload.Metadata["logical_id"] = appendReq.LogicalID
	}
	var envelope domain.AssistantTranscriptAEADEnvelope
	var key AssistantTranscriptKeyRef
	if err := s.keys.WithActiveKey(ctx, func(ref AssistantTranscriptKeyRef, aead cipher.AEAD) error {
		key = ref
		var err error
		envelope, err = encryptAssistantTranscriptPayload(ctx, ref, aead, payload, assistantTranscriptAssociatedData(payload, ref))
		return err
	}); err != nil {
		return nil, fmt.Errorf("seal assistant transcript: %w", err)
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal assistant transcript envelope: %w", err)
	}

	tags := assistantTranscriptTags(payload, key, s.identity, strings.TrimSpace(appendReq.OperatorPubkey))
	ev := &nostr.Event{Kind: nostr.Kind(domain.KindAssistantTranscript), CreatedAt: nostr.Timestamp(s.now().Unix()), Tags: tags, Content: string(content)}
	if err := signGoNostrEvent(ctx, s.signer, ev); err != nil {
		return nil, fmt.Errorf("sign assistant transcript event: %w", err)
	}
	publishCtx, cancel := context.WithTimeout(ctx, s.publishTimeout)
	defer cancel()
	published, err := s.publisher.Publish(publishCtx, *ev)
	if err != nil {
		return nil, fmt.Errorf("publish assistant transcript event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish assistant transcript event: no relay accepted event")
	}
	return &AssistantTranscriptRecord{Payload: payload, EventID: ev.ID.Hex(), CreatedAt: ev.CreatedAt.Time().UTC(), Pubkey: ev.PubKey.Hex(), DTag: tagValue(ev.Tags, "d")}, nil
}

func (s *AssistantTranscriptStore) Replay(ctx context.Context, query AssistantTranscriptReplayQuery) ([]AssistantTranscriptRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("assistant transcript store is nil")
	}
	if s.subscriber == nil {
		return nil, fmt.Errorf("assistant transcript subscriber is not configured")
	}
	query.SessionID = strings.TrimSpace(query.SessionID)
	if query.SessionID == "" {
		return nil, fmt.Errorf("assistant transcript session_id is required")
	}
	filter, err := s.replayFilter(query)
	if err != nil {
		return nil, err
	}
	merged, err := s.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	defer merged.Close()

	seen := map[string]struct{}{}
	records := make([]AssistantTranscriptRecord, 0)
	eventsCh := merged.EventChan()
	closedCh := merged.ClosedChan()
	eoseCh := merged.EOSEChan()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case closed, ok := <-closedCh:
			if !ok {
				closedCh = nil
				continue
			}
			return nil, fmt.Errorf("assistant transcript replay subscription closed: relay=%s reason=%s", closed.RelayURL, closed.Reason)
		case ev, ok := <-eventsCh:
			if !ok {
				return truncateAssistantTranscriptRecords(dedupeAssistantTranscriptRecords(records), query.Limit), nil
			}
			record, err := s.decryptEvent(ctx, ev, query)
			if err != nil {
				return nil, err
			}
			if record == nil {
				continue
			}
			if _, dup := seen[record.EventID]; dup {
				continue
			}
			seen[record.EventID] = struct{}{}
			records = append(records, *record)
		case <-eoseCh:
			return truncateAssistantTranscriptRecords(dedupeAssistantTranscriptRecords(records), query.Limit), nil
		}
	}
}

// AppendMessageOnce publishes a logical transcript message at most once per
// logical identity. It performs a scoped EOSE-bounded backfill for the
// session; if the logical ID is already present the existing record is
// returned, otherwise the message is appended at the next session sequence.
// Replayed observations therefore never duplicate consumed transcript entries.
func (s *AssistantTranscriptStore) AppendMessageOnce(ctx context.Context, appendReq AssistantTranscriptAppend) (*AssistantTranscriptRecord, error) {
	if strings.TrimSpace(appendReq.LogicalID) == "" {
		return nil, fmt.Errorf("assistant transcript logical_id is required for idempotent append")
	}
	records, err := s.Replay(ctx, AssistantTranscriptReplayQuery{SessionID: appendReq.SessionID})
	if err != nil {
		return nil, err
	}
	next := 0
	for i := range records {
		if assistantTranscriptLogicalID(records[i]) == appendReq.LogicalID {
			existing := records[i]
			return &existing, nil
		}
		if records[i].Payload.Sequence >= next {
			next = records[i].Payload.Sequence + 1
		}
	}
	appendReq.Sequence = next
	return s.AppendMessage(ctx, appendReq)
}

func assistantTranscriptLogicalID(record AssistantTranscriptRecord) string {
	logical, _ := record.Payload.Metadata["logical_id"].(string)
	return logical
}

// dedupeAssistantTranscriptRecords collapses replayed copies of one message:
// first per addressable coordinate (several relays, or a relay that has not
// applied a replacement yet), then per logical id.
func dedupeAssistantTranscriptRecords(records []AssistantTranscriptRecord) []AssistantTranscriptRecord {
	return dedupeAssistantLogicalRecords(dedupeAssistantCoordinateRecords(records))
}

// dedupeAssistantCoordinateRecords keeps the NIP-01 winner per d tag: the
// newest created_at, ties broken by the lowest event id.
func dedupeAssistantCoordinateRecords(records []AssistantTranscriptRecord) []AssistantTranscriptRecord {
	chosen := map[string]int{}
	out := make([]AssistantTranscriptRecord, 0, len(records))
	for _, record := range records {
		if record.DTag == "" {
			out = append(out, record)
			continue
		}
		idx, ok := chosen[record.DTag]
		if !ok {
			chosen[record.DTag] = len(out)
			out = append(out, record)
			continue
		}
		current := out[idx]
		if record.CreatedAt.After(current.CreatedAt) || (record.CreatedAt.Equal(current.CreatedAt) && record.EventID < current.EventID) {
			out[idx] = record
		}
	}
	return out
}

// dedupeAssistantLogicalRecords keeps one record per logical ID, choosing the
// earliest by (sequence, created_at, event ID) so the result does not depend
// on relay arrival order.
func dedupeAssistantLogicalRecords(records []AssistantTranscriptRecord) []AssistantTranscriptRecord {
	chosen := map[string]int{}
	out := make([]AssistantTranscriptRecord, 0, len(records))
	for _, record := range records {
		logical := assistantTranscriptLogicalID(record)
		if logical == "" {
			out = append(out, record)
			continue
		}
		idx, ok := chosen[logical]
		if !ok {
			chosen[logical] = len(out)
			out = append(out, record)
			continue
		}
		if assistantTranscriptRecordBefore(record, out[idx]) {
			out[idx] = record
		}
	}
	return out
}

func assistantTranscriptRecordBefore(a, b AssistantTranscriptRecord) bool {
	if a.Payload.Sequence != b.Payload.Sequence {
		return a.Payload.Sequence < b.Payload.Sequence
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.EventID < b.EventID
}

func (s *AssistantTranscriptStore) BuildModelHistory(ctx context.Context, sessionID string, limit int) ([]domain.AssistantAgentMessage, error) {
	records, err := s.Replay(ctx, AssistantTranscriptReplayQuery{SessionID: sessionID, Limit: limit})
	if err != nil {
		return nil, err
	}
	messages := make([]domain.AssistantAgentMessage, 0, len(records))
	for _, record := range records {
		messages = append(messages, record.Payload.Message)
	}
	return messages, nil
}

// replayFilter scopes a replay REQ to one session by its single-letter "t"
// topic (kinds.AssistantTranscriptSessionTopic). NIP-01 relays index only
// single-letter tags, so schema, domain, turn and role are checked locally in
// decryptEvent rather than sent as #schema/#domain/#session/#turn/#role.
func (s *AssistantTranscriptStore) replayFilter(query AssistantTranscriptReplayQuery) (nostr.Filter, error) {
	query.TurnID = strings.TrimSpace(query.TurnID)
	tags := nostr.TagMap{"t": []string{kinds.AssistantTranscriptSessionTopic(query.SessionID)}}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(domain.KindAssistantTranscript)}, Tags: tags, Limit: s.relayLimit(query.Limit)}
	if query.Since != nil && !query.Since.IsZero() {
		filter.Since = nostr.Timestamp(query.Since.UTC().Unix())
	}
	if query.Until != nil && !query.Until.IsZero() {
		filter.Until = nostr.Timestamp(query.Until.UTC().Unix())
	}
	if s.servicePubkey != "" {
		pubkey, err := nostr.PubKeyFromHex(s.servicePubkey)
		if err != nil {
			return nostr.Filter{}, fmt.Errorf("decode assistant transcript service pubkey: %w", err)
		}
		filter.Authors = []nostr.PubKey{pubkey}
	}
	return filter, nil
}

func (s *AssistantTranscriptStore) relayLimit(queryLimit int) int {
	if queryLimit > 0 {
		return queryLimit
	}
	if s.replayLimit > 0 {
		return s.replayLimit
	}
	return defaultAssistantTranscriptReplayLimit
}

func (s *AssistantTranscriptStore) decryptEvent(ctx context.Context, ev *nostr.Event, query AssistantTranscriptReplayQuery) (*AssistantTranscriptRecord, error) {
	if err := validateAssistantTranscriptEvent(ev, s.now()); err != nil {
		return nil, fmt.Errorf("validate assistant transcript event: %w", err)
	}
	if ev.Kind != nostr.Kind(domain.KindAssistantTranscript) {
		return nil, nil
	}
	if tagValue(ev.Tags, domain.AssistantTranscriptTagSchema) != domain.AssistantTranscriptSchema || tagValue(ev.Tags, domain.AssistantTranscriptTagDomain) != domain.AssistantDomain {
		return nil, nil
	}
	if tagValue(ev.Tags, domain.AssistantTranscriptTagSession) != query.SessionID {
		return nil, nil
	}
	if query.TurnID != "" && tagValue(ev.Tags, domain.AssistantTranscriptTagTurn) != query.TurnID {
		return nil, nil
	}
	if len(query.Roles) > 0 && !roleAllowed(tagValue(ev.Tags, domain.AssistantTranscriptTagRole), query.Roles) {
		return nil, nil
	}

	var envelope domain.AssistantTranscriptAEADEnvelope
	if err := json.Unmarshal([]byte(ev.Content), &envelope); err != nil {
		return nil, fmt.Errorf("decode assistant transcript envelope %s: %w", ev.ID.Hex(), err)
	}
	if envelope.Schema != domain.AssistantTranscriptSchema || envelope.Envelope != domain.AssistantTranscriptEnvelopeServiceHeldAEAD || envelope.Algorithm != domain.AssistantTranscriptAEADAlgorithmXChaCha20 {
		return nil, fmt.Errorf("assistant transcript envelope %s has unsupported schema/envelope/algorithm", ev.ID.Hex())
	}
	if s.keys == nil {
		return nil, fmt.Errorf("assistant transcript key provider is not configured")
	}
	var payload domain.AssistantTranscriptPayload
	if err := s.keys.WithKey(ctx, envelope.KeyRef, envelope.KeyVersion, func(aead cipher.AEAD) error {
		var err error
		payload, err = decryptAssistantTranscriptPayload(aead, envelope)
		return err
	}); err != nil {
		return nil, fmt.Errorf("decrypt assistant transcript event %s: %w", ev.ID.Hex(), err)
	}
	if err := validateAssistantTranscriptPayloadAgainstTags(payload, ev.Tags, envelope); err != nil {
		return nil, fmt.Errorf("assistant transcript event %s tag/content mismatch: %w", ev.ID.Hex(), err)
	}
	return &AssistantTranscriptRecord{Payload: payload, EventID: ev.ID.Hex(), CreatedAt: ev.CreatedAt.Time().UTC(), Pubkey: ev.PubKey.Hex(), DTag: tagValue(ev.Tags, "d")}, nil
}

func encryptAssistantTranscriptPayload(ctx context.Context, key AssistantTranscriptKeyRef, aead cipher.AEAD, payload domain.AssistantTranscriptPayload, ad map[string]string) (domain.AssistantTranscriptAEADEnvelope, error) {
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return domain.AssistantTranscriptAEADEnvelope{}, fmt.Errorf("marshal assistant transcript payload: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return domain.AssistantTranscriptAEADEnvelope{}, fmt.Errorf("generate assistant transcript nonce: %w", err)
	}
	adBytes, err := json.Marshal(ad)
	if err != nil {
		return domain.AssistantTranscriptAEADEnvelope{}, fmt.Errorf("marshal assistant transcript associated data: %w", err)
	}
	select {
	case <-ctx.Done():
		return domain.AssistantTranscriptAEADEnvelope{}, ctx.Err()
	default:
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, adBytes)
	return domain.AssistantTranscriptAEADEnvelope{
		Schema:         domain.AssistantTranscriptSchema,
		Envelope:       domain.AssistantTranscriptEnvelopeServiceHeldAEAD,
		Algorithm:      domain.AssistantTranscriptAEADAlgorithmXChaCha20,
		KeyRef:         key.Ref,
		KeyVersion:     key.Version,
		KeyRotation:    key.Rotation,
		Nonce:          base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:     base64.RawStdEncoding.EncodeToString(ciphertext),
		AssociatedData: ad,
	}, nil
}

func decryptAssistantTranscriptPayload(aead cipher.AEAD, envelope domain.AssistantTranscriptAEADEnvelope) (domain.AssistantTranscriptPayload, error) {
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("decode ciphertext: %w", err)
	}
	adBytes, err := json.Marshal(envelope.AssociatedData)
	if err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("marshal assistant transcript associated data: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, adBytes)
	if err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("open AEAD envelope: %w", err)
	}
	var payload domain.AssistantTranscriptPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("decode assistant transcript payload: %w", err)
	}
	return payload, nil
}

func assistantTranscriptAssociatedData(payload domain.AssistantTranscriptPayload, key AssistantTranscriptKeyRef) map[string]string {
	ad := map[string]string{
		domain.AssistantTranscriptTagSchema:   domain.AssistantTranscriptSchema,
		domain.AssistantTranscriptTagDomain:   domain.AssistantDomain,
		domain.AssistantTranscriptTagSession:  payload.SessionID,
		domain.AssistantTranscriptTagRole:     string(payload.Message.Role),
		domain.AssistantTranscriptTagSequence: strconv.Itoa(payload.Sequence),
		domain.AssistantTranscriptTagKeyRef:   key.Ref,
		domain.AssistantTranscriptTagEnvelope: domain.AssistantTranscriptEnvelopeServiceHeldAEAD,
	}
	if payload.TurnID != "" {
		ad[domain.AssistantTranscriptTagTurn] = payload.TurnID
	}
	if key.Version != "" {
		ad[domain.AssistantTranscriptTagKeyVersion] = key.Version
	}
	if key.Rotation != "" {
		ad[domain.AssistantTranscriptTagKeyRotation] = key.Rotation
	}
	return ad
}

// assistantTranscriptDTag is the deterministic coordinate of one transcript
// message: <schema>:<session>:msg:<logical id>, or
// <schema>:<session>:seq:<sequence> for a message without a logical id. A
// retried publish of the same message lands on the same addressable
// coordinate and replaces the earlier copy instead of adding a duplicate. The
// logical id is preferred over the sequence because AppendMessageOnce derives
// the sequence from a replay, so a retry can compute a different one, and two
// concurrent appends can compute the same one for different messages.
func assistantTranscriptDTag(payload domain.AssistantTranscriptPayload) string {
	prefix := domain.AssistantTranscriptDTagPrefix + payload.SessionID
	if logicalID, ok := payload.Metadata["logical_id"].(string); ok && logicalID != "" {
		return prefix + ":msg:" + logicalID
	}
	return prefix + fmt.Sprintf(":seq:%020d", payload.Sequence)
}

func assistantTranscriptTags(payload domain.AssistantTranscriptPayload, key AssistantTranscriptKeyRef, identity AssistantIdentity, operatorPubkey string) nostr.Tags {
	tags := nostr.Tags{
		{"d", assistantTranscriptDTag(payload)},
		{domain.AssistantTranscriptTagSchema, domain.AssistantTranscriptSchema},
		{domain.AssistantTranscriptTagDomain, domain.AssistantDomain},
		{domain.AssistantTranscriptTagSession, payload.SessionID},
		{domain.AssistantTranscriptTagRole, string(payload.Message.Role)},
		{domain.AssistantTranscriptTagSequence, strconv.Itoa(payload.Sequence)},
		{domain.AssistantTranscriptTagKeyRef, key.Ref},
		{domain.AssistantTranscriptTagEnvelope, domain.AssistantTranscriptEnvelopeServiceHeldAEAD},
		{"t", kinds.AssistantTranscriptTopic},
		{"t", kinds.AssistantTranscriptSessionTopic(payload.SessionID)},
	}
	if payload.TurnID != "" {
		tags = append(tags, nostr.Tag{domain.AssistantTranscriptTagTurn, payload.TurnID})
	}
	if key.Version != "" {
		tags = append(tags, nostr.Tag{domain.AssistantTranscriptTagKeyVersion, key.Version})
	}
	if key.Rotation != "" {
		tags = append(tags, nostr.Tag{domain.AssistantTranscriptTagKeyRotation, key.Rotation})
	}
	if identity.AgentID != "" {
		tags = append(tags, nostr.Tag{"agent", identity.AgentID})
	}
	if operatorPubkey != "" {
		tags = append(tags, nostr.Tag{"p", operatorPubkey, "", "operator"})
	}
	return tags
}

func validateAssistantTranscriptEvent(ev *nostr.Event, now time.Time) error {
	if ev == nil {
		return fmt.Errorf("nil event")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ev.ID.Hex() == "" || ev.PubKey.Hex() == "" || ev.Sig == ([64]byte{}) {
		return fmt.Errorf("event id, pubkey, and signature are required")
	}
	if ev.CreatedAt <= 0 {
		return fmt.Errorf("created_at is required")
	}
	createdAt := ev.CreatedAt.Time()
	if createdAt.After(now.Add(assistantTranscriptMaxFutureSkew)) {
		return fmt.Errorf("created_at too far in future")
	}
	if createdAt.Before(now.Add(-assistantTranscriptMaxPastAge)) {
		return fmt.Errorf("created_at too far in past")
	}
	for i, tag := range ev.Tags {
		if len(tag) == 0 || strings.TrimSpace(tag[0]) == "" {
			return fmt.Errorf("tag %d has empty key", i)
		}
	}
	if !ev.CheckID() {
		return fmt.Errorf("event id does not match serialized event")
	}
	if !ev.VerifySignature() {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func validateAssistantTranscriptPayloadAgainstTags(payload domain.AssistantTranscriptPayload, tags nostr.Tags, envelope domain.AssistantTranscriptAEADEnvelope) error {
	checks := map[string]string{
		domain.AssistantTranscriptTagSchema:   domain.AssistantTranscriptSchema,
		domain.AssistantTranscriptTagDomain:   domain.AssistantDomain,
		domain.AssistantTranscriptTagSession:  payload.SessionID,
		domain.AssistantTranscriptTagRole:     string(payload.Message.Role),
		domain.AssistantTranscriptTagSequence: strconv.Itoa(payload.Sequence),
		domain.AssistantTranscriptTagKeyRef:   envelope.KeyRef,
		domain.AssistantTranscriptTagEnvelope: envelope.Envelope,
	}
	if payload.TurnID != "" {
		checks[domain.AssistantTranscriptTagTurn] = payload.TurnID
	}
	if envelope.KeyVersion != "" {
		checks[domain.AssistantTranscriptTagKeyVersion] = envelope.KeyVersion
	}
	if envelope.KeyRotation != "" {
		checks[domain.AssistantTranscriptTagKeyRotation] = envelope.KeyRotation
	}
	for key, want := range checks {
		if got := tagValue(tags, key); got != want {
			return fmt.Errorf("tag %s = %q, want %q", key, got, want)
		}
		if envelope.AssociatedData != nil && envelope.AssociatedData[key] != want {
			return fmt.Errorf("associated_data %s = %q, want %q", key, envelope.AssociatedData[key], want)
		}
	}
	return nil
}

func truncateAssistantTranscriptRecords(records []AssistantTranscriptRecord, limit int) []AssistantTranscriptRecord {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Payload.Sequence != records[j].Payload.Sequence {
			return records[i].Payload.Sequence < records[j].Payload.Sequence
		}
		if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].CreatedAt.Before(records[j].CreatedAt)
		}
		return records[i].EventID < records[j].EventID
	})
	if limit > 0 && len(records) > limit {
		records = records[len(records)-limit:]
	}
	out := make([]AssistantTranscriptRecord, len(records))
	copy(out, records)
	return out
}

func roleAllowed(role string, allowed []domain.AssistantAgentMessageRole) bool {
	for _, item := range allowed {
		if role == string(item) {
			return true
		}
	}
	return false
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

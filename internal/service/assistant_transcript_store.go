package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	defaultAssistantTranscriptPublishTimeout = 10 * time.Second
	defaultAssistantTranscriptReplayLimit    = 200
	assistantTranscriptMaxFutureSkew         = 10 * time.Minute
	assistantTranscriptMaxPastAge            = 365 * 24 * time.Hour
)

// AssistantTranscriptKey is a service-held symmetric transcript key plus the
// relay-visible reference metadata required to decrypt historical transcript
// events after reconnect, restart, or a new operator session.
type AssistantTranscriptKey struct {
	Ref      string
	Version  string
	Rotation string
	Key      []byte
}

// AssistantTranscriptKeyProvider resolves the active service-held symmetric key
// for new transcript events and historical keys for replay.
type AssistantTranscriptKeyProvider interface {
	ActiveTranscriptKey(ctx context.Context) (AssistantTranscriptKey, error)
	TranscriptKey(ctx context.Context, keyRef, keyVersion string) (AssistantTranscriptKey, error)
}

// StaticAssistantTranscriptKeyProvider is a small production-safe building block
// for deployments that load one service-held transcript key from configuration
// or a secret manager before constructing the store.
type StaticAssistantTranscriptKeyProvider struct {
	Key AssistantTranscriptKey
}

func (p StaticAssistantTranscriptKeyProvider) ActiveTranscriptKey(context.Context) (AssistantTranscriptKey, error) {
	return validateAssistantTranscriptKey(p.Key)
}

func (p StaticAssistantTranscriptKeyProvider) TranscriptKey(_ context.Context, keyRef, keyVersion string) (AssistantTranscriptKey, error) {
	key, err := validateAssistantTranscriptKey(p.Key)
	if err != nil {
		return AssistantTranscriptKey{}, err
	}
	if key.Ref != strings.TrimSpace(keyRef) {
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key %q is not available", keyRef)
	}
	if strings.TrimSpace(keyVersion) != "" && key.Version != strings.TrimSpace(keyVersion) {
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key %q version %q is not available", keyRef, keyVersion)
	}
	return key, nil
}

// AssistantTranscriptAppend describes one append-only assistant transcript
// message to encrypt and publish as kind 30316.
type AssistantTranscriptAppend struct {
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
	Signer         canonicalnostr.Signer
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
	signer         canonicalnostr.Signer
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
	key, err := s.activeKey(ctx)
	if err != nil {
		return nil, err
	}

	payload := domain.AssistantTranscriptPayload{
		SessionID: strings.TrimSpace(appendReq.SessionID),
		TurnID:    strings.TrimSpace(appendReq.TurnID),
		RunID:     strings.TrimSpace(appendReq.RunID),
		Sequence:  appendReq.Sequence,
		Message:   appendReq.Message,
		Metadata:  cloneAnyMap(appendReq.Metadata),
	}
	ad := assistantTranscriptAssociatedData(payload, key)
	envelope, err := encryptAssistantTranscriptPayload(ctx, key, payload, ad)
	if err != nil {
		return nil, err
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
				return truncateAssistantTranscriptRecords(records, query.Limit), nil
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
			return truncateAssistantTranscriptRecords(records, query.Limit), nil
		}
	}
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

func (s *AssistantTranscriptStore) activeKey(ctx context.Context) (AssistantTranscriptKey, error) {
	if s.keys == nil {
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key provider is not configured")
	}
	key, err := s.keys.ActiveTranscriptKey(ctx)
	if err != nil {
		return AssistantTranscriptKey{}, fmt.Errorf("resolve active assistant transcript key: %w", err)
	}
	return validateAssistantTranscriptKey(key)
}

func (s *AssistantTranscriptStore) replayKey(ctx context.Context, keyRef, keyVersion string) (AssistantTranscriptKey, error) {
	if s.keys == nil {
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key provider is not configured")
	}
	key, err := s.keys.TranscriptKey(ctx, keyRef, keyVersion)
	if err != nil {
		return AssistantTranscriptKey{}, fmt.Errorf("resolve assistant transcript key %q: %w", keyRef, err)
	}
	return validateAssistantTranscriptKey(key)
}

func (s *AssistantTranscriptStore) replayFilter(query AssistantTranscriptReplayQuery) (nostr.Filter, error) {
	tags := nostr.TagMap{
		domain.AssistantTranscriptTagSchema:  []string{domain.AssistantTranscriptSchema},
		domain.AssistantTranscriptTagDomain:  []string{domain.AssistantDomain},
		domain.AssistantTranscriptTagSession: []string{query.SessionID},
	}
	if query.TurnID = strings.TrimSpace(query.TurnID); query.TurnID != "" {
		tags[domain.AssistantTranscriptTagTurn] = []string{query.TurnID}
	}
	roles := make([]string, 0, len(query.Roles))
	for _, role := range query.Roles {
		if role = domain.AssistantAgentMessageRole(strings.TrimSpace(string(role))); role != "" {
			roles = append(roles, string(role))
		}
	}
	if len(roles) > 0 {
		tags[domain.AssistantTranscriptTagRole] = roles
	}
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
	key, err := s.replayKey(ctx, envelope.KeyRef, envelope.KeyVersion)
	if err != nil {
		return nil, err
	}
	payload, err := decryptAssistantTranscriptPayload(key, envelope)
	if err != nil {
		return nil, fmt.Errorf("decrypt assistant transcript event %s: %w", ev.ID.Hex(), err)
	}
	if err := validateAssistantTranscriptPayloadAgainstTags(payload, ev.Tags, envelope); err != nil {
		return nil, fmt.Errorf("assistant transcript event %s tag/content mismatch: %w", ev.ID.Hex(), err)
	}
	return &AssistantTranscriptRecord{Payload: payload, EventID: ev.ID.Hex(), CreatedAt: ev.CreatedAt.Time().UTC(), Pubkey: ev.PubKey.Hex(), DTag: tagValue(ev.Tags, "d")}, nil
}

func validateAssistantTranscriptKey(key AssistantTranscriptKey) (AssistantTranscriptKey, error) {
	key.Ref = strings.TrimSpace(key.Ref)
	key.Version = strings.TrimSpace(key.Version)
	key.Rotation = strings.TrimSpace(key.Rotation)
	if key.Ref == "" {
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key_ref is required")
	}
	if len(key.Key) != chacha20poly1305.KeySize {
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key must be %d bytes", chacha20poly1305.KeySize)
	}
	key.Key = append([]byte(nil), key.Key...)
	return key, nil
}

func encryptAssistantTranscriptPayload(ctx context.Context, key AssistantTranscriptKey, payload domain.AssistantTranscriptPayload, ad map[string]string) (domain.AssistantTranscriptAEADEnvelope, error) {
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return domain.AssistantTranscriptAEADEnvelope{}, fmt.Errorf("marshal assistant transcript payload: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key.Key)
	if err != nil {
		return domain.AssistantTranscriptAEADEnvelope{}, fmt.Errorf("create assistant transcript AEAD: %w", err)
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

func decryptAssistantTranscriptPayload(key AssistantTranscriptKey, envelope domain.AssistantTranscriptAEADEnvelope) (domain.AssistantTranscriptPayload, error) {
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("decode ciphertext: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key.Key)
	if err != nil {
		return domain.AssistantTranscriptPayload{}, fmt.Errorf("create assistant transcript AEAD: %w", err)
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

func assistantTranscriptAssociatedData(payload domain.AssistantTranscriptPayload, key AssistantTranscriptKey) map[string]string {
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

func assistantTranscriptTags(payload domain.AssistantTranscriptPayload, key AssistantTranscriptKey, identity AssistantIdentity, operatorPubkey string) nostr.Tags {
	dTagParts := []string{domain.AssistantTranscriptDTagPrefix + payload.SessionID, fmt.Sprintf("%020d", payload.Sequence)}
	if payload.TurnID != "" {
		dTagParts = append(dTagParts, payload.TurnID)
	}
	dTagParts = append(dTagParts, uuid.NewString())
	tags := nostr.Tags{
		{"d", strings.Join(dTagParts, ":")},
		{domain.AssistantTranscriptTagSchema, domain.AssistantTranscriptSchema},
		{domain.AssistantTranscriptTagDomain, domain.AssistantDomain},
		{domain.AssistantTranscriptTagSession, payload.SessionID},
		{domain.AssistantTranscriptTagRole, string(payload.Message.Role)},
		{domain.AssistantTranscriptTagSequence, strconv.Itoa(payload.Sequence)},
		{domain.AssistantTranscriptTagKeyRef, key.Ref},
		{domain.AssistantTranscriptTagEnvelope, domain.AssistantTranscriptEnvelopeServiceHeldAEAD},
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

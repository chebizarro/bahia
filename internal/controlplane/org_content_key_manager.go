package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// OCKEnvelopePublisher is the publishing interface the OCKManager uses to
// emit key-envelope records. It is implemented by the nostr adapter's shared
// cp-state publishing pipeline. Defined here to keep controlplane independent
// of the adapter package.
type OCKEnvelopePublisher interface {
	// PublishKeyEnvelope publishes a key-envelope record through the shared
	// cp-state signing/outbox pipeline with acceptance confirmation.
	PublishKeyEnvelope(ctx context.Context, dTag string, content string) error
}

// OCKEnvelopeHistory reads historical key-envelope records for OCK recovery.
type OCKEnvelopeHistory interface {
	// FindKeyEnvelopes returns key-envelope records for the given org, filtered
	// by the t topic tag. Returns content strings to decrypt.
	FindKeyEnvelopes(ctx context.Context, orgID string) ([]domain.KeyEnvelopeRecord, error)
}

// OCKMemberSource provides the current member set for an org for key wrapping.
type OCKMemberSource interface {
	// OrgMemberPubkeys returns the pubkeys of all current members of the org.
	OrgMemberPubkeys(ctx context.Context, orgID string) ([]string, error)
}

// OCKManager manages per-org content keys (OCKs): creation, wrapping to
// members via NIP-44 through the signer interface, rotation on membership
// changes, and recovery from persisted service-wrapped envelopes.
type OCKManager struct {
	signer        canonicalnostr.Keyer
	servicePubkey string
	publisher     OCKEnvelopePublisher
	history       OCKEnvelopeHistory
	members       OCKMemberSource
	logger        *zap.Logger

	mu    sync.RWMutex
	cache map[string]*orgKeyState // orgID → key state
}

// orgKeyState is the cached key state for one org.
type orgKeyState struct {
	current  OrgContentKey
	versions map[int]OrgContentKey // all known versions for decrypt
}

// OCKManagerConfig configures the OCK manager.
type OCKManagerConfig struct {
	Signer        canonicalnostr.Keyer
	ServicePubkey string
	Publisher     OCKEnvelopePublisher
	History       OCKEnvelopeHistory
	Members       OCKMemberSource
	Logger        *zap.Logger
}

// NewOCKManager creates a new OCK manager. All NIP-44 operations go through
// the signer interface so bunker signers work.
func NewOCKManager(cfg OCKManagerConfig) *OCKManager {
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	return &OCKManager{
		signer:        cfg.Signer,
		servicePubkey: cfg.ServicePubkey,
		publisher:     cfg.Publisher,
		history:       cfg.History,
		members:       cfg.Members,
		logger:        cfg.Logger.Named("ock-manager"),
		cache:         make(map[string]*orgKeyState),
	}
}

// GetKey returns the current OCK for the given org. If no key is cached, it
// attempts recovery from persisted service envelopes.
func (m *OCKManager) GetKey(ctx context.Context, orgID string) (OrgContentKey, error) {
	m.mu.RLock()
	if state, ok := m.cache[orgID]; ok {
		key := state.current
		m.mu.RUnlock()
		return key, nil
	}
	m.mu.RUnlock()

	// Try to recover from history.
	return m.recoverOrCreate(ctx, orgID)
}

// GetKeyByVersion returns a specific version of the OCK for decrypt of
// historical records.
func (m *OCKManager) GetKeyByVersion(ctx context.Context, orgID string, version int) (OrgContentKey, error) {
	m.mu.RLock()
	if state, ok := m.cache[orgID]; ok {
		if key, ok := state.versions[version]; ok {
			m.mu.RUnlock()
			return key, nil
		}
	}
	m.mu.RUnlock()

	// Try to recover this version from history.
	if err := m.recoverFromHistory(ctx, orgID); err != nil {
		return OrgContentKey{}, fmt.Errorf("recover OCK version %d for org %s: %w", version, orgID, err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if state, ok := m.cache[orgID]; ok {
		if key, ok := state.versions[version]; ok {
			return key, nil
		}
	}
	return OrgContentKey{}, fmt.Errorf("OCK version %d for org %s not found", version, orgID)
}

// EnsureKey ensures an OCK exists for the org, creating and distributing one
// if necessary. Returns the current OCK.
func (m *OCKManager) EnsureKey(ctx context.Context, orgID string) (OrgContentKey, error) {
	key, err := m.GetKey(ctx, orgID)
	if err == nil {
		return key, nil
	}

	// Create a new key and wrap to all current members.
	return m.createAndDistribute(ctx, orgID, 1)
}

// RotateKey creates a new OCK version for the org, wraps it to the current
// member set (excluding removed members), and activates it. Old versions
// remain available for historical reads.
func (m *OCKManager) RotateKey(ctx context.Context, orgID string) (OrgContentKey, error) {
	// Recover the current version before choosing the successor. A fresh daemon
	// must not reuse v1 when only the service envelope is in history.
	if _, err := m.GetKey(ctx, orgID); err != nil {
		return OrgContentKey{}, err
	}
	m.mu.RLock()
	nextVersion := 1
	if state, ok := m.cache[orgID]; ok {
		nextVersion = state.current.Version + 1
	}
	m.mu.RUnlock()

	return m.createAndDistribute(ctx, orgID, nextVersion)
}

// ServiceEncrypt encrypts plaintext to the service pubkey via NIP-44 through
// the signer interface. Used for service-inner fields.
func (m *OCKManager) ServiceEncrypt(ctx context.Context, plaintext string) (string, error) {
	pubkey, err := canonicalnostr.PubKeyFromHex(m.servicePubkey)
	if err != nil {
		return "", fmt.Errorf("parse service pubkey: %w", err)
	}
	return m.signer.Encrypt(ctx, plaintext, pubkey)
}

// ServiceDecrypt decrypts a NIP-44 ciphertext from the service pubkey.
func (m *OCKManager) ServiceDecrypt(ctx context.Context, ciphertext string) (string, error) {
	pubkey, err := canonicalnostr.PubKeyFromHex(m.servicePubkey)
	if err != nil {
		return "", fmt.Errorf("parse service pubkey: %w", err)
	}
	return m.signer.Decrypt(ctx, ciphertext, pubkey)
}

// WrapForRecipient wraps the current OCK for the given org to a specific
// recipient pubkey and publishes the envelope. Used when a new member is added
// to give them immediate access to existing records encrypted under the
// current OCK version. If no key exists for the org yet, this is a no-op
// (the next EncryptConfidential call will create and distribute the key).
func (m *OCKManager) WrapForRecipient(ctx context.Context, orgID string, recipientPubkey string) error {
	m.mu.RLock()
	state, ok := m.cache[orgID]
	m.mu.RUnlock()
	if !ok {
		// No key exists yet — EnsureKey at next encrypt will distribute to all members.
		return nil
	}
	return m.wrapAndPublish(ctx, state.current, recipientPubkey)
}

func (m *OCKManager) recoverOrCreate(ctx context.Context, orgID string) (OrgContentKey, error) {
	if err := m.recoverFromHistory(ctx, orgID); err != nil {
		m.logger.Debug("OCK history recovery failed, will create new",
			zap.String("org_id", orgID), zap.Error(err))
	}

	m.mu.RLock()
	if state, ok := m.cache[orgID]; ok {
		key := state.current
		m.mu.RUnlock()
		return key, nil
	}
	m.mu.RUnlock()

	return m.createAndDistribute(ctx, orgID, 1)
}

func (m *OCKManager) recoverFromHistory(ctx context.Context, orgID string) error {
	if m.history == nil {
		return fmt.Errorf("no OCK history available")
	}
	records, err := m.history.FindKeyEnvelopes(ctx, orgID)
	if err != nil {
		return fmt.Errorf("query OCK history: %w", err)
	}
	if len(records) == 0 {
		return fmt.Errorf("no OCK envelopes found for org %s", orgID)
	}

	servicePubkey, err := canonicalnostr.PubKeyFromHex(m.servicePubkey)
	if err != nil {
		return fmt.Errorf("parse service pubkey: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.cache[orgID]
	if !ok {
		state = &orgKeyState{versions: make(map[int]OrgContentKey)}
		m.cache[orgID] = state
	}

	for _, rec := range records {
		// Try to decrypt as the service recipient.
		plaintext, err := m.signer.Decrypt(ctx, rec.Content, servicePubkey)
		if err != nil {
			continue // Not our envelope.
		}
		key, recipientPubkey, err := UnmarshalOCKWrap([]byte(plaintext))
		if err != nil {
			m.logger.Debug("invalid OCK wrap payload", zap.Error(err))
			continue
		}
		if recipientPubkey != m.servicePubkey {
			continue // Not for us.
		}
		if key.OrgID != orgID {
			continue // Wrong org.
		}
		state.versions[key.Version] = key
		if key.Version > state.current.Version {
			state.current = key
		}
	}

	if state.current.Version == 0 {
		return fmt.Errorf("no service-decryptable OCK envelopes for org %s", orgID)
	}
	return nil
}

func (m *OCKManager) createAndDistribute(ctx context.Context, orgID string, version int) (OrgContentKey, error) {
	key, err := GenerateOrgContentKey(orgID, version)
	if err != nil {
		return OrgContentKey{}, err
	}

	// Get current member set.
	recipients := []string{m.servicePubkey}
	if m.members != nil {
		memberPubkeys, err := m.members.OrgMemberPubkeys(ctx, orgID)
		if err != nil {
			m.logger.Warn("failed to get org members for OCK distribution",
				zap.String("org_id", orgID), zap.Error(err))
		} else {
			seen := map[string]bool{m.servicePubkey: true}
			for _, pk := range memberPubkeys {
				if !seen[pk] {
					recipients = append(recipients, pk)
					seen[pk] = true
				}
			}
		}
	}

	// Wrap to each recipient and publish.
	for _, recipientPubkey := range recipients {
		if err := m.wrapAndPublish(ctx, key, recipientPubkey); err != nil {
			// Service envelope is critical — fail if service wrap fails.
			if recipientPubkey == m.servicePubkey {
				return OrgContentKey{}, fmt.Errorf("publish service OCK envelope: %w", err)
			}
			m.logger.Warn("failed to publish OCK envelope for member",
				zap.String("org_id", orgID),
				zap.String("recipient", recipientPubkey[:8]+"..."),
				zap.Error(err))
		}
	}

	// Cache the new key.
	m.mu.Lock()
	state, ok := m.cache[orgID]
	if !ok {
		state = &orgKeyState{versions: make(map[int]OrgContentKey)}
		m.cache[orgID] = state
	}
	state.current = key
	state.versions[version] = key
	m.mu.Unlock()

	m.logger.Info("OCK created and distributed",
		zap.String("org_id", orgID),
		zap.Int("version", version),
		zap.Int("recipients", len(recipients)))
	return key, nil
}

func (m *OCKManager) wrapAndPublish(ctx context.Context, key OrgContentKey, recipientPubkey string) error {
	wrapJSON, err := MarshalOCKWrap(key, recipientPubkey)
	if err != nil {
		return fmt.Errorf("marshal OCK wrap: %w", err)
	}

	recipientPK, err := canonicalnostr.PubKeyFromHex(recipientPubkey)
	if err != nil {
		return fmt.Errorf("parse recipient pubkey: %w", err)
	}

	encrypted, err := m.signer.Encrypt(ctx, string(wrapJSON), recipientPK)
	if err != nil {
		return fmt.Errorf("NIP-44 encrypt OCK wrap: %w", err)
	}

	// Generate a random handle for the d-tag so the recipient pubkey is
	// opaque. Recipients trial-decrypt all envelopes for their org+version.
	//
	// Member discovery cost: O(members) trial decrypts per org. Acceptable
	// for current deployment sizes (< 100 members/org). A deterministic
	// HMAC(conversation_key, org|version) handle would reduce this to O(1)
	// lookup, but the NIP-44 conversation key is not accessible through the
	// bunker signer interface (Keyer.Encrypt/Decrypt are black-box). Phase 4
	// web clients that hold their own key material can compute conversation
	// keys directly and would benefit from deterministic handles.
	//
	// Future: when bunker signers support conversation key derivation or a
	// DeriveHandle(pubkey, context) method, switch to HMAC-based handles.
	handle := make([]byte, 16)
	if _, err := rand.Read(handle); err != nil {
		return fmt.Errorf("generate recipient handle: %w", err)
	}

	dTag := ockEnvelopeDTag(key.OrgID, key.Version, hex.EncodeToString(handle))

	if m.publisher == nil {
		return fmt.Errorf("OCK envelope publisher not configured")
	}
	return m.publisher.PublishKeyEnvelope(ctx, dTag, encrypted)
}

// ockEnvelopeDTag returns the d-tag coordinate for an OCK envelope record.
func ockEnvelopeDTag(orgID string, version int, recipientHandle string) string {
	return "org-key:" + orgID + ":v" + strconv.Itoa(version) + ":" + recipientHandle
}

// VersionFromEnvelope extracts the OCK version from a confidential envelope
// content string without decrypting.
func VersionFromEnvelope(content string) (orgID string, version int, err error) {
	var envelope ConfidentialEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return "", 0, fmt.Errorf("parse envelope: %w", err)
	}
	if envelope.Schema != ConfidentialSchema {
		return "", 0, fmt.Errorf("unknown schema: %s", envelope.Schema)
	}
	v, err := strconv.Atoi(envelope.KeyVersion[1:]) // strip "v" prefix
	if err != nil {
		return "", 0, fmt.Errorf("parse key version %q: %w", envelope.KeyVersion, err)
	}
	return envelope.KeyOrg, v, nil
}

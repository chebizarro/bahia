package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

	mu           sync.RWMutex
	cache        map[string]*orgKeyState // orgID → key state
	pending      map[string]*pendingRotation
	pendingWraps map[string]map[string]*ockRetry
	rotationMu   sync.Mutex // serializes recovery, activation, wrapping and encryption
	now          func() time.Time
}

// OCKRotationPendingError prevents new ciphertext under a key whose reader
// set may no longer match the canonical membership state.
type OCKRotationPendingError struct{ OrgID string }

func (e *OCKRotationPendingError) Error() string {
	return "org " + e.OrgID + ": confidential publishes withheld pending key rotation"
}

type pendingRotation struct {
	excluded map[string]bool
	key      *OrgContentKey
	retry    ockRetry
}

// ockRetry rate-limits retries driven by subsequent publish requests. It never
// starts a timer or goroutine; an idle org stays pending until relevant activity.
type ockRetry struct {
	delay time.Duration
	after time.Time
}

func (r *ockRetry) failed(now time.Time) {
	if r.delay == 0 {
		r.delay = time.Second
	} else {
		r.delay = min(2*r.delay, time.Minute)
	}
	r.after = now.Add(r.delay)
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
		pending:       make(map[string]*pendingRotation),
		pendingWraps:  make(map[string]map[string]*ockRetry),
		now:           time.Now,
	}
}

// PendingRotations is a snapshot for operator readiness diagnostics.
func (m *OCKManager) PendingRotations() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	orgs := make([]string, 0, len(m.pending))
	for orgID := range m.pending {
		orgs = append(orgs, orgID)
	}
	sort.Strings(orgs)
	return orgs
}

func (m *OCKManager) rotationGuard(orgID string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, pending := m.pending[orgID]; pending {
		return &OCKRotationPendingError{OrgID: orgID}
	}
	return nil
}

// GetKey returns the current OCK for the given org. If no key is cached, it
// attempts recovery from persisted service envelopes.
func (m *OCKManager) GetKey(ctx context.Context, orgID string) (OrgContentKey, error) {
	m.rotationMu.Lock()
	defer m.rotationMu.Unlock()
	return m.getKey(ctx, orgID)
}

func (m *OCKManager) getKey(ctx context.Context, orgID string) (OrgContentKey, error) {
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
	m.rotationMu.Lock()
	defer m.rotationMu.Unlock()
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
	m.rotationMu.Lock()
	defer m.rotationMu.Unlock()
	m.mu.RLock()
	pending := m.pending[orgID]
	m.mu.RUnlock()
	if pending != nil {
		if !m.now().Before(pending.retry.after) {
			if _, err := m.rotatePending(ctx, orgID); err != nil {
				m.logger.Warn("pending OCK rotation retry failed", zap.String("org_id", orgID), zap.Error(err))
			}
		}
		if err := m.rotationGuard(orgID); err != nil {
			return OrgContentKey{}, err
		}
	}
	key, err := m.getKey(ctx, orgID)
	if err != nil {
		return OrgContentKey{}, err
	}
	m.retryPendingWraps(ctx, key)
	return key, nil
}

// RotateKey creates a new OCK version for the org, wraps it to the current
// member set (excluding removed members), and activates it. Old versions
// remain available for historical reads.
func (m *OCKManager) RotateKey(ctx context.Context, orgID string) (OrgContentKey, error) {
	return m.rotateKey(ctx, orgID, "")
}

// RotateKeyExcluding rotates before a removal's canonical tombstone is
// published. The relay trust set may still include that member, so the
// exclusion is mandatory and is retained for retries.
func (m *OCKManager) RotateKeyExcluding(ctx context.Context, orgID, removedPubkey string) (OrgContentKey, error) {
	if removedPubkey == "" {
		return OrgContentKey{}, fmt.Errorf("removed member pubkey is required")
	}
	return m.rotateKey(ctx, orgID, removedPubkey)
}

func (m *OCKManager) rotateKey(ctx context.Context, orgID, removedPubkey string) (OrgContentKey, error) {
	m.rotationMu.Lock()
	defer m.rotationMu.Unlock()
	m.mu.Lock()
	state := m.pending[orgID]
	if state == nil {
		state = &pendingRotation{excluded: make(map[string]bool)}
		m.pending[orgID] = state
	}
	if removedPubkey != "" {
		state.excluded[removedPubkey] = true
		if wraps := m.pendingWraps[orgID]; wraps != nil {
			delete(wraps, removedPubkey)
		}
	}
	m.mu.Unlock()
	return m.rotatePending(ctx, orgID)
}

// rotatePending is called with rotationMu held. A successful rotation is the
// only operation that clears the guard.
func (m *OCKManager) rotatePending(ctx context.Context, orgID string) (key OrgContentKey, err error) {
	m.mu.RLock()
	state := m.pending[orgID]
	m.mu.RUnlock()
	if state == nil {
		return OrgContentKey{}, fmt.Errorf("no pending rotation for org %s", orgID)
	}
	defer func() {
		if err != nil {
			state.retry.failed(m.now())
		}
	}()
	if state.key == nil {
		// Recover without creating an intermediate key for the old roster.
		// A cold removal must never distribute v1 to its target.
		m.mu.RLock()
		cached := m.cache[orgID] != nil
		m.mu.RUnlock()
		if !cached {
			if err := m.recoverFromHistory(ctx, orgID); err != nil {
				return OrgContentKey{}, err
			}
		}
		m.mu.RLock()
		nextVersion := 1
		if current := m.cache[orgID]; current != nil {
			nextVersion = current.current.Version + 1
		}
		m.mu.RUnlock()
		candidate, err := GenerateOrgContentKey(orgID, nextVersion)
		if err != nil {
			return OrgContentKey{}, err
		}
		// Reuse the candidate after an ambiguous acknowledgment instead of
		// publishing different key material at the same version on retry.
		state.key = &candidate
	}
	key = *state.key
	if err = m.distribute(ctx, key, state.excluded); err != nil {
		return OrgContentKey{}, err
	}
	m.mu.Lock()
	delete(m.pending, orgID)
	m.mu.Unlock()
	return key, nil
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
	m.rotationMu.Lock()
	defer m.rotationMu.Unlock()
	if err := m.rotationGuard(orgID); err != nil {
		if !m.pending[orgID].excluded[recipientPubkey] {
			m.queueWrapRetry(orgID, recipientPubkey)
		}
		return err
	}
	m.mu.RLock()
	state, ok := m.cache[orgID]
	var key OrgContentKey
	if ok {
		key = state.current
	}
	m.mu.RUnlock()
	if !ok {
		// No key exists yet — EnsureKey at next encrypt will distribute to all members.
		return nil
	}
	if err := m.wrapAndPublish(ctx, key, recipientPubkey); err != nil {
		m.queueWrapRetry(orgID, recipientPubkey)
		return err
	}
	m.clearPendingWrap(orgID, recipientPubkey)
	return nil
}

func (m *OCKManager) clearPendingWrap(orgID, pubkey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if wraps := m.pendingWraps[orgID]; wraps != nil {
		delete(wraps, pubkey)
		if len(wraps) == 0 {
			delete(m.pendingWraps, orgID)
		}
	}
}

// queueWrapRetry and retryPendingWraps run under rotationMu. Retries re-check
// membership, so a delayed add never wraps a newer epoch to a removed member.
func (m *OCKManager) queueWrapRetry(orgID, pubkey string) {
	wraps := m.pendingWraps[orgID]
	if wraps == nil {
		wraps = make(map[string]*ockRetry)
		m.pendingWraps[orgID] = wraps
	}
	retry := wraps[pubkey]
	if retry == nil {
		retry = &ockRetry{}
		wraps[pubkey] = retry
	}
	retry.failed(m.now())
}

func (m *OCKManager) retryPendingWraps(ctx context.Context, key OrgContentKey) {
	for pubkey, retry := range m.pendingWraps[key.OrgID] {
		if m.now().Before(retry.after) {
			continue
		}
		if m.members == nil {
			retry.failed(m.now())
			continue
		}
		recipients, err := m.members.OrgMemberPubkeys(ctx, key.OrgID)
		if err != nil {
			retry.failed(m.now())
			continue
		}
		member := false
		for _, recipient := range recipients {
			if recipient == pubkey {
				member = true
				break
			}
		}
		if !member {
			m.clearPendingWrap(key.OrgID, pubkey)
			continue
		}
		if err := m.wrapAndPublish(ctx, key, pubkey); err != nil {
			retry.failed(m.now())
			m.logger.Warn("pending OCK member wrap retry failed", zap.String("org_id", key.OrgID), zap.Error(err))
		} else {
			m.clearPendingWrap(key.OrgID, pubkey)
		}
	}
}

func (m *OCKManager) recoverOrCreate(ctx context.Context, orgID string) (OrgContentKey, error) {
	if err := m.recoverFromHistory(ctx, orgID); err != nil {
		return OrgContentKey{}, err
	}

	m.mu.RLock()
	if state, ok := m.cache[orgID]; ok {
		key := state.current
		m.mu.RUnlock()
		return key, nil
	}
	m.mu.RUnlock()

	if err := m.rotationGuard(orgID); err != nil {
		return OrgContentKey{}, err
	}
	return m.createAndDistribute(ctx, orgID, 1, nil)
}

func (m *OCKManager) recoverFromHistory(ctx context.Context, orgID string) error {
	if m.history == nil {
		return nil
	}
	records, err := m.history.FindKeyEnvelopes(ctx, orgID)
	if err != nil {
		return fmt.Errorf("query OCK history: %w", err)
	}
	if len(records) == 0 {
		return nil
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
		// History includes other scopes. Never cache a zero key or reset a
		// scope to v1 when its envelopes exist but its service wrap is missing.
		for _, rec := range records {
			if strings.HasPrefix(rec.DTag, "org-key:"+orgID+":") {
				return fmt.Errorf("no service-decryptable OCK envelopes for org %s", orgID)
			}
		}
		return nil
	}
	m.cache[orgID] = state
	return nil
}

func (m *OCKManager) createAndDistribute(ctx context.Context, orgID string, version int, excluded map[string]bool) (OrgContentKey, error) {
	key, err := GenerateOrgContentKey(orgID, version)
	if err != nil {
		return OrgContentKey{}, err
	}

	if err := m.distribute(ctx, key, excluded); err != nil {
		return OrgContentKey{}, err
	}
	return key, nil
}

func (m *OCKManager) distribute(ctx context.Context, key OrgContentKey, excluded map[string]bool) error {
	orgID := key.OrgID
	// Get current member set.
	recipients := []string{m.servicePubkey}
	if m.members != nil {
		memberPubkeys, err := m.members.OrgMemberPubkeys(ctx, orgID)
		if err != nil {
			return fmt.Errorf("read org members for OCK distribution: %w", err)
		} else {
			seen := map[string]bool{m.servicePubkey: true}
			for _, pk := range memberPubkeys {
				if !seen[pk] && !excluded[pk] {
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
				return fmt.Errorf("publish service OCK envelope: %w", err)
			}
			m.logger.Warn("failed to publish OCK envelope for member",
				zap.String("org_id", orgID),
				zap.String("recipient", recipientPubkey[:min(len(recipientPubkey), 8)]+"..."),
				zap.Error(err))
			m.queueWrapRetry(orgID, recipientPubkey)
		} else {
			m.clearPendingWrap(orgID, recipientPubkey)
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
	state.versions[key.Version] = key
	m.mu.Unlock()

	m.logger.Info("OCK created and distributed",
		zap.String("org_id", orgID),
		zap.Int("version", key.Version),
		zap.Int("recipients", len(recipients)))
	return nil
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
	// bunker signer interface (Keyer.Encrypt/Decrypt are black-box). Web
	// clients that hold their own key material can compute conversation
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

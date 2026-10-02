package controlplane

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// ConfidentialEncryptor encrypts and decrypts confidential cp-state records
// using a per-org content key (OCK). It replaces the previous LegacyOrgStateDecryptor
// and NIP-44 self-encryption paths with a single scheme where:
//   - Org-visible content is AEAD-encrypted under the OCK (any member can decrypt)
//   - Service-only fields use an additional NIP-44 inner layer to the service pubkey
//   - All NIP-44 operations go through the signer interface (bunker-compatible)
//
// Phase 3 C1.
//
// Method signatures use primitive types so that the nostr adapter can define a
// matching interface (ConfidentialStateEncryptor) without importing this
// package — Go structural typing handles the match.
type ConfidentialEncryptor struct {
	ockManager *OCKManager
	logger     *zap.Logger
}

// NewConfidentialEncryptor creates a confidential encryptor backed by the given
// OCK manager.
func NewConfidentialEncryptor(ockManager *OCKManager, logger *zap.Logger) *ConfidentialEncryptor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ConfidentialEncryptor{
		ockManager: ockManager,
		logger:     logger.Named("confidential-encryptor"),
	}
}

// EncryptConfidential encrypts plaintext content for the given org.
// legacyKind, dTag, and topic identify the record's coordinate for AD binding.
// serviceOnlyPlaintext, if non-nil, is NIP-44-encrypted to the service pubkey
// and embedded as service_inner (for secret values, webhook credentials, etc.).
func (e *ConfidentialEncryptor) EncryptConfidential(
	ctx context.Context,
	orgID string,
	plaintext []byte,
	legacyKind int,
	dTag, topic string,
	serviceOnlyPlaintext []byte,
) (string, error) {
	key, err := e.ockManager.EnsureKey(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("ensure OCK for org %s: %w", orgID, err)
	}

	recordCtx := ConfidentialRecordContext{
		LegacyKind: legacyKind,
		DTag:       dTag,
		Topic:      topic,
	}

	var serviceEncrypt func(ctx context.Context, pt string) (string, error)
	if len(serviceOnlyPlaintext) > 0 {
		serviceEncrypt = e.ockManager.ServiceEncrypt
	}

	return EncryptConfidentialContent(ctx, key, plaintext, recordCtx, serviceOnlyPlaintext, serviceEncrypt)
}

// DecryptConfidential decrypts the org-visible content of a confidential
// envelope. legacyKind, dTag, and topic are verified against the AEAD
// associated data.
func (e *ConfidentialEncryptor) DecryptConfidential(ctx context.Context, content string, legacyKind int, dTag, topic string) ([]byte, error) {
	// Parse version from the envelope.
	orgID, version, err := VersionFromEnvelope(content)
	if err != nil {
		return nil, fmt.Errorf("parse OCK version: %w", err)
	}

	key, err := e.ockManager.GetKeyByVersion(ctx, orgID, version)
	if err != nil {
		return nil, fmt.Errorf("resolve OCK v%d for org %s: %w", version, orgID, err)
	}

	recordCtx := ConfidentialRecordContext{
		LegacyKind: legacyKind,
		DTag:       dTag,
		Topic:      topic,
	}

	return DecryptConfidentialContent(key, content, recordCtx)
}

// DecryptServiceInner decrypts the service-only inner layer of a confidential
// envelope. Returns nil if no service_inner is present.
func (e *ConfidentialEncryptor) DecryptServiceInner(ctx context.Context, content string) ([]byte, error) {
	return DecryptConfidentialServiceInner(content, func(ciphertext string) (string, error) {
		return e.ockManager.ServiceDecrypt(ctx, ciphertext)
	})
}

// DecryptOrgState implements the legacy DecryptOrgState interface used by
// RelayMemberEventHandler and DecryptMemberContent. It tries the new
// confidential format; callers with legacy support should try legacy
// decryptors separately if this returns an error.
func (e *ConfidentialEncryptor) DecryptOrgState(content string) ([]byte, error) {
	return e.DecryptConfidential(context.Background(), content, 0, "", "")
}

// RotateKey triggers OCK rotation for an org. Called after member removal or
// role downgrade.
func (e *ConfidentialEncryptor) RotateKey(ctx context.Context, orgID string) error {
	_, err := e.ockManager.RotateKey(ctx, orgID)
	return err
}

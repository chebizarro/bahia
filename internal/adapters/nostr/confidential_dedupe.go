package nostr

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// confidentialStateHashTag carries a keyed digest of a confidential record's
// stable plaintext. OCK ciphertext is randomized, so without it every publish
// of unchanged state would look new to the projector's fingerprint dedupe.
const confidentialStateHashTag = "state_hash"

// canonicalViewLimit bounds how many retained records a canonical-first
// producer reads back per family from the local event store. It is the same
// bound the dedupe cache hydrates with; a view that reaches it fails instead of
// answering from a truncated history.
const canonicalViewLimit = projectionHydrateLimit

// confidentialStateHash is a keyed digest of the stable plaintext. It lets the
// projector dedupe randomized OCK ciphertext without exposing a guessable hash
// of sensitive payment or vulnerability data in a public tag: only the daemon
// can recompute it, and it changes whenever the plaintext does. The key is the
// confidential-dedupe system key (secret_store.system_keys), held in Signet.
// Replacing it changes every digest, so each coordinate is republished once,
// on its producer's next publish, and deduplicated again from then on.
func confidentialStateHash(key []byte, plaintext string) gonostr.Tag {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(stableContent(plaintext, false)))
	return gonostr.Tag{confidentialStateHashTag, hex.EncodeToString(mac.Sum(nil))}
}

// publishCanonicalFirst signs one confidential cp-state record for a producer
// that publishes before it updates any derived index. The
// record is made durable in the publish outbox before the first relay round,
// so the caller sees exactly two outcomes: nil (accepted, or queued and being
// retried per relay) or an error (never admitted, or abandoned). The caller
// must not touch its index on an error.
//
// It shares publishSigned's per-coordinate lock, created_at floor and
// fingerprint memory, and like publishAuthoritative it does not honour the
// projector's shared backoff window: a suppressed publish there is only kept
// in memory, which is not a durable admission. Unlike the projection paths it
// also skips a tombstone the coordinate already carries, because these
// producers re-assert state on every retry.
func (p *Projector) publishCanonicalFirst(ctx context.Context, legacyKind int, id string, deleted bool, tags gonostr.Tags, plaintext, ciphertext, entityType string, entityID *uuid.UUID) error {
	wireKind, baseTags := controlStateEnvelope(legacyKind, id, deleted)
	all := make(gonostr.Tags, 0, len(baseTags)+len(tags)+1)
	all = append(all, baseTags...)
	all = append(all, tags...)
	if p.stateHashKeys == nil {
		return fmt.Errorf("publish confidential %s: %w: no confidential-dedupe system key (secret_store.system_keys.confidential_dedupe)", entityType, secretstore.ErrNotConfigured)
	}
	var stateHash gonostr.Tag
	if err := p.stateHashKeys.Use(ctx, secretstore.PurposeConfidentialDedupe, "", func(_ secretstore.SystemKeyRef, key []byte) error {
		stateHash = confidentialStateHash(key, plaintext)
		return nil
	}); err != nil {
		return fmt.Errorf("publish confidential %s: %w", entityType, err)
	}
	all = append(all, stateHash)
	key := projectionKeyOf(wireKind, all)
	fingerprint := projectionFingerprint(wireKind, all, ciphertext)
	// Retained state only sharpens the dedupe and the created_at floor; a
	// failed load is logged by hydrateProjectionCache and costs one re-sign.
	_ = p.hydrateProjectionCache(ctx, wireKind)
	_, unlock := p.lockProjectionKey(key)
	defer unlock()

	if p.projectionUnchanged(key, fingerprint) {
		return nil
	}
	createdAt := p.nextProjectionCreatedAt(key)
	if _, err := p.publishSignedDirect(ctx, wireKind, createdAt, all, ciphertext, entityType, entityID); err != nil {
		return err
	}
	p.rememberProjection(key, fingerprint, createdAt)
	return nil
}

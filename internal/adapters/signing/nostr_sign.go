package signing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// NostrSignatureKind is the custom Nostr event kind for artifact attestations.
// Using replaceable parameterized event (3xxxx range) so each pubkey+artifact is unique.
const NostrSignatureKind = 31200

// NostrArtifactAttestation is the expected content structure of a kind 31200 event.
type NostrArtifactAttestation struct {
	ImageRepo   string `json:"image_repo"`
	ImageDigest string `json:"image_digest"`
	Approved    bool   `json:"approved"`
	Reason      string `json:"reason,omitempty"`
}

// NostrVerifier validates Nostr-native artifact signature events.
// It verifies that a kind 31200 event was signed by a trusted pubkey
// and attests the given artifact digest.
type NostrVerifier struct {
	trustedPubkeys map[string]bool
	logger         *zap.Logger
}

// NewNostrVerifier creates a Nostr signature verifier.
// trustedPubkeys is a list of hex pubkeys allowed to sign artifacts.
func NewNostrVerifier(trustedPubkeys []string, logger *zap.Logger) *NostrVerifier {
	trusted := make(map[string]bool, len(trustedPubkeys))
	for _, pk := range trustedPubkeys {
		trusted[pk] = true
	}
	return &NostrVerifier{
		trustedPubkeys: trusted,
		logger:         logger,
	}
}

// VerifyEvent checks a Nostr event as an artifact attestation.
// Returns a signature record if the event is valid and from a trusted pubkey.
func (v *NostrVerifier) VerifyEvent(ctx context.Context, ev *gonostr.Event, artifact *domain.Artifact) (*domain.ArtifactSignature, error) {
	// Check kind.
	if ev.Kind != NostrSignatureKind {
		return nil, fmt.Errorf("expected kind %d, got %d", NostrSignatureKind, ev.Kind)
	}

	// Verify Schnorr signature.
	ok, err := ev.CheckSignature()
	if err != nil {
		return nil, fmt.Errorf("checking event signature: %w", err)
	}
	if !ok {
		now := time.Now().UTC()
		return &domain.ArtifactSignature{
			ID:                uuid.New(),
			ArtifactID:        artifact.ID,
			SignerIdentity:    ev.PubKey,
			SignatureType:     domain.SignatureNostr,
			SignatureRef:      ev.ID,
			Verified:          false,
			VerifiedAt:        &now,
			VerificationError: "invalid Schnorr signature",
			CreatedAt:         now,
		}, nil
	}

	// Check trusted pubkey.
	trusted := v.trustedPubkeys[ev.PubKey]

	// Parse attestation content.
	var attestation NostrArtifactAttestation
	if err := json.Unmarshal([]byte(ev.Content), &attestation); err != nil {
		return nil, fmt.Errorf("parsing attestation content: %w", err)
	}

	// Verify the attestation matches the artifact.
	now := time.Now().UTC()
	sig := &domain.ArtifactSignature{
		ID:             uuid.New(),
		ArtifactID:     artifact.ID,
		SignerIdentity: ev.PubKey,
		SignatureType:  domain.SignatureNostr,
		SignatureRef:   ev.ID,
		VerifiedAt:     &now,
		CreatedAt:      now,
		Metadata: map[string]any{
			"event_kind":    ev.Kind,
			"event_created": ev.CreatedAt.Time().UTC().Format(time.RFC3339),
			"approved":      attestation.Approved,
			"trusted":       trusted,
		},
	}

	switch {
	case attestation.ImageDigest != artifact.ImageDigest:
		sig.Verified = false
		sig.VerificationError = fmt.Sprintf("digest mismatch: attestation=%s artifact=%s",
			attestation.ImageDigest, artifact.ImageDigest)
	case !attestation.Approved:
		sig.Verified = false
		sig.VerificationError = "attestation explicitly disapproved"
		if attestation.Reason != "" {
			sig.VerificationError += ": " + attestation.Reason
		}
	case !trusted:
		sig.Verified = false
		sig.VerificationError = fmt.Sprintf("signer %s is not in trusted pubkey list", ev.PubKey)
	default:
		sig.Verified = true
	}

	return sig, nil
}

// VerifySignatures implements SignatureVerifier by checking if the artifact
// has matching Nostr attestation events. This is a no-op for now — Nostr
// signatures are verified reactively when events arrive, not proactively.
// The processor.go handler calls VerifyEvent when it receives kind 31200 events.
func (v *NostrVerifier) VerifySignatures(_ context.Context, _ *domain.Artifact) ([]domain.ArtifactSignature, error) {
	// Nostr signatures are event-driven, not pull-based.
	// They arrive via the subscriber → processor pipeline.
	return nil, nil
}

// Compile-time interface check.
var _ SignatureVerifier = (*NostrVerifier)(nil)

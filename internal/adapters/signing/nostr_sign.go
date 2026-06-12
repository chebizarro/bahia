package signing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
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
	if !ev.CheckID() {
		return nil, fmt.Errorf("event ID mismatch")
	}
	if !ev.VerifySignature() {
		now := time.Now().UTC()
		sig := &domain.ArtifactSignature{
			ID:                 uuid.New(),
			ArtifactID:         artifact.ID,
			SignerIdentity:     nostrutil.EventPubKeyHex(ev),
			SignatureType:      domain.SignatureNostr,
			SignatureRef:       nostrutil.EventIDHex(ev),
			Verified:           false,
			VerificationStatus: domain.SignatureStatusRejected,
			VerificationError:  "invalid Schnorr signature",
			CreatedAt:          now,
		}
		sig.NormalizeVerificationStatus()
		return sig, nil
	}

	// Check trusted pubkey.
	pubkeyHex := nostrutil.EventPubKeyHex(ev)
	trusted := v.trustedPubkeys[pubkeyHex]

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
		SignerIdentity: pubkeyHex,
		SignatureType:  domain.SignatureNostr,
		SignatureRef:   nostrutil.EventIDHex(ev),
		CreatedAt:      now,
		Metadata: map[string]any{
			"event_kind":    int(ev.Kind),
			"event_created": ev.CreatedAt.Time().UTC().Format(time.RFC3339),
			"approved":      attestation.Approved,
			"trusted":       trusted,
		},
	}

	switch {
	case attestation.ImageDigest != artifact.ImageDigest:
		sig.VerificationStatus = domain.SignatureStatusRejected
		sig.VerificationError = fmt.Sprintf("digest mismatch: attestation=%s artifact=%s",
			attestation.ImageDigest, artifact.ImageDigest)
	case !attestation.Approved:
		sig.VerificationStatus = domain.SignatureStatusRejected
		sig.VerificationError = "attestation explicitly disapproved"
		if attestation.Reason != "" {
			sig.VerificationError += ": " + attestation.Reason
		}
	case !trusted:
		sig.VerificationStatus = domain.SignatureStatusRejected
		sig.VerificationError = fmt.Sprintf("signer %s is not in trusted pubkey list", pubkeyHex)
	default:
		sig.VerificationStatus = domain.SignatureStatusVerified
		sig.VerifiedAt = &now
	}

	sig.NormalizeVerificationStatus()
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

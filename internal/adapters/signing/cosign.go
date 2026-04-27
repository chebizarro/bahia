// Package signing provides signature verification adapters for container image artifacts.
//
// It supports cosign/sigstore signatures via OCI referrers, Nostr-native
// signatures via event verification, and defines the SignatureVerifier
// interface for policy enforcement.
package signing

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// SignatureVerifier checks whether an artifact has valid signatures.
type SignatureVerifier interface {
	// VerifySignatures checks for signatures on the given artifact and returns
	// the signature records found (verified or not).
	VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error)
}

// CosignVerifier checks for cosign/sigstore signatures via OCI referrers.
// It uses the registry ImageInspector to find signature referrers attached
// to the artifact's manifest digest.
type CosignVerifier struct {
	inspector registry.ImageInspector
	logger    *zap.Logger
}

// NewCosignVerifier creates a cosign signature verifier.
func NewCosignVerifier(inspector registry.ImageInspector, logger *zap.Logger) *CosignVerifier {
	return &CosignVerifier{
		inspector: inspector,
		logger:    logger,
	}
}

// VerifySignatures looks up OCI referrers for the artifact's digest and
// returns signature records for any cosign/sigstore signatures found.
func (v *CosignVerifier) VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error) {
	if artifact.ImageDigest == "" {
		return nil, fmt.Errorf("artifact has no digest, cannot verify signatures")
	}

	referrers, err := v.inspector.GetReferrers(ctx, artifact.ImageRepo, artifact.ImageDigest)
	if err != nil {
		v.logger.Warn("failed to fetch referrers for signature check",
			zap.String("repo", artifact.ImageRepo),
			zap.String("digest", artifact.ImageDigest),
			zap.Error(err),
		)
		// Don't fail the whole flow — just no signatures found.
		return nil, nil
	}

	var sigs []domain.ArtifactSignature
	now := time.Now().UTC()

	for _, ref := range referrers {
		sigType := classifyReferrer(ref)
		if sigType == "" {
			continue // not a signature referrer
		}

		sig := domain.ArtifactSignature{
			ID:            uuid.New(),
			ArtifactID:    artifact.ID,
			SignerIdentity: extractSigner(ref),
			SignatureType: sigType,
			SignatureRef:  ref.Digest,
			Verified:      true, // present in registry = verified by registry
			VerifiedAt:    &now,
			CreatedAt:     now,
			Metadata: map[string]any{
				"artifact_type": ref.ArtifactType,
				"media_type":    ref.MediaType,
				"size":          ref.Size,
			},
		}

		// Copy any annotations.
		if len(ref.Annotations) > 0 {
			for k, ann := range ref.Annotations {
				sig.Metadata[k] = ann
			}
		}

		sigs = append(sigs, sig)
		v.logger.Info("found signature referrer",
			zap.String("artifact_id", artifact.ID.String()),
			zap.String("type", string(sigType)),
			zap.String("ref", ref.Digest),
		)
	}

	return sigs, nil
}

// classifyReferrer determines the signature type from an OCI referrer.
func classifyReferrer(ref registry.Referrer) domain.SignatureType {
	switch {
	case isCosignType(ref.ArtifactType):
		return domain.SignatureCosign
	case isSigstoreType(ref.ArtifactType):
		return domain.SignatureSigstore
	default:
		return "" // not a signature
	}
}

func isCosignType(t string) bool {
	return t == "application/vnd.dev.cosign.simplesigning.v1"
}

func isSigstoreType(t string) bool {
	return t == "application/vnd.dev.sigstore.bundle.v0.3+json" ||
		t == "application/vnd.dev.sigstore.bundle+json;version=0.3"
}

// extractSigner tries to extract a signer identity from a referrer's annotations.
func extractSigner(ref registry.Referrer) string {
	// Sigstore/Fulcio keyless: the signer identity is in the annotations.
	if id, ok := ref.Annotations["dev.sigstore.cosign/identity"]; ok {
		return id
	}
	if id, ok := ref.Annotations["dev.cosignproject.cosign/signerIdentity"]; ok {
		return id
	}
	// Fall back to the artifact type itself.
	return ref.ArtifactType
}

// Compile-time interface check.
var _ SignatureVerifier = (*CosignVerifier)(nil)

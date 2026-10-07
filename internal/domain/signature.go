package domain

import (
	"time"

	"github.com/google/uuid"
)

// SignatureType identifies the method used to sign an artifact.
type SignatureType string

const (
	SignatureCosign   SignatureType = "cosign"
	SignatureNostr    SignatureType = "nostr"
	SignatureSigstore SignatureType = "sigstore"
	SignatureNotary   SignatureType = "notary"
)

// SignatureVerificationStatus records what trust decision has been made for a signature.
type SignatureVerificationStatus string

const (
	// SignatureStatusVerified means the signature was cryptographically verified and trusted.
	SignatureStatusVerified SignatureVerificationStatus = "verified"
	// SignatureStatusDiscovered means a possible signature was found, but not cryptographically verified.
	SignatureStatusDiscovered SignatureVerificationStatus = "discovered"
	// SignatureStatusRejected means cryptographic verification or trust policy rejected the signature.
	SignatureStatusRejected SignatureVerificationStatus = "rejected"
	// SignatureStatusError means verification could not complete due to an operational error.
	SignatureStatusError SignatureVerificationStatus = "error"
)

// ArtifactSignature records a discovered, verified, or rejected signature for a container image artifact.
type ArtifactSignature struct {
	ID                 uuid.UUID                   `json:"id"`
	ArtifactID         uuid.UUID                   `json:"artifact_id"`
	SignerIdentity     string                      `json:"signer_identity"` // e.g. email, pubkey, OIDC identity
	SignatureType      SignatureType               `json:"signature_type"`  // cosign, nostr, sigstore, notary
	SignatureRef       string                      `json:"signature_ref"`   // OCI referrer digest or event ID
	Verified           bool                        `json:"verified"`
	VerificationStatus SignatureVerificationStatus `json:"verification_status"`
	VerifiedAt         *time.Time                  `json:"verified_at,omitempty"`
	VerificationError  string                      `json:"verification_error,omitempty"`
	Metadata           map[string]any              `json:"metadata,omitempty"` // extra data (issuer, cert chain, etc.)
	CreatedAt          time.Time                   `json:"created_at"`
}

// NormalizeVerificationStatus keeps the legacy Verified boolean derived from VerificationStatus.
// Empty statuses are backfilled from legacy fields for compatibility with existing callers/tests.
func (s *ArtifactSignature) NormalizeVerificationStatus() {
	switch s.VerificationStatus {
	case SignatureStatusVerified:
		s.Verified = true
	case SignatureStatusDiscovered, SignatureStatusRejected, SignatureStatusError:
		s.Verified = false
	case "":
		switch {
		case s.Verified:
			s.VerificationStatus = SignatureStatusVerified
		case s.VerificationError != "":
			s.VerificationStatus = SignatureStatusRejected
		default:
			s.VerificationStatus = SignatureStatusDiscovered
		}
	default:
		s.Verified = false
	}
}

// SigningPolicy defines requirements for artifact signatures.
type SigningPolicy struct {
	// RequireSignature rejects artifacts without at least one verified signature.
	RequireSignature bool `json:"require_signature"`

	// AllowedSignatureTypes limits which signature methods are accepted.
	// Empty means all types are allowed.
	AllowedSignatureTypes []SignatureType `json:"allowed_signature_types,omitempty"`

	// TrustedSigners limits which identities are trusted.
	// Empty means any signer is accepted.
	TrustedSigners []string `json:"trusted_signers,omitempty"`
}

// AllowsType returns true if the policy allows the given signature type.
func (p *SigningPolicy) AllowsType(t SignatureType) bool {
	if len(p.AllowedSignatureTypes) == 0 {
		return true
	}
	for _, allowed := range p.AllowedSignatureTypes {
		if allowed == t {
			return true
		}
	}
	return false
}

// TrustsSigner returns true if the policy trusts the given identity.
func (p *SigningPolicy) TrustsSigner(identity string) bool {
	if len(p.TrustedSigners) == 0 {
		return true
	}
	for _, trusted := range p.TrustedSigners {
		if trusted == identity {
			return true
		}
	}
	return false
}

package sbom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/openagentsinc/bahia/internal/domain"
)

const DSSEPayloadTypeInToto = "application/vnd.in-toto+json"

// AttestationSigner signs the SHA-256 digest of a DSSE pre-authentication
// encoding and identifies the verification key placed in the envelope.
type AttestationSigner interface {
	KeyID() string
	Sign(context.Context, []byte) ([]byte, error)
}

// NostrDSSESigner uses Bahia's configured Nostr service key for BIP-340
// signatures without exposing or duplicating key material.
type NostrDSSESigner struct {
	secret nostr.SecretKey
	keyID  string
}

// NewNostrDSSESigner adapts an existing configured Nostr service private key
// to the SBOM DSSE signing interface.
func NewNostrDSSESigner(privateKeyHex string) (*NostrDSSESigner, error) {
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(privateKeyHex))
	if err != nil {
		return nil, fmt.Errorf("parse SBOM attestation signing key: %w", err)
	}
	return &NostrDSSESigner{secret: secret, keyID: secret.Public().Hex()}, nil
}

func (s *NostrDSSESigner) KeyID() string {
	if s == nil {
		return ""
	}
	return s.keyID
}

func (s *NostrDSSESigner) Sign(ctx context.Context, digest []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.keyID == "" {
		return nil, fmt.Errorf("SBOM attestation signer is not configured")
	}
	if len(digest) != sha256.Size {
		return nil, fmt.Errorf("SBOM attestation signing digest must be %d bytes", sha256.Size)
	}
	privateKey, _ := btcec.PrivKeyFromBytes(s.secret[:])
	signature, err := schnorr.Sign(privateKey, digest)
	if err != nil {
		return nil, fmt.Errorf("sign SBOM DSSE payload: %w", err)
	}
	return signature.Serialize(), nil
}

// SignAttestation creates a DSSE envelope over the exact in-toto statement.
func SignAttestation(ctx context.Context, att *domain.SBOMAttestation, signer AttestationSigner) error {
	if att == nil {
		return fmt.Errorf("SBOM attestation is required")
	}
	if signer == nil || strings.TrimSpace(signer.KeyID()) == "" {
		return fmt.Errorf("SBOM attestation signer is not configured")
	}
	payload, err := marshalAttestationStatement(att)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(dssePAE(DSSEPayloadTypeInToto, payload))
	signature, err := signer.Sign(ctx, digest[:])
	if err != nil {
		return err
	}
	if len(signature) == 0 {
		return fmt.Errorf("SBOM attestation signer returned an empty signature")
	}
	att.Envelope = &domain.DSSEEnvelope{
		PayloadType: DSSEPayloadTypeInToto,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures: []domain.DSSESignature{{
			KeyID: strings.ToLower(strings.TrimSpace(signer.KeyID())),
			Sig:   base64.StdEncoding.EncodeToString(signature),
		}},
	}
	return nil
}

// VerifyAttestationSignature verifies the DSSE payload and its binding to the
// visible statement. When trustedPubkey is non-empty, only that service key is
// accepted; otherwise each embedded key ID is treated as a verification
// candidate, which proves integrity but not external trust.
func VerifyAttestationSignature(att *domain.SBOMAttestation, trustedPubkey string) error {
	if att == nil {
		return fmt.Errorf("SBOM attestation is required")
	}
	if att.Envelope == nil || len(att.Envelope.Signatures) == 0 {
		return fmt.Errorf("SBOM attestation is unsigned")
	}
	if att.Envelope.PayloadType != DSSEPayloadTypeInToto {
		return fmt.Errorf("unsupported SBOM DSSE payload type %q", att.Envelope.PayloadType)
	}
	payload, err := base64.StdEncoding.DecodeString(att.Envelope.Payload)
	if err != nil {
		return fmt.Errorf("decode SBOM DSSE payload: %w", err)
	}
	var signedStatement attestationStatement
	if err := json.Unmarshal(payload, &signedStatement); err != nil {
		return fmt.Errorf("decode signed SBOM attestation statement: %w", err)
	}
	signedCanonical, err := json.Marshal(signedStatement)
	if err != nil {
		return fmt.Errorf("canonicalize signed SBOM attestation statement: %w", err)
	}
	visibleCanonical, err := marshalAttestationStatement(att)
	if err != nil {
		return err
	}
	if !bytes.Equal(signedCanonical, visibleCanonical) {
		return fmt.Errorf("SBOM DSSE payload does not match the visible attestation statement")
	}

	trustedPubkey = strings.ToLower(strings.TrimSpace(trustedPubkey))
	digest := sha256.Sum256(dssePAE(att.Envelope.PayloadType, payload))
	for _, candidate := range att.Envelope.Signatures {
		keyID := strings.ToLower(strings.TrimSpace(candidate.KeyID))
		if trustedPubkey != "" && keyID != trustedPubkey {
			continue
		}
		if verifyNostrDSSESignature(keyID, candidate.Sig, digest[:]) {
			return nil
		}
	}
	if trustedPubkey != "" {
		return fmt.Errorf("SBOM attestation has no valid signature from trusted service pubkey %s", trustedPubkey)
	}
	return fmt.Errorf("SBOM attestation has no valid DSSE signature")
}

func verifyNostrDSSESignature(keyID, encodedSignature string, digest []byte) bool {
	pubkeyBytes, err := hex.DecodeString(keyID)
	if err != nil {
		return false
	}
	pubkey, err := schnorr.ParsePubKey(pubkeyBytes)
	if err != nil {
		return false
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(encodedSignature)
	if err != nil {
		return false
	}
	signature, err := schnorr.ParseSignature(signatureBytes)
	if err != nil {
		return false
	}
	return signature.Verify(digest, pubkey)
}

type attestationStatement struct {
	Type          string                      `json:"_type"`
	Subject       []domain.AttestationSubject `json:"subject"`
	PredicateType domain.SBOMAttestationType  `json:"predicateType"`
	Predicate     domain.SBOMPredicate        `json:"predicate"`
}

func marshalAttestationStatement(att *domain.SBOMAttestation) ([]byte, error) {
	if att.Type != InTotoStatementType {
		return nil, fmt.Errorf("invalid attestation type: %s", att.Type)
	}
	return json.Marshal(attestationStatement{
		Type:          att.Type,
		Subject:       att.Subject,
		PredicateType: att.PredicateType,
		Predicate:     att.Predicate,
	})
}

func dssePAE(payloadType string, payload []byte) []byte {
	return []byte("DSSEv1 " + strconv.Itoa(len(payloadType)) + " " + payloadType + " " + strconv.Itoa(len(payload)) + " " + string(payload))
}

package sbom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const DSSEPayloadTypeInToto = "application/vnd.in-toto+json"

// Attestation events are NIP-CAS-0005 CAS_AUDIT records. The content is the
// exact canonical in-toto statement JSON; the tags repeat its digests so the
// event is routable and self-describing without parsing the content.
const (
	attestationAuditDomain = "sbom"
	attestationAuditType   = "attestation"
	tagAttestationSubject  = "subject"
	tagAttestationSBOM     = "sbom"
)

// AttestationSigner signs a standard Nostr event as the attesting service
// identity through the injected service signer (local key, NIP-46 bunker or
// NIP-55L; see internal/servicesigner).
type AttestationSigner interface {
	SignEvent(context.Context, *nostr.Event) error
}

// SignAttestation signs the exact canonical in-toto statement as the content
// of a standard Nostr event and attaches the verified signed event.
func SignAttestation(ctx context.Context, att *domain.SBOMAttestation, signer AttestationSigner) error {
	if att == nil {
		return fmt.Errorf("SBOM attestation is required")
	}
	if signer == nil {
		return fmt.Errorf("SBOM attestation signer is not configured")
	}
	if att.Envelope != nil || att.Event != nil {
		return fmt.Errorf("SBOM attestation is already signed")
	}
	statement, err := marshalAttestationStatement(att)
	if err != nil {
		return err
	}
	tags, err := attestationEventTags(att)
	if err != nil {
		return err
	}
	event := nostr.Event{Kind: kinds.CASAudit, CreatedAt: nostr.Now(), Tags: tags, Content: string(statement)}
	if err := signer.SignEvent(ctx, &event); err != nil {
		return fmt.Errorf("sign SBOM attestation event: %w", err)
	}
	signed := domain.SignedNostrEvent{
		ID:        event.ID.Hex(),
		PubKey:    event.PubKey.Hex(),
		CreatedAt: int64(event.CreatedAt),
		Kind:      int(event.Kind),
		Tags:      make([][]string, len(event.Tags)),
		Content:   event.Content,
		Sig:       hex.EncodeToString(event.Sig[:]),
	}
	for i, tag := range event.Tags {
		signed.Tags[i] = append([]string(nil), tag...)
	}
	candidate := *att
	candidate.Event = &signed
	if err := verifyAttestationEvent(&candidate, statement, ""); err != nil {
		return fmt.Errorf("SBOM attestation signer returned an invalid event: %w", err)
	}
	att.Event = &signed
	return nil
}

// VerifyAttestationSignature verifies the attestation's signature and its
// binding to the visible statement. New attestations carry a signed Nostr
// event; historical ones carry a DSSE envelope, which is still verified with
// the service pubkey alone. When trustedPubkey is non-empty, only that service
// key is accepted; otherwise the embedded key is treated as a verification
// candidate, which proves integrity but not external trust.
func VerifyAttestationSignature(att *domain.SBOMAttestation, trustedPubkey string) error {
	if att == nil {
		return fmt.Errorf("SBOM attestation is required")
	}
	trustedPubkey = strings.ToLower(strings.TrimSpace(trustedPubkey))
	if att.Event != nil {
		if att.Envelope != nil {
			return fmt.Errorf("SBOM attestation carries both a signed event and a DSSE envelope")
		}
		statement, err := marshalAttestationStatement(att)
		if err != nil {
			return err
		}
		return verifyAttestationEvent(att, statement, trustedPubkey)
	}
	return verifyDSSEEnvelope(att, trustedPubkey)
}

// verifyAttestationEvent accepts the embedded event only if it is a valid
// signed CAS_AUDIT SBOM attestation from the trusted key whose content is
// byte-identical to the visible statement and whose tags are exactly those
// derived from it.
func verifyAttestationEvent(att *domain.SBOMAttestation, statement []byte, trustedPubkey string) error {
	signed := att.Event
	if signed.Kind != kinds.CASAudit {
		return fmt.Errorf("SBOM attestation event has kind %d, want %d", signed.Kind, kinds.CASAudit)
	}
	wire, err := json.Marshal(signed)
	if err != nil {
		return fmt.Errorf("encode SBOM attestation event: %w", err)
	}
	var event nostr.Event
	if err := json.Unmarshal(wire, &event); err != nil {
		return fmt.Errorf("decode SBOM attestation event: %w", err)
	}
	if event.ID.Hex() != signed.ID || event.PubKey.Hex() != signed.PubKey || hex.EncodeToString(event.Sig[:]) != signed.Sig {
		return fmt.Errorf("SBOM attestation event has non-canonical id, pubkey or signature encoding")
	}
	if !event.CheckID() {
		return fmt.Errorf("SBOM attestation event id does not match its content")
	}
	if !event.VerifySignature() {
		return fmt.Errorf("SBOM attestation event has an invalid signature")
	}
	if trustedPubkey != "" && signed.PubKey != trustedPubkey {
		return fmt.Errorf("SBOM attestation has no valid signature from trusted service pubkey %s", trustedPubkey)
	}
	if signed.Content != string(statement) {
		return fmt.Errorf("SBOM attestation event content does not match the visible attestation statement")
	}
	expected, err := attestationEventTags(att)
	if err != nil {
		return err
	}
	if !slices.EqualFunc(expected, signed.Tags, func(want nostr.Tag, got []string) bool { return slices.Equal(want, got) }) {
		return fmt.Errorf("SBOM attestation event tags do not match the visible attestation statement")
	}
	return nil
}

// attestationEventTags derives the event's tags from the statement: the
// CAS_AUDIT routing tags, then one subject tag per subject digest and one
// sbom tag per SBOM payload digest, each as "<algorithm>:<value>" in subject
// order and sorted algorithm order.
func attestationEventTags(att *domain.SBOMAttestation) (nostr.Tags, error) {
	tags := nostr.Tags{
		{cascadia.TagDomain, attestationAuditDomain},
		{cascadia.TagType, attestationAuditType},
		{cascadia.TagSchema, InTotoStatementType},
	}
	subjects := 0
	for _, subject := range att.Subject {
		for _, algorithm := range sortedDigestAlgorithms(subject.Digest) {
			tags = append(tags, nostr.Tag{tagAttestationSubject, algorithm + ":" + subject.Digest[algorithm]})
			subjects++
		}
	}
	payloads := 0
	for _, algorithm := range sortedDigestAlgorithms(att.Predicate.Digest) {
		tags = append(tags, nostr.Tag{tagAttestationSBOM, algorithm + ":" + att.Predicate.Digest[algorithm]})
		payloads++
	}
	if subjects == 0 || payloads == 0 {
		return nil, fmt.Errorf("SBOM attestation needs subject and SBOM payload digests")
	}
	return tags, nil
}

func sortedDigestAlgorithms(digests map[string]string) []string {
	algorithms := make([]string, 0, len(digests))
	for algorithm := range digests {
		algorithms = append(algorithms, algorithm)
	}
	slices.Sort(algorithms)
	return algorithms
}

// verifyDSSEEnvelope verifies a historical DSSE envelope and its binding to
// the visible statement with the BIP-340 service pubkey in each key ID.
func verifyDSSEEnvelope(att *domain.SBOMAttestation, trustedPubkey string) error {
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

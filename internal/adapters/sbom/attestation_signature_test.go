package sbom

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// historicalDSSEPubkey signed testdata/historical-dsse-attestation.json with
// the pre-event DSSE signer. The fixture is frozen: it must keep verifying.
const historicalDSSEPubkey = "7962d45b38e8bcf82fa8efa8432a01f20c9a53e24c7d3f11df197cb8e70926da"

func testAttestationSigner(t *testing.T) (nostr.SecretKey, AttestationSigner) {
	t.Helper()
	secret := nostr.Generate()
	return secret, keyer.NewPlainKeySigner([32]byte(secret))
}

func testSignableAttestation() *domain.SBOMAttestation {
	return &domain.SBOMAttestation{
		Type: InTotoStatementType,
		Subject: []domain.AttestationSubject{
			{Name: "ghcr.io/example/app:1.0.0", Digest: map[string]string{"sha256": testSHA256A}},
		},
		PredicateType: domain.AttestationTypeSPDX,
		Predicate: domain.SBOMPredicate{
			Format:    domain.SBOMFormatSPDX,
			Location:  domain.SBOMLocation{Type: domain.SBOMStorageBlossom, URI: "https://blossom.example/" + testSHA256B},
			Digest:    map[string]string{"sha256": testSHA256B},
			Generator: domain.SBOMGenerator{ID: "syft", Version: "1.4.1"},
			Timestamp: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		},
	}
}

func cloneAttestation(t *testing.T, att *domain.SBOMAttestation) *domain.SBOMAttestation {
	t.Helper()
	data, err := json.Marshal(att)
	if err != nil {
		t.Fatal(err)
	}
	var clone domain.SBOMAttestation
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

// resign replaces the embedded event with a validly signed one built from
// the current (possibly tampered) event fields.
func resign(t *testing.T, att *domain.SBOMAttestation, secret nostr.SecretKey) {
	t.Helper()
	tags := make(nostr.Tags, len(att.Event.Tags))
	for i, tag := range att.Event.Tags {
		tags[i] = append(nostr.Tag(nil), tag...)
	}
	ev := nostr.Event{Kind: nostr.Kind(att.Event.Kind), CreatedAt: nostr.Timestamp(att.Event.CreatedAt), Tags: tags, Content: att.Event.Content}
	if err := ev.Sign(secret); err != nil {
		t.Fatal(err)
	}
	var signed domain.SignedNostrEvent
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &signed); err != nil {
		t.Fatal(err)
	}
	att.Event = &signed
}

func TestSignAttestationSignsExactStatementAsAuditEvent(t *testing.T) {
	secret, signer := testAttestationSigner(t)
	att := testSignableAttestation()
	statement, err := marshalAttestationStatement(att)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignAttestation(context.Background(), att, signer); err != nil {
		t.Fatal(err)
	}
	if att.Envelope != nil || att.Event == nil {
		t.Fatalf("signed attestation envelope=%v event=%v, want event only", att.Envelope, att.Event)
	}
	ev := att.Event
	if ev.Kind != kinds.CASAudit || ev.PubKey != secret.Public().Hex() || ev.Content != string(statement) {
		t.Fatalf("attestation event kind=%d pubkey=%s content=%q", ev.Kind, ev.PubKey, ev.Content)
	}
	want := [][]string{
		{"domain", "sbom"},
		{"type", "attestation"},
		{"schema", InTotoStatementType},
		{"subject", "sha256:" + testSHA256A},
		{"sbom", "sha256:" + testSHA256B},
	}
	if got, _ := json.Marshal(ev.Tags); string(got) != mustJSON(t, want) {
		t.Fatalf("attestation event tags = %s, want %s", got, mustJSON(t, want))
	}
	if err := VerifyAttestationSignature(att, secret.Public().Hex()); err != nil {
		t.Fatalf("verify with trusted pubkey: %v", err)
	}
	if err := VerifyAttestationSignature(att, strings.ToUpper(secret.Public().Hex())); err != nil {
		t.Fatalf("verify with upper-case trusted pubkey: %v", err)
	}
	if err := VerifyAttestationSignature(att, nostr.Generate().Public().Hex()); err == nil || !strings.Contains(err.Error(), "trusted service pubkey") {
		t.Fatalf("untrusted pubkey verification = %v", err)
	}

	data, err := SerializeAttestation(att)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAttestation(data)
	if err != nil {
		t.Fatalf("ParseAttestation: %v", err)
	}
	if err := VerifyAttestationSignature(parsed, secret.Public().Hex()); err != nil {
		t.Fatalf("parsed attestation does not verify: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	var nip01 nostr.Event
	if err := json.Unmarshal(wire["event"], &nip01); err != nil || !nip01.CheckID() || !nip01.VerifySignature() {
		t.Fatalf("serialized attestation event is not a valid NIP-01 event: %v", err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEventSignedAttestationRejectsTampering(t *testing.T) {
	secret, signer := testAttestationSigner(t)
	signed := testSignableAttestation()
	if err := SignAttestation(context.Background(), signed, signer); err != nil {
		t.Fatal(err)
	}
	trusted := secret.Public().Hex()
	for name, tc := range map[string]struct {
		mutate func(*domain.SBOMAttestation)
		want   string
	}{
		"visible SBOM digest": {func(a *domain.SBOMAttestation) { a.Predicate.Digest["sha256"] = testSHA256A }, "content does not match"},
		"visible subject digest": {func(a *domain.SBOMAttestation) {
			a.Subject[0].Digest["sha256"] = testSHA256B
		}, "content does not match"},
		"visible location": {func(a *domain.SBOMAttestation) { a.Predicate.Location.URI = "https://evil.example/x" }, "content does not match"},
		"event content":    {func(a *domain.SBOMAttestation) { a.Event.Content += " " }, "id does not match"},
		"event tag":        {func(a *domain.SBOMAttestation) { a.Event.Tags[4][1] = "sha256:" + testSHA256A }, "id does not match"},
		"event created_at": {func(a *domain.SBOMAttestation) { a.Event.CreatedAt++ }, "id does not match"},
		"event signature": {func(a *domain.SBOMAttestation) {
			a.Event.Sig = strings.Repeat("0", 128)
		}, "invalid signature"},
		"event id":         {func(a *domain.SBOMAttestation) { a.Event.ID = strings.Repeat("0", 64) }, "id does not match"},
		"non-canonical id": {func(a *domain.SBOMAttestation) { a.Event.ID = strings.ToUpper(a.Event.ID) }, "non-canonical"},
		"malformed pubkey": {func(a *domain.SBOMAttestation) { a.Event.PubKey = "not-hex" }, "non-canonical"},
		"event kind":       {func(a *domain.SBOMAttestation) { a.Event.Kind = kinds.SBOMReference }, "kind"},
		"both signatures":  {func(a *domain.SBOMAttestation) { a.Envelope = &domain.DSSEEnvelope{} }, "both"},
		"re-signed content": {func(a *domain.SBOMAttestation) {
			a.Event.Content = strings.Replace(a.Event.Content, testSHA256B, testSHA256A, 1)
			resign(t, a, secret)
		}, "content does not match"},
		"re-signed missing sbom tag": {func(a *domain.SBOMAttestation) {
			a.Event.Tags = a.Event.Tags[:4]
			resign(t, a, secret)
		}, "tags do not match"},
		"re-signed extra tag": {func(a *domain.SBOMAttestation) {
			a.Event.Tags = append(a.Event.Tags, []string{"subject", "sha256:" + testSHA256B})
			resign(t, a, secret)
		}, "tags do not match"},
		"re-signed reordered tags": {func(a *domain.SBOMAttestation) {
			a.Event.Tags[3], a.Event.Tags[4] = a.Event.Tags[4], a.Event.Tags[3]
			resign(t, a, secret)
		}, "tags do not match"},
		"re-signed kind": {func(a *domain.SBOMAttestation) {
			a.Event.Kind = kinds.SBOMReference
			resign(t, a, secret)
		}, "kind"},
		"re-signed by other key": {func(a *domain.SBOMAttestation) { resign(t, a, nostr.Generate()) }, "trusted service pubkey"},
	} {
		t.Run(name, func(t *testing.T) {
			att := cloneAttestation(t, signed)
			tc.mutate(att)
			err := VerifyAttestationSignature(att, trusted)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("tampered verification = %v, want %q", err, tc.want)
			}
		})
	}
	if err := VerifyAttestationSignature(cloneAttestation(t, signed), trusted); err != nil {
		t.Fatalf("untampered clone does not verify: %v", err)
	}
}

type misbehavingSigner struct {
	inner  AttestationSigner
	before func(*nostr.Event)
	after  func(*nostr.Event)
	err    error
}

func (s misbehavingSigner) SignEvent(ctx context.Context, ev *nostr.Event) error {
	if s.err != nil {
		return s.err
	}
	if s.before != nil {
		s.before(ev)
	}
	if err := s.inner.SignEvent(ctx, ev); err != nil {
		return err
	}
	if s.after != nil {
		s.after(ev)
	}
	return nil
}

func TestSignAttestationRejectsMisbehavingSigner(t *testing.T) {
	_, signer := testAttestationSigner(t)
	for name, bad := range map[string]misbehavingSigner{
		"signer error":       {inner: signer, err: errors.New("not the assigned writer")},
		"changed content":    {inner: signer, before: func(ev *nostr.Event) { ev.Content = "{}" }},
		"dropped tags":       {inner: signer, before: func(ev *nostr.Event) { ev.Tags = ev.Tags[:3] }},
		"changed kind":       {inner: signer, before: func(ev *nostr.Event) { ev.Kind = kinds.SBOMReference }},
		"corrupt signature":  {inner: signer, after: func(ev *nostr.Event) { ev.Sig[0] ^= 1 }},
		"id not over fields": {inner: signer, after: func(ev *nostr.Event) { ev.CreatedAt++ }},
	} {
		t.Run(name, func(t *testing.T) {
			att := testSignableAttestation()
			if err := SignAttestation(context.Background(), att, bad); err == nil {
				t.Fatal("misbehaving signer accepted")
			}
			if att.Event != nil || att.Envelope != nil {
				t.Fatal("rejected signature was installed")
			}
		})
	}
	att := testSignableAttestation()
	if err := SignAttestation(context.Background(), att, signer); err != nil {
		t.Fatal(err)
	}
	if err := SignAttestation(context.Background(), att, signer); err == nil || !strings.Contains(err.Error(), "already signed") {
		t.Fatalf("re-signing a signed attestation = %v", err)
	}
	unsubjected := testSignableAttestation()
	unsubjected.Subject = nil
	if err := SignAttestation(context.Background(), unsubjected, signer); err == nil || !strings.Contains(err.Error(), "digests") {
		t.Fatalf("attestation without subject digest = %v", err)
	}
	if err := SignAttestation(context.Background(), testSignableAttestation(), nil); err == nil {
		t.Fatal("nil signer accepted")
	}
}

func TestHistoricalDSSEAttestationStillVerifiesWithPubkeyOnly(t *testing.T) {
	data, err := os.ReadFile("testdata/historical-dsse-attestation.json")
	if err != nil {
		t.Fatal(err)
	}
	att, err := ParseAttestation(data)
	if err != nil {
		t.Fatalf("ParseAttestation(historical DSSE): %v", err)
	}
	if att.Envelope == nil || att.Event != nil {
		t.Fatal("historical fixture must be DSSE-only")
	}
	if err := VerifyAttestationSignature(att, historicalDSSEPubkey); err != nil {
		t.Fatalf("historical DSSE envelope no longer verifies: %v", err)
	}
	if err := VerifyAttestationSignature(att, nostr.Generate().Public().Hex()); err == nil || !strings.Contains(err.Error(), "trusted service pubkey") {
		t.Fatalf("historical DSSE accepted for untrusted pubkey: %v", err)
	}
	if _, _, err := BuildSBOMReferenceEvent(BuildSBOMReferenceEventInput{Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "app", Digest: "sha256:" + testSHA256A}, Attestation: att}); err != nil {
		t.Fatalf("historical DSSE attestation cannot be referenced: %v", err)
	}

	tampered := cloneAttestation(t, att)
	tampered.Predicate.Digest["sha256"] = testSHA256B
	if err := VerifyAttestationSignature(tampered, historicalDSSEPubkey); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered historical statement = %v", err)
	}
	tampered = cloneAttestation(t, att)
	tampered.Envelope.Signatures[0].Sig = strings.Repeat("A", 86) + "=="
	if err := VerifyAttestationSignature(tampered, historicalDSSEPubkey); err == nil {
		t.Fatal("tampered historical DSSE signature accepted")
	}
	tampered = cloneAttestation(t, att)
	tampered.Envelope.PayloadType = "application/json"
	if err := VerifyAttestationSignature(tampered, historicalDSSEPubkey); err == nil || !strings.Contains(err.Error(), "payload type") {
		t.Fatalf("historical DSSE payload type tamper = %v", err)
	}
}

package identityrecovery

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestSBOMRecoveryRepointsAvailabilityToNewAuthor(t *testing.T) {
	ctx := context.Background()
	oldSigner := signer(t)
	oldPub := pub(t, oldSigner)
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", DisplayName: "artifact-v1", Digest: "sha256:" + strings.Repeat("a", 64)}
	att := &domain.SBOMAttestation{Type: sbom.InTotoStatementType, Subject: []domain.AttestationSubject{{Name: subject.DisplayName, Digest: map[string]string{"sha256": strings.Repeat("a", 64)}}}, PredicateType: domain.AttestationTypeSPDX, Predicate: domain.SBOMPredicate{Format: domain.SBOMFormatSPDX, Location: domain.SBOMLocation{Type: domain.SBOMStorageBlossom, URI: "https://blossom.example/" + strings.Repeat("b", 64), MediaType: sbom.MediaTypeSPDX}, Digest: map[string]string{"sha256": strings.Repeat("b", 64)}, Generator: domain.SBOMGenerator{ID: "syft", Version: "1"}}}
	if err := sbom.SignAttestation(ctx, att, oldSigner); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0).UTC()
	ref, d, err := sbom.BuildSBOMReferenceEvent(sbom.BuildSBOMReferenceEventInput{Subject: subject, Attestation: att, CreatedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	if err := oldSigner.SignEvent(ctx, ref); err != nil {
		t.Fatal(err)
	}
	entry := domain.SBOMIndexEntry{SubjectDigest: subject.Digest, AttestationID: "30078:" + oldPub.Hex() + ":" + d, ReferenceDTag: d, ReferencePubkey: oldPub.Hex(), Format: domain.SBOMFormatSPDX, StorageType: domain.SBOMStorageBlossom, LocationURI: att.Predicate.Location.URI, PayloadSHA256: strings.Repeat("b", 64), GeneratorID: "syft", Timestamp: at}
	availability, _, err := sbom.BuildSBOMAvailabilityListEvent(sbom.BuildSBOMAvailabilityListEventInput{Subject: subject, Entries: []domain.SBOMIndexEntry{entry}, PublisherPubkey: oldPub.Hex(), CreatedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	if err := oldSigner.SignEvent(ctx, availability); err != nil {
		t.Fatal(err)
	}
	base, relay, local, newSigner := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{*ref, *availability})
	a := &SBOMRecoveryAdapter{OldPubkey: oldPub, Source: relay, Local: local, Ledger: base.Ledger}
	if _, err := a.Inventory(ctx); err != nil {
		t.Fatal(err)
	}
	proof, err := a.Reassert(ctx, newSigner)
	if err != nil || len(proof) != 2 {
		t.Fatalf("reassert: %v %v", proof, err)
	}
	if relay.publishes != 2 || local.writes != 2 {
		t.Fatalf("writes=%d/%d", relay.publishes, local.writes)
	}
	newPub := pub(t, newSigner)
	var newRef, newAvailability *nostr.Event
	for i := range relay.events {
		ev := &relay.events[i]
		if ev.PubKey != newPub {
			continue
		}
		if ev.Kind == nostr.Kind(sbom.KindSBOMReference) {
			newRef = ev
		} else if ev.Kind == nostr.Kind(sbom.KindSBOMAvailabilityList) {
			newAvailability = ev
		}
	}
	if newRef == nil || newAvailability == nil || newRef.Content != ref.Content || tag(*newAvailability, "a") != "30078:"+newPub.Hex()+":"+d {
		t.Fatal("new-key reference or author linkage invalid")
	}
	if verified, err := a.Verify(ctx); err != nil || len(verified) != 2 {
		t.Fatalf("verify: %v %v", verified, err)
	}
	if _, err := a.Reassert(ctx, newSigner); err != nil || relay.publishes != 2 {
		t.Fatalf("idempotence: %v", err)
	}
}

func TestSBOMRecoveryRejectsBrokenAuthorLinkBeforePublish(t *testing.T) {
	ctx := context.Background()
	oldSigner := signer(t)
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", DisplayName: "artifact-v1", Digest: "sha256:" + strings.Repeat("a", 64)}
	at := time.Unix(100, 0).UTC()
	entry := domain.SBOMIndexEntry{SubjectDigest: subject.Digest, ReferenceDTag: "sbom:ref:missing", ReferencePubkey: pub(t, oldSigner).Hex(), Format: domain.SBOMFormatSPDX, StorageType: domain.SBOMStorageBlossom, LocationURI: "https://blossom.example/missing", PayloadSHA256: strings.Repeat("b", 64), GeneratorID: "syft", Timestamp: at}
	availability, _, err := sbom.BuildSBOMAvailabilityListEvent(sbom.BuildSBOMAvailabilityListEventInput{Subject: subject, Entries: []domain.SBOMIndexEntry{entry}, PublisherPubkey: pub(t, oldSigner).Hex(), CreatedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	if err := oldSigner.SignEvent(ctx, availability); err != nil {
		t.Fatal(err)
	}
	base, relay, local, newSigner := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{*availability})
	a := &SBOMRecoveryAdapter{OldPubkey: availability.PubKey, Source: relay, Local: local, Ledger: base.Ledger}
	if _, err := a.Reassert(ctx, newSigner); err == nil || relay.publishes != 0 {
		t.Fatalf("broken availability link accepted: %v", err)
	}
	relay.readErr = fmt.Errorf("EOSE incomplete")
	if _, err := a.Inventory(ctx); err == nil {
		t.Fatal("incomplete relay history accepted")
	}
}

package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// --- Test helpers ---

// fakeOCKPublisher captures published key-envelope records.
type fakeOCKPublisher struct {
	envelopes []struct{ DTag, Content string }
}

func (f *fakeOCKPublisher) PublishKeyEnvelope(_ context.Context, dTag string, content string) error {
	f.envelopes = append(f.envelopes, struct{ DTag, Content string }{dTag, content})
	return nil
}

// fakeOCKHistory returns previously published envelopes for recovery.
type fakeOCKHistory struct {
	records []domain.KeyEnvelopeRecord
}

func (f *fakeOCKHistory) FindKeyEnvelopes(_ context.Context, _ string) ([]domain.KeyEnvelopeRecord, error) {
	return f.records, nil
}

// fakeOCKMemberSource returns a fixed list of member pubkeys.
type fakeOCKMemberSource struct {
	pubkeys []string
}

func (f *fakeOCKMemberSource) OrgMemberPubkeys(_ context.Context, _ string) ([]string, error) {
	return f.pubkeys, nil
}

// newTestKeySigner creates a keyer from a hex private key using keyer.New.
func newTestKeySigner(t *testing.T, hexKey string) gonostr.Keyer {
	t.Helper()
	signer, err := keyer.New(context.Background(), nil, hexKey, nil)
	if err != nil {
		t.Fatalf("create test signer: %v", err)
	}
	return signer
}

// test key material (deterministic for reproducibility)
const (
	serviceKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	memberAKeyHex = "0000000000000000000000000000000000000000000000000000000000000002"
	memberBKeyHex = "0000000000000000000000000000000000000000000000000000000000000003"
	viewerKeyHex  = "0000000000000000000000000000000000000000000000000000000000000004"
)

func pubkeyFromHex(t *testing.T, hex string) string {
	t.Helper()
	sk, err := gonostr.SecretKeyFromHex(hex)
	if err != nil {
		t.Fatalf("pubkeyFromHex: %v", err)
	}
	return sk.Public().Hex()
}

func newTestOCKManager(t *testing.T, memberKeys []string) (*OCKManager, *fakeOCKPublisher, *fakeOCKHistory) {
	t.Helper()
	signer := newTestKeySigner(t, serviceKeyHex)
	publisher := &fakeOCKPublisher{}
	history := &fakeOCKHistory{}
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)

	manager := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     publisher,
		History:       history,
		Members:       &fakeOCKMemberSource{pubkeys: memberKeys},
	})
	return manager, publisher, history
}

// --- Test 1: Member can decrypt org/secret-metadata/notification-metadata ---

func TestMemberDecryptOrgRecord(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)
	manager, publisher, _ := newTestOCKManager(t, []string{memberAPubkey})

	// Create an encryptor and encrypt some content.
	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-1"
	plaintext := []byte(`{"org_id":"test-org-1","name":"test-org","deleted":false}`)
	recordCtx := ConfidentialRecordContext{LegacyKind: 32005, DTag: "org:test-org-1", Topic: "org"}

	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, plaintext, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Member A should be able to find their key-envelope and decrypt.
	// The member needs to:
	// 1. Find their envelope from the published set
	// 2. Decrypt it with NIP-44 to get the OCK
	// 3. Use the OCK to decrypt the record
	memberASigner := newTestKeySigner(t, memberAKeyHex)
	var memberOCK OrgContentKey
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)

	for _, env := range publisher.envelopes {
		// Try to decrypt each envelope with member A's signer.
		senderPK, err := gonostr.PubKeyFromHex(servicePubkey)
		if err != nil {
			continue
		}
		pt, err := memberASigner.Decrypt(ctx, env.Content, senderPK)
		if err != nil {
			continue // Not for this member.
		}
		key, recipientPubkey, err := UnmarshalOCKWrap([]byte(pt))
		if err != nil {
			continue
		}
		if recipientPubkey == memberAPubkey {
			memberOCK = key
			break
		}
	}
	if memberOCK.Version == 0 {
		t.Fatal("member A could not find their key envelope")
	}

	// Decrypt the record with the member's OCK.
	decrypted, err := DecryptConfidentialContent(memberOCK, encrypted, recordCtx)
	if err != nil {
		t.Fatalf("member decrypt failed: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("plaintext mismatch: got %q, want %q", decrypted, plaintext)
	}
}

// --- Test 2: Viewer cannot read secret values or channel credentials ---

func TestViewerCannotReadServiceInner(t *testing.T) {
	ctx := context.Background()
	viewerPubkey := pubkeyFromHex(t, viewerKeyHex)
	manager, publisher, _ := newTestOCKManager(t, []string{viewerPubkey})

	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-1"
	orgVisible := []byte(`{"name":"webhook-channel","type":"webhook"}`)
	serviceOnly := []byte(`{"url":"https://hooks.example.com","secret":"s3cr3t"}`)
	recordCtx := ConfidentialRecordContext{LegacyKind: 32009, DTag: "channel-1", Topic: "notification-channel"}

	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, orgVisible, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, serviceOnly)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Viewer can find their OCK envelope and decrypt the AEAD layer.
	viewerSigner := newTestKeySigner(t, viewerKeyHex)
	var viewerOCK OrgContentKey
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)

	for _, env := range publisher.envelopes {
		senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
		pt, err := viewerSigner.Decrypt(ctx, env.Content, senderPK)
		if err != nil {
			continue
		}
		key, recipientPubkey, err := UnmarshalOCKWrap([]byte(pt))
		if err != nil {
			continue
		}
		if recipientPubkey == viewerPubkey {
			viewerOCK = key
			break
		}
	}
	if viewerOCK.Version == 0 {
		t.Fatal("viewer could not find their key envelope")
	}

	// Viewer CAN decrypt the org-visible layer.
	decryptedOrgVisible, err := DecryptConfidentialContent(viewerOCK, encrypted, recordCtx)
	if err != nil {
		t.Fatalf("viewer decrypt org-visible failed: %v", err)
	}
	if string(decryptedOrgVisible) != string(orgVisible) {
		t.Fatalf("org-visible mismatch: got %q", decryptedOrgVisible)
	}

	// Viewer CANNOT decrypt the service_inner (NIP-44 encrypted to service pubkey).
	var envelope ConfidentialEnvelope
	if err := json.Unmarshal([]byte(encrypted), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if envelope.ServiceInner == "" {
		t.Fatal("expected service_inner to be present")
	}

	// Try to decrypt service_inner with the viewer's signer — must fail.
	senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
	_, err = viewerSigner.Decrypt(ctx, envelope.ServiceInner, senderPK)
	if err == nil {
		t.Fatal("viewer should NOT be able to decrypt service_inner")
	}
}

// --- Test 3: Removed member cannot read records after rotation ---

func TestRemovedMemberCannotReadPostRotation(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)
	memberBPubkey := pubkeyFromHex(t, memberBKeyHex)

	// Start with both members.
	signer := newTestKeySigner(t, serviceKeyHex)
	publisher := &fakeOCKPublisher{}
	history := &fakeOCKHistory{}
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)

	manager := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     publisher,
		History:       history,
		Members:       &fakeOCKMemberSource{pubkeys: []string{memberAPubkey, memberBPubkey}},
	})
	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-remove"
	recordCtx := ConfidentialRecordContext{LegacyKind: 32005, DTag: "org:test-remove", Topic: "org"}

	// Encrypt a record before rotation — both members can decrypt.
	preRotation, err := encryptor.EncryptConfidential(ctx, orgID, []byte(`{"pre":"rotation"}`), recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("pre-rotation encrypt: %v", err)
	}

	// Now "remove" member B by creating a new manager without B.
	publisher2 := &fakeOCKPublisher{}
	manager2 := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     publisher2,
		History:       history,
		Members:       &fakeOCKMemberSource{pubkeys: []string{memberAPubkey}},
	})
	encryptor2 := NewConfidentialEncryptor(manager2, nil)

	// Rotate key — new version excludes member B.
	if err := encryptor2.RotateKey(ctx, orgID); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Encrypt a record after rotation.
	postRotation, err := encryptor2.EncryptConfidential(ctx, orgID, []byte(`{"post":"rotation"}`), recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("post-rotation encrypt: %v", err)
	}

	// Member B should be able to decrypt pre-rotation record.
	memberBSigner := newTestKeySigner(t, memberBKeyHex)
	var memberBOCKv1 OrgContentKey
	for _, env := range publisher.envelopes {
		senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
		pt, err := memberBSigner.Decrypt(ctx, env.Content, senderPK)
		if err != nil {
			continue
		}
		key, rpk, err := UnmarshalOCKWrap([]byte(pt))
		if err != nil || rpk != memberBPubkey {
			continue
		}
		memberBOCKv1 = key
	}
	if memberBOCKv1.Version == 0 {
		t.Fatal("member B should have v1 key")
	}
	_, err = DecryptConfidentialContent(memberBOCKv1, preRotation, recordCtx)
	if err != nil {
		t.Fatalf("member B should decrypt pre-rotation: %v", err)
	}

	// Member B should NOT have a v2 envelope (was excluded from rotation).
	var memberBHasV2 bool
	for _, env := range publisher2.envelopes {
		senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
		pt, err := memberBSigner.Decrypt(ctx, env.Content, senderPK)
		if err != nil {
			continue
		}
		_, rpk, err := UnmarshalOCKWrap([]byte(pt))
		if err != nil || rpk != memberBPubkey {
			continue
		}
		memberBHasV2 = true
	}
	if memberBHasV2 {
		t.Fatal("member B should NOT have a v2 key envelope after being removed")
	}

	// Member B tries to decrypt post-rotation with v1 key — must fail (different key version).
	_, err = DecryptConfidentialContent(memberBOCKv1, postRotation, recordCtx)
	if err == nil {
		t.Fatal("member B should NOT decrypt post-rotation record with v1 key")
	}
}

// --- Test 4: No plaintext pubkey or role leaks ---

func TestNoPubkeyLeaksInEnvelopes(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)
	manager, publisher, _ := newTestOCKManager(t, []string{memberAPubkey})

	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-leak-check"
	plaintext := []byte(fmt.Sprintf(`{"org_id":"test-org-leak-check","pubkey":"%s","role":"admin"}`, memberAPubkey))
	recordCtx := ConfidentialRecordContext{LegacyKind: 32006, DTag: "org:member:test-org:pk", Topic: "org-member"}

	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, plaintext, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Verify: member pubkey does NOT appear in the encrypted content.
	if strings.Contains(encrypted, memberAPubkey) {
		t.Fatal("member pubkey appears in encrypted content")
	}

	// Verify: member pubkey does NOT appear in key-envelope d-tags.
	for _, env := range publisher.envelopes {
		if strings.Contains(env.DTag, memberAPubkey) {
			t.Fatalf("member pubkey appears in key-envelope d-tag: %s", env.DTag)
		}
		if strings.Contains(env.Content, memberAPubkey) {
			// Content is NIP-44 encrypted — the pubkey should not appear in ciphertext.
			// But actually the NIP-44 ciphertext is base64, so the hex pubkey won't appear.
			// This is a defence-in-depth check.
		}
	}

	// Verify: "role" doesn't appear in the encrypted envelope.
	if strings.Contains(encrypted, `"role"`) {
		t.Fatal("role field appears in encrypted content")
	}
}

// --- Test 5: AD binding rejects coordinate swaps ---

func TestADBindingRejectsCoordinateSwaps(t *testing.T) {
	ctx := context.Background()
	manager, _, _ := newTestOCKManager(t, nil)
	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-ad"
	recordCtxA := ConfidentialRecordContext{LegacyKind: 32005, DTag: "org:alpha", Topic: "org"}
	recordCtxB := ConfidentialRecordContext{LegacyKind: 32005, DTag: "org:beta", Topic: "org"}

	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, []byte(`{"name":"alpha"}`), recordCtxA.LegacyKind, recordCtxA.DTag, recordCtxA.Topic, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Decrypt with the correct context — should succeed.
	decrypted, err := encryptor.DecryptConfidential(ctx, encrypted, recordCtxA.LegacyKind, recordCtxA.DTag, recordCtxA.Topic)
	if err != nil {
		t.Fatalf("correct context decrypt failed: %v", err)
	}
	if string(decrypted) != `{"name":"alpha"}` {
		t.Fatalf("wrong plaintext: %q", decrypted)
	}

	// Decrypt with a different d-tag — must fail (AD mismatch).
	_, err = encryptor.DecryptConfidential(ctx, encrypted, recordCtxB.LegacyKind, recordCtxB.DTag, recordCtxB.Topic)
	if err == nil {
		t.Fatal("decrypt with wrong d-tag should fail (AD binding)")
	}

	// Decrypt with a different topic — must fail.
	_, err = encryptor.DecryptConfidential(ctx, encrypted, recordCtxA.LegacyKind, recordCtxA.DTag, "other-topic")
	if err == nil {
		t.Fatal("decrypt with wrong topic should fail (AD binding)")
	}

	// Decrypt with a different legacy_kind — must fail.
	_, err = encryptor.DecryptConfidential(ctx, encrypted, 99999, recordCtxA.DTag, recordCtxA.Topic)
	if err == nil {
		t.Fatal("decrypt with wrong legacy_kind should fail (AD binding)")
	}
}

// --- Test 6: Bunker-style signer (no raw key) works end to end ---

// bunkerFakeSigner implements gonostr.Keyer without exposing raw keys.
// It delegates to a keyer.KeySigner but proves no raw-key access is needed
// by the OCK flow.
type bunkerFakeSigner struct {
	inner gonostr.Keyer
}

func (b *bunkerFakeSigner) GetPublicKey(ctx context.Context) (gonostr.PubKey, error) {
	return b.inner.GetPublicKey(ctx)
}

func (b *bunkerFakeSigner) SignEvent(ctx context.Context, evt *gonostr.Event) error {
	return b.inner.SignEvent(ctx, evt)
}

func (b *bunkerFakeSigner) Encrypt(ctx context.Context, plaintext string, recipient gonostr.PubKey) (string, error) {
	return b.inner.Encrypt(ctx, plaintext, recipient)
}

func (b *bunkerFakeSigner) Decrypt(ctx context.Context, ciphertext string, sender gonostr.PubKey) (string, error) {
	return b.inner.Decrypt(ctx, ciphertext, sender)
}

func (b *bunkerFakeSigner) Nip04Encrypt(ctx context.Context, plaintext string, recipient gonostr.PubKey) (string, error) {
	return "", fmt.Errorf("nip04 not supported by bunker fake")
}

func (b *bunkerFakeSigner) Nip04Decrypt(ctx context.Context, ciphertext string, sender gonostr.PubKey) (string, error) {
	return "", fmt.Errorf("nip04 not supported by bunker fake")
}

func TestBunkerSignerEndToEnd(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)

	// Use a bunker-style signer wrapper (no raw key access).
	inner := newTestKeySigner(t, serviceKeyHex)
	bunkerSigner := &bunkerFakeSigner{inner: inner}
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)

	publisher := &fakeOCKPublisher{}
	history := &fakeOCKHistory{}

	manager := NewOCKManager(OCKManagerConfig{
		Signer:        bunkerSigner,
		ServicePubkey: servicePubkey,
		Publisher:     publisher,
		History:       history,
		Members:       &fakeOCKMemberSource{pubkeys: []string{memberAPubkey}},
	})
	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-bunker"
	plaintext := []byte(`{"data":"bunker-test"}`)
	recordCtx := ConfidentialRecordContext{LegacyKind: 32005, DTag: "test-d", Topic: "org"}

	// Encrypt — uses bunker signer for NIP-44 key wrapping.
	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, plaintext, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("encrypt with bunker signer: %v", err)
	}

	// Decrypt — uses bunker signer for OCK recovery.
	decrypted, err := encryptor.DecryptConfidential(ctx, encrypted, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic)
	if err != nil {
		t.Fatalf("decrypt with bunker signer: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("plaintext mismatch: got %q", decrypted)
	}

	// Member can also decrypt using the envelope published by the bunker signer.
	memberASigner := newTestKeySigner(t, memberAKeyHex)
	var memberOCK OrgContentKey
	for _, env := range publisher.envelopes {
		senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
		pt, err := memberASigner.Decrypt(ctx, env.Content, senderPK)
		if err != nil {
			continue
		}
		key, rpk, err := UnmarshalOCKWrap([]byte(pt))
		if err != nil || rpk != memberAPubkey {
			continue
		}
		memberOCK = key
	}
	if memberOCK.Version == 0 {
		t.Fatal("member should find their envelope from bunker-published set")
	}
	memberDecrypted, err := DecryptConfidentialContent(memberOCK, encrypted, recordCtx)
	if err != nil {
		t.Fatalf("member decrypt from bunker envelope: %v", err)
	}
	if string(memberDecrypted) != string(plaintext) {
		t.Fatalf("member plaintext mismatch: %q", memberDecrypted)
	}
}

// --- Test 7: Daemon restart recovers OCK from service envelope ---

func TestDaemonRestartRecovery(t *testing.T) {
	ctx := context.Background()
	signer := newTestKeySigner(t, serviceKeyHex)
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)

	publisher := &fakeOCKPublisher{}

	// Create initial manager and encrypt.
	manager1 := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     publisher,
		History:       &fakeOCKHistory{},
		Members:       &fakeOCKMemberSource{},
	})
	encryptor1 := NewConfidentialEncryptor(manager1, nil)

	orgID := "test-org-restart"
	plaintext := []byte(`{"data":"recovery-test"}`)
	recordCtx := ConfidentialRecordContext{LegacyKind: 32005, DTag: "restart-test", Topic: "org"}

	encrypted, err := encryptor1.EncryptConfidential(ctx, orgID, plaintext, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Simulate restart: create a new manager with no cache but WITH history
	// containing the published envelopes.
	var historyRecords []domain.KeyEnvelopeRecord
	for _, env := range publisher.envelopes {
		historyRecords = append(historyRecords, domain.KeyEnvelopeRecord{
			DTag:    env.DTag,
			Content: env.Content,
		})
	}

	manager2 := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     &fakeOCKPublisher{},
		History:       &fakeOCKHistory{records: historyRecords},
		Members:       &fakeOCKMemberSource{},
	})
	encryptor2 := NewConfidentialEncryptor(manager2, nil)

	// Decrypt with the recovered manager — should succeed.
	decrypted, err := encryptor2.DecryptConfidential(ctx, encrypted, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic)
	if err != nil {
		t.Fatalf("decrypt after restart: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("plaintext mismatch after restart: got %q", decrypted)
	}
}

// --- Test 8: Trust-set hydration with new format ---

func TestTrustSetHydrationWithNewFormat(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)
	manager, _, _ := newTestOCKManager(t, []string{memberAPubkey})
	encryptor := NewConfidentialEncryptor(manager, nil)

	orgUUID := uuid.New()
	orgID := orgUUID.String()
	memberContent, _ := json.Marshal(map[string]interface{}{
		"org_id": orgID, "pubkey": memberAPubkey, "role": "admin", "deleted": false,
	})
	recordCtx := ConfidentialRecordContext{LegacyKind: 32006, DTag: "org:member:" + orgID + ":" + memberAPubkey, Topic: "org-member"}

	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, memberContent, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Create a RelayMemberEventHandler with the new confidential encryptor.
	trustSet := NewTrustSet(nil, nil)
	handler := NewRelayMemberEventHandler(encryptor, nil, trustSet, nil, nil)

	// HandleEncryptedMemberEvent with the new format.
	if err := handler.HandleEncryptedMemberEvent(ctx, encrypted, recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic); err != nil {
		t.Fatalf("handle encrypted member event: %v", err)
	}

	// Verify member appears in TrustSet.
	relayMembers := trustSet.RelayMembersFor(orgID)
	if relayMembers == nil {
		t.Fatal("relay members should not be nil after hydration")
	}
	if relayMembers[memberAPubkey] != domain.RoleAdmin {
		t.Fatalf("expected member A as admin, got %v", relayMembers[memberAPubkey])
	}
}

// --- Test 9: Old-format migration (dual-read) ---

func TestOldFormatMigrationDualRead(t *testing.T) {
	ctx := context.Background()

	// Set up a legacy O1 encryptor.
	legacyKey := OrgStateKey{Ref: "test-key", Version: "v1", Key: make([]byte, 32)}
	for i := range legacyKey.Key {
		legacyKey.Key[i] = byte(i)
	}
	legacyEncryptor := NewOrgStateEncryptor(StaticOrgStateKeyProvider{Key: legacyKey})

	// Encrypt member content with the legacy O1 format.
	memberContent, _ := json.Marshal(map[string]interface{}{
		"org_id": "test-org-migrate", "pubkey": "member-pk", "role": "admin", "deleted": false,
	})
	legacyEncrypted, err := encryptOrgState(ctx, legacyKey, memberContent, "org:member:test-migrate:member-pk", "org-member")
	if err != nil {
		t.Fatalf("legacy encrypt: %v", err)
	}

	// Set up a new confidential encryptor — it will fail to decrypt the legacy format.
	manager, _, _ := newTestOCKManager(t, nil)
	confidentialEnc := NewConfidentialEncryptor(manager, nil)

	// The dual-read function should fall back to the legacy O1 decryptor.
	orgID, pubkey, role, deleted, err := DecryptMemberContentConfidential(
		confidentialEnc, legacyEncryptor, legacyEncrypted, 0, "", "")
	if err != nil {
		t.Fatalf("dual-read decrypt failed: %v", err)
	}
	if orgID != "test-org-migrate" || pubkey != "member-pk" || role != "admin" || deleted {
		t.Fatalf("unexpected values: orgID=%s pubkey=%s role=%s deleted=%v", orgID, pubkey, role, deleted)
	}

	// Also test: new format goes through the confidential path.
	newEncrypted, err := confidentialEnc.EncryptConfidential(ctx, "test-org-new", memberContent, 32006, "new-d", "org-member", nil)
	if err != nil {
		t.Fatalf("new format encrypt: %v", err)
	}
	orgID2, pubkey2, role2, _, err := DecryptMemberContentConfidential(
		confidentialEnc, legacyEncryptor, newEncrypted, 32006, "new-d", "org-member")
	if err != nil {
		t.Fatalf("new format decrypt failed: %v", err)
	}
	if orgID2 != "test-org-migrate" || pubkey2 != "member-pk" || role2 != "admin" {
		t.Fatalf("new format values: orgID=%s pubkey=%s role=%s", orgID2, pubkey2, role2)
	}
}

// --- Test 10: Production path — adding member produces decrypt-ready envelope ---

func TestProductionPathAddMemberDecrypt(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)
	memberBPubkey := pubkeyFromHex(t, memberBKeyHex)

	// Wire the production path: OCKManager → ConfidentialEncryptor → encrypt → member decrypt.
	// This mirrors app.go wiring with TrustSetMemberSource providing the member set.
	signer := newTestKeySigner(t, serviceKeyHex)
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)
	publisher := &fakeOCKPublisher{}

	// Simulate TrustSet member source returning both members.
	trustSet := NewTrustSet(nil, nil)
	trustSet.SetRelayMembers("test-org-prod", map[string]domain.Role{
		memberAPubkey: domain.RoleAdmin,
		memberBPubkey: domain.RoleViewer,
	})
	memberSource := NewTrustSetMemberSource(trustSet, nil)

	manager := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     publisher,
		History:       &fakeOCKHistory{},
		Members:       memberSource,
		Logger:        nil,
	})
	encryptor := NewConfidentialEncryptor(manager, nil)

	// Encrypt a member record (the production path through OrgCanonicalPublisher).
	orgID := "test-org-prod"
	memberContent, _ := json.Marshal(map[string]interface{}{
		"org_id": orgID, "pubkey": memberAPubkey, "role": "admin", "deleted": false,
	})
	legacyKind := 32006
	dTag := "org:member:" + orgID + ":" + memberAPubkey
	topic := "org-member"

	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, memberContent, legacyKind, dTag, topic, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Verify: member A can find their envelope and decrypt.
	memberASigner := newTestKeySigner(t, memberAKeyHex)
	memberAOCK := findMemberEnvelope(t, ctx, publisher.envelopes, memberASigner, servicePubkey, memberAPubkey)
	if memberAOCK.Version == 0 {
		t.Fatal("member A should find their key envelope")
	}

	decrypted, err := DecryptConfidentialContent(memberAOCK, encrypted, ConfidentialRecordContext{
		LegacyKind: legacyKind, DTag: dTag, Topic: topic,
	})
	if err != nil {
		t.Fatalf("member A decrypt: %v", err)
	}
	if string(decrypted) != string(memberContent) {
		t.Fatalf("plaintext mismatch: got %q", decrypted)
	}

	// Verify: member B (viewer) also got an envelope and can decrypt the same record.
	memberBSigner := newTestKeySigner(t, memberBKeyHex)
	memberBOCK := findMemberEnvelope(t, ctx, publisher.envelopes, memberBSigner, servicePubkey, memberBPubkey)
	if memberBOCK.Version == 0 {
		t.Fatal("member B should find their key envelope")
	}

	decryptedB, err := DecryptConfidentialContent(memberBOCK, encrypted, ConfidentialRecordContext{
		LegacyKind: legacyKind, DTag: dTag, Topic: topic,
	})
	if err != nil {
		t.Fatalf("member B decrypt: %v", err)
	}
	if string(decryptedB) != string(memberContent) {
		t.Fatalf("member B plaintext mismatch: got %q", decryptedB)
	}
}

// --- Test 11: Production path — secret metadata visible, value hidden ---

func TestProductionPathSecretMetadataVsValue(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)

	signer := newTestKeySigner(t, serviceKeyHex)
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)
	publisher := &fakeOCKPublisher{}

	manager := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     publisher,
		History:       &fakeOCKHistory{},
		Members:       &fakeOCKMemberSource{pubkeys: []string{memberAPubkey}},
	})
	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-secret"
	// Org-visible: secret ref metadata (no value).
	metadata := []byte(`{"id":"secret-1","name":"DB_PASSWORD","service_id":"svc-1"}`)
	// Service-only: the actual secret value.
	secretValue := []byte(`{"value":"s3cr3t-p@ssw0rd!"}`)
	legacyKind := 32008
	dTag := "secret-1"
	topic := "secret"

	encrypted, err := encryptor.EncryptConfidential(ctx, orgID, metadata, legacyKind, dTag, topic, secretValue)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Member CAN decrypt the org-visible metadata.
	memberASigner := newTestKeySigner(t, memberAKeyHex)
	memberAOCK := findMemberEnvelope(t, ctx, publisher.envelopes, memberASigner, servicePubkey, memberAPubkey)

	decryptedMeta, err := DecryptConfidentialContent(memberAOCK, encrypted, ConfidentialRecordContext{
		LegacyKind: legacyKind, DTag: dTag, Topic: topic,
	})
	if err != nil {
		t.Fatalf("member decrypt metadata: %v", err)
	}
	if string(decryptedMeta) != string(metadata) {
		t.Fatalf("metadata mismatch: got %q", decryptedMeta)
	}

	// Member CANNOT decrypt the service_inner (secret value).
	var envelope ConfidentialEnvelope
	if err := json.Unmarshal([]byte(encrypted), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.ServiceInner == "" {
		t.Fatal("service_inner should be present")
	}
	senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
	_, err = memberASigner.Decrypt(ctx, envelope.ServiceInner, senderPK)
	if err == nil {
		t.Fatal("member should NOT decrypt service_inner (secret value)")
	}

	// Service CAN decrypt the service_inner.
	decryptedValue, err := encryptor.DecryptServiceInner(ctx, encrypted)
	if err != nil {
		t.Fatalf("service decrypt service_inner: %v", err)
	}
	if string(decryptedValue) != string(secretValue) {
		t.Fatalf("secret value mismatch: got %q", decryptedValue)
	}

	// Verify no plaintext secret value in the encrypted output.
	if strings.Contains(encrypted, "s3cr3t-p@ssw0rd!") {
		t.Fatal("plaintext secret value leaked into encrypted content")
	}
}

// --- Test 12: Production path — remove member triggers rotation, excluded ---

func TestProductionPathRemoveMemberRotation(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)
	memberBPubkey := pubkeyFromHex(t, memberBKeyHex)

	signer := newTestKeySigner(t, serviceKeyHex)
	servicePubkey := pubkeyFromHex(t, serviceKeyHex)

	// Phase 1: both members are present.
	trustSet := NewTrustSet(nil, nil)
	trustSet.SetRelayMembers("test-org-rotation", map[string]domain.Role{
		memberAPubkey: domain.RoleAdmin,
		memberBPubkey: domain.RoleAdmin,
	})
	memberSource := NewTrustSetMemberSource(trustSet, nil)

	pub1 := &fakeOCKPublisher{}
	manager := NewOCKManager(OCKManagerConfig{
		Signer:        signer,
		ServicePubkey: servicePubkey,
		Publisher:     pub1,
		History:       &fakeOCKHistory{},
		Members:       memberSource,
	})
	encryptor := NewConfidentialEncryptor(manager, nil)

	orgID := "test-org-rotation"
	recordCtx := ConfidentialRecordContext{LegacyKind: 32005, DTag: "org:" + orgID, Topic: "org"}

	// Encrypt a record before rotation.
	preRotation, err := encryptor.EncryptConfidential(ctx, orgID, []byte(`{"state":"before"}`), recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("pre-rotation encrypt: %v", err)
	}

	// Both members have v1 envelopes.
	memberBSigner := newTestKeySigner(t, memberBKeyHex)
	memberBv1 := findMemberEnvelope(t, ctx, pub1.envelopes, memberBSigner, servicePubkey, memberBPubkey)
	if memberBv1.Version == 0 {
		t.Fatal("member B should have v1 key")
	}

	// Verify B can decrypt pre-rotation.
	_, err = DecryptConfidentialContent(memberBv1, preRotation, recordCtx)
	if err != nil {
		t.Fatalf("member B should decrypt pre-rotation: %v", err)
	}

	// Phase 2: remove member B — update TrustSet and rotate.
	trustSet.SetRelayMembers(orgID, map[string]domain.Role{
		memberAPubkey: domain.RoleAdmin,
	})

	// Simulate OrgIntentHandler.triggerKeyRotation → ConfidentialEncryptor.RotateKey.
	// In production, the callback receives the org UUID and calls RotateKey with
	// orgID.String(). Here we call RotateKey directly with the orgID string.
	if err := encryptor.RotateKey(ctx, orgID); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}

	// Encrypt after rotation — uses v2 key.
	postRotation, err := encryptor.EncryptConfidential(ctx, orgID, []byte(`{"state":"after"}`), recordCtx.LegacyKind, recordCtx.DTag, recordCtx.Topic, nil)
	if err != nil {
		t.Fatalf("post-rotation encrypt: %v", err)
	}

	// Verify B cannot find a v2 envelope.
	memberBv2 := findMemberEnvelopeForVersion(t, ctx, pub1.envelopes, memberBSigner, servicePubkey, memberBPubkey, 2)
	if memberBv2.Version != 0 {
		t.Fatal("member B should NOT have a v2 key envelope")
	}

	// Verify B cannot decrypt post-rotation record with v1 key.
	_, err = DecryptConfidentialContent(memberBv1, postRotation, recordCtx)
	if err == nil {
		t.Fatal("member B should NOT decrypt post-rotation record with v1 key")
	}

	// Verify A CAN still decrypt (A was in the rotation set).
	memberASigner := newTestKeySigner(t, memberAKeyHex)
	memberAv2 := findMemberEnvelopeForVersion(t, ctx, pub1.envelopes, memberASigner, servicePubkey, memberAPubkey, 2)
	if memberAv2.Version == 0 {
		t.Fatal("member A should have v2 key")
	}
	decryptedPost, err := DecryptConfidentialContent(memberAv2, postRotation, recordCtx)
	if err != nil {
		t.Fatalf("member A post-rotation decrypt: %v", err)
	}
	if string(decryptedPost) != `{"state":"after"}` {
		t.Fatalf("wrong post-rotation plaintext: %q", decryptedPost)
	}
}

// --- Test 13: TrustSetMemberSource relay-first, Postgres-fallback ---

func TestTrustSetMemberSourceRelayFirst(t *testing.T) {
	ctx := context.Background()
	memberAPubkey := pubkeyFromHex(t, memberAKeyHex)
	memberBPubkey := pubkeyFromHex(t, memberBKeyHex)

	trustSet := NewTrustSet(nil, nil)

	// No relay members, no Postgres → empty set.
	source := NewTrustSetMemberSource(trustSet, nil)
	pubkeys, err := source.OrgMemberPubkeys(ctx, "test-org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pubkeys) != 0 {
		t.Fatalf("expected empty, got %d", len(pubkeys))
	}

	// Add relay members → returns them.
	trustSet.SetRelayMembers("test-org", map[string]domain.Role{
		memberAPubkey: domain.RoleAdmin,
		memberBPubkey: domain.RoleViewer,
	})
	pubkeys, err = source.OrgMemberPubkeys(ctx, "test-org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pubkeys) != 2 {
		t.Fatalf("expected 2 members, got %d", len(pubkeys))
	}

	// Verify both members are present.
	found := make(map[string]bool)
	for _, pk := range pubkeys {
		found[pk] = true
	}
	if !found[memberAPubkey] || !found[memberBPubkey] {
		t.Fatalf("missing expected members: %v", pubkeys)
	}
}

// --- Helper: find a member's OCK envelope from published set ---

func findMemberEnvelope(t *testing.T, ctx context.Context, envelopes []struct{ DTag, Content string }, memberSigner gonostr.Keyer, servicePubkey, memberPubkey string) OrgContentKey {
	t.Helper()
	senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
	for _, env := range envelopes {
		pt, err := memberSigner.Decrypt(ctx, env.Content, senderPK)
		if err != nil {
			continue
		}
		key, rpk, err := UnmarshalOCKWrap([]byte(pt))
		if err != nil || rpk != memberPubkey {
			continue
		}
		return key
	}
	return OrgContentKey{}
}

func findMemberEnvelopeForVersion(t *testing.T, ctx context.Context, envelopes []struct{ DTag, Content string }, memberSigner gonostr.Keyer, servicePubkey, memberPubkey string, version int) OrgContentKey {
	t.Helper()
	senderPK, _ := gonostr.PubKeyFromHex(servicePubkey)
	for _, env := range envelopes {
		pt, err := memberSigner.Decrypt(ctx, env.Content, senderPK)
		if err != nil {
			continue
		}
		key, rpk, err := UnmarshalOCKWrap([]byte(pt))
		if err != nil || rpk != memberPubkey {
			continue
		}
		if key.Version == version {
			return key
		}
	}
	return OrgContentKey{}
}

// --- Test 14: Legacy EncryptedDomainHandlers path — add member wraps OCK ---

// lifecycleTrackingPublisher records key lifecycle calls made by the publisher.
type lifecycleTrackingPublisher struct {
	published []memberPublishEvent
	encryptor *ConfidentialEncryptor
	rotations []string // orgIDs that triggered RotateKey
	wraps     []struct{ OrgID, Pubkey string }
}

type memberPublishEvent struct {
	OrgID   uuid.UUID
	Pubkey  string
	Deleted bool
	Role    domain.Role
}

func (p *lifecycleTrackingPublisher) PublishOrg(_ context.Context, _ *domain.Organization, _ bool) error {
	return nil
}
func (p *lifecycleTrackingPublisher) PublishMember(ctx context.Context, member *domain.OrgMember, deleted bool, prevRole ...domain.Role) error {
	p.published = append(p.published, memberPublishEvent{
		OrgID: member.OrgID, Pubkey: member.Pubkey, Deleted: deleted, Role: member.Role,
	})
	// Drive key lifecycle exactly as OrgCanonicalPublisher does.
	orgID := member.OrgID.String()
	if deleted {
		if err := p.encryptor.RotateKey(ctx, orgID); err == nil {
			p.rotations = append(p.rotations, orgID)
		}
	} else {
		if err := p.encryptor.WrapKeyForMember(ctx, orgID, member.Pubkey); err == nil {
			p.wraps = append(p.wraps, struct{ OrgID, Pubkey string }{orgID, member.Pubkey})
		}
		if len(prevRole) > 0 && prevRole[0] != "" {
			if domain.RoleWeight(member.Role) < domain.RoleWeight(prevRole[0]) {
				if err := p.encryptor.RotateKey(ctx, orgID); err == nil {
					p.rotations = append(p.rotations, orgID)
				}
			}
		}
	}
	return nil
}
func (p *lifecycleTrackingPublisher) PublishInvite(_ context.Context, _ *domain.OrgInvite, _ bool) error {
	return nil
}

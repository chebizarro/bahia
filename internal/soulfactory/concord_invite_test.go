package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip59"
)

type fakeConcordSigner struct {
	fakeSigner
	encryptErr error
	decryptErr error
}

func (s fakeConcordSigner) GetPublicKey(context.Context) (string, error) {
	return s.pubkey, nil
}

func (s fakeConcordSigner) NIP44Encrypt(_ context.Context, recipient nostr.PubKey, plaintext string) (string, error) {
	if s.encryptErr != nil {
		return "", s.encryptErr
	}
	secret, err := nostr.SecretKeyFromHex(s.secret)
	if err != nil {
		return "", err
	}
	conversationKey, err := nip44.GenerateConversationKey(recipient, secret)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(plaintext, conversationKey)
}

// NIP44EncryptBytes mirrors Signet's binary-safe encrypt path. A Go string is
// a byte string, so the fake carries binary plaintext verbatim; it is the
// NIP-46 JSON transport, not NIP-44, that cannot.
func (s fakeConcordSigner) NIP44EncryptBytes(_ context.Context, recipient nostr.PubKey, plaintext []byte) (string, error) {
	if s.encryptErr != nil {
		return "", s.encryptErr
	}
	secret, err := nostr.SecretKeyFromHex(s.secret)
	if err != nil {
		return "", err
	}
	conversationKey, err := nip44.GenerateConversationKey(recipient, secret)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(string(plaintext), conversationKey)
}

func (s fakeConcordSigner) NIP44Decrypt(_ context.Context, counterparty nostr.PubKey, ciphertext string) (string, error) {
	if s.decryptErr != nil {
		return "", s.decryptErr
	}
	secret, err := nostr.SecretKeyFromHex(s.secret)
	if err != nil {
		return "", err
	}
	conversationKey, err := nip44.GenerateConversationKey(counterparty, secret)
	if err != nil {
		return "", err
	}
	return nip44.Decrypt(ciphertext, conversationKey)
}

func TestConcordMembershipAssignPublishesCORD05DirectInvite(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	recipient := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}
	unrelated := newFakeRelayEndpoint("wss://unrelated.example")
	unrelated.publishResults = []RelayPublishResult{{Accepted: true}}
	queueConcordInboxLookup(endpoint)
	queueConcordInboxLookup(unrelated)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{unrelated, endpoint}, WithRelayBusSigner(staff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership, err := newConcordMembership([]ConcordCommunity{community}, staff, bus)
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}

	assigned, err := membership.Assign(t.Context(), recipient.pubkey)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 1 || assigned[0] != community.CommunityID {
		t.Fatalf("Assign() communities = %#v", assigned)
	}
	if len(endpoint.published) != 1 {
		t.Fatalf("published invites = %d, want 1", len(endpoint.published))
	}
	if len(unrelated.published) != 0 {
		t.Fatalf("unrelated relay published invites = %d, want 0", len(unrelated.published))
	}
	wrap := endpoint.published[0]
	if !validConcordGiftWrap(wrap, recipient.pubkey) {
		t.Fatalf("published invalid giftwrap: %+v", wrap)
	}
	if wrap.PubKey.Hex() == staff.pubkey || wrap.PubKey.Hex() == recipient.pubkey {
		t.Fatalf("giftwrap author %s is not ephemeral", wrap.PubKey.Hex())
	}

	recipientSecret, err := nostr.SecretKeyFromHex(recipient.secret)
	if err != nil {
		t.Fatalf("recipient secret: %v", err)
	}
	rumor, err := nip59.GiftUnwrap(wrap, func(other nostr.PubKey, ciphertext string) (string, error) {
		conversationKey, keyErr := nip44.GenerateConversationKey(other, recipientSecret)
		if keyErr != nil {
			return "", keyErr
		}
		return nip44.Decrypt(ciphertext, conversationKey)
	})
	if err != nil {
		t.Fatalf("GiftUnwrap() error = %v", err)
	}
	if rumor.Kind != concordDirectInviteKind || rumor.PubKey.Hex() != staff.pubkey {
		t.Fatalf("rumor kind/pubkey = %d/%s", rumor.Kind, rumor.PubKey.Hex())
	}
	if len(rumor.Tags) != 0 {
		t.Fatalf("rumor tags = %#v, want empty", rumor.Tags)
	}
	if rumor.Content != string(community.InviteBundle) {
		t.Fatal("rumor content does not preserve configured CommunityInvite")
	}
}

func TestConcordMembershipRejectsBundleThatDoesNotSelfCertify(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	community := concordTestCommunity(t, nil)
	var bundle map[string]any
	if err := json.Unmarshal(community.InviteBundle, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	bundle["owner_salt"] = strings.Repeat("f", 64)
	community.InviteBundle, _ = json.Marshal(bundle)
	bus := newEOSEOnlyRelayBus(t)

	if _, err := newConcordMembership([]ConcordCommunity{community}, staff, bus); err == nil || !strings.Contains(err.Error(), "self-certification failed") {
		t.Fatalf("newConcordMembership() error = %v", err)
	}
}

func TestConcordMembershipRejectsBundleRelayMissingFromBus(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	community := concordTestCommunity(t, nil)
	endpoint := newFakeRelayEndpoint("wss://other.example")
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(staff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}

	if _, err := newConcordMembership([]ConcordCommunity{community}, staff, bus); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("newConcordMembership() error = %v", err)
	}
}

func TestConcordMembershipRejectsSignerWithoutNIP44Capability(t *testing.T) {
	community := concordTestCommunity(t, nil)
	if _, err := newConcordMembership([]ConcordCommunity{community}, newFakeSigner(t), newEOSEOnlyRelayBus(t)); err == nil || !strings.Contains(err.Error(), "NIP-44") {
		t.Fatalf("newConcordMembership() error = %v", err)
	}
}

func TestConcordMembershipAssignFailsClosedForExpiredBundle(t *testing.T) {
	expiresAt := time.Now().Add(-time.Minute).UnixMilli()
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	community := concordTestCommunity(t, &expiresAt)
	membership, err := newConcordMembership([]ConcordCommunity{community}, staff, newConcordTestBus(t, staff))
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}

	assigned, err := membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 0 {
		t.Fatalf("Assign() communities = %#v", assigned)
	}
}

func TestConcordMembershipAssignFailsClosedOnEncryptionError(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t), encryptErr: errors.New("bunker denied nip44_encrypt")}
	community := concordTestCommunity(t, nil)
	membership, err := newConcordMembership([]ConcordCommunity{community}, staff, newConcordTestBus(t, staff))
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "bunker denied nip44_encrypt") {
		t.Fatalf("Assign() error = %v", err)
	}
}

func TestConcordMembershipAssignFailsClosedOnRelayRejection(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	community := concordTestCommunity(t, nil)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: false, Reason: "restricted"}}
	queueConcordInboxLookup(endpoint)
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(staff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership, err := newConcordMembership([]ConcordCommunity{community}, staff, bus)
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Assign() error = %v", err)
	}
}

func TestConcordMembershipAssignRetriesPublishAfterAuthRace(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	community := concordTestCommunity(t, nil)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{
		{Accepted: false, Reason: "auth-required: challenge pending"},
		{Accepted: true},
	}
	queueConcordInboxLookup(endpoint)
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(staff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership, err := newConcordMembership([]ConcordCommunity{community}, staff, bus)
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}

	recipient := newFakeSigner(t)
	assigned, err := membership.Assign(t.Context(), recipient.pubkey)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 1 || assigned[0] != community.CommunityID {
		t.Fatalf("Assign() communities = %#v", assigned)
	}
	if endpoint.publishCalls != 2 {
		t.Fatalf("publish calls = %d, want auth-race retry", endpoint.publishCalls)
	}
	if len(endpoint.published) == 0 || !validConcordGiftWrap(endpoint.published[len(endpoint.published)-1], recipient.pubkey) {
		t.Fatal("auth-race retry did not republish a valid CORD-05 direct invite")
	}
}

// queueConcordInboxLookup answers one inbox resolution on an endpoint. With no
// events the recipient has published neither a kind-10050 nor a NIP-65 list,
// which is the freshly provisioned agent case.
func queueConcordInboxLookup(endpoint *fakeRelayEndpoint, events ...*nostr.Event) {
	subscription := newFakeRelaySubscription()
	for _, event := range events {
		if event != nil {
			subscription.events <- event
		}
	}
	close(subscription.eose)
	endpoint.subscribeQueue <- subscription
}

func newConcordTestBus(t *testing.T, signer relayAuthSigner) *SoulFactoryRelayBus {
	t.Helper()
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}
	queueConcordInboxLookup(endpoint)
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(signer))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	return bus
}

func concordTestCommunity(t *testing.T, expiresAt *int64) ConcordCommunity {
	t.Helper()
	owner := newFakeSigner(t)
	ownerSalt := strings.Repeat("1", 64)
	communityID := computeConcordCommunityID(owner.pubkey, ownerSalt)
	bundle := concordInviteBundle{
		CommunityID:   communityID,
		Owner:         owner.pubkey,
		OwnerSalt:     ownerSalt,
		CommunityRoot: strings.Repeat("2", 64),
		RootEpoch:     3,
		ControlPK:     newFakeSigner(t).pubkey,
		Channels: []concordInviteChannel{{
			ID:    strings.Repeat("3", 64),
			Key:   strings.Repeat("4", 64),
			Epoch: 5,
			Name:  "general",
		}},
		Relays:    []string{"wss://community.example"},
		Name:      "Fleet Private",
		ExpiresAt: expiresAt,
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal invite bundle: %v", err)
	}
	return ConcordCommunity{CommunityID: communityID, InviteBundle: raw}
}

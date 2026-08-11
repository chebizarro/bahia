package soulfactory

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip59"
)

func TestConcordRotationRefoundingRollsRootAndRedistributes(t *testing.T) {
	fixture := newConcordRotationFixture(t, 4)
	first := newFakeSigner(t)
	second := newFakeSigner(t)

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{first.pubkey, strings.ToUpper(second.pubkey), first.pubkey},
		Reason:      "banned a compromised operator",
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	if !receipt.Refounded || receipt.PrevRootEpoch != 3 || receipt.RootEpoch != 4 {
		t.Fatalf("receipt epochs = %d -> %d (refounded=%v)", receipt.PrevRootEpoch, receipt.RootEpoch, receipt.Refounded)
	}
	expectedCommit, err := concordEpochCommitment(3, fixture.priorRoot)
	if err != nil {
		t.Fatalf("expected commitment: %v", err)
	}
	if receipt.RootPrevCommit != expectedCommit {
		t.Fatalf("receipt root prevcommit = %s, want %s", receipt.RootPrevCommit, expectedCommit)
	}
	if len(receipt.Recipients) != 2 || receipt.Recipients[0] != first.pubkey || receipt.Recipients[1] != second.pubkey {
		t.Fatalf("receipt recipients = %#v", receipt.Recipients)
	}

	record, err := fixture.custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var rotated concordInviteBundle
	if err := json.Unmarshal(record.Bundle, &rotated); err != nil {
		t.Fatalf("decode rotated bundle: %v", err)
	}
	if rotated.CommunityRoot == fixture.priorRoot || !validConcordHex32(rotated.CommunityRoot) {
		t.Fatalf("community_root = %q, want a fresh 32-byte key", rotated.CommunityRoot)
	}
	if rotated.RootEpoch != 4 {
		t.Fatalf("root_epoch = %d, want 4", rotated.RootEpoch)
	}
	if rotated.CommunityID != fixture.communityID || rotated.Owner != fixture.owner {
		t.Fatal("refounding must preserve the community identity")
	}

	// CORD-06 §3: a compliant base rotation mints a fresh control_root beside
	// the new root, and the bundle's control_pk must derive from it.
	if !validConcordHex32(record.ControlRoot) {
		t.Fatalf("custody control_root = %q, want a fresh staff secret", record.ControlRoot)
	}
	controlRootBytes, err := hex.DecodeString(record.ControlRoot)
	if err != nil {
		t.Fatalf("decode control root: %v", err)
	}
	communityID32, err := concordID32(fixture.communityID)
	if err != nil {
		t.Fatalf("community id: %v", err)
	}
	epoch := uint64(4)
	controlSigner, err := deriveConcordGroupKey(concordLabelControlSigner, controlRootBytes, communityID32, &epoch)
	if err != nil {
		t.Fatalf("derive control signer: %v", err)
	}
	if rotated.ControlPK != controlSigner.PubKey.Hex() {
		t.Fatalf("control_pk = %s, want the new epoch's control-signer address", rotated.ControlPK)
	}

	// CORD-03: a Public Channel derives from the community_root and rotates
	// with the base; a Private Channel keeps its own key and epoch.
	public := concordChannelByID(t, rotated, fixture.publicChannelID)
	if public.Key != rotated.CommunityRoot || public.Epoch != 4 {
		t.Fatalf("public channel = %s/%d, want the rotated root at epoch 4", public.Key, public.Epoch)
	}
	private := concordChannelByID(t, rotated, fixture.privateChannelID)
	if private.Key != fixture.priorPrivateKey || private.Epoch != 5 {
		t.Fatalf("unnamed private channel rotated: %s/%d", private.Key, private.Epoch)
	}
	if len(receipt.Channels) != 1 || receipt.Channels[0].ChannelID != fixture.publicChannelID || !receipt.Channels[0].Public {
		t.Fatalf("receipt channels = %#v", receipt.Channels)
	}

	// Unknown bundle and channel fields round-trip verbatim (CORD-02 §6).
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record.Bundle, &fields); err != nil {
		t.Fatalf("decode rotated fields: %v", err)
	}
	if string(fields["icon"]) != `"https://fleet.example/icon.png"` {
		t.Fatalf("unknown bundle field lost: %s", fields["icon"])
	}
	if !strings.Contains(string(fields["channels"]), `"topic":"ops"`) {
		t.Fatalf("unknown channel field lost: %s", fields["channels"])
	}

	// CORD-06 §3's order: the base rotation's Rekey Blobs go out first, the
	// compaction follows only after that root roll is published, the Guestbook
	// snapshot is the best-effort final step, and both survivors then receive
	// the rotated material as a CORD-05 direct invite.
	if len(fixture.endpoint.published) != 4 {
		t.Fatalf("published events = %d, want a rekey chunk, a snapshot chunk, and one invite per survivor", len(fixture.endpoint.published))
	}
	// This fixture's prior Control Plane carries no editions, so the compaction
	// re-wraps nothing — an empty plane folds reliably, which is not the same as
	// a plane that could not be folded (that aborts, see the Refounding tests).
	if receipt.Compaction == nil || receipt.Compaction.Entities != 0 || receipt.Compaction.Epoch != 4 {
		t.Fatalf("receipt compaction = %#v", receipt.Compaction)
	}
	if receipt.Compaction.Address != rotated.ControlPK {
		t.Fatalf("compaction address = %s, want the rotated control_pk %s", receipt.Compaction.Address, rotated.ControlPK)
	}
	if receipt.GuestbookSnapshot == nil || receipt.GuestbookSnapshot.Chunks != 1 ||
		receipt.GuestbookSnapshot.Members != 2 || receipt.GuestbookSnapshot.Error != "" {
		t.Fatalf("receipt guestbook snapshot = %#v", receipt.GuestbookSnapshot)
	}
	if len(receipt.Rekeys) != 1 || receipt.Rekeys[0].Scope != strings.Repeat("0", 64) || receipt.Rekeys[0].Chunks != 1 {
		t.Fatalf("receipt rekeys = %#v", receipt.Rekeys)
	}
	if fixture.endpoint.published[0].Kind != nostr.KindGiftWrap ||
		fixture.endpoint.published[0].PubKey.Hex() != receipt.Rekeys[0].Address {
		t.Fatalf("first published event is not the rekey wrap: %+v", fixture.endpoint.published[0])
	}
	for i, recipient := range []fakeSigner{first, second} {
		delivered := concordUnwrapInvite(t, fixture.endpoint.published[i+2], recipient)
		if delivered != string(record.Bundle) {
			t.Fatalf("recipient %d received stale material", i)
		}
	}
}

func TestConcordRotationRekeysOnlyTheNamedPrivateChannel(t *testing.T) {
	fixture := newConcordRotationFixture(t, 2)
	survivor := newFakeSigner(t)

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		ChannelIDs:  []string{strings.ToUpper(fixture.privateChannelID)},
		Recipients:  []string{survivor.pubkey},
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if receipt.Refounded || receipt.RootEpoch != 3 {
		t.Fatalf("a channel rekey must not roll the base: %+v", receipt)
	}
	if len(receipt.Channels) != 1 || receipt.Channels[0].ChannelID != fixture.privateChannelID ||
		receipt.Channels[0].PrevEpoch != 5 || receipt.Channels[0].NewEpoch != 6 || receipt.Channels[0].Public {
		t.Fatalf("receipt channels = %#v", receipt.Channels)
	}
	expectedCommit, err := concordEpochCommitment(5, fixture.priorPrivateKey)
	if err != nil {
		t.Fatalf("expected commitment: %v", err)
	}
	if receipt.Channels[0].PrevCommit != expectedCommit {
		t.Fatalf("channel prevcommit = %s, want %s", receipt.Channels[0].PrevCommit, expectedCommit)
	}

	record, err := fixture.custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var rotated concordInviteBundle
	if err := json.Unmarshal(record.Bundle, &rotated); err != nil {
		t.Fatalf("decode rotated bundle: %v", err)
	}
	if rotated.CommunityRoot != fixture.priorRoot || rotated.RootEpoch != 3 {
		t.Fatal("a channel rekey must leave the community_root untouched")
	}
	private := concordChannelByID(t, rotated, fixture.privateChannelID)
	if private.Key == fixture.priorPrivateKey || !validConcordHex32(private.Key) || private.Epoch != 6 {
		t.Fatalf("private channel = %s/%d", private.Key, private.Epoch)
	}
	public := concordChannelByID(t, rotated, fixture.publicChannelID)
	if public.Key != fixture.priorRoot || public.Epoch != 3 {
		t.Fatalf("public channel rotated without a Refounding: %s/%d", public.Key, public.Epoch)
	}
	if record.ControlRoot != "" {
		t.Fatal("a channel rekey must not mint a control_root")
	}
}

func TestConcordRotationRejectsPublicChannelRekeyWithoutRefounding(t *testing.T) {
	fixture := newConcordRotationFixture(t, 1)
	_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		ChannelIDs:  []string{fixture.publicChannelID},
		Recipients:  []string{newFakeSigner(t).pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "rotates only with a Refounding") {
		t.Fatalf("Rotate() error = %v", err)
	}
	fixture.assertUnrotated(t)
}

func TestConcordRotationRejectsChannelOutsideTheBundle(t *testing.T) {
	fixture := newConcordRotationFixture(t, 1)
	_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		ChannelIDs:  []string{strings.Repeat("9", 64)},
		Recipients:  []string{newFakeSigner(t).pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "not granted by the current invite bundle") {
		t.Fatalf("Rotate() error = %v", err)
	}
	fixture.assertUnrotated(t)
}

func TestConcordRotationFailsClosedForReadOnlyCustody(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	community := concordTestCommunity(t, nil)
	membership, err := newConcordMembership([]ConcordCommunity{community}, staff, newConcordTestBus(t, staff))
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}

	_, err = membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: community.CommunityID,
		Refound:     true,
		Recipients:  []string{newFakeSigner(t).pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") || !strings.Contains(err.Error(), "invite_bundle_sealed_file") {
		t.Fatalf("Rotate() error = %v", err)
	}
}

func TestConcordRotationRejectsMalformedRequests(t *testing.T) {
	fixture := newConcordRotationFixture(t, 1)
	survivor := newFakeSigner(t).pubkey

	cases := map[string]struct {
		rotation ConcordRotation
		want     string
	}{
		"unknown community": {
			rotation: ConcordRotation{CommunityID: strings.Repeat("c", 64), Refound: true, Recipients: []string{survivor}},
			want:     "is not configured",
		},
		"no scope": {
			rotation: ConcordRotation{CommunityID: fixture.communityID, Recipients: []string{survivor}},
			want:     "must name a Refounding or at least one channel",
		},
		"no recipients": {
			rotation: ConcordRotation{CommunityID: fixture.communityID, Refound: true},
			want:     "severs every member",
		},
		"invalid recipient": {
			rotation: ConcordRotation{CommunityID: fixture.communityID, Refound: true, Recipients: []string{"not-a-pubkey"}},
			want:     "not a 32-byte lowercase hex pubkey",
		},
		"invalid channel id": {
			rotation: ConcordRotation{CommunityID: fixture.communityID, ChannelIDs: []string{"nope"}, Recipients: []string{survivor}},
			want:     "channel id must be 32-byte lowercase hex",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := fixture.membership.Rotate(t.Context(), testCase.rotation)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Rotate() error = %v, want %q", err, testCase.want)
			}
			fixture.assertUnrotated(t)
		})
	}
}

func TestConcordRotationDoesNotPublishWhenCustodyStoreFails(t *testing.T) {
	fixture := newConcordRotationFixture(t, 1)
	fixture.membership.signer = fakeConcordSigner{fakeSigner: fixture.staff.fakeSigner, decryptErr: errors.New("bunker denied nip44_decrypt")}
	// Custody keeps the working signer, so the current material still loads and
	// only the verify-before-replace step fails.
	fixture.custody.signer = fakeConcordSigner{fakeSigner: fixture.staff.fakeSigner, decryptErr: errors.New("bunker denied nip44_decrypt")}

	_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{newFakeSigner(t).pubkey},
	})
	if err == nil {
		t.Fatal("Rotate() succeeded with unusable custody")
	}
	if len(fixture.endpoint.published) != 0 {
		t.Fatal("Rotate() published material it could not persist")
	}
}

func TestConcordRotationFailsClosedWhenMintingFails(t *testing.T) {
	fixture := newConcordRotationFixture(t, 1)
	fixture.membership.mintKey = func() (string, error) { return "", errors.New("entropy source unavailable") }

	_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{newFakeSigner(t).pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "entropy source unavailable") {
		t.Fatalf("Rotate() error = %v", err)
	}
	fixture.assertUnrotated(t)
	if len(fixture.endpoint.published) != 0 {
		t.Fatal("Rotate() published after a failed mint")
	}
}

func TestConcordRotationReceiptCarriesNoKeyMaterial(t *testing.T) {
	// A base rekey chunk, a private-channel rekey chunk, the Guestbook snapshot
	// chunk, and the survivor's direct invite.
	fixture := newConcordRotationFixture(t, 4)
	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		ChannelIDs:  []string{fixture.privateChannelID},
		Recipients:  []string{newFakeSigner(t).pubkey},
		Reason:      "post-removal secrecy",
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	record, err := fixture.custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var rotated concordInviteBundle
	if err := json.Unmarshal(record.Bundle, &rotated); err != nil {
		t.Fatalf("decode rotated bundle: %v", err)
	}
	private := concordChannelByID(t, rotated, fixture.privateChannelID)
	for name, secret := range map[string]string{
		"community_root":    rotated.CommunityRoot,
		"control_root":      record.ControlRoot,
		"prior root":        fixture.priorRoot,
		"channel key":       private.Key,
		"prior channel key": fixture.priorPrivateKey,
	} {
		if secret != "" && strings.Contains(string(encoded), secret) {
			t.Fatalf("rotation receipt leaks %s", name)
		}
	}
}

func TestConcordAssignDeliversRotatedMaterialWithoutRestart(t *testing.T) {
	fixture := newConcordRotationFixture(t, 4)
	survivor := newFakeSigner(t)

	if _, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{survivor.pubkey},
	}); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	joiner := newFakeSigner(t)
	assigned, err := fixture.membership.Assign(t.Context(), joiner.pubkey)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 1 || assigned[0] != fixture.communityID {
		t.Fatalf("Assign() communities = %#v", assigned)
	}
	record, err := fixture.custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	delivered := concordUnwrapInvite(t, fixture.endpoint.published[len(fixture.endpoint.published)-1], joiner)
	if delivered != string(record.Bundle) {
		t.Fatal("Assign() delivered pre-rotation material")
	}
	if strings.Contains(delivered, fixture.priorRoot) {
		t.Fatal("Assign() delivered the severed community_root")
	}
}

func TestConcordConcurrentRotationsAdvanceEpochsWithoutLosingOne(t *testing.T) {
	// Per rotation: a base rekey chunk, a Guestbook snapshot chunk, and the
	// survivor's direct invite.
	fixture := newConcordRotationFixture(t, 6)
	first := newFakeSigner(t)
	second := newFakeSigner(t)

	var wait sync.WaitGroup
	epochs := make([]uint64, 2)
	errs := make([]error, 2)
	for i, recipient := range []fakeSigner{first, second} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
				CommunityID: fixture.communityID,
				Refound:     true,
				Recipients:  []string{recipient.pubkey},
			})
			errs[i] = err
			if receipt != nil {
				epochs[i] = receipt.RootEpoch
			}
		}()
	}
	wait.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Rotate(%d) error = %v", i, err)
		}
	}
	// Each rotation must read the other's committed material: one lands at
	// epoch 4 and the other at 5, never both at 4.
	if epochs[0] == epochs[1] {
		t.Fatalf("concurrent rotations both minted epoch %d", epochs[0])
	}
	if epochs[0]+epochs[1] != 9 {
		t.Fatalf("rotation epochs = %v, want 4 and 5", epochs)
	}
	record, err := fixture.custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var rotated concordInviteBundle
	if err := json.Unmarshal(record.Bundle, &rotated); err != nil {
		t.Fatalf("decode rotated bundle: %v", err)
	}
	if rotated.RootEpoch != 5 {
		t.Fatalf("custody root_epoch = %d, want the later rotation", rotated.RootEpoch)
	}
}

type concordRotationFixture struct {
	membership       *concordMembership
	custody          *sealedConcordCustody
	endpoint         *fakeRelayEndpoint
	staff            fakeConcordSigner
	communityID      string
	owner            string
	priorRoot        string
	priorControlRoot string
	priorPrivateKey  string
	publicChannelID  string
	privateChannelID string
	path             string
}

// newConcordRotationFixture builds a Signet-sealed community holding one Public
// Channel (keyed by the community_root) and one Private Channel, plus unknown
// bundle and channel fields that every rotation must round-trip.
func newConcordRotationFixture(t *testing.T, publishes int) *concordRotationFixture {
	t.Helper()
	// The fleet-provisioned case: Soul Factory's Signet-held staff key minted
	// the Community, so it rotates as the owner — whose authority the
	// community_id itself proves — and CORD-04 §1 leaves the `vac` citation
	// absent. newConcordRotationFixtureOwnedBy covers the Rotator who must cite.
	return newConcordRotationFixtureOwnedBy(t, publishes, "")
}

// newConcordRotationFixtureOwnedBy names an owner other than the staff key. A
// Rotator who is not the owner must cite the Grant it acts under (CORD-06 §3),
// resolved from the folded Control Plane at the community's control_pk. The
// empty string keeps the staff-is-owner default.
func newConcordRotationFixtureOwnedBy(t *testing.T, publishes int, ownerHex string) *concordRotationFixture {
	t.Helper()
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	if ownerHex == "" {
		ownerHex = staff.pubkey
	}
	ownerSalt := strings.Repeat("1", 64)
	communityID := computeConcordCommunityID(ownerHex, ownerSalt)
	priorRoot := strings.Repeat("2", 64)
	priorControlRoot := strings.Repeat("5", 64)
	publicChannelID := strings.Repeat("3", 64)
	privateChannelID := strings.Repeat("7", 64)
	priorPrivateKey := strings.Repeat("4", 64)

	// The Control Plane address is the control_root-derived signer at the
	// current epoch (CORD-02 §5), not an unrelated key: a fold has to be able
	// to check each wrap's author against exactly this pubkey.
	controlSigner := concordTestControlSigner(t, priorControlRoot, communityID, 3)

	bundle := map[string]any{
		"community_id":   communityID,
		"owner":          ownerHex,
		"owner_salt":     ownerSalt,
		"community_root": priorRoot,
		"root_epoch":     3,
		"control_pk":     controlSigner.PubKey.Hex(),
		"channels": []map[string]any{
			{"id": publicChannelID, "key": priorRoot, "epoch": 3, "name": "general", "topic": "ops"},
			{"id": privateChannelID, "key": priorPrivateKey, "epoch": 5, "name": "staff"},
		},
		"relays": []string{"wss://community.example"},
		"name":   "Fleet Private",
		"icon":   "https://fleet.example/icon.png",
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal invite bundle: %v", err)
	}

	path := filepath.Join(t.TempDir(), "custody.sealed")
	writeSealedConcordCustodyFile(t, path, staff, string(raw))

	endpoint := newFakeRelayEndpoint("wss://community.example")
	for range publishes {
		endpoint.publishResults = append(endpoint.publishResults, RelayPublishResult{Accepted: true})
	}
	// Survivors in these fixtures publish no relay list, so every inbox lookup
	// resolves empty and delivery stays on the community relays.
	//
	// Only the staff-is-owner default pre-fills: an owner cites no Grant, so no
	// Control Plane fetch competes for the queue. A Rotator that must cite one
	// gets an empty queue and seeds it itself, since the fetch is the *first*
	// subscription a rotation makes and its events are the point of the test.
	if ownerHex == staff.pubkey {
		for range cap(endpoint.subscribeQueue) {
			queueConcordInboxLookup(endpoint)
		}
	}
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(staff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership, err := newConcordMembership([]ConcordCommunity{{CommunityID: communityID, SealedBundlePath: path}}, staff, bus)
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}
	custody, ok := membership.communities[0].custody.(*sealedConcordCustody)
	if !ok {
		t.Fatalf("custody type = %T, want Signet-sealed custody", membership.communities[0].custody)
	}
	if membership.communities[0].cached != nil {
		t.Fatal("sealed custody material must not be cached in memory")
	}
	return &concordRotationFixture{
		membership:       membership,
		custody:          custody,
		endpoint:         endpoint,
		staff:            staff,
		communityID:      communityID,
		owner:            ownerHex,
		priorRoot:        priorRoot,
		priorControlRoot: priorControlRoot,
		priorPrivateKey:  priorPrivateKey,
		publicChannelID:  publicChannelID,
		privateChannelID: privateChannelID,
		path:             path,
	}
}

func (f *concordRotationFixture) assertUnrotated(t *testing.T) {
	t.Helper()
	sealed, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("read custody file: %v", err)
	}
	staffPK, err := nostr.PubKeyFromHex(f.staff.pubkey)
	if err != nil {
		t.Fatalf("staff pubkey: %v", err)
	}
	plaintext, err := f.staff.NIP44Decrypt(t.Context(), staffPK, strings.TrimSpace(string(sealed)))
	if err != nil {
		t.Fatalf("unseal custody: %v", err)
	}
	if !strings.Contains(plaintext, f.priorRoot) || !strings.Contains(plaintext, f.priorPrivateKey) {
		t.Fatal("a rejected rotation must leave custody untouched")
	}
}

func concordChannelByID(t *testing.T, bundle concordInviteBundle, channelID string) concordInviteChannel {
	t.Helper()
	for _, channel := range bundle.Channels {
		if channel.ID == channelID {
			return channel
		}
	}
	t.Fatalf("channel %s is missing from the rotated bundle", channelID)
	return concordInviteChannel{}
}

func concordUnwrapInvite(t *testing.T, wrap nostr.Event, recipient fakeSigner) string {
	t.Helper()
	if !validConcordGiftWrap(wrap, recipient.pubkey) {
		t.Fatalf("published an invalid CORD-05 giftwrap for %s", recipient.pubkey)
	}
	secret, err := nostr.SecretKeyFromHex(recipient.secret)
	if err != nil {
		t.Fatalf("recipient secret: %v", err)
	}
	rumor, err := nip59.GiftUnwrap(wrap, func(other nostr.PubKey, ciphertext string) (string, error) {
		conversationKey, keyErr := nip44.GenerateConversationKey(other, secret)
		if keyErr != nil {
			return "", keyErr
		}
		return nip44.Decrypt(ciphertext, conversationKey)
	})
	if err != nil {
		t.Fatalf("GiftUnwrap() error = %v", err)
	}
	if rumor.Kind != concordDirectInviteKind {
		t.Fatalf("rumor kind = %d, want %d", rumor.Kind, concordDirectInviteKind)
	}
	return rumor.Content
}

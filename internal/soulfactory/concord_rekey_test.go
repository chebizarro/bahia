package soulfactory

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
)

// TestConcordRekeyBlobWidthsAreByteExact pins the CORD-06 §1 blob layouts.
// The width is the format signal — a conformant receiver drops anything else
// as malformed — so these vectors are byte-for-byte, not merely well-shaped.
func TestConcordRekeyBlobWidthsAreByteExact(t *testing.T) {
	channelID := concordTestBytes32(t, 0x11)
	newKey := concordTestBytes32(t, 0x22)
	controlPK := concordTestBytes32(t, 0x33)
	controlRoot := concordTestBytes32(t, 0x44)

	channelScope := concordRekeyScope{
		scopeID:  channelID,
		newEpoch: 6,
		newKey:   newKey,
	}
	baseScope := concordRekeyScope{
		base:           true,
		scopeID:        concordBaseScopeID,
		newEpoch:       4,
		newKey:         newKey,
		newControlPK:   controlPK,
		newControlRoot: controlRoot,
	}

	zeros := strings.Repeat("00", 32)
	cases := map[string]struct {
		scope     concordRekeyScope
		staff     bool
		wantWidth int
		want      string
	}{
		// scope_id[32] ‖ epoch_be[8] ‖ new_key[32]
		"channel": {
			scope:     channelScope,
			wantWidth: concordChannelBlobLen,
			want:      strings.Repeat("11", 32) + "0000000000000006" + strings.Repeat("22", 32),
		},
		// A channel rotation has one form: staff carry no extra Control keys.
		"channel for staff": {
			scope:     channelScope,
			staff:     true,
			wantWidth: concordChannelBlobLen,
			want:      strings.Repeat("11", 32) + "0000000000000006" + strings.Repeat("22", 32),
		},
		// 0…0[32] ‖ epoch_be[8] ‖ new_root[32] ‖ new_control_pk[32]
		"base for a member": {
			scope:     baseScope,
			wantWidth: concordMemberBaseBlobLen,
			want:      zeros + "0000000000000004" + strings.Repeat("22", 32) + strings.Repeat("33", 32),
		},
		// … ‖ new_control_root[32]
		"base for staff": {
			scope:     baseScope,
			staff:     true,
			wantWidth: concordStaffBaseBlobLen,
			want: zeros + "0000000000000004" + strings.Repeat("22", 32) +
				strings.Repeat("33", 32) + strings.Repeat("44", 32),
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			blob, err := testCase.scope.blobPlaintext(testCase.staff)
			if err != nil {
				t.Fatalf("blobPlaintext() error = %v", err)
			}
			if len(blob) != testCase.wantWidth {
				t.Fatalf("blob width = %d, want %d", len(blob), testCase.wantWidth)
			}
			if got := hex.EncodeToString(blob); got != testCase.want {
				t.Fatalf("blob = %s, want %s", got, testCase.want)
			}
		})
	}
}

// TestConcordRekeyBlobNeverMintsTheLegacyBaseWidth covers CORD-06 §3: the
// 72-byte base form is read for old epochs and never minted, so a base
// rotation missing its Control pair must fail rather than emit one.
func TestConcordRekeyBlobNeverMintsTheLegacyBaseWidth(t *testing.T) {
	scope := concordRekeyScope{
		base:     true,
		scopeID:  concordBaseScopeID,
		newEpoch: 4,
		newKey:   concordTestBytes32(t, 0x22),
	}
	if _, err := scope.blobPlaintext(false); err == nil ||
		!strings.Contains(err.Error(), "Control Plane keys") {
		t.Fatalf("blobPlaintext() error = %v, want a refusal to mint the legacy form", err)
	}
}

// TestConcordRekeyLocatorMatchesTheFrozenDerivation recomputes the CORD-02 A.1
// layout independently, so a drifted label, field order, or epoch encoding
// fails here rather than at a receiver that can never find its blob.
func TestConcordRekeyLocatorMatchesTheFrozenDerivation(t *testing.T) {
	rotator := nostr.PubKey(concordTestBytes32(t, 0xa1))
	recipient := nostr.PubKey(concordTestBytes32(t, 0xb2))
	scopeID := concordTestBytes32(t, 0xc3)
	const epoch = uint64(7)

	ikm := append(append([]byte{}, rotator[:]...), recipient[:]...)
	info := append([]byte(concordLabelRecipientPseudonym), 0x00)
	info = append(info, scopeID[:]...)
	info = binary.BigEndian.AppendUint64(info, epoch)
	expected, err := hkdf.Key(sha256.New, ikm, nil, string(info), 32)
	if err != nil {
		t.Fatalf("hkdf: %v", err)
	}

	locator, err := concordRekeyLocator(rotator, recipient, scopeID, epoch)
	if err != nil {
		t.Fatalf("concordRekeyLocator() error = %v", err)
	}
	if locator != hex.EncodeToString(expected) {
		t.Fatalf("locator = %s, want %s", locator, hex.EncodeToString(expected))
	}
	// The locator is ordered: the rotator's key comes first, so swapping the
	// pair must not resolve to the same blob.
	swapped, err := concordRekeyLocator(recipient, rotator, scopeID, epoch)
	if err != nil {
		t.Fatalf("concordRekeyLocator() error = %v", err)
	}
	if swapped == locator {
		t.Fatal("locator is not bound to the rotator/recipient order")
	}
}

// TestConcordRotationPublishesRekeyBlobs is the CORD-06 acceptance path: a
// survivor who receives no Direct Invite converges on the new epoch from the
// blobs alone, and a staff survivor additionally receives the control_root.
func TestConcordRotationPublishesRekeyBlobs(t *testing.T) {
	fixture := newConcordRotationFixture(t, 3)
	member := newFakeSigner(t)
	staff := newFakeSigner(t)

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{member.pubkey, staff.pubkey},
		Staff:       []string{strings.ToUpper(staff.pubkey)},
		// Only the staff survivor is reachable by the fleet's invite lane.
		DirectInvites: []string{staff.pubkey},
		Reason:        "banned a compromised operator",
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if len(receipt.Recipients) != 2 || len(receipt.DirectInvites) != 1 ||
		receipt.DirectInvites[0] != staff.pubkey || len(receipt.Staff) != 1 {
		t.Fatalf("receipt lanes = %#v / %#v / %#v", receipt.Recipients, receipt.DirectInvites, receipt.Staff)
	}
	if len(receipt.Rekeys) != 1 {
		t.Fatalf("receipt rekeys = %#v", receipt.Rekeys)
	}
	rekey := receipt.Rekeys[0]
	if rekey.Scope != strings.Repeat("00", 32) || rekey.PrevEpoch != 3 || rekey.NewEpoch != 4 ||
		rekey.Chunks != 1 || rekey.Blobs != 2 {
		t.Fatalf("base rekey publication = %#v", rekey)
	}
	if rekey.PrevCommit != receipt.RootPrevCommit {
		t.Fatalf("rekey prevcommit = %s, want the receipt's %s", rekey.PrevCommit, receipt.RootPrevCommit)
	}

	record, err := fixture.custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var rotated concordInviteBundle
	if err := json.Unmarshal(record.Bundle, &rotated); err != nil {
		t.Fatalf("decode rotated bundle: %v", err)
	}

	// A member holding the *prior* root precomputes the next base rekey
	// address and subscribes to it (CORD-06 §2).
	address := concordTestRekeyAddress(t, fixture.priorRoot, concordLabelBaseRekeyPseudonym, fixture.communityID, 4)
	if address.PubKey.Hex() != rekey.Address {
		t.Fatalf("receipt address = %s, want the derived rekey address %s", rekey.Address, address.PubKey.Hex())
	}
	wrap := fixture.endpoint.published[0]
	if wrap.PubKey != address.PubKey {
		t.Fatalf("rekey wrap author = %s, want the rekey address", wrap.PubKey.Hex())
	}
	rumor := concordUnwrapStream(t, address, wrap, fixture.staff.pubkey)
	if rumor.Kind != concordRekeyKind {
		t.Fatalf("rumor kind = %d, want %d", rumor.Kind, concordRekeyKind)
	}
	assertConcordTag(t, rumor, "scope", strings.Repeat("00", 32))
	assertConcordTag(t, rumor, "newepoch", "4")
	assertConcordTag(t, rumor, "prevepoch", "3")
	assertConcordTag(t, rumor, "prevcommit", receipt.RootPrevCommit)
	assertConcordTag(t, rumor, "chunk", "0", "1")

	blobs := concordDecodeRekeyBlobs(t, rumor)
	if len(blobs) != 2 {
		t.Fatalf("blobs = %d, want one per survivor", len(blobs))
	}
	staffPK, err := nostr.PubKeyFromHex(fixture.staff.pubkey)
	if err != nil {
		t.Fatalf("staff pubkey: %v", err)
	}

	// The blob-only survivor: a 104-byte member form carrying the new root and
	// the next epoch's control_pk.
	memberBlob := concordOpenRekeyBlob(t, blobs, staffPK, member, concordBaseScopeID, 4)
	if len(memberBlob) != concordMemberBaseBlobLen {
		t.Fatalf("member blob width = %d, want %d", len(memberBlob), concordMemberBaseBlobLen)
	}
	if hex.EncodeToString(memberBlob[:32]) != strings.Repeat("00", 32) {
		t.Fatal("base blob scope is not the all-zero community_root id")
	}
	if binary.BigEndian.Uint64(memberBlob[32:40]) != 4 {
		t.Fatal("base blob does not carry the new root epoch")
	}
	if hex.EncodeToString(memberBlob[40:72]) != rotated.CommunityRoot {
		t.Fatal("base blob does not carry the rotated community_root")
	}
	if hex.EncodeToString(memberBlob[72:104]) != rotated.ControlPK {
		t.Fatal("base blob does not carry the new epoch's control_pk")
	}
	// Converging from the blob alone means the severed root is gone.
	if strings.Contains(hex.EncodeToString(memberBlob), fixture.priorRoot) {
		t.Fatal("base blob carries the severed community_root")
	}

	// The staff survivor: a 136-byte form whose control_root derives to
	// exactly the delivered control_pk (CORD-06 §1).
	staffBlob := concordOpenRekeyBlob(t, blobs, staffPK, staff, concordBaseScopeID, 4)
	if len(staffBlob) != concordStaffBaseBlobLen {
		t.Fatalf("staff blob width = %d, want %d", len(staffBlob), concordStaffBaseBlobLen)
	}
	if !strings.HasPrefix(hex.EncodeToString(staffBlob), hex.EncodeToString(memberBlob)) {
		t.Fatal("the staff form is not the member form plus the control_root")
	}
	if hex.EncodeToString(staffBlob[104:136]) != record.ControlRoot {
		t.Fatal("staff blob does not carry the new control_root")
	}
	communityID32, err := concordID32(fixture.communityID)
	if err != nil {
		t.Fatalf("community id: %v", err)
	}
	epoch := uint64(4)
	derived, err := deriveConcordGroupKey(concordLabelControlSigner, staffBlob[104:136], communityID32, &epoch)
	if err != nil {
		t.Fatalf("derive control signer: %v", err)
	}
	if derived.PubKey.Hex() != rotated.ControlPK {
		t.Fatal("the delivered control_root does not derive to the delivered control_pk")
	}

	// The blob-only survivor was never sent a Direct Invite.
	for _, published := range fixture.endpoint.published[1:] {
		if len(published.Tags) > 0 && published.Tags[0][0] == "p" && published.Tags[0][1] == member.pubkey {
			t.Fatal("a DirectInvites-narrowed rotation still giftwrapped the blob-only survivor")
		}
	}
}

// TestConcordRekeyChunksAtTheBlobCap covers the CORD-06 §1 cap: at most 120
// blobs per event, with every chunk repeating one rotation's continuity fields
// so a receiver can tell a complete set from a missing one.
func TestConcordRekeyChunksAtTheBlobCap(t *testing.T) {
	fixture := newConcordRotationFixture(t, 3)
	recipients := make([]string, 0, concordRekeyBlobsPerEvent+1)
	for i := 0; i <= concordRekeyBlobsPerEvent; i++ {
		recipients = append(recipients, newFakeSigner(t).pubkey)
	}

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID:   fixture.communityID,
		ChannelIDs:    []string{fixture.privateChannelID},
		Recipients:    recipients,
		DirectInvites: []string{recipients[0]},
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if len(receipt.Rekeys) != 1 || receipt.Rekeys[0].Chunks != 2 ||
		receipt.Rekeys[0].Blobs != concordRekeyBlobsPerEvent+1 {
		t.Fatalf("receipt rekeys = %#v", receipt.Rekeys)
	}
	if receipt.Rekeys[0].Scope != fixture.privateChannelID {
		t.Fatalf("channel rekey scope = %s", receipt.Rekeys[0].Scope)
	}

	address := concordTestRekeyAddress(t, fixture.priorRoot, concordLabelRekeyPseudonym, fixture.privateChannelID, 6)
	counts := []int{concordRekeyBlobsPerEvent, 1}
	for index, want := range counts {
		rumor := concordUnwrapStream(t, address, fixture.endpoint.published[index], fixture.staff.pubkey)
		assertConcordTag(t, rumor, "chunk", concordDecimal(uint64(index)), "2")
		assertConcordTag(t, rumor, "scope", fixture.privateChannelID)
		assertConcordTag(t, rumor, "newepoch", "6")
		assertConcordTag(t, rumor, "prevepoch", "5")
		assertConcordTag(t, rumor, "prevcommit", receipt.Channels[0].PrevCommit)
		if blobs := concordDecodeRekeyBlobs(t, rumor); len(blobs) != want {
			t.Fatalf("chunk %d blobs = %d, want %d", index+1, len(blobs), want)
		}
	}
}

// TestConcordRekeyChunkIndicesAreZeroBased pins the numbering a conformant
// reader requires: CORD-06 §1 says only "chunk i of n", and a reader that
// rejects index >= count (openclaw-nostr) drops a 1-based final chunk — which
// for a single-chunk rotation is the whole rotation, and for an n-chunk one
// keeps the set forever incomplete, so removal is never concluded (§2). The
// indices of one rotation must therefore be exactly 0..n-1.
func TestConcordRekeyChunkIndicesAreZeroBased(t *testing.T) {
	fixture := newConcordRotationFixture(t, 3)
	recipients := make([]string, 0, concordRekeyBlobsPerEvent+1)
	for i := 0; i <= concordRekeyBlobsPerEvent; i++ {
		recipients = append(recipients, newFakeSigner(t).pubkey)
	}

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID:   fixture.communityID,
		ChannelIDs:    []string{fixture.privateChannelID},
		Recipients:    recipients,
		DirectInvites: []string{recipients[0]},
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if len(receipt.Rekeys) != 1 || receipt.Rekeys[0].Chunks != 2 {
		t.Fatalf("receipt rekeys = %#v", receipt.Rekeys)
	}
	chunks := receipt.Rekeys[0].Chunks

	address := concordTestRekeyAddress(t, fixture.priorRoot, concordLabelRekeyPseudonym, fixture.privateChannelID, 6)
	seen := make(map[uint64]int, chunks)
	for index := 0; index < chunks; index++ {
		rumor := concordUnwrapStream(t, address, fixture.endpoint.published[index], fixture.staff.pubkey)
		i, n := concordChunkTag(t, rumor)
		if n != uint64(chunks) {
			t.Fatalf("chunk count = %d, want %d", n, chunks)
		}
		// The rule every conformant reader applies before folding a chunk.
		if i >= n {
			t.Fatalf("chunk index %d >= count %d: a conformant reader drops this chunk", i, n)
		}
		seen[i]++
	}
	for i := uint64(0); i < uint64(chunks); i++ {
		if seen[i] != 1 {
			t.Fatalf("chunk index %d appears %d times, want exactly once across 0..n-1", i, seen[i])
		}
	}
}

// concordChunkTag reads the CORD-06 §1 chunk tag as the pair (i, n).
func concordChunkTag(t *testing.T, rumor nostr.Event) (uint64, uint64) {
	t.Helper()
	for _, tag := range rumor.Tags {
		if len(tag) == 0 || tag[0] != "chunk" {
			continue
		}
		if len(tag) != 3 {
			t.Fatalf("chunk tag = %#v, want [chunk i n]", tag)
		}
		i, err := strconv.ParseUint(tag[1], 10, 64)
		if err != nil {
			t.Fatalf("chunk index %q: %v", tag[1], err)
		}
		n, err := strconv.ParseUint(tag[2], 10, 64)
		if err != nil {
			t.Fatalf("chunk count %q: %v", tag[2], err)
		}
		return i, n
	}
	t.Fatalf("chunk tag is missing from %#v", rumor.Tags)
	return 0, 0
}

// TestConcordRekeyStaffChunksStayWithinTheNIP44Ceiling covers the tension
// between CORD-06 §1's 120-blob cap and CORD-01's double wrap: the 136-byte
// staff form is the widest, and 120 of them would grow past NIP-44's 65535
// plaintext ceiling once sealed and wrapped. Chunking must bind on bytes as
// well as on count, and no recipient may be dropped at a boundary.
func TestConcordRekeyStaffChunksStayWithinTheNIP44Ceiling(t *testing.T) {
	fixture := newConcordRotationFixture(t, 6)
	survivors := make([]fakeSigner, 0, concordRekeyBlobsPerEvent)
	recipients := make([]string, 0, concordRekeyBlobsPerEvent)
	for i := 0; i < concordRekeyBlobsPerEvent; i++ {
		survivor := newFakeSigner(t)
		survivors = append(survivors, survivor)
		recipients = append(recipients, survivor.pubkey)
	}

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  recipients,
		// Every survivor is staff, so every blob is the widest form.
		Staff:         recipients,
		DirectInvites: []string{recipients[0]},
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if len(receipt.Rekeys) != 1 || receipt.Rekeys[0].Blobs != concordRekeyBlobsPerEvent {
		t.Fatalf("receipt rekeys = %#v", receipt.Rekeys)
	}
	chunks := receipt.Rekeys[0].Chunks
	if chunks < 2 {
		t.Fatalf("chunks = %d: the widest form must chunk below the count cap", chunks)
	}

	address := concordTestRekeyAddress(t, fixture.priorRoot, concordLabelBaseRekeyPseudonym, fixture.communityID, 4)
	staffPK, err := nostr.PubKeyFromHex(fixture.staff.pubkey)
	if err != nil {
		t.Fatalf("staff pubkey: %v", err)
	}
	seen := make(map[string]int, len(recipients))
	for index := 0; index < chunks; index++ {
		wrap := fixture.endpoint.published[index]
		// Both wrapped plaintexts must stay inside the ceiling a conformant
		// NIP-44 enforces, or only the minting client could read them back.
		sealJSON, err := nip44.Decrypt(wrap.Content, address.ConversationKey)
		if err != nil {
			t.Fatalf("decrypt chunk %d: %v", index+1, err)
		}
		if len(sealJSON) > concordStreamPlaintextLimit {
			t.Fatalf("chunk %d seal is %d bytes, past the NIP-44 ceiling", index+1, len(sealJSON))
		}
		rumor := concordUnwrapStream(t, address, wrap, fixture.staff.pubkey)
		if len(rumor.String()) > concordStreamPlaintextLimit {
			t.Fatalf("chunk %d rumor is %d bytes, past the NIP-44 ceiling", index+1, len(rumor.String()))
		}
		assertConcordTag(t, rumor, "chunk", concordDecimal(uint64(index)), concordDecimal(uint64(chunks)))
		for _, blob := range concordDecodeRekeyBlobs(t, rumor) {
			seen[blob.Locator]++
		}
	}
	// A boundary that dropped or duplicated a recipient would leave them
	// unable to conclude anything from a complete chunk set.
	for _, survivor := range survivors {
		recipientPK, err := nostr.PubKeyFromHex(survivor.pubkey)
		if err != nil {
			t.Fatalf("recipient pubkey: %v", err)
		}
		locator, err := concordRekeyLocator(staffPK, recipientPK, concordBaseScopeID, 4)
		if err != nil {
			t.Fatalf("locator: %v", err)
		}
		if seen[locator] != 1 {
			t.Fatalf("locator %s appears %d times across the chunk set", locator, seen[locator])
		}
	}
}

func TestConcordRotationRejectsStaffOutsideTheSurvivors(t *testing.T) {
	fixture := newConcordRotationFixture(t, 1)
	_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{newFakeSigner(t).pubkey},
		Staff:       []string{newFakeSigner(t).pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "not among the surviving recipients") {
		t.Fatalf("Rotate() error = %v", err)
	}
	fixture.assertUnrotated(t)
}

func TestConcordRotationRejectsDirectInvitesOutsideTheSurvivors(t *testing.T) {
	fixture := newConcordRotationFixture(t, 1)
	_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID:   fixture.communityID,
		Refound:       true,
		Recipients:    []string{newFakeSigner(t).pubkey},
		DirectInvites: []string{newFakeSigner(t).pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "not among the surviving recipients") {
		t.Fatalf("Rotate() error = %v", err)
	}
	fixture.assertUnrotated(t)
}

func concordTestBytes32(t *testing.T, fill byte) [32]byte {
	t.Helper()
	var value [32]byte
	for i := range value {
		value[i] = fill
	}
	return value
}

// concordTestRekeyAddress recomputes a rekey address the way a subscribing
// member would: from the prior community_root and the next epoch.
func concordTestRekeyAddress(t *testing.T, priorRootHex, label, idHex string, epoch uint64) concordGroupKey {
	t.Helper()
	priorRoot, err := hex.DecodeString(priorRootHex)
	if err != nil {
		t.Fatalf("prior root: %v", err)
	}
	id, err := concordID32(idHex)
	if err != nil {
		t.Fatalf("rekey address id: %v", err)
	}
	address, err := deriveConcordGroupKey(label, priorRoot, id, &epoch)
	if err != nil {
		t.Fatalf("derive rekey address: %v", err)
	}
	return address
}

// concordUnwrapStream opens a CORD-01 reversed Stream wrap: both layers are
// encrypted under the stream's own conversation key, and the seal must be an
// encrypted kind 20013 signed by the Rotator's real key.
func concordUnwrapStream(t *testing.T, stream concordGroupKey, wrap nostr.Event, rotatorHex string) nostr.Event {
	t.Helper()
	if wrap.Kind != nostr.KindGiftWrap || !validSignedEvent(&wrap) {
		t.Fatalf("stream wrap is not a signed kind-1059: %+v", wrap)
	}
	if len(wrap.Tags) != 1 || wrap.Tags[0][0] != "p" {
		t.Fatalf("stream wrap tags = %#v, want one ephemeral p tag", wrap.Tags)
	}
	if wrap.Tags[0][1] == rotatorHex || wrap.Tags[0][1] == stream.PubKey.Hex() {
		t.Fatal("the p tag must be ephemeral, not a real participant")
	}
	sealJSON, err := nip44.Decrypt(wrap.Content, stream.ConversationKey)
	if err != nil {
		t.Fatalf("decrypt stream wrap: %v", err)
	}
	var seal nostr.Event
	if err := json.Unmarshal([]byte(sealJSON), &seal); err != nil {
		t.Fatalf("decode seal: %v", err)
	}
	if seal.Kind != concordStreamSealKind {
		t.Fatalf("seal kind = %d, want the encrypted %d", seal.Kind, concordStreamSealKind)
	}
	if seal.PubKey.Hex() != rotatorHex || !validSignedEvent(&seal) {
		t.Fatalf("seal is not signed by the Rotator: %+v", seal)
	}
	rumorJSON, err := nip44.Decrypt(seal.Content, stream.ConversationKey)
	if err != nil {
		t.Fatalf("decrypt seal: %v", err)
	}
	var rumor nostr.Event
	if err := json.Unmarshal([]byte(rumorJSON), &rumor); err != nil {
		t.Fatalf("decode rumor: %v", err)
	}
	if rumor.PubKey.Hex() != rotatorHex {
		t.Fatalf("rumor author = %s, want the Rotator", rumor.PubKey.Hex())
	}
	return rumor
}

func concordDecodeRekeyBlobs(t *testing.T, rumor nostr.Event) []concordRekeyBlob {
	t.Helper()
	var blobs []concordRekeyBlob
	if err := json.Unmarshal([]byte(rumor.Content), &blobs); err != nil {
		t.Fatalf("decode rekey blobs: %v", err)
	}
	return blobs
}

// concordOpenRekeyBlob finds a recipient's blob the way a client does: compute
// the locator from public keys, then open the ciphertext with the pairwise
// NIP-44 key.
func concordOpenRekeyBlob(t *testing.T, blobs []concordRekeyBlob, rotator nostr.PubKey, recipient fakeSigner, scopeID [32]byte, epoch uint64) []byte {
	t.Helper()
	recipientPK, err := nostr.PubKeyFromHex(recipient.pubkey)
	if err != nil {
		t.Fatalf("recipient pubkey: %v", err)
	}
	locator, err := concordRekeyLocator(rotator, recipientPK, scopeID, epoch)
	if err != nil {
		t.Fatalf("locator: %v", err)
	}
	secret, err := nostr.SecretKeyFromHex(recipient.secret)
	if err != nil {
		t.Fatalf("recipient secret: %v", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(rotator, secret)
	if err != nil {
		t.Fatalf("conversation key: %v", err)
	}
	for _, blob := range blobs {
		if blob.Locator != locator {
			continue
		}
		plaintext, err := nip44.Decrypt(blob.Wrapped, conversationKey)
		if err != nil {
			t.Fatalf("decrypt blob: %v", err)
		}
		return []byte(plaintext)
	}
	t.Fatalf("no blob for locator %s", locator)
	return nil
}

func assertConcordTag(t *testing.T, event nostr.Event, name string, want ...string) {
	t.Helper()
	for _, tag := range event.Tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		if len(tag) != len(want)+1 {
			t.Fatalf("tag %s = %#v, want %d values", name, tag, len(want))
		}
		for i, value := range want {
			if tag[i+1] != value {
				t.Fatalf("tag %s[%d] = %q, want %q", name, i, tag[i+1], value)
			}
		}
		return
	}
	t.Fatalf("tag %s is missing from %#v", name, event.Tags)
}

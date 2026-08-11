package soulfactory

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

// TestConcordRefoundingCompactsAndRepublishesTheControlPlane is the CORD-06 §3
// acceptance path. A Refounding folds the prior epoch's Control Plane and
// re-wraps each entity's head at the new epoch's Control address — signed by
// the new control_root-derived signer, readable under the new community_root —
// so a fresh joiner holding only the newest root has something to re-anchor on.
func TestConcordRefoundingCompactsAndRepublishesTheControlPlane(t *testing.T) {
	owner := newFakeSigner(t)
	// A base rekey chunk, two compacted entities, the Guestbook snapshot chunk,
	// and the survivor's direct invite.
	fixture := newConcordRotationFixtureOwnedBy(t, 5, owner.pubkey)
	survivor := newFakeSigner(t)

	// The plane the Refounding will compact: the Rotator's own Grant at
	// version 2, plus a second entity whose head sits above a pruned ancestor.
	grantFirst := concordTestGrantEdition(t, fixture, owner, fixture.staff.pubkey, 1, "")
	grantHead := concordTestGrantEdition(t, fixture, owner, fixture.staff.pubkey, 2, grantFirst.hash)
	metadata := concordTestControlEdition(t, fixture, owner, concordTestBytes32(t, 0x5e), 7, strings.Repeat("cd", 32), `{"name":"Fleet Private"}`)

	queueConcordInboxLookup(fixture.endpoint, grantFirst.wrap, grantHead.wrap, metadata.wrap)
	queueConcordInboxLookup(fixture.endpoint)

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{survivor.pubkey},
		Reason:      "banned a compromised operator",
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	// Three editions folded down to two heads: the compaction ratio a
	// Refounding exists to restore.
	if receipt.Compaction == nil || receipt.Compaction.Entities != 2 || receipt.Compaction.Editions != 3 {
		t.Fatalf("receipt compaction = %#v", receipt.Compaction)
	}
	if receipt.Compaction.Epoch != 4 {
		t.Fatalf("compaction epoch = %d, want the new root epoch", receipt.Compaction.Epoch)
	}

	rotated := concordRotatedBundle(t, fixture)
	if receipt.Compaction.Address != rotated.ControlPK {
		t.Fatalf("compaction address = %s, want the rotated control_pk %s", receipt.Compaction.Address, rotated.ControlPK)
	}

	// CORD-06 §3's order: the root roll is published first, and the compaction
	// only after it. The snapshot and the direct invite follow.
	if len(fixture.endpoint.published) != 5 {
		t.Fatalf("published = %d events", len(fixture.endpoint.published))
	}
	if fixture.endpoint.published[0].PubKey.Hex() != receipt.Rekeys[0].Address {
		t.Fatal("the compaction was published before the root roll")
	}

	// Fold the compaction back the way a fresh joiner would: at the new
	// address, under the new epoch's read key.
	compacted := concordFoldRepublished(t, fixture, rotated, fixture.endpoint.published[1:3])
	if compacted.editions != 2 {
		t.Fatalf("republished editions = %d, want the two heads", compacted.editions)
	}

	grantEID, err := concordID32(concordTestGrantEID(t, fixture.communityID, fixture.staff.pubkey))
	if err != nil {
		t.Fatalf("grant eid: %v", err)
	}
	head, ok := compacted.head(grantEID)
	if !ok {
		t.Fatal("the compaction dropped the Rotator's Grant")
	}
	// The head is re-wrapped, never rebuilt: version, hash, and the original
	// author's signature all survive the re-encryption, which is the whole
	// reason the Control Plane's seal is plaintext (CORD-02 §5).
	if head.version != 2 || head.hash != grantHead.hash {
		t.Fatalf("compacted Grant = v%d %s, want v2 %s", head.version, head.hash, grantHead.hash)
	}
	if head.actor.Hex() != owner.pubkey {
		t.Fatalf("compacted Grant author = %s, want the original granter %s", head.actor.Hex(), owner.pubkey)
	}
	// Superseded ancestors are pruned: only the head crosses the epoch.
	if head.prev != grantFirst.hash {
		t.Fatalf("compacted head prev = %s, want the pruned ancestor's hash", head.prev)
	}

	// An entity whose ancestors were already pruned re-wraps just the same: its
	// dangling `prev` is CORD-04 §1's reset floor, not a gap.
	if head, ok := compacted.head(concordTestBytes32(t, 0x5e)); !ok || head.version != 7 || head.hash != metadata.hash {
		t.Fatalf("compacted metadata head = %#v ok=%v", head, ok)
	}

	// The prior epoch's plane is unreadable at the new address: the compaction
	// is a re-encryption, not a mirror (CORD-06 §3 forbids mirroring to the old
	// derivation precisely because it re-opens the closed surface).
	stale := concordFoldRepublished(t, fixture, rotated, []nostr.Event{*grantHead.wrap})
	if stale.editions != 0 {
		t.Fatal("a prior-epoch wrap folded at the new epoch's address")
	}
}

// TestConcordRefoundingAbortsOnAnUnfoldablePlane covers CORD-06 §3's abort
// rule: if the Refounder cannot reliably fold all Control events, the
// Refounding must be aborted rather than publishing a partial one. A forked
// entity is exactly that — compacting without it would prune the ancestors
// that still carried its state.
func TestConcordRefoundingAbortsOnAnUnfoldablePlane(t *testing.T) {
	owner := newFakeSigner(t)
	fixture := newConcordRotationFixtureOwnedBy(t, 0, owner.pubkey)
	survivor := newFakeSigner(t)

	// The Rotator's Grant folds cleanly, so authority resolves; a second
	// entity's chain forks, so the plane as a whole does not.
	grant := concordTestGrantEdition(t, fixture, owner, fixture.staff.pubkey, 1, "")
	entity := concordTestBytes32(t, 0x5e)
	first := concordTestControlEdition(t, fixture, owner, entity, 1, "", `{"v":1}`)
	forged := concordTestControlEdition(t, fixture, owner, entity, 2, strings.Repeat("ab", 32), `{"v":2}`)
	queueConcordInboxLookup(fixture.endpoint, grant.wrap, first.wrap, forged.wrap)

	_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{survivor.pubkey},
	})
	if err == nil || !strings.Contains(err.Error(), "forked chains") {
		t.Fatalf("Rotate() error = %v, want an aborted Refounding", err)
	}
	// Aborting before anything is minted is the point: the community keeps its
	// epoch, its root, and its keys.
	fixture.assertUnrotated(t)
	if len(fixture.endpoint.published) != 0 {
		t.Fatalf("an aborted Refounding published %d events", len(fixture.endpoint.published))
	}
}

// TestConcordRefoundingSeedsTheGuestbookSnapshot covers CORD-02 §5's snapshot:
// present members only, chunked, one id and one created_at across the set,
// published into the new epoch's Guestbook and signed by the Refounder.
func TestConcordRefoundingSeedsTheGuestbookSnapshot(t *testing.T) {
	fixture := newConcordRotationFixture(t, 4)
	first := newFakeSigner(t)
	second := newFakeSigner(t)

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID:   fixture.communityID,
		Refound:       true,
		Recipients:    []string{first.pubkey, second.pubkey},
		DirectInvites: []string{first.pubkey},
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	snapshot := receipt.GuestbookSnapshot
	if snapshot == nil || snapshot.Error != "" {
		t.Fatalf("receipt guestbook snapshot = %#v", snapshot)
	}
	if snapshot.Members != 2 || snapshot.Chunks != 1 || snapshot.Epoch != 4 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	rotated := concordRotatedBundle(t, fixture)
	guestbook := concordTestGuestbookAddress(t, rotated.CommunityRoot, fixture.communityID, 4)
	if snapshot.Address != guestbook.PubKey.Hex() {
		t.Fatalf("snapshot address = %s, want the new epoch's guestbook_pk %s", snapshot.Address, guestbook.PubKey.Hex())
	}

	// Published after the compaction and before the direct invite.
	wrap := fixture.endpoint.published[1]
	if wrap.PubKey != guestbook.PubKey {
		t.Fatalf("snapshot wrap author = %s, want the guestbook address", wrap.PubKey.Hex())
	}
	rumor := concordUnwrapStream(t, guestbook, wrap, fixture.staff.pubkey)
	if rumor.Kind != concordSnapshotKind {
		t.Fatalf("snapshot rumor kind = %d, want %d", rumor.Kind, concordSnapshotKind)
	}
	if rumor.PubKey.Hex() != fixture.staff.pubkey {
		t.Fatal("a snapshot is honored only from the npub whose Refounding minted the epoch")
	}
	// The index is 0-based and the id is 32-byte hex, the intersection every
	// conformant reader folds.
	assertConcordTag(t, rumor, "snap", snapshot.SnapshotID, "0", "1")
	if !validConcordHex32(snapshot.SnapshotID) {
		t.Fatalf("snapshot id = %q, want 32-byte lowercase hex", snapshot.SnapshotID)
	}

	var members []string
	if err := json.Unmarshal([]byte(rumor.Content), &members); err != nil {
		t.Fatalf("decode snapshot content: %v", err)
	}
	// Present members only, in the rotation's own recipient order. The
	// direct-invite lane is narrower than the rotation; the snapshot seeds
	// every survivor regardless.
	if len(members) != 2 || members[0] != first.pubkey || members[1] != second.pubkey {
		t.Fatalf("snapshot members = %#v", members)
	}
}

// TestConcordGuestbookSnapshotIsBestEffort covers CORD-06 §3's final-step rule:
// a Refounding succeeds with or without the snapshot. A member entering the new
// epoch to find their own state absent simply publishes a fresh Join, so a
// failed seeding is a blip that belongs on the receipt, never a rolled-back
// rotation whose keys are already distributed.
func TestConcordGuestbookSnapshotIsBestEffort(t *testing.T) {
	fixture := newConcordRotationFixture(t, 0)
	survivor := newFakeSigner(t)
	// The base rekey chunk, then the snapshot chunk the relay refuses, then
	// the survivor's direct invite.
	fixture.endpoint.publishResults = []RelayPublishResult{
		{Accepted: true},
		{Accepted: false, Reason: "blocked: snapshot too large"},
		{Accepted: true},
	}

	receipt, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		Refound:     true,
		Recipients:  []string{survivor.pubkey},
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v, want the Refounding to succeed without its snapshot", err)
	}
	if receipt.GuestbookSnapshot == nil || receipt.GuestbookSnapshot.Chunks != 0 ||
		!strings.Contains(receipt.GuestbookSnapshot.Error, "blocked") {
		t.Fatalf("receipt guestbook snapshot = %#v", receipt.GuestbookSnapshot)
	}
	// The epoch still rolled and the survivor still received their material.
	if receipt.RootEpoch != 4 || len(receipt.DirectInvites) != 1 {
		t.Fatalf("receipt = epoch %d, invites %#v", receipt.RootEpoch, receipt.DirectInvites)
	}
}

// TestConcordSnapshotChunkingMatchesTheFrozenCap pins CORD-02 §5's 400-member
// chunk, and the derived correlator that makes a resumed Refounding re-publish
// into one snapshot rather than minting a competing set.
func TestConcordSnapshotChunkingMatchesTheFrozenCap(t *testing.T) {
	members := make([]string, 0, 401)
	for i := range 401 {
		members = append(members, concordTestMemberHex(i))
	}
	chunks := concordChunkSnapshotMembers(members)
	if len(chunks) != 2 || len(chunks[0]) != concordSnapshotChunkSize || len(chunks[1]) != 1 {
		t.Fatalf("chunks = %d of sizes %d/%d", len(chunks), len(chunks[0]), len(chunks[len(chunks)-1]))
	}
	// Order is preserved and nothing spans a boundary: a partially received
	// snapshot seeds whoever arrived.
	if chunks[0][0] != members[0] || chunks[1][0] != members[400] {
		t.Fatal("snapshot chunking reordered its members")
	}
	if got := concordChunkSnapshotMembers(members[:concordSnapshotChunkSize]); len(got) != 1 {
		t.Fatalf("an exactly-full snapshot chunked into %d events", len(got))
	}

	communityID := concordTestBytes32(t, 0xcc)
	rotator := nostr.PubKey(concordTestBytes32(t, 0xa1))
	id := concordSnapshotID(communityID, 4, rotator)
	if id != concordSnapshotID(communityID, 4, rotator) {
		t.Fatal("the snapshot correlator is not stable across a resumed Refounding")
	}
	if id == concordSnapshotID(communityID, 5, rotator) {
		t.Fatal("two epochs share a snapshot correlator")
	}
	if !validConcordHex32(id) {
		t.Fatalf("snapshot id = %q, want 32-byte lowercase hex", id)
	}
}

// --- helpers ---------------------------------------------------------------

func concordRotatedBundle(t *testing.T, fixture *concordRotationFixture) concordInviteBundle {
	t.Helper()
	record, err := fixture.custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var rotated concordInviteBundle
	if err := json.Unmarshal(record.Bundle, &rotated); err != nil {
		t.Fatalf("decode rotated bundle: %v", err)
	}
	return rotated
}

// concordFoldRepublished folds events the way a fresh joiner at the new epoch
// would: at the rotated bundle's control_pk, under the new community_root's
// read key.
func concordFoldRepublished(t *testing.T, fixture *concordRotationFixture, rotated concordInviteBundle, events []nostr.Event) *concordControlFold {
	t.Helper()
	communityID, err := concordID32(fixture.communityID)
	if err != nil {
		t.Fatalf("community id: %v", err)
	}
	newRoot, err := hex.DecodeString(rotated.CommunityRoot)
	if err != nil {
		t.Fatalf("rotated community root: %v", err)
	}
	read, err := concordControlReadKey(newRoot, communityID, rotated.RootEpoch)
	if err != nil {
		t.Fatalf("control read key: %v", err)
	}
	address, err := nostr.PubKeyFromHex(rotated.ControlPK)
	if err != nil {
		t.Fatalf("rotated control_pk: %v", err)
	}
	pointers := make([]*nostr.Event, 0, len(events))
	for i := range events {
		pointers = append(pointers, &events[i])
	}
	fold, err := foldConcordControlPlane(pointers, address, read.ConversationKey)
	if err != nil {
		t.Fatalf("foldConcordControlPlane() error = %v", err)
	}
	return fold
}

func concordTestGuestbookAddress(t *testing.T, communityRootHex, communityIDHex string, epoch uint64) concordGroupKey {
	t.Helper()
	root, err := hex.DecodeString(communityRootHex)
	if err != nil {
		t.Fatalf("community root: %v", err)
	}
	communityID, err := concordID32(communityIDHex)
	if err != nil {
		t.Fatalf("community id: %v", err)
	}
	address, err := deriveConcordGroupKey(concordLabelGuestbook, root, communityID, &epoch)
	if err != nil {
		t.Fatalf("derive guestbook address: %v", err)
	}
	return address
}

func concordTestMemberHex(index int) string {
	return strings.Repeat("0", 60) + hex.EncodeToString([]byte{byte(index >> 8), byte(index)})
}

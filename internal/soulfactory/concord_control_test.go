package soulfactory

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
)

// TestConcordEditionHashIsByteExact pins CORD-04 §1's edition identity. The
// preimage is rebuilt here field by field rather than by calling the same
// helper, so a reordered field, a dropped length prefix, or a drifted label
// fails here rather than at a verifier whose `ep` chain silently stops linking.
func TestConcordEditionHashIsByteExact(t *testing.T) {
	entity := concordTestBytes32(t, 0x11)
	prev := concordTestBytes32(t, 0x22)
	content := []byte(`{"member":"aa","role_ids":[]}`)

	// len64(label) || label || eid[32] || version_be[8]
	//   || (prev ? 0x01 || prev[32] : 0x00 || zero[32]) || len64(content) || content
	build := func(version uint64, withPrev bool) string {
		preimage := binary.BigEndian.AppendUint64(nil, uint64(len(concordEditionLabel)))
		preimage = append(preimage, []byte(concordEditionLabel)...)
		preimage = append(preimage, entity[:]...)
		preimage = binary.BigEndian.AppendUint64(preimage, version)
		if withPrev {
			preimage = append(preimage, 0x01)
			preimage = append(preimage, prev[:]...)
		} else {
			preimage = append(preimage, 0x00)
			preimage = append(preimage, make([]byte, 32)...)
		}
		preimage = binary.BigEndian.AppendUint64(preimage, uint64(len(content)))
		preimage = append(preimage, content...)
		sum := sha256.Sum256(preimage)
		return hex.EncodeToString(sum[:])
	}

	first, err := concordEditionHash(entity, 1, nil, content)
	if err != nil {
		t.Fatalf("concordEditionHash() error = %v", err)
	}
	if want := build(1, false); first != want {
		t.Fatalf("first edition hash = %s, want %s", first, want)
	}
	chained, err := concordEditionHash(entity, 4, prev[:], content)
	if err != nil {
		t.Fatalf("concordEditionHash() error = %v", err)
	}
	if want := build(4, true); chained != want {
		t.Fatalf("chained edition hash = %s, want %s", chained, want)
	}

	// The absent-prev branch is a flag byte plus zeroes, never a shorter
	// buffer: a first edition and one citing an all-zero prev must not collide.
	zero := [32]byte{}
	zeroPrev, err := concordEditionHash(entity, 1, zero[:], content)
	if err != nil {
		t.Fatalf("concordEditionHash() error = %v", err)
	}
	if zeroPrev == first {
		t.Fatal("an absent prev and an all-zero prev share a preimage")
	}
	// content is length-prefixed, so a byte moved across the boundary changes
	// the hash rather than sliding between fields.
	shifted, err := concordEditionHash(entity, 1, nil, append(content, 'x'))
	if err != nil {
		t.Fatalf("concordEditionHash() error = %v", err)
	}
	if shifted == first {
		t.Fatal("edition content is not bound by length")
	}
	if _, err := concordEditionHash(entity, 1, prev[:31], content); err == nil {
		t.Fatal("a short prev must be refused rather than padded")
	}
}

// TestConcordGrantLocatorMatchesTheFrozenDerivation recomputes CORD-02 A.6's
// `concord/grant` independently. The coordinate takes no epoch on purpose — it
// survives every Refounding — so an epoch leaking into the info string would
// re-address every Grant on the next rotation.
func TestConcordGrantLocatorMatchesTheFrozenDerivation(t *testing.T) {
	communityID := concordTestBytes32(t, 0xc0)
	member := concordTestBytes32(t, 0x0d)

	info := append([]byte(concordLabelGrant), 0x00)
	info = append(info, member[:]...)
	expected, err := hkdf.Key(sha256.New, communityID[:], nil, string(info), 32)
	if err != nil {
		t.Fatalf("reference hkdf: %v", err)
	}

	got, err := concordGrantLocator(communityID, member)
	if err != nil {
		t.Fatalf("concordGrantLocator() error = %v", err)
	}
	if hex.EncodeToString(got[:]) != hex.EncodeToString(expected) {
		t.Fatalf("grant locator = %x, want %x", got, expected)
	}

	// An epoch-bearing derivation is a different coordinate entirely; this
	// guards the "no epoch" column of the A.6 table.
	epoch := uint64(0)
	withEpoch, err := concordHKDF(communityID[:], concordLabelGrant, member, &epoch, nil)
	if err != nil {
		t.Fatalf("concordHKDF() error = %v", err)
	}
	if withEpoch == got {
		t.Fatal("the Grant coordinate must not fold an epoch into its info string")
	}
}

// TestConcordControlFoldTakesTheChainedHead covers CORD-04 §1's fold: the head
// is the highest version whose held chain is intact, a lower version never
// downgrades it, and a compaction's dangling `prev` is a baseline rather than a
// gap.
func TestConcordControlFoldTakesTheChainedHead(t *testing.T) {
	plane := newConcordControlPlaneFixture(t)
	actor := newFakeSigner(t)
	entity := concordTestBytes32(t, 0x42)

	first := plane.edition(t, actor, entity, 1, "", `{"v":1}`)
	second := plane.edition(t, actor, entity, 2, first.hash, `{"v":2}`)
	third := plane.edition(t, actor, entity, 3, second.hash, `{"v":3}`)

	// Out of order, and with a duplicate re-wrap of the head: a compaction
	// re-wraps the same signed seal, so identity is the rumor's, not the wrap's.
	fold := plane.fold(t, third.wrap, first.wrap, second.wrap, plane.rewrap(t, third.seal))
	if fold.editions != 3 {
		t.Fatalf("folded editions = %d, want 3 distinct rumors", fold.editions)
	}
	head, ok := fold.head(entity)
	if !ok {
		t.Fatal("entity has no head")
	}
	if head.version != 3 || head.hash != third.hash || head.actor.Hex() != actor.pubkey {
		t.Fatalf("head = v%d %s by %s", head.version, head.hash, head.actor.Hex())
	}

	// A compaction seats the head alone, its `prev` citing a pruned ancestor.
	// Bahia folds from nothing every time — CORD-04 §1's fresh joiner — so it
	// takes that head as its baseline instead of stalling on the dangling link.
	compacted := plane.fold(t, third.wrap)
	if head, ok := compacted.head(entity); !ok || head.version != 3 {
		t.Fatalf("a compacted head must fold as a baseline, got v%d ok=%v", head.version, ok)
	}
}

// TestConcordControlFoldSuspendsAForkedChain covers the other half of CORD-04
// §1: two adjoining versions that do not link are a fork, and the entity is
// suspended rather than guessed at. A citation must not name it and a
// compaction must not seat it.
func TestConcordControlFoldSuspendsAForkedChain(t *testing.T) {
	plane := newConcordControlPlaneFixture(t)
	actor := newFakeSigner(t)
	entity := concordTestBytes32(t, 0x42)

	first := plane.edition(t, actor, entity, 1, "", `{"v":1}`)
	forged := plane.edition(t, actor, entity, 2, strings.Repeat("ab", 32), `{"v":2}`)

	fold := plane.fold(t, first.wrap, forged.wrap)
	if _, ok := fold.head(entity); ok {
		t.Fatal("a forked entity must have no head")
	}
	if _, suspended := fold.suspended[entity]; !suspended {
		t.Fatal("a forked entity must be suspended")
	}
}

// TestConcordControlFoldRefusesUnprovableEditions covers the provenance the
// fold does enforce. Rank is the receiver's judgment (CORD-04 §5), but a wrap
// from another address, an encrypted seal at the plaintext plane, and a rumor
// claiming an author its seal never signed are all refused outright.
func TestConcordControlFoldRefusesUnprovableEditions(t *testing.T) {
	plane := newConcordControlPlaneFixture(t)
	actor := newFakeSigner(t)
	entity := concordTestBytes32(t, 0x42)
	good := plane.edition(t, actor, entity, 1, "", `{"v":1}`)

	t.Run("wrap from another address", func(t *testing.T) {
		stranger := nostr.Generate()
		wrap := good.wrap
		if err := wrap.Sign(stranger); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if fold := plane.fold(t, wrap); fold.editions != 0 {
			t.Fatal("a wrap authored by anything but the held control_pk must be dropped")
		}
	})

	t.Run("encrypted seal", func(t *testing.T) {
		// CORD-02 §5: the Control Plane's seal MUST be plaintext. A kind-20013
		// seal here is another plane's event, and no compaction could re-wrap it.
		rumor := plane.rumor(actor, entity, 1, "", `{"v":1}`)
		sealed, err := nip44.Encrypt(rumor.String(), plane.read.ConversationKey)
		if err != nil {
			t.Fatalf("encrypt rumor: %v", err)
		}
		seal := nostr.Event{Kind: concordStreamSealKind, PubKey: nostr.PubKey{}, CreatedAt: rumor.CreatedAt, Tags: nostr.Tags{}, Content: sealed}
		plane.sign(t, actor, &seal)
		if fold := plane.fold(t, plane.rewrap(t, seal)); fold.editions != 0 {
			t.Fatal("an encrypted seal must be dropped from the Control Plane")
		}
	})

	t.Run("impersonated rumor", func(t *testing.T) {
		rumor := plane.rumor(actor, entity, 1, "", `{"v":1}`)
		rumor.PubKey = nostr.PubKey(concordTestBytes32(t, 0x99))
		seal := nostr.Event{Kind: concordControlSealKind, CreatedAt: rumor.CreatedAt, Tags: nostr.Tags{}, Content: rumor.String()}
		plane.sign(t, actor, &seal)
		if fold := plane.fold(t, plane.rewrap(t, seal)); fold.editions != 0 {
			t.Fatal("a rumor claiming an author its seal did not sign must be dropped")
		}
	})

	t.Run("tampered seal signature", func(t *testing.T) {
		seal := good.seal
		seal.Content = plane.rumor(actor, entity, 1, "", `{"v":999}`).String()
		if fold := plane.fold(t, plane.rewrap(t, seal)); fold.editions != 0 {
			t.Fatal("a seal whose content no longer matches its signature must be dropped")
		}
	})
}

// TestConcordControlEditionRejectsMalformedTags covers the CORD-01 tag
// encoding and CORD-04 §1's version rules at the parse boundary.
func TestConcordControlEditionRejectsMalformedTags(t *testing.T) {
	plane := newConcordControlPlaneFixture(t)
	actor := newFakeSigner(t)
	entity := concordTestBytes32(t, 0x42)
	entityHex := hex.EncodeToString(entity[:])

	cases := map[string]nostr.Tags{
		"missing eid":     {{"vsk", "3"}, {"ev", "1"}},
		"missing version": {{"vsk", "3"}, {"eid", entityHex}},
		"missing vsk":     {{"eid", entityHex}, {"ev", "1"}},
		"zero version":    {{"vsk", "3"}, {"eid", entityHex}, {"ev", "0"}},
		"leading zero":    {{"vsk", "3"}, {"eid", entityHex}, {"ev", "01"}},
		// Identifiers are never hex on the wire (CORD-02 A) and a mixed-case one
		// would derive a different coordinate on a stricter reader.
		"uppercase eid":     {{"vsk", "3"}, {"eid", strings.Repeat("AB", 32)}, {"ev", "1"}},
		"first with a prev": {{"vsk", "3"}, {"eid", entityHex}, {"ev", "1"}, {"ep", strings.Repeat("ab", 32)}},
		"later without one": {{"vsk", "3"}, {"eid", entityHex}, {"ev", "2"}},
	}
	for name, tags := range cases {
		t.Run(name, func(t *testing.T) {
			rumor := nostr.Event{Kind: concordControlEditionKind, PubKey: mustConcordPubKey(t, actor.pubkey), CreatedAt: nostr.Now(), Tags: tags, Content: `{}`}
			seal := nostr.Event{Kind: concordControlSealKind, CreatedAt: rumor.CreatedAt, Tags: nostr.Tags{}, Content: rumor.String()}
			plane.sign(t, actor, &seal)
			if fold := plane.fold(t, plane.rewrap(t, seal)); fold.editions != 0 {
				t.Fatalf("%s must be refused", name)
			}
		})
	}
}

// --- fixture ---------------------------------------------------------------

// concordControlPlaneFixture mints a Control Plane the way a real one is built:
// the address and its wrap signer derive from a staff-held control_root, and
// the wraps are encrypted under the community_root-derived read key every
// member holds (CORD-02 §5).
type concordControlPlaneFixture struct {
	communityID [32]byte
	signer      concordGroupKey
	read        concordGroupKey
}

type concordTestEdition struct {
	hash string
	seal nostr.Event
	wrap *nostr.Event
}

func newConcordControlPlaneFixture(t *testing.T) *concordControlPlaneFixture {
	t.Helper()
	communityID := concordTestBytes32(t, 0xcc)
	controlRoot := concordTestBytes32(t, 0xc1)
	communityRoot := concordTestBytes32(t, 0xc2)
	const epoch = uint64(3)

	signerEpoch := epoch
	signer, err := deriveConcordGroupKey(concordLabelControlSigner, controlRoot[:], communityID, &signerEpoch)
	if err != nil {
		t.Fatalf("derive control signer: %v", err)
	}
	read, err := concordControlReadKey(communityRoot[:], communityID, epoch)
	if err != nil {
		t.Fatalf("derive control read key: %v", err)
	}
	return &concordControlPlaneFixture{communityID: communityID, signer: signer, read: read}
}

func (f *concordControlPlaneFixture) sign(t *testing.T, actor fakeSigner, event *nostr.Event) {
	t.Helper()
	if err := actor.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign as actor: %v", err)
	}
}

func (f *concordControlPlaneFixture) rumor(actor fakeSigner, entity [32]byte, version uint64, prev, content string) nostr.Event {
	tags := nostr.Tags{
		{"vsk", "3"},
		{"eid", hex.EncodeToString(entity[:])},
		{"ev", concordDecimal(version)},
	}
	if prev != "" {
		tags = append(tags, nostr.Tag{"ep", prev})
	}
	pk, _ := nostr.PubKeyFromHex(actor.pubkey)
	return nostr.Event{
		Kind:      concordControlEditionKind,
		PubKey:    pk,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   content,
	}
}

// edition publishes one signed edition onto the fixture's plane.
func (f *concordControlPlaneFixture) edition(t *testing.T, actor fakeSigner, entity [32]byte, version uint64, prev, content string) concordTestEdition {
	t.Helper()
	rumor := f.rumor(actor, entity, version, prev, content)
	seal := nostr.Event{Kind: concordControlSealKind, CreatedAt: rumor.CreatedAt, Tags: nostr.Tags{}, Content: rumor.String()}
	f.sign(t, actor, &seal)

	var prevBytes []byte
	if prev != "" {
		decoded, err := hex.DecodeString(prev)
		if err != nil {
			t.Fatalf("decode prev: %v", err)
		}
		prevBytes = decoded
	}
	hash, err := concordEditionHash(entity, version, prevBytes, []byte(content))
	if err != nil {
		t.Fatalf("edition hash: %v", err)
	}
	return concordTestEdition{hash: hash, seal: seal, wrap: f.rewrap(t, seal)}
}

// rewrap wraps a seal under the plane's current epoch, which is exactly what a
// compaction does to a head it carries forward (CORD-06 §3).
func (f *concordControlPlaneFixture) rewrap(t *testing.T, seal nostr.Event) *nostr.Event {
	t.Helper()
	encrypted, err := nip44.Encrypt(seal.String(), f.read.ConversationKey)
	if err != nil {
		t.Fatalf("encrypt seal: %v", err)
	}
	wrap := nostr.Event{
		Kind:      nostr.KindGiftWrap,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", nostr.Generate().Public().Hex()}},
		Content:   encrypted,
	}
	if err := wrap.Sign(f.signer.SecretKey); err != nil {
		t.Fatalf("sign wrap: %v", err)
	}
	return &wrap
}

func (f *concordControlPlaneFixture) fold(t *testing.T, events ...*nostr.Event) *concordControlFold {
	t.Helper()
	fold, err := foldConcordControlPlane(events, f.signer.PubKey, f.read.ConversationKey)
	if err != nil {
		t.Fatalf("foldConcordControlPlane() error = %v", err)
	}
	return fold
}

func mustConcordPubKey(t *testing.T, hexKey string) nostr.PubKey {
	t.Helper()
	pk, err := nostr.PubKeyFromHex(hexKey)
	if err != nil {
		t.Fatalf("pubkey %s: %v", hexKey, err)
	}
	return pk
}

// --- CORD-06 §3 Authority: the rotation's citation ------------------------

// TestConcordRotationCitesTheFoldedGrant is the acceptance path for CORD-06
// §3's Authority rule. A Rotator who is not the owner cites the Grant it acts
// under, resolved from the Control Plane it just folded, so a lagging client
// can block until it holds that Grant and then judge the Rotator's rank for
// itself (CORD-04 §5).
func TestConcordRotationCitesTheFoldedGrant(t *testing.T) {
	owner := newFakeSigner(t)
	fixture := newConcordRotationFixtureOwnedBy(t, 2, owner.pubkey)
	survivor := newFakeSigner(t)

	// The owner grants Soul Factory's staff key its Roles. Version 2 supersedes
	// version 1, and the citation must name the head rather than the first.
	first := concordTestGrantEdition(t, fixture, owner, fixture.staff.pubkey, 1, "")
	head := concordTestGrantEdition(t, fixture, owner, fixture.staff.pubkey, 2, first.hash)

	// The Control Plane fetch comes first, then the survivor's inbox lookup.
	queueConcordInboxLookup(fixture.endpoint, first.wrap, head.wrap)
	queueConcordInboxLookup(fixture.endpoint)

	if _, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		ChannelIDs:  []string{fixture.privateChannelID},
		Recipients:  []string{survivor.pubkey},
		Reason:      "removed a tester",
	}); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	address := concordTestRekeyAddress(t, fixture.priorRoot, concordLabelRekeyPseudonym, fixture.privateChannelID, 6)
	rumor := concordUnwrapStream(t, address, fixture.endpoint.published[0], fixture.staff.pubkey)
	// Coordinate, version, and content hash — the three-part pin of CORD-04 §5.
	// A citation whose hash does not match the edition the verifier holds at
	// that version parks exactly like an unsynced one, so all three are exact.
	assertConcordTag(t, rumor, "vac", concordTestGrantEID(t, fixture.communityID, fixture.staff.pubkey), "2", head.hash)

	// The citation is a rotation-wide fact, so every chunk of every scope
	// repeats it; here that is the one chunk the rotation minted.
	if got := len(fixture.endpoint.published); got != 2 {
		t.Fatalf("published = %d, want the rekey chunk and the direct invite", got)
	}
}

// TestConcordRotationRefusesWithoutAGrant covers the bead's core rule: a
// citation must name a real Grant, and minting a fabricated one — or omitting
// the tag and spending the epoch on a rotation conformant receivers drop —
// is worse than refusing.
func TestConcordRotationRefusesWithoutAGrant(t *testing.T) {
	owner := newFakeSigner(t)
	fixture := newConcordRotationFixtureOwnedBy(t, 0, owner.pubkey)
	survivor := newFakeSigner(t)

	t.Run("empty plane", func(t *testing.T) {
		queueConcordInboxLookup(fixture.endpoint)
		_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
			CommunityID: fixture.communityID,
			ChannelIDs:  []string{fixture.privateChannelID},
			Recipients:  []string{survivor.pubkey},
		})
		if err == nil || !strings.Contains(err.Error(), "holds no Grant") {
			t.Fatalf("Rotate() error = %v, want a refusal to rotate uncited", err)
		}
		fixture.assertUnrotated(t)
	})

	t.Run("grant for somebody else", func(t *testing.T) {
		// A Grant at another member's coordinate is not this Rotator's, and
		// the coordinate is derived rather than claimed, so it cannot be
		// borrowed.
		stranger := newFakeSigner(t)
		grant := concordTestGrantEdition(t, fixture, owner, stranger.pubkey, 1, "")
		queueConcordInboxLookup(fixture.endpoint, grant.wrap)
		_, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
			CommunityID: fixture.communityID,
			ChannelIDs:  []string{fixture.privateChannelID},
			Recipients:  []string{survivor.pubkey},
		})
		if err == nil || !strings.Contains(err.Error(), "holds no Grant") {
			t.Fatalf("Rotate() error = %v, want a refusal", err)
		}
		fixture.assertUnrotated(t)
	})
}

// TestConcordOwnerRotationCitesNothing covers CORD-04 §1's exception: the
// owner's rank comes from the community_id itself, so their rotation carries no
// `vac` — and needs no Control Plane fetch to prove it.
func TestConcordOwnerRotationCitesNothing(t *testing.T) {
	fixture := newConcordRotationFixture(t, 2)
	survivor := newFakeSigner(t)

	if _, err := fixture.membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: fixture.communityID,
		ChannelIDs:  []string{fixture.privateChannelID},
		Recipients:  []string{survivor.pubkey},
	}); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	address := concordTestRekeyAddress(t, fixture.priorRoot, concordLabelRekeyPseudonym, fixture.privateChannelID, 6)
	rumor := concordUnwrapStream(t, address, fixture.endpoint.published[0], fixture.staff.pubkey)
	for _, tag := range rumor.Tags {
		if len(tag) > 0 && tag[0] == "vac" {
			t.Fatalf("the owner's rotation carries a citation: %#v", tag)
		}
	}
	// The frozen CORD-06 §1 tags are unaffected by the citation's absence.
	assertConcordTag(t, rumor, "scope", fixture.privateChannelID)
	assertConcordTag(t, rumor, "newepoch", "6")
}

// --- fixture helpers -------------------------------------------------------

// concordTestControlSigner derives a Community's Control Plane address at one
// epoch: the staff-held control_root's signer, whose pk is the address and
// whose sk signs every wrap published there (CORD-02 §5).
func concordTestControlSigner(t *testing.T, controlRootHex, communityIDHex string, epoch uint64) concordGroupKey {
	t.Helper()
	controlRoot, err := hex.DecodeString(controlRootHex)
	if err != nil {
		t.Fatalf("control root: %v", err)
	}
	communityID, err := concordID32(communityIDHex)
	if err != nil {
		t.Fatalf("community id: %v", err)
	}
	signer, err := deriveConcordGroupKey(concordLabelControlSigner, controlRoot, communityID, &epoch)
	if err != nil {
		t.Fatalf("derive control signer: %v", err)
	}
	return signer
}

func concordTestGrantEID(t *testing.T, communityIDHex, memberHex string) string {
	t.Helper()
	communityID, err := concordID32(communityIDHex)
	if err != nil {
		t.Fatalf("community id: %v", err)
	}
	member, err := concordID32(memberHex)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	eid, err := concordGrantLocator(communityID, member)
	if err != nil {
		t.Fatalf("grant locator: %v", err)
	}
	return hex.EncodeToString(eid[:])
}

// concordTestGrantEdition publishes a Grant edition (vsk 3) for member onto the
// fixture's Control Plane, wrapped exactly as a real one is: signed inside by
// the granter's real npub, encrypted under the community_root read key, and
// authored by the control_root-derived address.
func concordTestGrantEdition(t *testing.T, fixture *concordRotationFixture, granter fakeSigner, memberHex string, version uint64, prev string) concordTestEdition {
	t.Helper()
	eid, err := concordID32(concordTestGrantEID(t, fixture.communityID, memberHex))
	if err != nil {
		t.Fatalf("grant eid: %v", err)
	}
	content := `{"member":"` + memberHex + `","role_ids":["` + strings.Repeat("a1", 32) + `"]}`
	return concordTestControlEdition(t, fixture, granter, eid, version, prev, content)
}

// concordTestControlEdition publishes one signed edition at an arbitrary
// coordinate onto the fixture's Control Plane.
func concordTestControlEdition(t *testing.T, fixture *concordRotationFixture, actor fakeSigner, eid [32]byte, version uint64, prev, content string) concordTestEdition {
	t.Helper()
	eidHex := hex.EncodeToString(eid[:])
	granter := actor

	tags := nostr.Tags{{"vsk", "3"}, {"eid", eidHex}, {"ev", concordDecimal(version)}}
	var prevBytes []byte
	if prev != "" {
		tags = append(tags, nostr.Tag{"ep", prev})
		decoded, decodeErr := hex.DecodeString(prev)
		if decodeErr != nil {
			t.Fatalf("decode prev: %v", decodeErr)
		}
		prevBytes = decoded
	}
	rumor := nostr.Event{
		Kind:      concordControlEditionKind,
		PubKey:    mustConcordPubKey(t, granter.pubkey),
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   content,
	}
	seal := nostr.Event{Kind: concordControlSealKind, CreatedAt: rumor.CreatedAt, Tags: nostr.Tags{}, Content: rumor.String()}
	if err := granter.Sign(t.Context(), &seal); err != nil {
		t.Fatalf("sign seal: %v", err)
	}
	hash, err := concordEditionHash(eid, version, prevBytes, []byte(content))
	if err != nil {
		t.Fatalf("edition hash: %v", err)
	}

	communityID, err := concordID32(fixture.communityID)
	if err != nil {
		t.Fatalf("community id: %v", err)
	}
	communityRoot, err := hex.DecodeString(fixture.priorRoot)
	if err != nil {
		t.Fatalf("community root: %v", err)
	}
	read, err := concordControlReadKey(communityRoot, communityID, 3)
	if err != nil {
		t.Fatalf("control read key: %v", err)
	}
	encrypted, err := nip44.Encrypt(seal.String(), read.ConversationKey)
	if err != nil {
		t.Fatalf("encrypt seal: %v", err)
	}
	wrap := nostr.Event{
		Kind:      nostr.KindGiftWrap,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", nostr.Generate().Public().Hex()}},
		Content:   encrypted,
	}
	signer := concordTestControlSigner(t, fixture.priorControlRoot, fixture.communityID, 3)
	if err := wrap.Sign(signer.SecretKey); err != nil {
		t.Fatalf("sign wrap: %v", err)
	}
	return concordTestEdition{hash: hash, seal: seal, wrap: &wrap}
}

package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
)

// Frozen CORD-02 A.6 derivation labels for the planes a rotation reads. Like
// every label these are never edited in place: a changed byte re-addresses
// every event ever published under them.
const (
	// concordLabelControl derives the Control Plane *read* key from the
	// community_root. Its conv_key decrypts the wraps every member reads,
	// while the address and its signer come from the staff-held control_root
	// instead (CORD-02 §5).
	concordLabelControl = "concord/control"
	// concordLabelGrant derives a member's Grant coordinate — the eid a `vac`
	// citation names. It takes no epoch, deliberately: the coordinate binds to
	// the community_id and never to a key, so it survives every Refounding and
	// a fresh joiner holding only the newest root derives the same one
	// (CORD-04 §1).
	concordLabelGrant = "concord/grant"
)

// concordEditionLabel is CORD-04 §1's edition-hash domain separator. It is a
// vector-community label rather than a concord/ one, frozen exactly as spelled.
const concordEditionLabel = "vector-community/v1/edition"

const (
	// concordControlEditionKind is the CORD-04 §1 Control Plane edition rumor.
	concordControlEditionKind nostr.Kind = 3308
	// concordControlSealKind is CORD-02 §5's PLAINTEXT seal, which the Control
	// Plane and only the Control Plane uses: a compaction re-wraps its signed
	// editions into a new epoch (CORD-06 §3), and a signature over ciphertext
	// could not survive that re-encryption.
	concordControlSealKind nostr.Kind = 20014
)

// concordControlEditionLimit bounds one fold. The Control Plane "is small and
// must stay complete" (CORD-02 §5), so a plane past this size is a relay
// flooding the fold rather than a Community. Exceeding it is refused outright
// rather than truncated: a truncated fold looks complete and would compact real
// heads away (CORD-06 §3 aborts a Refounding it cannot reliably fold).
const concordControlEditionLimit = 20000

// concordAuthorityCitation is CORD-04 §1's `vac` tag: the exact Grant edition
// an actor claims their rank under, pinned by coordinate, version, *and*
// content hash.
//
// It is a sync floor, never the verdict — a verifier resolves the actor's rank
// against its own current roster once it holds the cited Grant (CORD-04 §5).
// The zero value means the tag is absent, which CORD-04 §1 reserves for the
// owner, whose authority the community_id itself proves.
type concordAuthorityCitation struct {
	eid     string
	version uint64
	hash    string
}

func (c concordAuthorityCitation) absent() bool { return c.eid == "" }

// tag renders the citation in CORD-04 §1's frozen three-value form.
func (c concordAuthorityCitation) tag() nostr.Tag {
	return nostr.Tag{"vac", c.eid, concordDecimal(c.version), c.hash}
}

// concordEdition is one folded Control Plane edition, retained with the exact
// bytes that carried it.
//
// Nothing here may be re-serialized. The actor's signature covers the seal's
// content string byte-verbatim, and the edition hash covers the rumor's content
// bytes as they arrived, which is precisely what lets a compaction re-wrap a
// signed head into a new epoch with its signature and its hash intact
// (CORD-04 §1, CORD-06 §3).
type concordEdition struct {
	entity  [32]byte
	vsk     uint64
	version uint64
	// prev is the hash of the edition this supersedes, empty on the first.
	prev string
	// hash is this edition's own identity, what the next edition's `ep` cites.
	hash string
	// actor is the seal's real npub: the authority a receiver judges, never the
	// control_root holder who published the wrap (CORD-04 §5).
	actor   nostr.PubKey
	rumorID nostr.ID
	// seal is the kind-20014 plaintext seal, verbatim as it arrived.
	seal nostr.Event
}

// concordControlFold is a folded Control Plane: one head per entity.
//
// Bahia folds to *cite* and to *compact*, never to adjudicate. It deliberately
// does not resolve the CORD-04 Roster: a `vac` is a citation of the Grant the
// actor holds, and every receiver resolves rank against its own current roster
// before honoring anything (CORD-04 §5). Folding rank here would duplicate that
// judgment without being able to bind it.
type concordControlFold struct {
	// heads maps an entity coordinate to its current edition.
	heads map[[32]byte]concordEdition
	// suspended names entities whose held chain forked. They have no usable
	// head: a compaction must not seat one and a citation must not name one.
	suspended map[[32]byte]struct{}
	// editions counts what was accepted, for operator-facing receipts.
	editions int
}

// head returns the entity's current edition, if it has an unsuspended one.
func (f *concordControlFold) head(entity [32]byte) (concordEdition, bool) {
	if f == nil {
		return concordEdition{}, false
	}
	if _, forked := f.suspended[entity]; forked {
		return concordEdition{}, false
	}
	edition, ok := f.heads[entity]
	return edition, ok
}

// concordEditionHash implements CORD-04 §1's edition identity:
//
//	sha256( len64(label) || label || entity_id[32] || version_be[8]
//	        || (prev ? 0x01 || prev[32] : 0x00 || zero[32])
//	        || len64(content) || content )
//
// Every field is fixed-width or length-prefixed, so distinct inputs can never
// collide, and content is hashed as the exact wire bytes rather than a
// re-serialization, so a compaction re-wrap preserves the hash.
func concordEditionHash(entity [32]byte, version uint64, prev []byte, content []byte) (string, error) {
	if prev != nil && len(prev) != 32 {
		return "", fmt.Errorf("edition prev must be 32 bytes")
	}
	hash := sha256.New()
	_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, uint64(len(concordEditionLabel))))
	_, _ = hash.Write([]byte(concordEditionLabel))
	_, _ = hash.Write(entity[:])
	_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, version))
	// The absent-prev branch is a distinct flag byte followed by zeroes, never
	// a shorter buffer: a first edition and one citing an all-zero prev must
	// not share a preimage.
	if prev != nil {
		_, _ = hash.Write([]byte{0x01})
		_, _ = hash.Write(prev)
	} else {
		_, _ = hash.Write([]byte{0x00})
		_, _ = hash.Write(make([]byte, 32))
	}
	_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, uint64(len(content))))
	_, _ = hash.Write(content)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// concordGrantLocator implements CORD-02 A.6's `concord/grant`: the ikm is the
// community_id, the id is the member, and there is no epoch.
func concordGrantLocator(communityID, member [32]byte) ([32]byte, error) {
	return concordHKDF(communityID[:], concordLabelGrant, member, nil, nil)
}

// concordControlReadKey derives the Control Plane read key (CORD-02 §5). Its
// conv_key decrypts every wrap at the plane's address; the address itself is
// held, never derived, so it is checked against the bundle rather than computed.
func concordControlReadKey(communityRoot []byte, communityID [32]byte, epoch uint64) (concordGroupKey, error) {
	return deriveConcordGroupKey(concordLabelControl, communityRoot, communityID, &epoch)
}

// foldConcordControlPlane folds a bag of wraps from one Control Plane address
// into current state.
//
// Acceptance is not authorization (CORD-04 §5): an edition is retained on its
// seal's signature, and the actor's rank is the receiver's judgment. What is
// enforced here is provenance and shape — the wrap is authored by the held
// address, the seal is plaintext and self-signed, and the rumor's own pubkey
// matches the seal's (NIP-59's impersonation check).
func foldConcordControlPlane(events []*nostr.Event, address nostr.PubKey, convKey [32]byte) (*concordControlFold, error) {
	if address == nostr.ZeroPK {
		return nil, fmt.Errorf("Control Plane address is unset")
	}
	byEntity := make(map[[32]byte][]concordEdition)
	seen := make(map[nostr.ID]struct{}, len(events))
	accepted := 0
	for _, event := range events {
		if event == nil || event.Kind != nostr.KindGiftWrap || event.PubKey != address {
			continue
		}
		// A wrap's valid signature proves only that *a* control_root holder
		// published it, never who or with what right (CORD-02 §5). The clock
		// window every other Bahia event check applies is deliberately absent:
		// a compaction re-wraps editions that may be years old, and a Control
		// Plane that drops its own history is not a plane.
		if !event.CheckID() || !event.VerifySignature() {
			continue
		}
		sealJSON, err := nip44.Decrypt(event.Content, convKey)
		if err != nil {
			// Not ours to read: a wrap from another epoch, or garbage. The
			// plane is address-scoped, so this is expected traffic, not an
			// error that should abort a fold.
			continue
		}
		edition, err := parseConcordControlEdition(sealJSON)
		if err != nil {
			continue
		}
		// A re-wrap of the same signed seal is the same edition, and a
		// compaction mints exactly that (CORD-06 §3), so identity is the
		// rumor's, never the wrap's.
		if _, duplicate := seen[edition.rumorID]; duplicate {
			continue
		}
		seen[edition.rumorID] = struct{}{}
		accepted++
		if accepted > concordControlEditionLimit {
			return nil, fmt.Errorf("Control Plane exceeds %d editions; refusing to fold a partial plane", concordControlEditionLimit)
		}
		byEntity[edition.entity] = append(byEntity[edition.entity], edition)
	}

	fold := &concordControlFold{
		heads:     make(map[[32]byte]concordEdition, len(byEntity)),
		suspended: make(map[[32]byte]struct{}),
		editions:  accepted,
	}
	for entity, editions := range byEntity {
		head, ok := foldConcordEntity(editions)
		if !ok {
			fold.suspended[entity] = struct{}{}
			continue
		}
		fold.heads[entity] = head
	}
	return fold, nil
}

// foldConcordEntity resolves one entity's editions to its head.
//
// The head is the highest version whose held chain is intact (CORD-04 §1). A
// *gap* is not a break: a compaction prunes an entity's ancestors and re-seats
// its head alone, so the head's `prev` cites an edition that no longer exists.
// Bahia starts every fold from nothing, which is precisely CORD-04 §1's fresh
// joiner — it takes the highest head as its baseline rather than treating the
// dangling `prev` as a gap to refetch. A held pair that *does* adjoin and does
// not link is a fork, and the entity is suspended rather than guessed at.
//
// Two editions tying on version break by the lower rumor id. CORD-04 §1 breaks
// that tie by authority first; Bahia does not resolve rank (see
// concordControlFold), so a same-version tie it settles by id alone may differ
// from a rank-resolving client's. The consequence is bounded and named: a
// rotation citing the losing twin parks at its receivers until Bahia re-folds,
// which is exactly the block-until-synced behavior of any unresolved citation
// (CORD-04 §5), never a forged one.
func foldConcordEntity(editions []concordEdition) (concordEdition, bool) {
	sort.Slice(editions, func(i, j int) bool {
		if editions[i].version != editions[j].version {
			return editions[i].version < editions[j].version
		}
		return editions[i].rumorID.Hex() < editions[j].rumorID.Hex()
	})
	deduped := make([]concordEdition, 0, len(editions))
	for _, edition := range editions {
		if n := len(deduped); n > 0 && deduped[n-1].version == edition.version {
			continue
		}
		deduped = append(deduped, edition)
	}
	for i := 1; i < len(deduped); i++ {
		previous, current := deduped[i-1], deduped[i]
		if current.version != previous.version+1 {
			// A gap, not a break: the fold below the gap is unreachable but
			// the head above it stands on its own signature.
			continue
		}
		if current.prev != previous.hash {
			return concordEdition{}, false
		}
	}
	if len(deduped) == 0 {
		return concordEdition{}, false
	}
	return deduped[len(deduped)-1], true
}

// parseConcordControlEdition opens one plaintext seal into an edition.
func parseConcordControlEdition(sealJSON string) (concordEdition, error) {
	var seal nostr.Event
	if err := json.Unmarshal([]byte(sealJSON), &seal); err != nil {
		return concordEdition{}, fmt.Errorf("decode seal: %w", err)
	}
	// CORD-02 §5: the Control Plane's seal MUST be plaintext. An encrypted
	// kind-20013 seal here is another plane's event at the wrong address, and
	// honoring it would seat state no compaction could ever re-wrap.
	if seal.Kind != concordControlSealKind {
		return concordEdition{}, fmt.Errorf("seal kind %d is not the plaintext Control seal", seal.Kind)
	}
	if !seal.CheckID() || !seal.VerifySignature() {
		return concordEdition{}, fmt.Errorf("seal signature is invalid")
	}
	var rumor nostr.Event
	if err := json.Unmarshal([]byte(seal.Content), &rumor); err != nil {
		return concordEdition{}, fmt.Errorf("decode edition rumor: %w", err)
	}
	if rumor.Kind != concordControlEditionKind {
		return concordEdition{}, fmt.Errorf("rumor kind %d is not a Control edition", rumor.Kind)
	}
	// NIP-59's impersonation check: renderers display rumor fields, so a rumor
	// claiming an author its seal did not sign is refused outright.
	if rumor.PubKey != seal.PubKey {
		return concordEdition{}, fmt.Errorf("edition rumor author does not match its seal")
	}

	eid, err := concordID32(concordSingleTag(rumor.Tags, "eid"))
	if err != nil {
		return concordEdition{}, fmt.Errorf("edition eid: %w", err)
	}
	vsk, err := concordTagUint(rumor.Tags, "vsk")
	if err != nil {
		return concordEdition{}, fmt.Errorf("edition vsk: %w", err)
	}
	version, err := concordTagUint(rumor.Tags, "ev")
	if err != nil {
		return concordEdition{}, fmt.Errorf("edition ev: %w", err)
	}
	// A per-entity counter starting at 1 that only ever climbs (CORD-04 §1).
	if version == 0 {
		return concordEdition{}, fmt.Errorf("edition version must start at 1")
	}
	var prevBytes []byte
	prev := concordSingleTag(rumor.Tags, "ep")
	if prev != "" {
		decoded, err := concordID32(prev)
		if err != nil {
			return concordEdition{}, fmt.Errorf("edition ep: %w", err)
		}
		prevBytes = decoded[:]
	}
	// The first edition carries no chain link, and every later one must.
	if (version == 1) != (prevBytes == nil) {
		return concordEdition{}, fmt.Errorf("edition version %d and its chain link disagree", version)
	}
	hash, err := concordEditionHash(eid, version, prevBytes, []byte(rumor.Content))
	if err != nil {
		return concordEdition{}, err
	}
	return concordEdition{
		entity:  eid,
		vsk:     vsk,
		version: version,
		prev:    prev,
		hash:    hash,
		actor:   seal.PubKey,
		// Recomputed from the decrypted bytes; an embedded id is never trusted.
		rumorID: rumor.GetID(),
		seal:    seal,
	}, nil
}

// concordSingleTag returns the first value of the named tag, or "" when the tag
// is absent or valueless.
func concordSingleTag(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// concordTagUint reads a CORD-01 decimal tag: no leading zeros, always a string.
func concordTagUint(tags nostr.Tags, name string) (uint64, error) {
	raw := concordSingleTag(tags, name)
	if raw == "" {
		return 0, fmt.Errorf("tag is absent")
	}
	if len(raw) > 1 && raw[0] == '0' {
		return 0, fmt.Errorf("tag %q carries a leading zero", raw)
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tag %q is not a decimal number", raw)
	}
	return value, nil
}

// fetchConcordControlPlane fetches and folds the Control Plane at one epoch.
//
// A fetch failure is an error rather than an empty fold: CORD-06 §3 aborts a
// Refounding whose Control Plane cannot be reliably folded, and an empty plane
// and an unreachable one are indistinguishable from their result alone.
func (m *concordMembership) fetchConcordControlPlane(
	ctx context.Context,
	community validatedConcordCommunity,
	bundle concordInviteBundle,
) (*concordControlFold, error) {
	communityID32, err := concordID32(bundle.CommunityID)
	if err != nil {
		return nil, fmt.Errorf("community_id: %w", err)
	}
	communityRoot32, err := concordID32(bundle.CommunityRoot)
	if err != nil {
		return nil, fmt.Errorf("community_root: %w", err)
	}
	address, err := nostr.PubKeyFromHex(strings.ToLower(strings.TrimSpace(bundle.ControlPK)))
	if err != nil {
		return nil, fmt.Errorf("control_pk: %w", err)
	}
	read, err := concordControlReadKey(communityRoot32[:], communityID32, bundle.RootEpoch)
	if err != nil {
		return nil, err
	}
	if err := authenticateConcordRelays(ctx, m.bus, community.relayEndpoints); err != nil {
		return nil, fmt.Errorf("authenticate Concord relays: %w", err)
	}
	events, err := m.bus.Query(ctx, []nostr.Filter{{
		Kinds:   []nostr.Kind{nostr.KindGiftWrap},
		Authors: []nostr.PubKey{address},
	}})
	if err != nil {
		return nil, fmt.Errorf("fetch the Control Plane at epoch %d: %w", bundle.RootEpoch, err)
	}
	return foldConcordControlPlane(events, address, read.ConversationKey)
}

// resolveConcordRotationAuthority resolves the `vac` a rotation acts under
// (CORD-06 §3 Authority).
//
// A rotation cites the Grant it acts under like any authority action, so a
// just-demoted admin's rotation is never honored by a lagging client. Two
// outcomes are correct and a third is not:
//
//   - The Rotator is the owner. CORD-04 §1 leaves the tag absent, because the
//     owner's rank comes from the community_id itself rather than any Grant.
//   - The Rotator holds a Grant on the folded plane. It cites that head, by
//     coordinate, version, and hash.
//   - The Rotator holds neither. The rotation is refused. Minting keys under a
//     citation nobody can resolve — or under none at all — spends the community's
//     epoch on a rotation every conformant receiver drops.
func (m *concordMembership) resolveConcordRotationAuthority(
	ctx context.Context,
	community validatedConcordCommunity,
	bundle concordInviteBundle,
	rotator nostr.PubKey,
	needFold bool,
) (concordAuthorityCitation, *concordControlFold, error) {
	isOwner := strings.EqualFold(bundle.Owner, rotator.Hex())
	if isOwner && !needFold {
		// Nothing on the plane can change an owner's channel rekey, and CORD-04
		// §1 wants no citation from them, so the fetch is skipped rather than
		// made a soft dependency that fails a rotation it cannot affect.
		return concordAuthorityCitation{}, nil, nil
	}

	fold, err := m.fetchConcordControlPlane(ctx, community, bundle)
	if err != nil {
		return concordAuthorityCitation{}, nil, err
	}
	if isOwner {
		return concordAuthorityCitation{}, fold, nil
	}

	communityID32, err := concordID32(bundle.CommunityID)
	if err != nil {
		return concordAuthorityCitation{}, nil, fmt.Errorf("community_id: %w", err)
	}
	eid, err := concordGrantLocator(communityID32, rotator)
	if err != nil {
		return concordAuthorityCitation{}, nil, fmt.Errorf("derive the Rotator's Grant coordinate: %w", err)
	}
	head, ok := fold.head(eid)
	if !ok {
		return concordAuthorityCitation{}, nil, fmt.Errorf(
			"Rotator %s holds no Grant on the folded Control Plane at epoch %d: CORD-06 §3 requires a rotation to cite the Grant it acts under, and a fabricated citation is worse than none",
			rotator.Hex(), bundle.RootEpoch)
	}
	return concordAuthorityCitation{eid: hex.EncodeToString(eid[:]), version: head.version, hash: head.hash}, fold, nil
}

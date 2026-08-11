package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
)

// concordLabelGuestbook derives the Guestbook Plane group key from the
// community_root (CORD-02 A.6). Frozen like every label.
const concordLabelGuestbook = "concord/guestbook"

const (
	// concordSnapshotKind is CORD-02 §5's Guestbook membership snapshot.
	concordSnapshotKind nostr.Kind = 3312
	// concordSnapshotChunkSize is the frozen CORD-02 §5 chunk: a snapshot
	// lists present members only and chunks at 400 per event.
	concordSnapshotChunkSize = 400
)

// concordSnapshotCorrelator domain-separates the snapshot id Bahia mints.
//
// CORD-02 §5 fixes only that all chunks of one snapshot share *one* id and one
// created_at; the value itself is an opaque correlator, so this derivation is
// Bahia's own and not a frozen CORD label. It is derived rather than random on
// purpose: CORD-06 §3 makes a Refounding resumable by making every step
// idempotent, and a random id would mint a second, competing snapshot set every
// time a crashed Refounder resumed.
const concordSnapshotCorrelator = "bahia/concord/guestbook-snapshot-id"

// ConcordCompaction records the Control Plane republished at a new epoch. Every
// field is safe to log: it names an address, an epoch, and counts.
type ConcordCompaction struct {
	Address string `json:"address"`
	Epoch   uint64 `json:"epoch"`
	// Entities is the number of heads re-wrapped, and Editions the number of
	// editions folded at the prior epoch to find them — the compaction ratio a
	// Refounding exists to restore (CORD-06 §3).
	Entities int `json:"entities"`
	Editions int `json:"editions"`
}

// ConcordGuestbookSnapshot records the membership snapshot seeded into the new
// epoch's Guestbook (CORD-02 §5).
type ConcordGuestbookSnapshot struct {
	Address    string `json:"address"`
	Epoch      uint64 `json:"epoch"`
	SnapshotID string `json:"snapshot_id"`
	Members    int    `json:"members"`
	Chunks     int    `json:"chunks"`
	// Error records why seeding stopped. The snapshot is CORD-06 §3's
	// best-effort final step — a Refounding succeeds with or without it — so a
	// failure is reported on the receipt rather than returned.
	Error string `json:"error,omitempty"`
}

// republishConcordCompaction performs CORD-06 §3's compaction: each entity's
// current head is re-wrapped at the new epoch's Control address, signed by the
// new control_root-derived signer and readable under the new community_root.
//
// The heads are not rebuilt, they are *re-wrapped*. Control Plane seals are
// plaintext (CORD-02 §5) precisely so this re-encryption preserves the original
// authors' signatures, which is what lets a fresh joiner verify authority it was
// never present for. The head's `prev` will cite an edition that no longer
// exists here; CORD-04 §1 resets the floor for exactly this reason.
//
// Publication is idempotent in CORD-06 §3's resumable sense: re-running
// re-wraps the same signed heads in the same order, so a crashed Refounder
// simply resumes.
func (m *concordMembership) republishConcordCompaction(
	ctx context.Context,
	community validatedConcordCommunity,
	fold *concordControlFold,
	communityID [32]byte,
	newRoot []byte,
	newControlRoot []byte,
	newEpoch uint64,
	expectedAddress string,
) (ConcordCompaction, error) {
	compaction := ConcordCompaction{Epoch: newEpoch, Editions: fold.editions}

	signer, err := deriveConcordGroupKey(concordLabelControlSigner, newControlRoot, communityID, &newEpoch)
	if err != nil {
		return compaction, err
	}
	// The plane a member reads is the one the rotated bundle told them to read.
	// A compaction published anywhere else is invisible to every joiner it
	// exists for, so the address is checked rather than assumed.
	if signer.PubKey.Hex() != expectedAddress {
		return compaction, fmt.Errorf("compaction address %s does not match the rotated bundle's control_pk %s", signer.PubKey.Hex(), expectedAddress)
	}
	read, err := concordControlReadKey(newRoot, communityID, newEpoch)
	if err != nil {
		return compaction, err
	}
	compaction.Address = signer.PubKey.Hex()

	// Deterministic order, so a resumed Refounding re-publishes the same heads
	// in the same sequence and a partial run resumes where it stopped.
	entities := make([][32]byte, 0, len(fold.heads))
	for entity := range fold.heads {
		entities = append(entities, entity)
	}
	sort.Slice(entities, func(i, j int) bool {
		return hex.EncodeToString(entities[i][:]) < hex.EncodeToString(entities[j][:])
	})

	at := nostr.Timestamp(m.now().Unix())
	for _, entity := range entities {
		head := fold.heads[entity]
		wrap, err := rewrapConcordEdition(head, signer, read.ConversationKey, at)
		if err != nil {
			return compaction, fmt.Errorf("re-wrap entity %x: %w", entity, err)
		}
		if err := publishConcordInvite(ctx, m.bus, community.relayEndpoints, wrap); err != nil {
			return compaction, fmt.Errorf("publish compacted entity %x: %w", entity, err)
		}
		compaction.Entities++
	}
	return compaction, nil
}

// rewrapConcordEdition re-encrypts one folded head under a new epoch. The seal
// is carried verbatim — its signature is the whole point of the plaintext form
// — and only the wrap around it is fresh.
func rewrapConcordEdition(edition concordEdition, signer concordGroupKey, readKey [32]byte, at nostr.Timestamp) (nostr.Event, error) {
	sealJSON := edition.seal.String()
	// NIP-44 hard-caps plaintext at 65,535 bytes and libraries are lenient
	// about it; a wrap built past the ceiling is unreadable to every client but
	// the one that minted it (CORD-02 Appendix B).
	if len(sealJSON) > concordStreamPlaintextLimit {
		return nostr.Event{}, fmt.Errorf("edition seal exceeds the NIP-44 plaintext limit")
	}
	encrypted, err := nip44.Encrypt(sealJSON, readKey)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encrypt seal: %w", err)
	}
	wrap := nostr.Event{
		Kind:      nostr.KindGiftWrap,
		CreatedAt: at,
		Tags:      nostr.Tags{{"p", nostr.Generate().Public().Hex()}},
		Content:   encrypted,
	}
	if err := wrap.Sign(signer.SecretKey); err != nil {
		return nostr.Event{}, fmt.Errorf("sign compaction wrap: %w", err)
	}
	if wrap.PubKey != signer.PubKey || !validSignedEvent(&wrap) {
		return nostr.Event{}, fmt.Errorf("compaction wrap is not authored by the Control Plane address")
	}
	return wrap, nil
}

// seedConcordGuestbookSnapshot publishes CORD-02 §5's membership snapshot into
// the new epoch's Guestbook.
//
// The Guestbook rides the epoch, so a Refounding would otherwise start it
// empty. The snapshot is *secondhand* — the Refounder's attestation, not a
// member's own word — so it merely seeds an npub's state at its timestamp, and
// any self-signed Join or authorized Kick newer than it supersedes it. It lists
// present members only: absence means "no seed", never a negative state, so a
// Refounder omitting somebody creates a blip rather than a disappearance.
//
// The members seeded are the rotation's surviving Recipients rather than a
// coalesce of the prior epoch's Guestbook. That *is* the subtraction CORD-02 §5
// describes for Bahia: a Recipient is exactly who receives the new epoch's
// keys, so seeding anyone else would attest the presence of a member this
// Refounding just severed.
func (m *concordMembership) seedConcordGuestbookSnapshot(
	ctx context.Context,
	community validatedConcordCommunity,
	rotator nostr.PubKey,
	communityID [32]byte,
	newRoot []byte,
	newEpoch uint64,
	members []string,
) ConcordGuestbookSnapshot {
	snapshot := ConcordGuestbookSnapshot{Epoch: newEpoch, Members: len(members)}
	if len(members) == 0 {
		return snapshot
	}

	guestbook, err := deriveConcordGroupKey(concordLabelGuestbook, newRoot, communityID, &newEpoch)
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	snapshot.Address = guestbook.PubKey.Hex()
	snapshot.SnapshotID = concordSnapshotID(communityID, newEpoch, rotator)

	chunks := concordChunkSnapshotMembers(members)
	// One created_at across every chunk (CORD-02 §5), so a receiver seeds every
	// member it reaches at the same instant no matter which chunks arrive.
	at := nostr.Timestamp(m.now().Unix())
	for index, chunk := range chunks {
		content, err := json.Marshal(chunk)
		if err != nil {
			snapshot.Error = fmt.Sprintf("encode chunk %d: %v", index, err)
			return snapshot
		}
		rumor := nostr.Event{
			Kind:      concordSnapshotKind,
			PubKey:    rotator,
			CreatedAt: at,
			// The index is 0-based: CORD-02 §5 fixes only that the pair
			// correlates the set, and a reader requiring index < count refuses
			// a 1-based final chunk while one allowing index <= count accepts
			// either. 0-based is the form every conformant reader folds.
			Tags:    nostr.Tags{{"snap", snapshot.SnapshotID, concordDecimal(uint64(index)), concordDecimal(uint64(len(chunks)))}},
			Content: string(content),
		}
		// The Guestbook's seal MUST be encrypted (kind 20013, CORD-02 §5):
		// unlike Control, it re-seeds with fresh attestations rather than being
		// re-wrapped, so nothing here has to survive a re-encryption.
		wrap, err := m.wrapConcordStreamEvent(ctx, guestbook, rotator, rumor)
		if err != nil {
			snapshot.Error = fmt.Sprintf("wrap chunk %d/%d: %v", index, len(chunks), err)
			return snapshot
		}
		if err := publishConcordInvite(ctx, m.bus, community.relayEndpoints, wrap); err != nil {
			snapshot.Error = fmt.Sprintf("publish chunk %d/%d: %v", index, len(chunks), err)
			return snapshot
		}
		snapshot.Chunks++
	}
	return snapshot
}

// concordChunkSnapshotMembers splits the survivors into CORD-02 §5's 400-member
// chunks. Chunks are independently useful — a partially received snapshot seeds
// whoever arrived and the rest heal by observation — so order is preserved and
// nothing spans a boundary.
func concordChunkSnapshotMembers(members []string) [][]string {
	chunks := make([][]string, 0, (len(members)+concordSnapshotChunkSize-1)/concordSnapshotChunkSize)
	for start := 0; start < len(members); start += concordSnapshotChunkSize {
		end := start + concordSnapshotChunkSize
		if end > len(members) {
			end = len(members)
		}
		chunks = append(chunks, members[start:end])
	}
	return chunks
}

// concordSnapshotID mints the correlator every chunk of one snapshot shares.
// Derived, never random, so a resumed Refounding re-publishes into the same set
// rather than minting a competing one (CORD-06 §3).
func concordSnapshotID(communityID [32]byte, epoch uint64, rotator nostr.PubKey) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(concordSnapshotCorrelator))
	_, _ = hash.Write(communityID[:])
	_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, epoch))
	_, _ = hash.Write(rotator[:])
	return hex.EncodeToString(hash.Sum(nil))
}

package soulfactory

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
)

// Frozen CORD-02 A.6 rekey labels. Like every derivation label these are never
// edited in place: a changed byte re-addresses every rotation ever published.
const (
	concordLabelRekeyPseudonym     = "concord/rekey-pseudonym"
	concordLabelBaseRekeyPseudonym = "concord/base-rekey-pseudonym"
	concordLabelRecipientPseudonym = "concord/recipient-pseudonym"
)

const (
	// concordRekeyKind is the CORD-06 §1 Rekey Blob rumor.
	concordRekeyKind nostr.Kind = 3303
	// concordStreamSealKind is CORD-01's encrypted seal. The rekey plane must
	// use it (CORD-02 §5), never the plaintext kind 20014 form.
	concordStreamSealKind nostr.Kind = 20013

	// concordRekeyBlobsPerEvent is the CORD-06 §1 cap: up to 120 blobs travel
	// in one event and a larger rotation spans several chunks.
	concordRekeyBlobsPerEvent = 120

	// CORD-06 §1 blob widths. The width *is* the format signal — a conformant
	// receiver drops any other length as malformed — so these are asserted on
	// every minted blob rather than assumed from the code path.
	concordChannelBlobLen    = 72  // scope_id[32] ‖ epoch_be[8] ‖ new_key[32]
	concordMemberBaseBlobLen = 104 // … ‖ new_control_pk[32]
	concordStaffBaseBlobLen  = 136 // … ‖ new_control_root[32]

	// concordStreamPlaintextLimit is NIP-44's plaintext ceiling. A conformant
	// implementation carries the plaintext length in two bytes and refuses
	// anything larger, so a wrap built past it would be unreadable to every
	// client but the one that minted it.
	concordStreamPlaintextLimit = concordInviteMaxBytes

	// concordRekeyChunkBytes bounds one chunk's blob array.
	//
	// CORD-06 §1 caps a chunk at 120 blobs, but that cap alone is not enough:
	// the CORD-01 wrap encrypts the rumor, base64s it into a seal, then
	// encrypts and base64s that again, so a chunk's bytes grow by roughly a
	// third at each layer. 120 of the 136-byte staff form would push the wrap
	// plaintext past NIP-44's ceiling. An event carries *up to* 120 blobs, so
	// chunking smaller is spec-legal: the byte budget binds first for the
	// widest forms and the count cap binds for the rest.
	concordRekeyChunkBytes = 38000
)

// concordBaseScopeID is id32(community_root): all-zeroes, which never collides
// with a channel id (CORD-06 §1).
var concordBaseScopeID = [32]byte{}

// concordRekeyRecipient is one blob destination. Staff recipients hold a
// Control-writing permission (CORD-04 §3) and so receive the 136-byte base
// form carrying the next epoch's control_root.
type concordRekeyRecipient struct {
	hex   string
	pk    nostr.PubKey
	staff bool
}

// concordRekeyScope is one rotated key with everything its blobs and its rekey
// address need. It holds fresh key material, so it is never logged and never
// reaches a receipt.
type concordRekeyScope struct {
	// base marks a community_root rotation (a Refounding); otherwise this is
	// a single Private Channel rekey.
	base bool
	// scopeID is id32(Scope): the channel id, or all-zeroes for the base.
	scopeID [32]byte
	// addressLabel and addressID derive the rekey address from the *prior*
	// community_root, so a rotation stays openable on either branch of a race
	// (CORD-06 §3).
	addressLabel string
	addressID    [32]byte
	prevEpoch    uint64
	newEpoch     uint64
	prevCommit   string
	newKey       [32]byte
	// Base rotations only: the next epoch's Control Plane pair (CORD-02 §2).
	newControlPK   [32]byte
	newControlRoot [32]byte
}

// concordRekeyBlob is one located, wrapped key inside a kind-3303 rumor.
type concordRekeyBlob struct {
	Locator string `json:"locator"`
	Wrapped string `json:"wrapped"`
}

// ConcordRekeyPublication records the kind-3303 events minted for one rotated
// scope. Every field is safe to log: it names addresses, epochs, and counts,
// never material.
type ConcordRekeyPublication struct {
	// Scope is id32(Scope) hex; all-zero hex means the community_root.
	Scope      string `json:"scope"`
	Address    string `json:"address"`
	PrevEpoch  uint64 `json:"prev_epoch"`
	NewEpoch   uint64 `json:"new_epoch"`
	PrevCommit string `json:"prev_commit"`
	Chunks     int    `json:"chunks"`
	Blobs      int    `json:"blobs"`
}

// blobPlaintext builds the fixed-width CORD-06 §1 blob for one recipient.
//
// The width declares the form, so the length is checked against the form that
// was actually built. The 72-byte *base* blob is the legacy, pre-split shape
// (CORD-06 §3) and is never minted: a base rotation without a control pair is
// an error rather than a silently downgraded epoch.
func (s concordRekeyScope) blobPlaintext(staff bool) ([]byte, error) {
	blob := make([]byte, 0, concordStaffBaseBlobLen)
	blob = append(blob, s.scopeID[:]...)
	blob = binary.BigEndian.AppendUint64(blob, s.newEpoch)
	blob = append(blob, s.newKey[:]...)

	want := concordChannelBlobLen
	if s.base {
		if s.newControlPK == ([32]byte{}) || s.newControlRoot == ([32]byte{}) {
			return nil, fmt.Errorf("base rotation is missing the next epoch's Control Plane keys")
		}
		blob = append(blob, s.newControlPK[:]...)
		want = concordMemberBaseBlobLen
		if staff {
			blob = append(blob, s.newControlRoot[:]...)
			want = concordStaffBaseBlobLen
		}
	}
	if len(blob) != want {
		return nil, fmt.Errorf("rekey blob is %d bytes, want %d", len(blob), want)
	}
	return blob, nil
}

// concordRekeyLocator implements the CORD-06 §2 locator:
//
//	hkdf(rotator_xonly || recipient_xonly, "concord/recipient-pseudonym", scope_id, epoch)
//
// Both inputs are public keys, so a recipient whose own key lives in a bunker
// can still compute where to look without touching a secret.
func concordRekeyLocator(rotator, recipient nostr.PubKey, scopeID [32]byte, epoch uint64) (string, error) {
	ikm := make([]byte, 0, 64)
	ikm = append(ikm, rotator[:]...)
	ikm = append(ikm, recipient[:]...)
	derived, err := concordHKDF(ikm, concordLabelRecipientPseudonym, scopeID, &epoch, nil)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(derived[:]), nil
}

// publishConcordRekeys mints and publishes the kind-3303 Rekey Blobs for every
// rotated scope (CORD-06 §1).
//
// Direct Invites (CORD-05 §6) reach the members Soul Factory provisioned; the
// blobs are what lets anyone else holding the prior key converge on the new
// epoch without being individually re-invited. Publication is idempotent in
// the resumable sense of CORD-06 §3: re-running a rotation re-delivers the
// same keys, so a partial publish is retried rather than repaired.
func (m *concordMembership) publishConcordRekeys(
	ctx context.Context,
	community validatedConcordCommunity,
	rotator nostr.PubKey,
	priorRoot []byte,
	scopes []concordRekeyScope,
	recipients []concordRekeyRecipient,
	citation concordAuthorityCitation,
) ([]ConcordRekeyPublication, error) {
	if len(scopes) == 0 || len(recipients) == 0 {
		return nil, nil
	}
	if err := authenticateConcordRelays(ctx, m.bus, community.relayEndpoints); err != nil {
		return nil, fmt.Errorf("authenticate Concord relays for %s: %w", community.communityID, err)
	}

	published := make([]ConcordRekeyPublication, 0, len(scopes))
	for _, scope := range scopes {
		// The rekey address derives from the prior community_root, so both
		// forks of a racing rotation can open it (CORD-06 §3).
		address, err := deriveConcordGroupKey(scope.addressLabel, priorRoot, scope.addressID, &scope.newEpoch)
		if err != nil {
			return published, fmt.Errorf("derive %s rekey address: %w", scope.label(), err)
		}
		blobs, err := m.mintConcordRekeyBlobs(ctx, rotator, scope, recipients)
		if err != nil {
			return published, err
		}
		chunked, err := concordChunkRekeyBlobs(blobs)
		if err != nil {
			return published, fmt.Errorf("chunk %s rekey blobs: %w", scope.label(), err)
		}
		for index, chunk := range chunked {
			rumor, err := m.buildConcordRekeyRumor(rotator, scope, chunk, index, len(chunked), citation)
			if err != nil {
				return published, err
			}
			wrap, err := m.wrapConcordStreamEvent(ctx, address, rotator, rumor)
			if err != nil {
				return published, fmt.Errorf("wrap %s rekey chunk %d/%d: %w", scope.label(), index+1, len(chunked), err)
			}
			if err := publishConcordInvite(ctx, m.bus, community.relayEndpoints, wrap); err != nil {
				return published, fmt.Errorf("publish %s rekey chunk %d/%d for %s: %w", scope.label(), index+1, len(chunked), community.communityID, err)
			}
		}
		published = append(published, ConcordRekeyPublication{
			Scope:      hex.EncodeToString(scope.scopeID[:]),
			Address:    address.PubKey.Hex(),
			PrevEpoch:  scope.prevEpoch,
			NewEpoch:   scope.newEpoch,
			PrevCommit: scope.prevCommit,
			Chunks:     len(chunked),
			Blobs:      len(blobs),
		})
	}
	return published, nil
}

// label names a scope for operator-facing errors without leaking material.
func (s concordRekeyScope) label() string {
	if s.base {
		return "community_root"
	}
	return "channel " + hex.EncodeToString(s.scopeID[:])
}

// mintConcordRekeyBlobs wraps one scope's fresh key to every recipient.
func (m *concordMembership) mintConcordRekeyBlobs(
	ctx context.Context,
	rotator nostr.PubKey,
	scope concordRekeyScope,
	recipients []concordRekeyRecipient,
) ([]concordRekeyBlob, error) {
	blobs := make([]concordRekeyBlob, 0, len(recipients))
	for _, recipient := range recipients {
		plaintext, err := scope.blobPlaintext(recipient.staff)
		if err != nil {
			return nil, fmt.Errorf("mint %s rekey blob: %w", scope.label(), err)
		}
		locator, err := concordRekeyLocator(rotator, recipient.pk, scope.scopeID, scope.newEpoch)
		if err != nil {
			return nil, fmt.Errorf("derive %s rekey locator: %w", scope.label(), err)
		}
		// The wrap key is the Rotator↔recipient pairwise NIP-44 key, so the
		// encryption happens inside Signet. Blob plaintexts are binary, and
		// NIP-46's JSON params cannot carry them, hence the binary-safe path.
		wrapped, err := m.signer.NIP44EncryptBytes(ctx, recipient.pk, plaintext)
		if err != nil {
			return nil, fmt.Errorf("Signet NIP-44 encrypt %s rekey blob for %s: %w", scope.label(), recipient.hex, err)
		}
		blobs = append(blobs, concordRekeyBlob{Locator: locator, Wrapped: wrapped})
	}
	return blobs, nil
}

// concordChunkRekeyBlobs splits a scope's blobs into the events that will
// carry them, binding on the CORD-06 §1 count cap and on the byte budget the
// CORD-01 double wrap leaves. Order is preserved, so the same rotation chunks
// the same way every time it is resumed.
func concordChunkRekeyBlobs(blobs []concordRekeyBlob) ([][]concordRekeyBlob, error) {
	chunks := make([][]concordRekeyBlob, 0, 1)
	current := make([]concordRekeyBlob, 0, concordRekeyBlobsPerEvent)
	size := len("[]")
	for _, blob := range blobs {
		encoded, err := json.Marshal(blob)
		if err != nil {
			return nil, fmt.Errorf("encode blob: %w", err)
		}
		if len(current) == 0 && len(encoded)+len("[]") > concordRekeyChunkBytes {
			return nil, fmt.Errorf("a single blob exceeds the %d-byte chunk budget", concordRekeyChunkBytes)
		}
		next := size + len(encoded)
		if len(current) > 0 {
			next += len(",")
		}
		if len(current) >= concordRekeyBlobsPerEvent || next > concordRekeyChunkBytes {
			chunks = append(chunks, current)
			current = make([]concordRekeyBlob, 0, concordRekeyBlobsPerEvent)
			size = len("[]") + len(encoded)
		} else {
			size = next
		}
		current = append(current, blob)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks, nil
}

// buildConcordRekeyRumor builds the kind-3303 rumor carrying one chunk. Every
// chunk of a rotation repeats the same continuity fields so a receiver can
// correlate them and tell a complete set from a missing one (CORD-06 §2).
//
// The chunk index is 0-based, i in 0..n-1: CORD-06 §1 says only "chunk i of n",
// so a reader requiring index < count drops a 1-based final chunk — and with it
// every single-chunk rotation — while one allowing index <= count folds either.
// 0-based is the intersection every conformant reader accepts, and matches the
// CORD-02 §5 snap tag this repo already mints (concord_compaction.go).
//
// The `vac` closes CORD-06 §3's Authority requirement: a rotation cites the
// Grant it acts under like any CORD-04 authority action, so a lagging client
// never honors a just-demoted admin's rotation. It rides after the frozen §1
// tags — additive, so a reader indexing by name is unaffected — and is absent
// exactly when the owner rotates (CORD-04 §1).
func (m *concordMembership) buildConcordRekeyRumor(
	rotator nostr.PubKey,
	scope concordRekeyScope,
	blobs []concordRekeyBlob,
	chunk, chunks int,
	citation concordAuthorityCitation,
) (nostr.Event, error) {
	content, err := json.Marshal(blobs)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encode %s rekey blobs: %w", scope.label(), err)
	}
	tags := nostr.Tags{
		{"scope", hex.EncodeToString(scope.scopeID[:])},
		{"newepoch", concordDecimal(scope.newEpoch)},
		{"prevepoch", concordDecimal(scope.prevEpoch)},
		{"prevcommit", scope.prevCommit},
		{"chunk", concordDecimal(uint64(chunk)), concordDecimal(uint64(chunks))},
	}
	if !citation.absent() {
		tags = append(tags, citation.tag())
	}
	return nostr.Event{
		Kind:      concordRekeyKind,
		PubKey:    rotator,
		CreatedAt: nostr.Timestamp(m.now().Unix()),
		Tags:      tags,
		Content:   string(content),
	}, nil
}

// wrapConcordStreamEvent builds the CORD-01 reversed Stream wrap: a kind-1059
// authored by the *stream* key with an ephemeral p tag, around an encrypted
// kind-20013 seal signed by the Rotator's real key.
//
// Both layers are encrypted under the stream's own self-ECDH conversation key,
// never the p-tagged key — that tag is decoration that makes the event blend
// into giftwrap traffic.
func (m *concordMembership) wrapConcordStreamEvent(ctx context.Context, stream concordGroupKey, rotator nostr.PubKey, rumor nostr.Event) (nostr.Event, error) {
	rumor.ID = rumor.GetID()
	rumorJSON := rumor.String()
	if len(rumorJSON) > concordStreamPlaintextLimit {
		return nostr.Event{}, fmt.Errorf("rumor exceeds the NIP-44 plaintext limit")
	}
	sealed, err := nip44.Encrypt(rumorJSON, stream.ConversationKey)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encrypt rumor: %w", err)
	}
	seal := nostr.Event{
		Kind:      concordStreamSealKind,
		PubKey:    rotator,
		CreatedAt: rumor.CreatedAt,
		Tags:      nostr.Tags{},
		Content:   sealed,
	}
	// The seal carries the Rotator's authorship and is the actor a receiver
	// checks against its folded Roster, so it is signed by the Signet-held
	// staff key and never by anything derived locally.
	if err := m.signer.Sign(ctx, &seal); err != nil {
		return nostr.Event{}, fmt.Errorf("Signet sign seal: %w", err)
	}
	if seal.PubKey != rotator || seal.Kind != concordStreamSealKind || !validSignedEvent(&seal) {
		return nostr.Event{}, fmt.Errorf("Signet returned an invalid seal")
	}
	sealJSON := seal.String()
	// The library will happily encrypt past NIP-44's ceiling by widening the
	// length prefix, which no conformant client can read back, so the limit is
	// enforced here rather than left to the primitive.
	if len(sealJSON) > concordStreamPlaintextLimit {
		return nostr.Event{}, fmt.Errorf("seal exceeds the NIP-44 plaintext limit")
	}
	wrapped, err := nip44.Encrypt(sealJSON, stream.ConversationKey)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encrypt seal: %w", err)
	}
	wrap := nostr.Event{
		Kind:      nostr.KindGiftWrap,
		CreatedAt: rumor.CreatedAt,
		Tags:      nostr.Tags{{"p", nostr.Generate().Public().Hex()}},
		Content:   wrapped,
	}
	if err := wrap.Sign(stream.SecretKey); err != nil {
		return nostr.Event{}, fmt.Errorf("sign stream wrap: %w", err)
	}
	if wrap.PubKey != stream.PubKey || !validSignedEvent(&wrap) {
		return nostr.Event{}, fmt.Errorf("stream wrap is not authored by the rekey address")
	}
	return wrap, nil
}

// concordDecimal renders a tag number in CORD-01's encoding: decimal, no
// leading zeros, always a string.
func concordDecimal(value uint64) string {
	return fmt.Sprintf("%d", value)
}

package soulfactory

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"golang.org/x/crypto/hkdf"
)

func TestConcordHKDFMatchesFrozenInfoLayout(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	id := [32]byte{}
	copy(id[:], strings.Repeat("i", 32))
	epoch := uint64(7)

	derived, err := concordHKDF(secret, concordLabelControlSigner, id, &epoch, nil)
	if err != nil {
		t.Fatalf("concordHKDF() error = %v", err)
	}

	// CORD-02 A.1: info = utf8(label) || 0x00 || id[32] || epoch_be[8].
	info := append([]byte(concordLabelControlSigner), 0x00)
	info = append(info, id[:]...)
	info = binary.BigEndian.AppendUint64(info, epoch)
	expected := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, nil, info), expected); err != nil {
		t.Fatalf("reference HKDF: %v", err)
	}
	if hex.EncodeToString(derived[:]) != hex.EncodeToString(expected) {
		t.Fatalf("concordHKDF() = %x, want %x", derived, expected)
	}
}

func TestConcordHKDFSeparatesLabelsEpochsAndOmittedEpoch(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	id := [32]byte{}
	epochZero := uint64(0)
	epochOne := uint64(1)

	withEpochZero, err := concordHKDF(secret, concordLabelControlSigner, id, &epochZero, nil)
	if err != nil {
		t.Fatalf("concordHKDF() error = %v", err)
	}
	withEpochOne, err := concordHKDF(secret, concordLabelControlSigner, id, &epochOne, nil)
	if err != nil {
		t.Fatalf("concordHKDF() error = %v", err)
	}
	withoutEpoch, err := concordHKDF(secret, concordLabelControlSigner, id, nil, nil)
	if err != nil {
		t.Fatalf("concordHKDF() error = %v", err)
	}
	otherLabel, err := concordHKDF(secret, "concord/control", id, &epochZero, nil)
	if err != nil {
		t.Fatalf("concordHKDF() error = %v", err)
	}
	withCounter, err := concordHKDF(secret, concordLabelControlSigner, id, &epochZero, []byte{0})
	if err != nil {
		t.Fatalf("concordHKDF() error = %v", err)
	}

	for _, pair := range [][2][32]byte{
		{withEpochZero, withEpochOne},
		{withEpochZero, withoutEpoch},
		{withEpochZero, otherLabel},
		{withEpochZero, withCounter},
	} {
		if pair[0] == pair[1] {
			t.Fatal("distinct derivation inputs produced the same key")
		}
	}
}

func TestConcordHKDFRejectsEmptySecret(t *testing.T) {
	if _, err := concordHKDF(nil, concordLabelControlSigner, [32]byte{}, nil, nil); err == nil {
		t.Fatal("concordHKDF() accepted an empty secret")
	}
}

func TestDeriveConcordGroupKeyYieldsUsableStreamAddress(t *testing.T) {
	secret := []byte(strings.Repeat("r", 32))
	id := [32]byte{}
	copy(id[:], strings.Repeat("c", 32))
	epoch := uint64(4)

	group, err := deriveConcordGroupKey(concordLabelControlSigner, secret, id, &epoch)
	if err != nil {
		t.Fatalf("deriveConcordGroupKey() error = %v", err)
	}
	if group.PubKey == nostr.ZeroPK {
		t.Fatal("derived an unusable stream address")
	}
	if group.SecretKey.Public() != group.PubKey {
		t.Fatal("derived pubkey does not match its secret key")
	}
	expectedConversationKey, err := nip44.GenerateConversationKey(group.PubKey, group.SecretKey)
	if err != nil {
		t.Fatalf("conversation key: %v", err)
	}
	if group.ConversationKey != expectedConversationKey {
		t.Fatal("conversation key is not the NIP-44 self-ECDH of the group key")
	}

	repeat, err := deriveConcordGroupKey(concordLabelControlSigner, secret, id, &epoch)
	if err != nil {
		t.Fatalf("deriveConcordGroupKey() error = %v", err)
	}
	if repeat.PubKey != group.PubKey {
		t.Fatal("group key derivation is not deterministic")
	}
	nextEpoch := epoch + 1
	rotated, err := deriveConcordGroupKey(concordLabelControlSigner, secret, id, &nextEpoch)
	if err != nil {
		t.Fatalf("deriveConcordGroupKey() error = %v", err)
	}
	if rotated.PubKey == group.PubKey {
		t.Fatal("rotating the epoch did not rotate the stream address")
	}
}

func TestValidConcordScalarRejectsOutOfRangeSeeds(t *testing.T) {
	var zero [32]byte
	if validConcordScalar(zero) {
		t.Fatal("zero is not a valid secp256k1 scalar")
	}
	var order [32]byte
	orderBytes, err := hex.DecodeString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")
	if err != nil {
		t.Fatalf("decode curve order: %v", err)
	}
	copy(order[:], orderBytes)
	if validConcordScalar(order) {
		t.Fatal("the curve order is not a valid secp256k1 scalar")
	}
	order[31]--
	if !validConcordScalar(order) {
		t.Fatal("n-1 is a valid secp256k1 scalar")
	}
}

func TestConcordEpochCommitmentMatchesA5(t *testing.T) {
	prevKey := strings.Repeat("ab", 32)
	commitment, err := concordEpochCommitment(9, prevKey)
	if err != nil {
		t.Fatalf("concordEpochCommitment() error = %v", err)
	}

	decoded, err := hex.DecodeString(prevKey)
	if err != nil {
		t.Fatalf("decode prev key: %v", err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("concord/epoch-key-commitment"))
	_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, 9))
	_, _ = hash.Write(decoded)
	if commitment != hex.EncodeToString(hash.Sum(nil)) {
		t.Fatalf("concordEpochCommitment() = %s", commitment)
	}

	if _, err := concordEpochCommitment(9, "not-hex"); err == nil {
		t.Fatal("concordEpochCommitment() accepted a malformed previous key")
	}
	other, err := concordEpochCommitment(10, prevKey)
	if err != nil {
		t.Fatalf("concordEpochCommitment() error = %v", err)
	}
	if other == commitment {
		t.Fatal("commitment does not bind the previous epoch")
	}
}

func TestConcordID32RejectsMalformedIdentifiers(t *testing.T) {
	if _, err := concordID32(strings.Repeat("A", 64)); err == nil {
		t.Fatal("concordID32() accepted uppercase hex")
	}
	if _, err := concordID32(strings.Repeat("a", 63)); err == nil {
		t.Fatal("concordID32() accepted a short identifier")
	}
	id, err := concordID32(strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("concordID32() error = %v", err)
	}
	if hex.EncodeToString(id[:]) != strings.Repeat("a", 64) {
		t.Fatalf("concordID32() = %x", id)
	}
}

package soulfactory

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/btcsuite/btcd/btcec/v2"
)

// Frozen CORD-02 Appendix A derivation labels. Changing a labeled byte
// re-addresses every prior event, so these are never edited in place.
const (
	concordLabelControlSigner   = "concord/control-signer"
	concordEpochCommitmentLabel = "concord/epoch-key-commitment"
)

// concordScalarRetryLimit bounds the A.3 normalization retry loop. The reject
// branch is ~2^-128 rare, so exhausting it means the derivation is broken and
// the caller must fail closed rather than mint an address nobody can reach.
const concordScalarRetryLimit = 256

// concordGroupKey is the CORD-02 A.2 group_key triple: a plane's Stream
// address, the secret that signs its wraps, and the self-ECDH conversation key
// that encrypts them.
type concordGroupKey struct {
	SecretKey       nostr.SecretKey
	PubKey          nostr.PubKey
	ConversationKey [32]byte
}

// concordHKDF implements CORD-02 A.1:
//
//	HKDF-SHA256(ikm = secret, salt = none,
//	            info = utf8(label) || 0x00 || id[32] || epoch_be[8], len = 32)
//
// epoch is the only omittable field; suffix carries the A.3 retry counter,
// which appends after whatever fields are present.
func concordHKDF(secret []byte, label string, id [32]byte, epoch *uint64, suffix []byte) ([32]byte, error) {
	var derived [32]byte
	if len(secret) == 0 {
		return derived, fmt.Errorf("Concord derivation secret is empty")
	}
	info := make([]byte, 0, len(label)+1+32+8+len(suffix))
	info = append(info, label...)
	info = append(info, 0x00)
	info = append(info, id[:]...)
	if epoch != nil {
		info = binary.BigEndian.AppendUint64(info, *epoch)
	}
	info = append(info, suffix...)

	key, err := hkdf.Key(sha256.New, secret, nil, string(info), 32)
	if err != nil {
		return derived, fmt.Errorf("Concord HKDF %s: %w", label, err)
	}
	if len(key) != 32 {
		return derived, fmt.Errorf("Concord HKDF %s produced %d bytes, want 32", label, len(key))
	}
	copy(derived[:], key)
	return derived, nil
}

// deriveConcordGroupKey implements CORD-02 A.2/A.3: the hkdf output is a seed
// normalized into a valid secp256k1 secret key, whose x-only pubkey is the
// on-wire Stream address.
func deriveConcordGroupKey(label string, secret []byte, id [32]byte, epoch *uint64) (concordGroupKey, error) {
	var group concordGroupKey
	for counter := 0; counter <= concordScalarRetryLimit; counter++ {
		var suffix []byte
		if counter > 0 {
			// The first retry appends counter byte 0, then 1, then 2...
			suffix = []byte{byte(counter - 1)}
		}
		seed, err := concordHKDF(secret, label, id, epoch, suffix)
		if err != nil {
			return group, err
		}
		if !validConcordScalar(seed) {
			continue
		}
		secretKey := nostr.SecretKey(seed)
		pubKey := secretKey.Public()
		if pubKey == nostr.ZeroPK {
			continue
		}
		conversationKey, err := nip44.GenerateConversationKey(pubKey, secretKey)
		if err != nil {
			return group, fmt.Errorf("Concord %s conversation key: %w", label, err)
		}
		return concordGroupKey{SecretKey: secretKey, PubKey: pubKey, ConversationKey: conversationKey}, nil
	}
	return group, fmt.Errorf("Concord %s derivation exhausted scalar normalization retries", label)
}

// validConcordScalar reports whether seed is a usable secp256k1 secret key,
// i.e. 0 < seed < n. btcec silently reduces out-of-range scalars, which would
// make two implementations disagree, so the range is checked explicitly.
func validConcordScalar(seed [32]byte) bool {
	scalar := new(big.Int).SetBytes(seed[:])
	if scalar.Sign() == 0 {
		return false
	}
	return scalar.Cmp(btcec.S256().N) < 0
}

// concordEpochCommitment implements CORD-02 A.5, the CORD-06 continuity check:
//
//	sha256( utf8("concord/epoch-key-commitment") || prev_epoch_be[8] || prev_key[32] )
//
// A rotation carries it as prevcommit so a receiver can prove the new key
// extends exactly the key it already holds.
func concordEpochCommitment(prevEpoch uint64, prevKeyHex string) (string, error) {
	prevKey, err := hex.DecodeString(prevKeyHex)
	if err != nil || len(prevKey) != 32 {
		return "", fmt.Errorf("previous epoch key must be 32-byte hex")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(concordEpochCommitmentLabel))
	_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, prevEpoch))
	_, _ = hash.Write(prevKey)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// concordID32 decodes a 32-byte lowercase hex identifier into the raw form
// every derivation consumes (ids are never hex on the wire, CORD-02 A).
func concordID32(value string) ([32]byte, error) {
	var id [32]byte
	if !validConcordHex32(value) {
		return id, fmt.Errorf("identifier must be 32-byte lowercase hex")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return id, fmt.Errorf("identifier must be 32-byte lowercase hex")
	}
	copy(id[:], decoded)
	return id, nil
}

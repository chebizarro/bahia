package soulfactory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"fiatjaf.com/nostr"
)

func soulTestID(label string) nostr.ID {
	sum := sha256.Sum256([]byte("bahia-soulfactory-id:" + label))
	return nostr.ID(sum)
}

func soulTestPubKey(label string) nostr.PubKey {
	for attempt := 0; attempt < 128; attempt++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("bahia-soulfactory-secret:%s:%d", label, attempt)))
		secret, err := nostr.SecretKeyFromHex(hex.EncodeToString(sum[:]))
		if err == nil {
			return secret.Public()
		}
	}
	panic("unable to derive deterministic nostr pubkey for test")
}

func soulTestPubKeyHex(label string) string {
	return soulTestPubKey(label).Hex()
}

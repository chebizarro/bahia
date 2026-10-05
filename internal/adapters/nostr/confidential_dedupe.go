package nostr

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	gonostr "fiatjaf.com/nostr"
)

// confidentialStateHash is a keyed digest of the stable plaintext. It lets the
// projector dedupe randomized OCK ciphertext without exposing a guessable hash
// of sensitive payment or vulnerability data in a public tag.
func confidentialStateHash(privateKey, plaintext string) gonostr.Tag {
	mac := hmac.New(sha256.New, []byte(privateKey))
	mac.Write([]byte(stableContent(plaintext, false)))
	return gonostr.Tag{"state_hash", hex.EncodeToString(mac.Sum(nil))}
}

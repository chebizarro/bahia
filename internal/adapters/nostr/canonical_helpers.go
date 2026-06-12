package nostr

import (
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

func eventIDHex(ev *gonostr.Event) string {
	if ev == nil {
		return ""
	}
	return ev.ID.Hex()
}

func eventPubKeyHex(ev *gonostr.Event) string {
	if ev == nil {
		return ""
	}
	return ev.PubKey.Hex()
}

func eventSignatureHex(ev *gonostr.Event) string {
	if ev == nil {
		return ""
	}
	return nostrutil.SignatureHex(ev.Sig)
}

func eventKindInt(ev *gonostr.Event) int {
	if ev == nil {
		return 0
	}
	return int(ev.Kind)
}

func signEventWithPrivateKeyHex(ev *gonostr.Event, privateKeyHex string) error {
	if err := nostrutil.SignEventWithHexKey(ev, privateKeyHex); err != nil {
		return fmt.Errorf("signing nostr event: %w", err)
	}
	return nil
}

func publicKeyHexFromPrivateKeyHex(privateKeyHex string) (string, error) {
	return nostrutil.PublicKeyHexFromPrivateKeyHex(privateKeyHex)
}

func filterKindsFromInts(kinds []int) []gonostr.Kind {
	return nostrutil.KindsFromInts(kinds)
}

func filterAuthorsFromHex(authors []string) ([]gonostr.PubKey, error) {
	return nostrutil.PubKeysFromHex(authors)
}

func canonicalKind(kind int) gonostr.Kind {
	return gonostr.Kind(kind)
}

func eventKindMatches(ev *gonostr.Event, kind int) bool {
	return ev != nil && int(ev.Kind) == kind
}

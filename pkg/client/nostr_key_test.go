package client

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

func TestNormalizeNostrPrivateKey(t *testing.T) {
	secret := nostr.Generate()
	want := secret.Hex()
	for _, input := range []string{want, strings.ToUpper(want), "  " + nip19.EncodeNsec(secret) + "  "} {
		got, err := NormalizeNostrPrivateKey(input)
		if err != nil || got != want {
			t.Errorf("NormalizeNostrPrivateKey(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "not-a-key", strings.Repeat("a", 62)} {
		if _, err := NormalizeNostrPrivateKey(input); err == nil {
			t.Errorf("NormalizeNostrPrivateKey(%q) unexpectedly succeeded", input)
		}
	}
}

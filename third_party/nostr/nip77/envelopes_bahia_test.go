package nip77

import (
	"testing"
)

// TestErrorEnvelopeUsesNIP77Label (Bahia patch, see BAHIA_PATCHES.md): relays
// must send NEG-ERR, the label NIP-77 defines and ParseNegMessage reads, and a
// reason containing quotes must survive the round trip.
func TestErrorEnvelopeUsesNIP77Label(t *testing.T) {
	sent := ErrorEnvelope{SubscriptionID: "sub", Reason: `blocked: filter "too" large`}
	encoded, err := sent.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `["NEG-ERR","sub","blocked: filter \"too\" large"]`; got != want {
		t.Fatalf("MarshalJSON = %s, want %s", got, want)
	}
	for _, message := range []string{string(encoded), `["NEG-ERROR","sub","legacy"]`} {
		parsed, ok := ParseNegMessage(message).(*ErrorEnvelope)
		if !ok || parsed.SubscriptionID != "sub" {
			t.Fatalf("ParseNegMessage(%s) = %#v", message, ParseNegMessage(message))
		}
	}
	if parsed := ParseNegMessage(string(encoded)).(*ErrorEnvelope); parsed.Reason != sent.Reason {
		t.Fatalf("reason = %q, want %q", parsed.Reason, sent.Reason)
	}
}

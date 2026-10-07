package nostrutil

import (
	"testing"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

// ageCapKindCases is the kind table for the inbound-event age cap:
// the boundaries of every NIP-01 kind range, and kind 5.
var ageCapKindCases = map[canonicalnostr.Kind]bool{
	0: false, 3: false, 10000: false, 10002: false, 19999: false,
	30000: false, 30078: false, 39999: false,
	canonicalnostr.KindDeletion: false,
	1:                           true, 4: true, 1059: true, 9999: true, 20000: true, 29999: true, 40000: true, 65535: true,
}

func TestAgeCappedCapsOnlyRegularAndEphemeralKinds(t *testing.T) {
	for kind, capped := range ageCapKindCases {
		require.Equal(t, capped, AgeCapped(kind), "kind %d", kind)
	}
}

package nostr

import (
	"strconv"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
)

const testNostrPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

func signedTestEvent(t *testing.T, kind int, createdAt time.Time) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{
		Kind:      canonicalKind(kind),
		CreatedAt: gonostr.Timestamp(createdAt.Unix()),
		Content:   "{}",
		Tags:      gonostr.Tags{},
	}
	require.NoError(t, signEventWithPrivateKeyHex(ev, testNostrPrivateKey))
	return ev
}

func TestValidateInboundEventAcceptsValidSignedEvent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ev := signedTestEvent(t, 5101, now)

	require.NoError(t, ValidateInboundEvent(ev, now, InboundEventMaxFutureSkew))
}

func TestValidateInboundEventRejectsInvalidEvents(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	tests := []struct {
		name string
		ev   func() *gonostr.Event
	}{
		{
			name: "nil event",
			ev:   func() *gonostr.Event { return nil },
		},
		{
			name: "id mismatch",
			ev: func() *gonostr.Event {
				ev := signedTestEvent(t, 5101, now)
				ev.ID = gonostr.ID{}
				return ev
			},
		},
		{
			name: "signature mismatch",
			ev: func() *gonostr.Event {
				ev := signedTestEvent(t, 5101, now)
				ev.Sig = [64]byte{}
				return ev
			},
		},
		{
			name: "future timestamp",
			ev:   func() *gonostr.Event { return signedTestEvent(t, 5101, now.Add(InboundEventMaxFutureSkew+time.Second)) },
		},
		{
			name: "past timestamp",
			ev:   func() *gonostr.Event { return signedTestEvent(t, 5101, now.Add(-InboundEventMaxPastAge-time.Second)) },
		},
		{
			name: "pubkey mismatch",
			ev: func() *gonostr.Event {
				ev := signedTestEvent(t, 5101, now)
				ev.PubKey = gonostr.PubKey{}
				return ev
			},
		},
		{
			name: "invalid tag structure",
			ev: func() *gonostr.Event {
				ev := &gonostr.Event{Kind: canonicalKind(5101), CreatedAt: gonostr.Timestamp(now.Unix()), Content: "{}", Tags: gonostr.Tags{{}}}
				require.NoError(t, signEventWithPrivateKeyHex(ev, testNostrPrivateKey))
				return ev
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, ValidateInboundEvent(tt.ev(), now, InboundEventMaxFutureSkew))
		})
	}
}

// replaceable and addressable state and deletion requests keep their
// force however old they are; regular and ephemeral events stay capped. The
// rule is nostrutil.AgeCapped, shared with the relay sidecar's write policy
// .
func TestValidateInboundEventAgeCapAppliesOnlyToRegularKinds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	old := now.Add(-InboundEventMaxPastAge - 30*24*time.Hour)

	for _, kind := range []int{0, 3, 10002, 19999, 30000, 31410, 39999, 5} {
		require.False(t, nostrutil.AgeCapped(canonicalKind(kind)), "kind %d", kind)
		require.NoError(t, ValidateInboundEvent(signedTestEvent(t, kind, old), now, InboundEventMaxFutureSkew), "kind %d", kind)
	}
	for _, kind := range []int{1, 4903, 9999, 20000, 29999, 40000} {
		require.True(t, nostrutil.AgeCapped(canonicalKind(kind)), "kind %d", kind)
		err := ValidateInboundEvent(signedTestEvent(t, kind, old), now, InboundEventMaxFutureSkew)
		require.ErrorContains(t, err, "too far in past", "kind %d", kind)
	}
	require.Equal(t, nostrutil.MaxEventAge, InboundEventMaxPastAge)
}

// expired events (NIP-40) are dropped at the trust boundary.
func TestValidateInboundEventRejectsExpiredEvents(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	withExpiration := func(expiresAt time.Time) *gonostr.Event {
		ev := &gonostr.Event{
			Kind:      canonicalKind(30078),
			CreatedAt: gonostr.Timestamp(now.Add(-time.Hour).Unix()),
			Content:   "{}",
			Tags:      gonostr.Tags{{"d", "x"}, {"expiration", strconv.FormatInt(expiresAt.Unix(), 10)}},
		}
		require.NoError(t, signEventWithPrivateKeyHex(ev, testNostrPrivateKey))
		return ev
	}

	require.ErrorContains(t, ValidateInboundEvent(withExpiration(now), now, InboundEventMaxFutureSkew), "expired")
	require.ErrorContains(t, ValidateInboundEvent(withExpiration(now.Add(-time.Minute)), now, InboundEventMaxFutureSkew), "expired")
	require.NoError(t, ValidateInboundEvent(withExpiration(now.Add(time.Minute)), now, InboundEventMaxFutureSkew))
}

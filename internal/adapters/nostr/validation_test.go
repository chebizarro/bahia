package nostr

import (
	"strings"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
)

const testNostrPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

func signedTestEvent(t *testing.T, kind int, createdAt time.Time) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{
		Kind:      kind,
		CreatedAt: gonostr.Timestamp(createdAt.Unix()),
		Content:   "{}",
		Tags:      gonostr.Tags{},
	}
	require.NoError(t, ev.Sign(testNostrPrivateKey))
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
				ev.ID = strings.Repeat("0", 64)
				return ev
			},
		},
		{
			name: "signature mismatch",
			ev: func() *gonostr.Event {
				ev := signedTestEvent(t, 5101, now)
				ev.Sig = strings.Repeat("0", 128)
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
			name: "malformed pubkey",
			ev: func() *gonostr.Event {
				ev := signedTestEvent(t, 5101, now)
				ev.PubKey = "not-hex"
				return ev
			},
		},
		{
			name: "invalid tag structure",
			ev: func() *gonostr.Event {
				ev := &gonostr.Event{Kind: 5101, CreatedAt: gonostr.Timestamp(now.Unix()), Content: "{}", Tags: gonostr.Tags{{}}}
				require.NoError(t, ev.Sign(testNostrPrivateKey))
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

package khatru

import (
	"context"
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

// TestRemoveListenersBySSID (Bahia patch): a relay-initiated close removes
// exactly the named listeners, cancels their request context with
// ErrSubscriptionClosedByRelay and fires OnListenerRemoved, without touching a
// newer listener that reuses the same subscription id.
func TestRemoveListenersBySSID(t *testing.T) {
	rl := NewRelay()
	ws := &WebSocket{Context: rl.ctx}
	rl.clients[ws] = nil

	type removal struct {
		ssid int
		id   string
	}
	var removed []removal
	added := map[string][]int{}
	rl.OnListenerAdded = func(_ *WebSocket, ssid int, id string, _ nostr.Filter) {
		added[id] = append(added[id], ssid)
	}
	rl.OnListenerRemoved = func(_ *WebSocket, ssid int, id string, _ nostr.Filter) {
		removed = append(removed, removal{ssid, id})
	}

	oldCtx, oldCancel := context.WithCancelCause(context.Background())
	rl.addListener(ws, "x", nostr.Filter{Kinds: []nostr.Kind{1}}, oldCancel)
	rl.addListener(ws, "x", nostr.Filter{Kinds: []nostr.Kind{2}}, oldCancel)
	stale := append([]int(nil), added["x"]...)

	// the client re-REQs "x"; khatru replaces the old listeners
	rl.removeListenerId(ws, "x")
	removed = nil
	newCtx, newCancel := context.WithCancelCause(context.Background())
	rl.addListener(ws, "x", nostr.Filter{Kinds: []nostr.Kind{1}}, newCancel)
	current := added["x"][2]

	// a relay-side close aimed at the stale listeners is a no-op
	rl.RemoveListeners(ws, stale...)
	require.Empty(t, removed)
	require.NoError(t, newCtx.Err())
	require.Len(t, rl.clients[ws], 1)
	require.ErrorIs(t, context.Cause(oldCtx), ErrSubscriptionClosedByClient)

	rl.RemoveListeners(ws, current)
	require.Equal(t, []removal{{current, "x"}}, removed)
	require.True(t, errors.Is(context.Cause(newCtx), ErrSubscriptionClosedByRelay))
	require.Empty(t, rl.clients[ws])
	_, still := rl.dispatcher.subscriptions.Load(current)
	require.False(t, still)

	// unknown connection: no panic
	rl.RemoveListeners(&WebSocket{Context: rl.ctx}, current)
}

func TestGetSubscriptionIDOutsideREQ(t *testing.T) {
	require.Equal(t, "", GetSubscriptionID(context.Background()))
	ctx := context.WithValue(context.Background(), subscriptionIdKey, "sub")
	require.Equal(t, "sub", GetSubscriptionID(ctx))
}

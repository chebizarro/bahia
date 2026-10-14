package khatru

import (
	"context"
	"iter"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/fasthttp/websocket"
	"github.com/stretchr/testify/require"
)

func TestCloseDuringFirstStoredQueryRemovesRequest(t *testing.T) {
	relay := NewRelay()
	queryStarted := make(chan struct{})
	queryCanceled := make(chan struct{})
	releaseQuery := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseQuery) }) }
	relay.QueryStored = func(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		return func(yield func(nostr.Event) bool) {
			if len(filter.Kinds) == 0 || filter.Kinds[0] != 1 {
				return
			}
			close(queryStarted)
			<-ctx.Done()
			close(queryCanceled)
			<-releaseQuery
		}
	}
	server := httptest.NewServer(relay)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	defer release()
	require.NoError(t, conn.WriteJSON([]any{"REQ", "during-query", map[string]any{"kinds": []int{1}}, map[string]any{"kinds": []int{2}}}))
	select {
	case <-queryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first stored query did not start")
	}
	require.NoError(t, conn.WriteJSON([]any{"CLOSE", "during-query"}))
	select {
	case <-queryCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("CLOSE did not cancel the in-flight request")
	}
	// QueryStored may finish after CLOSE. Neither its first nor later filter may
	// attach a listener, even though the connection remains open.
	release()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	var frame []any
	require.NoError(t, conn.ReadJSON(&frame))
	require.Equal(t, "EOSE", frame[0])
	clients, listeners := relay.Stats()
	require.Equal(t, 1, clients)
	require.Zero(t, listeners)
}

func TestCloseThenReqSameIDKeepsReplacement(t *testing.T) {
	relay := NewRelay()
	server := httptest.NewServer(relay)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))

	for round := range 32 {
		require.NoError(t, conn.WriteJSON([]any{"REQ", "reuse", map[string]any{"kinds": []int{1}}}))
		var frame []any
		require.NoError(t, conn.ReadJSON(&frame))
		require.Equal(t, "EOSE", frame[0], "round %d", round)
		require.NoError(t, conn.WriteJSON([]any{"CLOSE", "reuse"}))
		require.NoError(t, conn.WriteJSON([]any{"REQ", "reuse", map[string]any{"kinds": []int{2}}}))
		require.NoError(t, conn.ReadJSON(&frame))
		require.Equal(t, "EOSE", frame[0], "round %d", round)
		_, listeners := relay.Stats()
		require.Equal(t, 1, listeners, "round %d: stale CLOSE removed the replacement", round)
	}
}

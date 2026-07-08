package wrappers

import (
	"context"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"github.com/stretchr/testify/require"
)

var sk = nostr.MustSecretKeyFromHex("486d5f6d4891f4ce3cd5f4d6b62d184ec8ea10db455830ab7918ca43d4d7ad24")

func TestRelayWrapper(t *testing.T) {
	ctx := context.Background()

	s := &slicestore.SliceStore{}
	s.Init()
	defer s.Close()

	w := StorePublisher{Store: s, MaxLimit: 500}

	evt1 := nostr.Event{
		Kind:      3,
		CreatedAt: 0,
		Tags:      nostr.Tags{},
		Content:   "first",
	}
	evt1.Sign(sk)

	evt2 := nostr.Event{
		Kind:      3,
		CreatedAt: 1,
		Tags:      nostr.Tags{},
		Content:   "second",
	}
	evt2.Sign(sk)

	for range 200 {
		go w.Publish(ctx, evt1)
		go w.Publish(ctx, evt2)
	}
	time.Sleep(time.Millisecond * 200)

	evts := slices.Collect(w.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{3}}))
	require.Len(t, evts, 1)
}

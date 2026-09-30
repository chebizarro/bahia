package nostr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCount(t *testing.T) {
	const RELAY = "wss://chorus.pjv.me"

	rl := mustRelayConnect(t, RELAY)
	defer rl.Close()

	count, _, err := rl.Count(context.Background(), Filter{
		Kinds: []Kind{KindFollowList}, Tags: TagMap{"p": []string{"3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"}},
	}, SubscriptionOptions{})
	assert.NoError(t, err)
	assert.Greater(t, count, uint32(0))
}

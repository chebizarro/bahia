package mcp

import (
	"iter"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestServerConstructionRequiresLocalStateStore(t *testing.T) {
	_, err := NewServerWithOptionsChecked(nil, zap.NewNop(), ServerDeps{})
	require.ErrorContains(t, err, "state event store is required")
	var typedNil *nilMCPStateStore
	_, err = NewServerWithOptionsChecked(nil, zap.NewNop(), ServerDeps{StateStore: typedNil})
	require.ErrorContains(t, err, "state event store is required")
	_, err = NewServerWithOptionsChecked(nil, zap.NewNop(), ServerDeps{StateStore: emptyMCPStateStore{}})
	require.ErrorContains(t, err, "service pubkey is required")
	server, err := NewServerWithOptionsChecked(nil, zap.NewNop(), ServerDeps{StateStore: emptyMCPStateStore{}, ServicePubkey: nostr.Generate().Public().Hex()})
	require.NoError(t, err)
	require.NotNil(t, server)
}

type nilMCPStateStore struct{}

func (*nilMCPStateStore) QueryEvents(nostr.Filter) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}

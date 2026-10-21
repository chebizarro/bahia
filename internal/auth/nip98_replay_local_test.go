package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
)

type sharedReplayFailure struct{ calls int }

func (s *sharedReplayFailure) Claim(context.Context, string, time.Time) (bool, error) {
	s.calls++
	return false, fmt.Errorf("shared replay store unavailable")
}

func TestDurableReplayDoesNotDowngradeSharedFailure(t *testing.T) {
	ledger, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ledger.Close()) })
	shared := &sharedReplayFailure{}
	store := NewDurableNIP98ReplayStore(ledger, shared)
	id := strings.Repeat("a", 64)
	expiry := time.Now().Add(time.Minute)
	ok, err := store.Claim(t.Context(), id, expiry)
	require.Error(t, err)
	require.False(t, ok)
	require.Equal(t, 1, shared.calls)
	localOnly := NewDurableNIP98ReplayStore(ledger, nil)
	ok, err = localOnly.Claim(t.Context(), id, expiry)
	require.NoError(t, err)
	require.False(t, ok, "SQL outage cannot clear an already consumed local claim")
	ok, err = NewDurableNIP98ReplayStore(nil, nil).Claim(t.Context(), id, expiry)
	require.Error(t, err)
	require.False(t, ok)
}

func TestDurableReplayStillCoordinatesIndependentLedgersWhenShared(t *testing.T) {
	shared := newInMemoryNIP98ReplayStore()
	id := strings.Repeat("b", 64)
	expiry := time.Now().Add(time.Minute)
	for i := 0; i < 2; i++ {
		ledger, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, ledger.Close()) })
		accepted, err := NewDurableNIP98ReplayStore(ledger, shared).Claim(t.Context(), id, expiry)
		require.NoError(t, err)
		require.Equal(t, i == 0, accepted)
	}
}

func TestDurableReplayRefusesAutoRecreatedCorruptLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.bolt")
	require.NoError(t, os.WriteFile(path, []byte("not a bbolt ledger"), 0o600))
	ledger, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ledger.Close()) })
	require.NotEmpty(t, ledger.MovedAside())
	accepted, err := NewDurableNIP98ReplayStore(ledger, nil).Claim(t.Context(), strings.Repeat("a", 64), time.Now().Add(time.Minute))
	require.Error(t, err)
	require.False(t, accepted, "corruption recovery cannot silently forget replay claims")
	require.NoError(t, ledger.Close())
	reopened, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	require.Empty(t, reopened.MovedAside(), "the in-memory corruption diagnostic does not survive reopening")
	accepted, err = NewDurableNIP98ReplayStore(reopened, nil).Claim(t.Context(), strings.Repeat("a", 64), time.Now().Add(time.Minute))
	require.Error(t, err)
	require.False(t, accepted, "restarting must not clear the durable auth quarantine")
}

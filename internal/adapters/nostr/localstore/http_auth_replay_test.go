package localstore

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestHTTPAuthClaimsAtomicAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.bolt")
	outbox, err := OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	id := strings.Repeat("a", 64)
	expires := time.Now().Add(time.Minute)
	var accepted atomic.Int32
	var workers sync.WaitGroup
	errors := make(chan error, 32)
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ok, err := outbox.ClaimHTTPAuth(t.Context(), id, expires)
			if err != nil {
				errors <- err
			}
			if ok {
				accepted.Add(1)
			}
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, accepted.Load())
	require.NoError(t, outbox.Close())
	outbox, err = OpenOutbox(path)
	require.NoError(t, err)
	ok, err := outbox.ClaimHTTPAuth(t.Context(), id, expires)
	require.NoError(t, err)
	require.False(t, ok)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ok, err = outbox.ClaimHTTPAuth(ctx, strings.Repeat("b", 64), expires)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, ok)
	require.NoError(t, outbox.Close())
	ok, err = outbox.ClaimHTTPAuth(t.Context(), strings.Repeat("c", 64), expires)
	require.Error(t, err)
	require.False(t, ok)
}

func TestHTTPAuthExpiryPruningIsBoundedAndCorruptionFailsClosed(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	now := time.Now()
	expired := make([]byte, 8)
	binary.BigEndian.PutUint64(expired, uint64(now.Add(-time.Minute).UnixNano()))
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		claims, err := tx.CreateBucketIfNotExists(httpAuthReplayBucket)
		if err != nil {
			return err
		}
		index, err := tx.CreateBucketIfNotExists(httpAuthReplayExpiryBucket)
		if err != nil {
			return err
		}
		for i := 0; i < 300; i++ {
			id := []byte(fmt.Sprintf("%064x", i))
			if err := claims.Put(id, expired); err != nil {
				return err
			}
			if err := index.Put(append(append([]byte{}, expired...), id...), []byte{1}); err != nil {
				return err
			}
		}
		return nil
	}))
	ok, err := outbox.ClaimHTTPAuth(t.Context(), strings.Repeat("e", 64), now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, outbox.shared.db.View(func(tx *bbolt.Tx) error {
		require.Equal(t, 45, tx.Bucket(httpAuthReplayBucket).Stats().KeyN)
		return nil
	}))
	ok, err = outbox.ClaimHTTPAuth(t.Context(), strings.Repeat("f", 64), now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, outbox.shared.db.View(func(tx *bbolt.Tx) error {
		require.Equal(t, 2, tx.Bucket(httpAuthReplayBucket).Stats().KeyN)
		return nil
	}))
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(httpAuthReplayBucket).Put([]byte(strings.Repeat("c", 64)), []byte("corrupt"))
	}))
	ok, err = outbox.ClaimHTTPAuth(t.Context(), strings.Repeat("c", 64), now.Add(time.Minute))
	require.Error(t, err)
	require.False(t, ok)
}

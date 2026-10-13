package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type retentionBatchCall struct {
	cutoff time.Time
	limit  int
}
type scriptedObservationDeleter struct {
	mu       sync.Mutex
	calls    []retentionBatchCall
	results  []int64
	errors   []error
	notified chan struct{}
}

func (d *scriptedObservationDeleter) DeleteExpiredBatch(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	d.mu.Lock()
	i := len(d.calls)
	d.calls = append(d.calls, retentionBatchCall{cutoff, limit})
	var count int64
	var err error
	if i < len(d.results) {
		count = d.results[i]
	}
	if i < len(d.errors) {
		err = d.errors[i]
	}
	d.mu.Unlock()
	d.notified <- struct{}{}
	return count, err
}

func TestRuntimeObservationRetentionRunnerPacesAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []int64
		errors  []error
	}{
		{name: "full batch", results: []int64{runtimeObservationBatchSize, 0}},
		{name: "failure", errors: []error{errors.New("database unavailable"), nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &scriptedObservationDeleter{results: tc.results, errors: tc.errors, notified: make(chan struct{}, 2)}
			r := NewRuntimeObservationRetentionRunner(d, zap.NewNop())
			now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
			r.now = func() time.Time { return now }
			r.batchPause = time.Millisecond
			r.errorPause = time.Millisecond
			r.idlePause = time.Hour
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- r.Run(ctx) }()
			for i := 0; i < 2; i++ {
				select {
				case <-d.notified:
				case <-time.After(time.Second):
					t.Fatal("retention batch did not run")
				}
			}
			cancel()
			require.NoError(t, <-done)
			d.mu.Lock()
			defer d.mu.Unlock()
			require.Len(t, d.calls, 2)
			for _, call := range d.calls {
				require.Equal(t, now.Add(-runtimeObservationRetention), call.cutoff)
				require.Equal(t, runtimeObservationBatchSize, call.limit)
			}
		})
	}
}

func TestRuntimeObservationRetentionRunnerAbsentDatabase(t *testing.T) {
	require.NoError(t, NewRuntimeObservationRetentionRunner(nil, nil).Run(t.Context()))
}

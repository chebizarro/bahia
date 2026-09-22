package app

import (
	"context"
	"errors"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"testing"
	"testing/synctest"
	"time"
)

func TestPlaneSupervisionRetriesTransientFailuresAndCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls, reports := 0, 0
		started := time.Now()
		err := superviseExecutionPlane(ctx, func(ctx context.Context) error {
			calls++
			switch calls {
			case 1:
				return context.DeadlineExceeded
			case 2:
				return &domain.VMProviderError{Code: domain.VMErrorUnavailable, Retryable: true}
			default:
				cancel()
				return ctx.Err()
			}
		}, func(error) { reports++ })
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 3, calls)
		require.Equal(t, 2, reports)
		require.Equal(t, 750*time.Millisecond, time.Since(started))
	})
}

func TestPlaneSupervisionCancellationInterruptsBackoffAndPermanentErrorsStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reported := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- superviseExecutionPlane(ctx, func(context.Context) error { return context.DeadlineExceeded }, func(error) { close(reported) })
		}()
		<-reported
		synctest.Wait()
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
	permanent := errors.New("policy denied")
	require.ErrorIs(t, superviseExecutionPlane(t.Context(), func(context.Context) error { return permanent }, func(error) { t.Fatal("permanent failure retried") }), permanent)
}

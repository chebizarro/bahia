package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunBootstrapStatusPublicationReturnsPublisherResult(t *testing.T) {
	want := errors.New("relay unavailable")
	err := runBootstrapStatusPublication(context.Background(), time.Second, func(context.Context) error {
		return want
	})
	require.ErrorIs(t, err, want)
}

func TestRunBootstrapStatusPublicationBoundsPublisherThatDoesNotReturn(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	t.Cleanup(func() { close(release) })

	startedAt := time.Now()
	err := runBootstrapStatusPublication(context.Background(), 20*time.Millisecond, func(context.Context) error {
		close(started)
		<-release
		return nil
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)
	select {
	case <-started:
	default:
		t.Fatal("publisher was not invoked")
	}
}

func TestRunBootstrapStatusPublicationHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	err := runBootstrapStatusPublication(ctx, time.Second, func(context.Context) error {
		<-release
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

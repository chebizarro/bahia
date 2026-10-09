package hiveci

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCanonicalRetryRestoreMatchesReservationAndPreservesNewerAttempt(t *testing.T) {
	ctx := context.Background()
	d := startCanonicalDaemon(t, t.TempDir(), nil, []string{hiveCITestPubkey(t)})
	run, result := signedRunAndResult(t, time.Unix(1_800_000_000, 0).UTC())
	ingest(t, d, &countingProcessor{}, run, result)
	previous, err := d.repo.GetResultByEventID(ctx, result.ID.Hex())
	require.NoError(t, err)
	require.NotNil(t, previous)
	firstAt := time.Unix(1_800_000_100, 123000000).UTC()
	first, err := d.repo.IncrementResultRetry(ctx, previous.ResultEventID, firstAt)
	require.NoError(t, err)
	require.Equal(t, 1, first)
	restored, err := d.repo.RestoreResultRetry(ctx, *previous, first, firstAt)
	require.NoError(t, err)
	require.True(t, restored)
	current, err := d.repo.GetResultByEventID(ctx, previous.ResultEventID)
	require.NoError(t, err)
	require.Zero(t, current.RetryCount)
	require.Nil(t, current.LastRetryAt)

	first, err = d.repo.IncrementResultRetry(ctx, previous.ResultEventID, firstAt)
	require.NoError(t, err)
	intermediate, err := d.repo.GetResultByEventID(ctx, previous.ResultEventID)
	require.NoError(t, err)
	secondAt := firstAt.Add(time.Second)
	second, err := d.repo.IncrementResultRetry(ctx, previous.ResultEventID, secondAt)
	require.NoError(t, err)
	restored, err = d.repo.RestoreResultRetry(ctx, *previous, first, firstAt)
	require.NoError(t, err)
	require.False(t, restored, "a later retry must not be overwritten")
	restored, err = d.repo.RestoreResultRetry(ctx, *intermediate, second, secondAt)
	require.NoError(t, err)
	require.True(t, restored)
	current, err = d.repo.GetResultByEventID(ctx, previous.ResultEventID)
	require.NoError(t, err)
	require.Equal(t, 1, current.RetryCount)
	require.Equal(t, firstAt, *current.LastRetryAt)
}

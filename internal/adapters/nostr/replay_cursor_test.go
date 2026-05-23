package nostr

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type replayCursorSource struct {
	timestamp time.Time
	err       error
}

func (s replayCursorSource) LatestEventTimestamp(context.Context, []int) (time.Time, error) {
	return s.timestamp, s.err
}

func TestReplayCursorPlannerNoSourcesReturnsNil(t *testing.T) {
	planner := NewReplayCursorPlanner(time.Second)

	since := planner.ComputeSince(context.Background(), []int{5101})

	require.Nil(t, since)
}

func TestReplayCursorPlannerSingleSourceReturnsTimestampMinusOverlap(t *testing.T) {
	planner := NewReplayCursorPlanner(time.Second, replayCursorSource{timestamp: time.Unix(100, 0).UTC()})

	since := planner.ComputeSince(context.Background(), []int{5101})

	require.NotNil(t, since)
	require.Equal(t, int64(99), int64(*since))
}

func TestReplayCursorPlannerMultipleSourcesPicksMostRecent(t *testing.T) {
	planner := NewReplayCursorPlanner(time.Second,
		replayCursorSource{timestamp: time.Unix(100, 0).UTC()},
		replayCursorSource{timestamp: time.Unix(120, 0).UTC()},
		replayCursorSource{timestamp: time.Unix(110, 0).UTC()},
	)

	since := planner.ComputeSince(context.Background(), []int{5101})

	require.NotNil(t, since)
	require.Equal(t, int64(119), int64(*since))
}

func TestReplayCursorPlannerOverlapSubtraction(t *testing.T) {
	planner := NewReplayCursorPlanner(5*time.Second, replayCursorSource{timestamp: time.Unix(100, 0).UTC()})

	since := planner.ComputeSince(context.Background(), []int{5101})

	require.NotNil(t, since)
	require.Equal(t, int64(95), int64(*since))
}

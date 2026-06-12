package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestBahiaStatusProjectorPublishesEachStatusEvent(t *testing.T) {
	ctx := context.Background()
	recorder := &bahiaStatusRecorder{}
	projector := NewBahiaStatusProjector(recorder, nil, "bahia-instance-1")

	require.NoError(t, projector.PublishIdentity(ctx, BahiaIdentityPayload{
		Version:        "v0.9.0",
		CatalogVersion: "2026-05-23.item4",
		Mode:           "full",
		StartedAt:      1779559200,
	}))
	require.NoError(t, projector.PublishCheckpoint(ctx, ReplayCheckpointPayload{
		CatalogVersion: "2026-05-23.item4",
		Cursors:        map[string]int64{"system_snapshot": 1779559300},
		Phase:          "snapshot",
	}))
	require.NoError(t, projector.PublishReadiness(ctx, ReadinessStatusPayload{
		Phase:         "ready",
		ActiveTier:    2,
		RequestedTier: 3,
		Ready:         false,
		Checks:        map[string]string{"relay_quorum": "ok"},
	}))

	require.Len(t, recorder.events, 3)
	require.Equal(t, gonostr.Kind(kindBahiaIdentityDefinition), recorder.events[0].Kind)
	require.Equal(t, "bahia-instance-1", bahiaStatusTagValue(recorder.events[0].Tags, "d"))
	identity := decodeBahiaStatusContent[BahiaIdentityPayload](t, recorder.events[0].Content)
	require.Equal(t, "full", identity.Mode)

	require.Equal(t, gonostr.Kind(kindBahiaReplayCheckpoint), recorder.events[1].Kind)
	checkpoint := decodeBahiaStatusContent[ReplayCheckpointPayload](t, recorder.events[1].Content)
	require.Equal(t, int64(1779559300), checkpoint.Cursors["system_snapshot"])

	require.Equal(t, gonostr.Kind(kindBahiaReadinessStatus), recorder.events[2].Kind)
	readiness := decodeBahiaStatusContent[ReadinessStatusPayload](t, recorder.events[2].Content)
	require.False(t, readiness.Ready)
	require.Equal(t, "ok", readiness.Checks["relay_quorum"])
}

func TestBahiaStatusProjectorDeduplicatesUnchangedPublish(t *testing.T) {
	ctx := context.Background()
	recorder := &bahiaStatusRecorder{}
	projector := NewBahiaStatusProjector(recorder, nil, "bahia-instance-1")
	payload := ReadinessStatusPayload{
		Phase:         "bootstrap",
		ActiveTier:    1,
		RequestedTier: 2,
		Ready:         false,
		Checks:        map[string]string{"live_catchup": "pending"},
	}

	require.NoError(t, projector.PublishReadiness(ctx, payload))
	require.NoError(t, projector.PublishReadiness(ctx, payload))
	require.Len(t, recorder.events, 1)

	payload.Checks["live_catchup"] = "ok"
	require.NoError(t, projector.PublishReadiness(ctx, payload))
	require.Len(t, recorder.events, 2)
}

func TestBahiaStatusProjectorDoesNotDeduplicateFailedPublish(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("relay rejected event")
	recorder := &bahiaStatusRecorder{err: failure}
	projector := NewBahiaStatusProjector(recorder, nil, "bahia-instance-1")
	payload := BahiaIdentityPayload{
		Version:        "v0.9.0",
		CatalogVersion: "2026-05-23.item4",
		Mode:           "emergency",
		StartedAt:      1779559200,
	}

	err := projector.PublishIdentity(ctx, payload)
	require.ErrorIs(t, err, failure)
	require.Len(t, recorder.events, 1)

	recorder.err = nil
	require.NoError(t, projector.PublishIdentity(ctx, payload))
	require.Len(t, recorder.events, 2)
}

type bahiaStatusRecorder struct {
	events []gonostr.Event
	err    error
}

func (r *bahiaStatusRecorder) PublishSignedEvent(_ context.Context, ev *gonostr.Event) error {
	if ev != nil {
		r.events = append(r.events, *ev)
	}
	return r.err
}

func decodeBahiaStatusContent[T any](t *testing.T, content string) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal([]byte(content), &out))
	return out
}

func bahiaStatusTagValue(tags gonostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

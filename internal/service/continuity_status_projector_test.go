package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
)

func TestContinuityStatusProjectorUpdatesStoreAndPublishesStatusAndActivation(t *testing.T) {
	ctx := context.Background()
	bus := newContinuityTestBus()
	store := NewInMemoryContinuityStatusStore()
	nostr := &continuityNostrRecorder{}
	projector := NewContinuityStatusProjector(bus, store, nostr.publish, nil)
	projector.clock = func() time.Time { return time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC) }

	bus.Publish(ctx, events.Event{Type: EventContinuityStatusChanged, Data: ContinuityStatus{
		ServiceKey:          "svc-api",
		ActiveProfile:       domain.ContinuityModeDegraded,
		OperationState:      ContinuityOperationFailoverInProgress,
		PrimaryWorkerPubKey: "primary-pubkey",
		ActiveWorkerPubKey:  "standby-pubkey",
		StandbyWorkerPubKey: "standby-pubkey",
		Reason:              "primary heartbeat expired",
		CurrentRunID:        "run-1",
		CurrentStepIndex:    1,
		CurrentStepCount:    3,
		CurrentStepAction:   "restore_backup",
	}})

	stored, ok := store.GetServiceStatus("svc-api")
	require.True(t, ok)
	require.Equal(t, domain.ContinuityModeDegraded, stored.ActiveProfile)
	require.Equal(t, "standby-pubkey", stored.ActiveWorkerPubKey)
	require.Equal(t, time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC), stored.ChangedAt)

	require.Len(t, nostr.events, 2)
	statusEvent := nostr.events[0]
	require.Equal(t, KindContinuityStatusReadModel, statusEvent.kind)
	require.Equal(t, "continuity-status:svc-api", nostrTagValue(statusEvent.tags, "d"))
	require.Equal(t, "svc-api", nostrTagValue(statusEvent.tags, "service"))
	require.Equal(t, "degraded", nostrTagValue(statusEvent.tags, "profile"))
	require.Equal(t, "standby-pubkey", nostrTagValue(statusEvent.tags, "active_worker"))
	require.Equal(t, "run-1", nostrTagValue(statusEvent.tags, "run"))

	content := decodeContinuityContent(t, statusEvent.content)
	require.Equal(t, "svc-api", content["service_key"])
	require.Equal(t, "degraded", content["active_profile"])
	require.Equal(t, "failover_in_progress", content["operation_state"])
	require.Equal(t, "primary heartbeat expired", content["reason"])
	require.Equal(t, "2026-05-23T13:00:00Z", content["changed_at"])

	activationEvent := nostr.events[1]
	require.Equal(t, KindContinuityDegradedModeActivation, activationEvent.kind)
	require.Empty(t, nostrTagValue(activationEvent.tags, "d"))
	require.Equal(t, "degraded-mode-activation", lastTagValue(activationEvent.tags, "t"))
}

func TestContinuityStatusProjectorStatusIdempotencyUsesConfiguredFields(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryContinuityStatusStore()
	nostr := &continuityNostrRecorder{}
	projector := NewContinuityStatusProjector(nil, store, nostr.publish, nil)

	base := ContinuityStatus{
		ServiceKey:          "svc-api",
		ActiveProfile:       domain.ContinuityModeFull,
		OperationState:      ContinuityOperationSteady,
		PrimaryWorkerPubKey: "primary",
		ActiveWorkerPubKey:  "primary",
		Reason:              "healthy",
		ChangedAt:           time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC),
	}
	require.NoError(t, projector.ProjectStatus(ctx, base))
	require.Len(t, nostr.events, 1)

	sameFingerprint := base
	sameFingerprint.Reason = "operator annotation changed"
	sameFingerprint.ChangedAt = sameFingerprint.ChangedAt.Add(time.Minute)
	require.NoError(t, projector.ProjectStatus(ctx, sameFingerprint))
	require.Len(t, nostr.events, 1)
	stored, ok := store.GetServiceStatus("svc-api")
	require.True(t, ok)
	require.Equal(t, "operator annotation changed", stored.Reason)

	stateChanged := sameFingerprint
	stateChanged.OperationState = ContinuityOperationFailoverInProgress
	stateChanged.CurrentRunID = "run-1"
	stateChanged.CurrentStepIndex = 1
	stateChanged.CurrentStepCount = 2
	stateChanged.CurrentStepAction = "move_service"
	require.NoError(t, projector.ProjectStatus(ctx, stateChanged))
	require.Len(t, nostr.events, 2)
	require.Equal(t, KindContinuityStatusReadModel, nostr.events[1].kind)

	profileChanged := stateChanged
	profileChanged.ActiveProfile = domain.ContinuityModeEmergency
	profileChanged.ActiveWorkerPubKey = "standby"
	require.NoError(t, projector.ProjectStatus(ctx, profileChanged))
	require.Len(t, nostr.events, 4)
	require.Equal(t, KindContinuityStatusReadModel, nostr.events[2].kind)
	require.Equal(t, KindContinuityDegradedModeActivation, nostr.events[3].kind)
	require.Equal(t, "full", nostrTagValue(nostr.events[3].tags, "previous_profile"))
}

func TestContinuityStatusProjectorPublishesRecoveryProgressByRun(t *testing.T) {
	ctx := context.Background()
	nostr := &continuityNostrRecorder{}
	projector := NewContinuityStatusProjector(nil, NewInMemoryContinuityStatusStore(), nostr.publish, nil)
	status := ContinuityStatus{
		ServiceKey:          "svc-api",
		ActiveProfile:       domain.ContinuityModeDegraded,
		OperationState:      ContinuityOperationRecoveryInProgress,
		PrimaryWorkerPubKey: "primary",
		ActiveWorkerPubKey:  "primary",
		ChangedAt:           time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC),
		CurrentRunID:        "recovery-run-1",
		CurrentStepIndex:    1,
		CurrentStepCount:    3,
		CurrentStepAction:   "restore_dns_routes",
	}

	require.NoError(t, projector.ProjectRecoveryProgress(ctx, status))
	require.Len(t, nostr.events, 3)
	require.Equal(t, KindContinuityStatusReadModel, nostr.events[0].kind)
	require.Equal(t, KindContinuityDegradedModeActivation, nostr.events[1].kind)
	require.Equal(t, KindContinuityRecoveryProgress, nostr.events[2].kind)
	require.Equal(t, "recovery-progress:svc-api:recovery-run-1", nostrTagValue(nostr.events[2].tags, "d"))
	require.Equal(t, "recovery-run-1", nostrTagValue(nostr.events[2].tags, "run"))
	require.Equal(t, "1", nostrTagValue(nostr.events[2].tags, "step"))
	require.Equal(t, "3", nostrTagValue(nostr.events[2].tags, "step_count"))
	require.Equal(t, "restore_dns_routes", nostrTagValue(nostr.events[2].tags, "action"))

	require.NoError(t, projector.ProjectRecoveryProgress(ctx, status))
	require.Len(t, nostr.events, 3)

	status.CurrentStepIndex = 2
	status.CurrentStepAction = "re_enable_agents"
	require.NoError(t, projector.ProjectRecoveryProgress(ctx, status))
	require.Len(t, nostr.events, 5)
	require.Equal(t, KindContinuityStatusReadModel, nostr.events[3].kind)
	require.Equal(t, KindContinuityRecoveryProgress, nostr.events[4].kind)
}

func TestContinuityStatusProjectorDoesNotMarkFailedStatusPublishAsPublished(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("relay rejected event")
	nostr := &continuityNostrRecorder{err: failure}
	projector := NewContinuityStatusProjector(nil, NewInMemoryContinuityStatusStore(), nostr.publish, nil)
	status := ContinuityStatus{
		ServiceKey:          "svc-api",
		ActiveProfile:       domain.ContinuityModeFull,
		OperationState:      ContinuityOperationSteady,
		PrimaryWorkerPubKey: "primary",
		ActiveWorkerPubKey:  "primary",
		ChangedAt:           time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC),
	}

	err := projector.ProjectStatus(ctx, status)
	require.ErrorIs(t, err, failure)
	require.Len(t, nostr.events, 1)

	nostr.err = nil
	require.NoError(t, projector.ProjectStatus(ctx, status))
	require.Len(t, nostr.events, 2)
	require.Equal(t, KindContinuityStatusReadModel, nostr.events[1].kind)
}

type continuityTestBus struct {
	handlers map[events.EventType][]events.Handler
}

func newContinuityTestBus() *continuityTestBus {
	return &continuityTestBus{handlers: map[events.EventType][]events.Handler{}}
}

func (b *continuityTestBus) Publish(ctx context.Context, e events.Event) {
	for _, h := range b.handlers[e.Type] {
		h(ctx, e)
	}
}

func (b *continuityTestBus) Subscribe(eventType events.EventType, handler events.Handler) {
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

type continuityPublishedEvent struct {
	kind    int
	tags    gonostr.Tags
	content string
}

type continuityNostrRecorder struct {
	events []continuityPublishedEvent
	err    error
}

func (r *continuityNostrRecorder) publish(_ context.Context, kind int, tags gonostr.Tags, content string) error {
	r.events = append(r.events, continuityPublishedEvent{kind: kind, tags: tags, content: content})
	return r.err
}

func nostrTagValue(tags gonostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

func lastTagValue(tags gonostr.Tags, name string) string {
	value := ""
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			value = tag[1]
		}
	}
	return value
}

func decodeContinuityContent(t *testing.T, content string) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(content), &out))
	return out
}

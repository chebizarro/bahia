package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type managedNostrRecorder struct {
	events []gonostr.Event
	err    error
}

func (r *managedNostrRecorder) PublishSignedEvent(_ context.Context, e *gonostr.Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, *e)
	return nil
}

func TestManagedInstanceHealthProjectorShapes(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := testKey()
	rec := &managedNostrRecorder{}
	p := NewManagedInstanceHealthProjector(nil, rec, zap.NewNop())
	payload := ManagedInstanceHealthChanged{EventID: "evt-1", Health: domain.ManagedInstanceHealth{ManagedInstanceKey: key, Host: "https://host-user:host-password@example.test", SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusUnhealthy, FailureReason: "password=secret", LastObservedAt: now}, PreviousStatus: domain.InstanceHealthStatusRunning, Severity: domain.AlertSeverityError, Alert: true, OccurredAt: now}
	require.NoError(t, p.handle(context.Background(), events.Event{Type: events.EventRuntimeInstanceHealthChanged, Data: payload}))
	require.Len(t, rec.events, 3)
	require.Equal(t, gonostr.Kind(kinds.NIP38Status), rec.events[0].Kind)
	require.Equal(t, gonostr.Kind(kinds.CASControlState), rec.events[1].Kind)
	require.Equal(t, gonostr.Kind(kinds.CASAudit), rec.events[2].Kind)
	require.Equal(t, managedInstanceDTag(key), managedTagValue(rec.events[0].Tags, "d"))
	require.Equal(t, managedHealthStatusSchema, managedTagValue(rec.events[0].Tags, "schema"))
	require.Empty(t, managedTagValue(rec.events[2].Tags, "d"))
	require.Contains(t, rec.events[1].Content, "[REDACTED]")
	require.NotContains(t, rec.events[1].Content, "host-password")
	var state map[string]any
	require.NoError(t, json.Unmarshal([]byte(rec.events[1].Content), &state))
	require.NotNil(t, state["health"])
	// Identical retry has a stable shape and is suppressed after verified publish.
	require.NoError(t, p.handle(context.Background(), events.Event{Type: events.EventRuntimeInstanceHealthChanged, Data: payload}))
	require.Len(t, rec.events, 3)
}

func TestManagedInstanceProjectorRecoveryAndMaintenanceFacts(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := testKey()
	rec := &managedNostrRecorder{}
	p := NewManagedInstanceHealthProjector(nil, rec, zap.NewNop())
	h := domain.ManagedInstanceHealth{ManagedInstanceKey: key, SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: now}
	attempt := domain.RecoveryAttempt{ManagedInstanceKey: key, CorrelationID: "corr", RequestedAt: now, Result: domain.RecoveryAttemptFailed, Evidence: "token=secret"}
	require.NoError(t, p.handle(context.Background(), events.Event{Type: events.EventRuntimeRecoveryFailed, Data: ManagedInstanceRecoveryEvent{EventID: "recovery", Health: h, Attempt: attempt, OccurredAt: now}}))
	require.Len(t, rec.events, 2)
	require.Equal(t, "corr", managedTagValue(rec.events[1].Tags, "correlation"))
	require.Contains(t, rec.events[1].Content, "[REDACTED]")
	o := domain.MaintenanceOverride{ManagedInstanceKey: key, Actor: "https://actor:password@example.test", Reason: "eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZA.c2lnbmF0dXJl", CreatedAt: now}
	require.NoError(t, p.handle(context.Background(), events.Event{Type: events.EventRuntimeMaintenanceChanged, Data: ManagedInstanceMaintenanceEvent{EventID: "override", Health: &h, Override: o, Active: true, OccurredAt: now}}))
	require.Len(t, rec.events, 5)
	require.Equal(t, gonostr.Kind(kinds.CASAudit), rec.events[4].Kind)
	require.NotContains(t, rec.events[4].Content, "actor:password")
	require.NotContains(t, rec.events[4].Content, "eyJhbGci")
}

func TestManagedInstanceDTagIncludesRuntimeTarget(t *testing.T) {
	key := testKey()
	other := key
	other.RuntimeTargetName = "worker"
	require.NotEqual(t, managedInstanceDTag(key), managedInstanceDTag(other))
}

func managedTagValue(tags gonostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) > 1 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

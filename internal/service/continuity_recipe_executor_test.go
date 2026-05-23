package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
)

func TestContinuityRecipeExecutorExecutesFailoverSequentiallyAndEmitsProgress(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	var order []string
	executor := NewContinuityRecipeExecutor(
		publisher,
		WithContinuityRecipeActionHandler(domain.RecipeActionDeployService, func(_ context.Context, step domain.RecipeStep, _ ContinuityRecipeRunContext) error {
			order = append(order, step.Action)
			return nil
		}),
		WithContinuityRecipeActionHandler(domain.RecipeActionPublishEndpoint, func(_ context.Context, step domain.RecipeStep, _ ContinuityRecipeRunContext) error {
			order = append(order, step.Action)
			return nil
		}),
	)

	err := executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-success", []domain.RecipeStep{
		{Name: "deploy degraded", Action: domain.RecipeActionDeployService},
		{Name: "publish endpoint", Action: domain.RecipeActionPublishEndpoint},
	}))

	require.NoError(t, err)
	require.Equal(t, []string{domain.RecipeActionDeployService, domain.RecipeActionPublishEndpoint}, order)
	require.Equal(t, []events.EventType{
		EventContinuityRecipeRunStarted,
		EventContinuityRecipeStepStarted,
		EventContinuityRecipeStepProgress,
		EventContinuityRecipeStepStarted,
		EventContinuityRecipeStepProgress,
		EventContinuityRecipeRunCompleted,
	}, publisher.eventTypes())
	progress := publisher.eventsOfType(EventContinuityRecipeStepProgress)
	require.Len(t, progress, 2)
	require.Equal(t, 1, progress[0].Data.(ContinuityRecipeProgressEvent).StepIndex)
	require.Equal(t, ContinuityRecipeStepStatusCompleted, progress[0].Data.(ContinuityRecipeProgressEvent).Status)
	require.Equal(t, 2, progress[1].Data.(ContinuityRecipeProgressEvent).StepIndex)
}

func TestContinuityRecipeExecutorValidatesWholeRecipeBeforeExecutingFirstStep(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	var called atomic.Int32
	executor := NewContinuityRecipeExecutor(
		publisher,
		WithContinuityRecipeActionHandler(domain.RecipeActionDeployService, func(context.Context, domain.RecipeStep, ContinuityRecipeRunContext) error {
			called.Add(1)
			return nil
		}),
	)

	err := executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-invalid", []domain.RecipeStep{
		{Name: "deploy degraded", Action: domain.RecipeActionDeployService},
		{Name: "emit completion", Action: domain.RecipeActionEmitEvent},
	}))

	require.Error(t, err)
	require.Contains(t, err.Error(), "emit_event")
	require.Zero(t, called.Load())
	require.Empty(t, publisher.snapshot())
}

func TestContinuityRecipeExecutorStopsOnStepFailureAndDoesNotStartLaterSteps(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	expectedErr := errors.New("publish endpoint rejected")
	var order []string
	executor := NewContinuityRecipeExecutor(
		publisher,
		WithContinuityRecipeActionHandler(domain.RecipeActionDeployService, func(_ context.Context, step domain.RecipeStep, _ ContinuityRecipeRunContext) error {
			order = append(order, step.Action)
			return nil
		}),
		WithContinuityRecipeActionHandler(domain.RecipeActionPublishEndpoint, func(_ context.Context, step domain.RecipeStep, _ ContinuityRecipeRunContext) error {
			order = append(order, step.Action)
			return expectedErr
		}),
		WithContinuityRecipeActionHandler(domain.RecipeActionStopService, func(_ context.Context, step domain.RecipeStep, _ ContinuityRecipeRunContext) error {
			order = append(order, step.Action)
			return nil
		}),
	)

	err := executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-failed", []domain.RecipeStep{
		{Name: "deploy degraded", Action: domain.RecipeActionDeployService},
		{Name: "publish endpoint", Action: domain.RecipeActionPublishEndpoint},
		{Name: "stop primary", Action: domain.RecipeActionStopService},
	}))

	require.ErrorIs(t, err, expectedErr)
	require.Equal(t, []string{domain.RecipeActionDeployService, domain.RecipeActionPublishEndpoint}, order)
	require.Equal(t, []events.EventType{
		EventContinuityRecipeRunStarted,
		EventContinuityRecipeStepStarted,
		EventContinuityRecipeStepProgress,
		EventContinuityRecipeStepStarted,
		EventContinuityRecipeStepFailed,
		EventContinuityRecipeRunFailed,
	}, publisher.eventTypes())
	failed := publisher.eventsOfType(EventContinuityRecipeRunFailed)
	require.Len(t, failed, 1)
	payload := failed[0].Data.(ContinuityRecipeProgressEvent)
	require.Equal(t, 2, payload.StepIndex)
	require.Contains(t, payload.Error, expectedErr.Error())
}

func TestContinuityRecipeExecutorContextCancellationStopsCurrentStepAndNoLaterSteps(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	var laterCalled atomic.Bool
	executor := NewContinuityRecipeExecutor(
		publisher,
		WithContinuityRecipeActionHandler(domain.RecipeActionDeployService, func(ctx context.Context, _ domain.RecipeStep, _ ContinuityRecipeRunContext) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}),
		WithContinuityRecipeActionHandler(domain.RecipeActionPublishEndpoint, func(context.Context, domain.RecipeStep, ContinuityRecipeRunContext) error {
			laterCalled.Store(true)
			return nil
		}),
	)

	err := executor.ExecuteFailover(ctx, failoverRequestWithRecipe("run-cancelled", []domain.RecipeStep{
		{Name: "deploy degraded", Action: domain.RecipeActionDeployService},
		{Name: "publish endpoint", Action: domain.RecipeActionPublishEndpoint},
	}))

	require.ErrorIs(t, err, context.Canceled)
	require.False(t, laterCalled.Load())
	require.Equal(t, []events.EventType{
		EventContinuityRecipeRunStarted,
		EventContinuityRecipeStepStarted,
		EventContinuityRecipeStepFailed,
		EventContinuityRecipeRunFailed,
	}, publisher.eventTypes())
}

func TestContinuityRecipeExecutorSerializesRunsByServiceKey(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	entered := make(chan string, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	executor := NewContinuityRecipeExecutor(
		publisher,
		WithContinuityRecipeActionHandler(domain.RecipeActionDeployService, func(_ context.Context, _ domain.RecipeStep, run ContinuityRecipeRunContext) error {
			current := active.Add(1)
			for {
				observed := maxActive.Load()
				if current <= observed || maxActive.CompareAndSwap(observed, current) {
					break
				}
			}
			entered <- run.RunID
			<-release
			active.Add(-1)
			return nil
		}),
	)

	errCh := make(chan error, 2)
	go func() {
		errCh <- executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-1", []domain.RecipeStep{{Name: "deploy", Action: domain.RecipeActionDeployService}}))
	}()
	require.Equal(t, "run-1", receiveString(t, entered))

	go func() {
		errCh <- executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-2", []domain.RecipeStep{{Name: "deploy", Action: domain.RecipeActionDeployService}}))
	}()

	release <- struct{}{}
	require.NoError(t, receiveErr(t, errCh))
	require.Equal(t, "run-2", receiveString(t, entered))
	release <- struct{}{}
	require.NoError(t, receiveErr(t, errCh))
	require.Equal(t, int32(1), maxActive.Load())
}

func TestContinuityRecipeExecutorDefaultActionsCallAdaptersAndPublishEventOnlyActions(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	adapters := &continuityRecipeMockAdapters{}
	executor := NewContinuityRecipeExecutor(
		publisher,
		WithContinuityWakeAdapter(adapters),
		WithContinuityHeartbeatWaiter(adapters),
		WithContinuityRuntimeAdapter(adapters),
		WithContinuityStorageAdapter(adapters),
		WithContinuityDNSAdapter(adapters),
	)

	err := executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-default-actions", []domain.RecipeStep{
		{Name: "wake standby", Action: domain.RecipeActionWakeOnLAN, Params: map[string]string{"mac_address": "00:11:22:33:44:55"}},
		{Name: "wait standby", Action: domain.RecipeActionWaitHeartbeat, Timeout: time.Minute, Params: map[string]string{"worker": "worker-standby"}},
		{Name: "mount data", Action: domain.RecipeActionMountVolume, Params: map[string]string{"source": "vol-1"}},
		{Name: "restore backup", Action: domain.RecipeActionRestoreBackup, Params: map[string]string{"snapshot_id": "snap-1"}},
		{Name: "restore scb", Action: domain.RecipeActionRestoreSCB, Params: map[string]string{"scb_path": "/secure/scb"}},
		{Name: "deploy", Action: domain.RecipeActionDeployService, Params: map[string]string{"target": "worker-standby"}},
		{Name: "publish endpoint", Action: domain.RecipeActionPublishEndpoint},
		{Name: "emit", Action: domain.RecipeActionEmitEvent, Params: map[string]string{"event_type": "continuity.custom"}},
		{Name: "sync relay", Action: domain.RecipeActionSyncRelayState},
		{Name: "stop primary", Action: domain.RecipeActionStopService, Params: map[string]string{"worker": "worker-primary"}},
		{Name: "restore dns", Action: domain.RecipeActionRestoreDNSRoutes},
		{Name: "move", Action: domain.RecipeActionMoveService, Params: map[string]string{"from_worker": "worker-standby", "to_worker": "worker-primary"}},
		{Name: "agents", Action: domain.RecipeActionReEnableAgents},
	}))

	require.NoError(t, err)
	require.Equal(t, []string{
		"wake:00:11:22:33:44:55",
		"heartbeat:worker-standby:1m0s",
		"mount:vol-1",
		"backup:snap-1",
		"scb:/secure/scb",
		"deploy:svc-api:worker-standby",
		"stop:svc-api:worker-primary",
		"dns:svc-api",
		"move:svc-api:worker-standby:worker-primary",
	}, adapters.snapshot())
	require.Contains(t, publisher.eventTypes(), events.EventType("continuity.recipe.action.publish_endpoint"))
	require.Contains(t, publisher.eventTypes(), events.EventType("continuity.custom"))
	require.Contains(t, publisher.eventTypes(), events.EventType("continuity.recipe.action.sync_relay_state"))
	require.Contains(t, publisher.eventTypes(), events.EventType("continuity.recipe.action.re_enable_agents"))
	require.Equal(t, EventContinuityRecipeRunCompleted, publisher.eventTypes()[len(publisher.eventTypes())-1])
}

func TestContinuityRecipeExecutorDefaultActionAdapterFailuresStopRun(t *testing.T) {
	cases := []struct {
		name   string
		step   domain.RecipeStep
		failed string
	}{
		{name: domain.RecipeActionWakeOnLAN, step: domain.RecipeStep{Name: "wake", Action: domain.RecipeActionWakeOnLAN, Params: map[string]string{"mac_address": "00:11:22:33:44:55"}}, failed: "wake"},
		{name: domain.RecipeActionWaitHeartbeat, step: domain.RecipeStep{Name: "heartbeat", Action: domain.RecipeActionWaitHeartbeat, Params: map[string]string{"worker": "worker-standby"}}, failed: "heartbeat"},
		{name: domain.RecipeActionMountVolume, step: domain.RecipeStep{Name: "mount", Action: domain.RecipeActionMountVolume, Params: map[string]string{"source": "vol-1"}}, failed: "mount"},
		{name: domain.RecipeActionRestoreBackup, step: domain.RecipeStep{Name: "backup", Action: domain.RecipeActionRestoreBackup, Params: map[string]string{"snapshot_id": "snap-1"}}, failed: "backup"},
		{name: domain.RecipeActionRestoreSCB, step: domain.RecipeStep{Name: "scb", Action: domain.RecipeActionRestoreSCB, Params: map[string]string{"scb_path": "/secure/scb"}}, failed: "scb"},
		{name: domain.RecipeActionDeployService, step: domain.RecipeStep{Name: "deploy", Action: domain.RecipeActionDeployService}, failed: "deploy"},
		{name: domain.RecipeActionStopService, step: domain.RecipeStep{Name: "stop", Action: domain.RecipeActionStopService}, failed: "stop"},
		{name: domain.RecipeActionRestoreDNSRoutes, step: domain.RecipeStep{Name: "dns", Action: domain.RecipeActionRestoreDNSRoutes}, failed: "dns"},
		{name: domain.RecipeActionMoveService, step: domain.RecipeStep{Name: "move", Action: domain.RecipeActionMoveService}, failed: "move"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &continuityRecipeCapturePublisher{}
			expectedErr := errors.New(tc.failed + " failed")
			adapters := &continuityRecipeMockAdapters{errByOperation: map[string]error{tc.failed: expectedErr}}
			executor := NewContinuityRecipeExecutor(
				publisher,
				WithContinuityWakeAdapter(adapters),
				WithContinuityHeartbeatWaiter(adapters),
				WithContinuityRuntimeAdapter(adapters),
				WithContinuityStorageAdapter(adapters),
				WithContinuityDNSAdapter(adapters),
			)

			err := executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-failing-"+tc.name, []domain.RecipeStep{tc.step}))

			require.ErrorIs(t, err, expectedErr)
			require.Equal(t, []events.EventType{
				EventContinuityRecipeRunStarted,
				EventContinuityRecipeStepStarted,
				EventContinuityRecipeStepFailed,
				EventContinuityRecipeRunFailed,
			}, publisher.eventTypes())
		})
	}
}

func TestContinuityRecipeExecutorNilAdaptersAreGracefulNoops(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	executor := NewContinuityRecipeExecutor(publisher)

	err := executor.ExecuteFailover(context.Background(), failoverRequestWithRecipe("run-nil-adapters", []domain.RecipeStep{
		{Name: "wake", Action: domain.RecipeActionWakeOnLAN, Params: map[string]string{"mac_address": "00:11:22:33:44:55"}},
		{Name: "heartbeat", Action: domain.RecipeActionWaitHeartbeat, Params: map[string]string{"worker": "worker-standby"}},
		{Name: "mount", Action: domain.RecipeActionMountVolume, Params: map[string]string{"source": "vol-1"}},
		{Name: "backup", Action: domain.RecipeActionRestoreBackup, Params: map[string]string{"snapshot_id": "snap-1"}},
		{Name: "scb", Action: domain.RecipeActionRestoreSCB, Params: map[string]string{"scb_path": "/secure/scb"}},
		{Name: "deploy", Action: domain.RecipeActionDeployService},
		{Name: "stop", Action: domain.RecipeActionStopService},
		{Name: "dns", Action: domain.RecipeActionRestoreDNSRoutes},
		{Name: "move", Action: domain.RecipeActionMoveService},
	}))

	require.NoError(t, err)
	require.Equal(t, EventContinuityRecipeRunCompleted, publisher.eventTypes()[len(publisher.eventTypes())-1])
}

func TestContinuityRecipeExecutorCancelledContextDoesNotCallDefaultAdapter(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	adapters := &continuityRecipeMockAdapters{}
	executor := NewContinuityRecipeExecutor(publisher, WithContinuityWakeAdapter(adapters))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := executor.ExecuteFailover(ctx, failoverRequestWithRecipe("run-cancel-before-adapter", []domain.RecipeStep{
		{Name: "wake", Action: domain.RecipeActionWakeOnLAN, Params: map[string]string{"mac_address": "00:11:22:33:44:55"}},
	}))

	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, adapters.snapshot())
	require.Empty(t, publisher.snapshot())
}

func TestContinuityRecipeExecutorExecutesRecoveryRecipe(t *testing.T) {
	publisher := &continuityRecipeCapturePublisher{}
	executor := NewContinuityRecipeExecutor(publisher)
	recipe := domain.ContinuityRecipe{
		Name:       "recover primary",
		ServiceKey: "svc-api",
		Kind:       domain.ContinuityRecipeKindRecovery,
		Steps: []domain.RecipeStep{
			{Name: "sync relay", Action: domain.RecipeActionSyncRelayState},
			{Name: "restore dns", Action: domain.RecipeActionRestoreDNSRoutes},
			{Name: "move service", Action: domain.RecipeActionMoveService},
			{Name: "re-enable agents", Action: domain.RecipeActionReEnableAgents},
		},
	}

	err := executor.ExecuteRecovery(context.Background(), RecoveryExecutionRequest{
		ServiceKey:            "svc-api",
		RecipeName:            "recover primary",
		TargetProfile:         domain.ContinuityModeFull,
		PrimaryWorkerPubKey:   "worker-primary",
		SelectedStandbyPubKey: "worker-standby",
		RequestedBy:           "operator",
		RunID:                 "recovery-run",
		Recipe:                recipe,
	})

	require.NoError(t, err)
	require.Equal(t, EventContinuityRecipeRunCompleted, publisher.eventTypes()[len(publisher.eventTypes())-1])
}

func failoverRequestWithRecipe(runID string, steps []domain.RecipeStep) FailoverExecutionRequest {
	return FailoverExecutionRequest{
		ServiceKey:            "svc-api",
		RecipeName:            "failover primary",
		TargetProfile:         domain.ContinuityModeDegraded,
		PrimaryWorkerPubKey:   "worker-primary",
		SelectedStandbyPubKey: "worker-standby",
		RequestedBy:           "operator",
		RunID:                 runID,
		Recipe: domain.ContinuityRecipe{
			Name:       "failover primary",
			ServiceKey: "svc-api",
			Kind:       domain.ContinuityRecipeKindFailover,
			Trigger: &domain.RecipeTrigger{
				Type:    domain.RecipeTriggerTypeHeartbeatLoss,
				Target:  "worker-primary",
				Timeout: time.Minute,
			},
			Steps: steps,
		},
	}
}

type continuityRecipeMockAdapters struct {
	mu             sync.Mutex
	calls          []string
	errByOperation map[string]error
}

func (m *continuityRecipeMockAdapters) WakeOnLAN(_ context.Context, macAddress string) error {
	m.record("wake:" + macAddress)
	return m.errByOperation["wake"]
}

func (m *continuityRecipeMockAdapters) WaitForHeartbeat(_ context.Context, workerPubKey string, timeout time.Duration) error {
	m.record("heartbeat:" + workerPubKey + ":" + timeout.String())
	return m.errByOperation["heartbeat"]
}

func (m *continuityRecipeMockAdapters) DeployService(_ context.Context, serviceKey string, workerPubKey string, _ map[string]string) error {
	m.record("deploy:" + serviceKey + ":" + workerPubKey)
	return m.errByOperation["deploy"]
}

func (m *continuityRecipeMockAdapters) StopService(_ context.Context, serviceKey string, workerPubKey string, _ map[string]string) error {
	m.record("stop:" + serviceKey + ":" + workerPubKey)
	return m.errByOperation["stop"]
}

func (m *continuityRecipeMockAdapters) MoveService(_ context.Context, serviceKey string, fromWorker string, toWorker string, _ map[string]string) error {
	m.record("move:" + serviceKey + ":" + fromWorker + ":" + toWorker)
	return m.errByOperation["move"]
}

func (m *continuityRecipeMockAdapters) MountVolume(_ context.Context, source string, _ map[string]string) error {
	m.record("mount:" + source)
	return m.errByOperation["mount"]
}

func (m *continuityRecipeMockAdapters) RestoreBackup(_ context.Context, snapshotID string, _ map[string]string) error {
	m.record("backup:" + snapshotID)
	return m.errByOperation["backup"]
}

func (m *continuityRecipeMockAdapters) RestoreSCB(_ context.Context, source string, _ map[string]string) error {
	m.record("scb:" + source)
	return m.errByOperation["scb"]
}

func (m *continuityRecipeMockAdapters) RestoreDNSRoutes(_ context.Context, serviceKey string, _ map[string]string) error {
	m.record("dns:" + serviceKey)
	return m.errByOperation["dns"]
}

func (m *continuityRecipeMockAdapters) record(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
}

func (m *continuityRecipeMockAdapters) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	copy(out, m.calls)
	return out
}

type continuityRecipeCapturePublisher struct {
	mu     sync.Mutex
	events []events.Event
}

func (p *continuityRecipeCapturePublisher) Publish(_ context.Context, event events.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func (p *continuityRecipeCapturePublisher) Subscribe(events.EventType, events.Handler) {}

func (p *continuityRecipeCapturePublisher) snapshot() []events.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]events.Event, len(p.events))
	copy(out, p.events)
	return out
}

func (p *continuityRecipeCapturePublisher) eventTypes() []events.EventType {
	snapshot := p.snapshot()
	types := make([]events.EventType, len(snapshot))
	for i, event := range snapshot {
		types[i] = event.Type
	}
	return types
}

func (p *continuityRecipeCapturePublisher) eventsOfType(eventType events.EventType) []events.Event {
	snapshot := p.snapshot()
	var out []events.Event
	for _, event := range snapshot {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

func receiveString(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for run to enter handler")
		return ""
	}
}

func receiveErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		return fmt.Errorf("timed out waiting for executor result")
	}
}

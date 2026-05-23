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

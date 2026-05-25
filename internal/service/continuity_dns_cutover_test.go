package service

import (
	"context"
	"errors"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
)

func TestContinuityDNSCutoverFailoverCompletionTriggersDNSReconciliation(t *testing.T) {
	ctx := context.Background()
	bus := newContinuityTestBus()
	reconciler := &recordingDNSReconciler{}
	NewContinuityDNSCutoverService(bus, reconciler, nil)

	bus.Publish(ctx, events.Event{Type: EventContinuityRecipeRunCompleted, EntityID: "run-failover", Data: ContinuityRecipeProgressEvent{
		ContinuityRecipeRunContext: ContinuityRecipeRunContext{
			ServiceKey:  "svc-api",
			RecipeKind:  domain.ContinuityRecipeKindFailover,
			RunID:       "run-failover",
			RecipeName:  "primary-failover",
			RequestedBy: "operator",
		},
		Status: ContinuityRecipeRunStatusCompleted,
	}})

	require.Equal(t, 1, reconciler.calls)
}

func TestContinuityDNSCutoverRecoveryCompletionTriggersDNSReconciliation(t *testing.T) {
	ctx := context.Background()
	bus := newContinuityTestBus()
	reconciler := &recordingDNSReconciler{}
	NewContinuityDNSCutoverService(bus, reconciler, nil)

	bus.Publish(ctx, events.Event{Type: EventContinuityRecipeRunCompleted, EntityID: "run-recovery", Data: ContinuityRecipeProgressEvent{
		ContinuityRecipeRunContext: ContinuityRecipeRunContext{
			ServiceKey:  "svc-api",
			RecipeKind:  domain.ContinuityRecipeKindRecovery,
			RunID:       "run-recovery",
			RecipeName:  "primary-recovery",
			RequestedBy: "operator",
		},
		Status: ContinuityRecipeRunStatusCompleted,
	}})

	require.Equal(t, 1, reconciler.calls)
}

func TestContinuityDNSCutoverStatusCompletionTransitionsTriggerDNSReconciliation(t *testing.T) {
	ctx := context.Background()
	bus := newContinuityTestBus()
	reconciler := &recordingDNSReconciler{}
	NewContinuityDNSCutoverService(bus, reconciler, nil)

	bus.Publish(ctx, events.Event{Type: EventContinuityStatusChanged, Data: ContinuityStatus{
		ServiceKey:     "svc-api",
		ActiveProfile:  domain.ContinuityModeDegraded,
		OperationState: ContinuityOperationFailoverInProgress,
	}})
	bus.Publish(ctx, events.Event{Type: EventContinuityStatusChanged, Data: ContinuityStatus{
		ServiceKey:     "svc-api",
		ActiveProfile:  domain.ContinuityModeDegraded,
		OperationState: ContinuityOperationSteady,
	}})
	bus.Publish(ctx, events.Event{Type: EventContinuityStatusChanged, Data: ContinuityStatus{
		ServiceKey:     "svc-api",
		ActiveProfile:  domain.ContinuityModeDegraded,
		OperationState: ContinuityOperationRecoveryInProgress,
	}})
	bus.Publish(ctx, events.Event{Type: EventContinuityStatusChanged, Data: ContinuityStatus{
		ServiceKey:     "svc-api",
		ActiveProfile:  domain.ContinuityModeFull,
		OperationState: ContinuityOperationSteady,
	}})

	require.Equal(t, 2, reconciler.calls)
}

func TestContinuityDNSCutoverPublishEndpointActionTriggersDNSReconciliation(t *testing.T) {
	ctx := context.Background()
	bus := newContinuityTestBus()
	reconciler := &recordingDNSReconciler{}
	NewContinuityDNSCutoverService(bus, reconciler, nil)

	bus.Publish(ctx, events.Event{Type: events.EventType("continuity.recipe.action." + domain.RecipeActionPublishEndpoint), EntityID: "run-publish", Data: ContinuityRecipeProgressEvent{
		ContinuityRecipeRunContext: ContinuityRecipeRunContext{
			ServiceKey:  "svc-api",
			RecipeKind:  domain.ContinuityRecipeKindFailover,
			RunID:       "run-publish",
			RecipeName:  "primary-failover",
			RequestedBy: "operator",
		},
		Status: ContinuityRecipeStepStatusCompleted,
		Action: domain.RecipeActionPublishEndpoint,
	}})

	require.Equal(t, 1, reconciler.calls)
}

func TestContinuityDNSCutoverRestoreDNSRoutesTriggersDNSReconciliation(t *testing.T) {
	reconciler := &recordingDNSReconciler{}
	cutover := NewContinuityDNSCutoverService(nil, reconciler, nil)

	require.NoError(t, cutover.RestoreDNSRoutes(context.Background(), "svc-api", nil))
	require.Equal(t, 1, reconciler.calls)
}

func TestContinuityDNSCutoverPropagatesRestoreDNSRoutesReconcileError(t *testing.T) {
	reconciler := &recordingDNSReconciler{err: errors.New("backend unavailable")}
	cutover := NewContinuityDNSCutoverService(nil, reconciler, nil)

	err := cutover.RestoreDNSRoutes(context.Background(), "svc-api", nil)
	require.ErrorContains(t, err, "restore_dns_routes DNS reconcile")
}

type recordingDNSReconciler struct {
	calls int
	err   error
}

func (r *recordingDNSReconciler) ReconcileAll(context.Context) error {
	r.calls++
	return r.err
}

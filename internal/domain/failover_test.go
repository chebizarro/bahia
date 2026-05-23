package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateContinuityRecipeAcceptsFailoverRecipeWithTrigger(t *testing.T) {
	recipe := &ContinuityRecipe{
		Name:       " primary heartbeat failover ",
		ServiceKey: " svc-api ",
		Kind:       ContinuityRecipeKindFailover,
		Trigger: &RecipeTrigger{
			Type:    RecipeTriggerTypeHeartbeatLoss,
			Target:  "worker-primary",
			Timeout: 30 * time.Second,
		},
		Steps: []RecipeStep{
			{Name: " wake standby ", Action: " wake_on_lan ", Timeout: 10 * time.Second, Params: map[string]string{"worker": "worker-standby"}},
			{Name: "deploy", Action: RecipeActionDeployService, Timeout: time.Minute},
			{Name: "publish", Action: RecipeActionPublishEndpoint},
		},
		SourceEventID: " event-1 ",
	}

	require.NoError(t, recipe.Validate())
	require.Equal(t, "primary heartbeat failover", recipe.Name)
	require.Equal(t, "svc-api", recipe.ServiceKey)
	require.Equal(t, "event-1", recipe.SourceEventID)
	require.Equal(t, "wake standby", recipe.Steps[0].Name)
	require.Equal(t, RecipeActionWakeOnLAN, recipe.Steps[0].Action)
}

func TestValidateContinuityRecipeAcceptsRecoveryRecipeWithoutTrigger(t *testing.T) {
	recipe := &ContinuityRecipe{
		Name:       "recover primary",
		ServiceKey: "svc-api",
		Kind:       ContinuityRecipeKindRecovery,
		Steps: []RecipeStep{
			{Name: "stop standby", Action: RecipeActionStopService},
			{Name: "restore routes", Action: RecipeActionRestoreDNSRoutes},
			{Name: "re-enable agents", Action: RecipeActionReEnableAgents},
		},
	}

	require.NoError(t, ValidateContinuityRecipe(recipe))
}

func TestValidateContinuityRecipeRequiresNameAndServiceKey(t *testing.T) {
	recipe := &ContinuityRecipe{
		Kind:    ContinuityRecipeKindRecovery,
		Steps:   []RecipeStep{{Action: RecipeActionEmitEvent}},
		Trigger: nil,
	}

	err := ValidateContinuityRecipe(recipe)

	require.ErrorIs(t, err, ErrEmptyField)
	require.Contains(t, err.Error(), "name")

	recipe.Name = "manual recovery"
	err = ValidateContinuityRecipe(recipe)

	require.ErrorIs(t, err, ErrEmptyField)
	require.Contains(t, err.Error(), "service_key")
}

func TestValidateContinuityRecipeRequiresFailoverTrigger(t *testing.T) {
	recipe := &ContinuityRecipe{
		Name:       "failover",
		ServiceKey: "svc-api",
		Kind:       ContinuityRecipeKindFailover,
		Steps:      []RecipeStep{{Action: RecipeActionMoveService}},
	}

	err := ValidateContinuityRecipe(recipe)

	require.ErrorIs(t, err, ErrEmptyField)
	require.Contains(t, err.Error(), "trigger")
}

func TestValidateContinuityRecipeRequiresSteps(t *testing.T) {
	recipe := &ContinuityRecipe{
		Name:       "recovery",
		ServiceKey: "svc-api",
		Kind:       ContinuityRecipeKindRecovery,
	}

	err := ValidateContinuityRecipe(recipe)

	require.ErrorIs(t, err, ErrEmptyField)
	require.Contains(t, err.Error(), "steps")
}

func TestValidateContinuityRecipeRejectsInvalidStepAction(t *testing.T) {
	recipe := &ContinuityRecipe{
		Name:       "recovery",
		ServiceKey: "svc-api",
		Kind:       ContinuityRecipeKindRecovery,
		Steps:      []RecipeStep{{Action: "poll_until_ready"}},
	}

	err := ValidateContinuityRecipe(recipe)

	require.ErrorIs(t, err, ErrInvalidValue)
	require.Contains(t, err.Error(), "poll_until_ready")
}

func TestContinuityRecipeKindAndActionsValidateSupportedValues(t *testing.T) {
	require.True(t, ContinuityRecipeKindFailover.IsValid())
	require.True(t, ContinuityRecipeKindRecovery.IsValid())
	require.False(t, ContinuityRecipeKind("restart").IsValid())

	supportedActions := []string{
		RecipeActionWakeOnLAN,
		RecipeActionWaitHeartbeat,
		RecipeActionMountVolume,
		RecipeActionRestoreBackup,
		RecipeActionRestoreSCB,
		RecipeActionDeployService,
		RecipeActionPublishEndpoint,
		RecipeActionEmitEvent,
		RecipeActionSyncRelayState,
		RecipeActionStopService,
		RecipeActionRestoreDNSRoutes,
		RecipeActionMoveService,
		RecipeActionReEnableAgents,
	}
	for _, action := range supportedActions {
		require.True(t, IsValidRecipeAction(action), action)
	}
	require.False(t, IsValidRecipeAction("poll"))
}

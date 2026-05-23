package domain

import (
	"fmt"
	"strings"
	"time"
)

// ContinuityRecipeKind identifies whether a recipe executes failover or recovery behavior.
type ContinuityRecipeKind string

const (
	ContinuityRecipeKindFailover ContinuityRecipeKind = "failover"
	ContinuityRecipeKindRecovery ContinuityRecipeKind = "recovery"
)

const (
	RecipeTriggerTypeHeartbeatLoss = "heartbeat_loss"
	RecipeTriggerTypeManual        = "manual"
)

const (
	RecipeActionWakeOnLAN        = "wake_on_lan"
	RecipeActionWaitHeartbeat    = "wait_for_heartbeat"
	RecipeActionMountVolume      = "mount_volume"
	RecipeActionRestoreBackup    = "restore_backup"
	RecipeActionRestoreSCB       = "restore_scb"
	RecipeActionDeployService    = "deploy_service"
	RecipeActionPublishEndpoint  = "publish_endpoint"
	RecipeActionEmitEvent        = "emit_event"
	RecipeActionSyncRelayState   = "sync_relay_state"
	RecipeActionStopService      = "stop_service"
	RecipeActionRestoreDNSRoutes = "restore_dns_routes"
	RecipeActionMoveService      = "move_service"
	RecipeActionReEnableAgents   = "re_enable_agents"
)

// ContinuityRecipe is an authoritative continuity workflow for failover or recovery.
type ContinuityRecipe struct {
	Name          string               `json:"name"`
	ServiceKey    string               `json:"service_key"`
	Kind          ContinuityRecipeKind `json:"kind"`
	Trigger       *RecipeTrigger       `json:"trigger,omitempty"`
	Steps         []RecipeStep         `json:"steps"`
	UpdatedAt     time.Time            `json:"updated_at"`
	SourceEventID string               `json:"source_event_id"`
}

// RecipeTrigger describes the condition that starts a continuity recipe.
type RecipeTrigger struct {
	Type    string        `json:"type"`
	Target  string        `json:"target"`
	Timeout time.Duration `json:"timeout"`
}

// RecipeStep describes one ordered action in a continuity recipe.
type RecipeStep struct {
	Name    string            `json:"name"`
	Action  string            `json:"action"`
	Timeout time.Duration     `json:"timeout"`
	Params  map[string]string `json:"params,omitempty"`
}

func (k ContinuityRecipeKind) IsValid() bool {
	switch k {
	case ContinuityRecipeKindFailover, ContinuityRecipeKindRecovery:
		return true
	default:
		return false
	}
}

func IsValidRecipeAction(action string) bool {
	switch strings.TrimSpace(action) {
	case RecipeActionWakeOnLAN,
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
		RecipeActionReEnableAgents:
		return true
	default:
		return false
	}
}

func ValidateContinuityRecipe(recipe *ContinuityRecipe) error {
	if recipe == nil {
		return fmt.Errorf("%w: continuity recipe must not be nil", ErrInvalidValue)
	}
	recipe.Name = strings.TrimSpace(recipe.Name)
	recipe.ServiceKey = strings.TrimSpace(recipe.ServiceKey)
	recipe.SourceEventID = strings.TrimSpace(recipe.SourceEventID)
	if err := ValidateRequiredString(recipe.Name, "name"); err != nil {
		return err
	}
	if err := ValidateRequiredString(recipe.ServiceKey, "service_key"); err != nil {
		return err
	}
	if !recipe.Kind.IsValid() {
		return fmt.Errorf("%w: continuity recipe kind %q is not valid", ErrInvalidValue, recipe.Kind)
	}
	if recipe.Kind == ContinuityRecipeKindFailover && recipe.Trigger == nil {
		return fmt.Errorf("%w: failover recipes require a trigger", ErrEmptyField)
	}
	if len(recipe.Steps) == 0 {
		return fmt.Errorf("%w: continuity recipe steps must not be empty", ErrEmptyField)
	}
	for i := range recipe.Steps {
		recipe.Steps[i].Name = strings.TrimSpace(recipe.Steps[i].Name)
		recipe.Steps[i].Action = strings.TrimSpace(recipe.Steps[i].Action)
		if !IsValidRecipeAction(recipe.Steps[i].Action) {
			return fmt.Errorf("%w: continuity recipe step %d action %q is not valid", ErrInvalidValue, i, recipe.Steps[i].Action)
		}
	}
	return nil
}

func (recipe *ContinuityRecipe) Validate() error {
	return ValidateContinuityRecipe(recipe)
}

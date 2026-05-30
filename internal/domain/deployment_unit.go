package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultDeploymentUnitKey = "default"

// ReconcileMode defines how Bahia responds when observed runtime state differs from desired state.
type ReconcileMode string

const (
	ReconcileModeObserveOnly      ReconcileMode = "observe_only"
	ReconcileModeAutoApply        ReconcileMode = "auto_apply"
	ReconcileModeApprovalRequired ReconcileMode = "approval_required"
	ReconcileModeDisabled         ReconcileMode = "disabled"
)

// OwnershipMode defines the boundary Bahia owns inside a deployment unit.
type OwnershipMode string

const (
	OwnershipModeBahiaManaged OwnershipMode = "bahia_managed"
	OwnershipModeAdopted      OwnershipMode = "adopted"
	OwnershipModeExternal     OwnershipMode = "external"
)

// DeploymentUnit is a runtime ownership boundary inside one environment.
type DeploymentUnit struct {
	ID            uuid.UUID      `json:"id"`
	EnvironmentID uuid.UUID      `json:"environment_id"`
	Key           string         `json:"key"`
	DisplayName   string         `json:"display_name,omitempty"`
	RuntimeType   RuntimeType    `json:"runtime_type"`
	ReconcileMode ReconcileMode  `json:"reconcile_mode"`
	OwnershipMode OwnershipMode  `json:"ownership_mode"`
	RuntimeConfig map[string]any `json:"runtime_config,omitempty"`
	Implicit      bool           `json:"implicit"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// NewImplicitDefaultDeploymentUnit resolves the in-memory default unit for an environment.
// The implicit unit is not persisted by apply, observe, or ordinary write paths; it becomes
// durable only through the explicit unit creation API or the first multi-unit config change.
func NewImplicitDefaultDeploymentUnit(env *Environment) (*DeploymentUnit, error) {
	if env == nil {
		return nil, fmt.Errorf("%w: environment must not be nil", ErrInvalidValue)
	}
	if env.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: environment_id", ErrNilUUID)
	}

	runtimeType := runtimeTypeFromConfig(env.RuntimeConfig)
	if runtimeType == "" {
		runtimeType = RuntimeTypeDocker
	}
	if err := ValidateRuntimeType(runtimeType); err != nil {
		return nil, err
	}

	return &DeploymentUnit{
		EnvironmentID: env.ID,
		Key:           DefaultDeploymentUnitKey,
		DisplayName:   "Default",
		RuntimeType:   runtimeType,
		ReconcileMode: ReconcileModeObserveOnly,
		OwnershipMode: OwnershipModeBahiaManaged,
		RuntimeConfig: copyRuntimeConfig(env.RuntimeConfig),
		Implicit:      true,
	}, nil
}

func runtimeTypeFromConfig(config map[string]any) RuntimeType {
	if config == nil {
		return ""
	}
	if raw, ok := config["type"]; ok {
		if s, ok := raw.(string); ok {
			return RuntimeType(strings.TrimSpace(s))
		}
	}
	return ""
}

func copyRuntimeConfig(config map[string]any) map[string]any {
	if len(config) == 0 {
		return nil
	}
	out := make(map[string]any, len(config))
	for k, v := range config {
		out[k] = v
	}
	return out
}

// ValidateReconcileMode checks that a reconcile mode is known.
func ValidateReconcileMode(mode ReconcileMode) error {
	switch mode {
	case ReconcileModeObserveOnly, ReconcileModeAutoApply, ReconcileModeApprovalRequired, ReconcileModeDisabled:
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("%w: reconcile mode %q is not valid (allowed: observe_only, auto_apply, approval_required, disabled)", ErrInvalidValue, mode)
	}
}

// ValidateOwnershipMode checks that an ownership mode is known.
func ValidateOwnershipMode(mode OwnershipMode) error {
	switch mode {
	case OwnershipModeBahiaManaged, OwnershipModeAdopted, OwnershipModeExternal:
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("%w: ownership mode %q is not valid (allowed: bahia_managed, adopted, external)", ErrInvalidValue, mode)
	}
}

// ValidateDeploymentUnit checks required deployment unit identity and policy fields.
func ValidateDeploymentUnit(unit *DeploymentUnit) error {
	if unit == nil {
		return fmt.Errorf("%w: deployment unit must not be nil", ErrInvalidValue)
	}
	if unit.EnvironmentID == uuid.Nil {
		return fmt.Errorf("%w: environment_id", ErrNilUUID)
	}
	if strings.TrimSpace(unit.Key) == "" {
		return fmt.Errorf("%w: key must not be empty", ErrEmptyField)
	}
	if err := ValidateRuntimeType(unit.RuntimeType); err != nil {
		return err
	}
	if unit.RuntimeType == "" {
		return fmt.Errorf("%w: runtime_type must not be empty", ErrEmptyField)
	}
	if err := ValidateReconcileMode(unit.ReconcileMode); err != nil {
		return err
	}
	if unit.ReconcileMode == "" {
		return fmt.Errorf("%w: reconcile_mode must not be empty", ErrEmptyField)
	}
	if err := ValidateOwnershipMode(unit.OwnershipMode); err != nil {
		return err
	}
	if unit.OwnershipMode == "" {
		return fmt.Errorf("%w: ownership_mode must not be empty", ErrEmptyField)
	}
	return nil
}

package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type environmentReadUnitRepo struct {
	units []domain.DeploymentUnit
	err   error
}

func (r environmentReadUnitRepo) Create(context.Context, *domain.DeploymentUnit) error {
	return nil
}
func (r environmentReadUnitRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentUnit, error) {
	return nil, nil
}
func (r environmentReadUnitRepo) GetByEnvironmentKey(context.Context, uuid.UUID, string) (*domain.DeploymentUnit, error) {
	return nil, nil
}
func (r environmentReadUnitRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.DeploymentUnit, error) {
	return r.units, r.err
}
func (r environmentReadUnitRepo) ResolveDefault(context.Context, *domain.Environment) (*domain.DeploymentUnit, error) {
	return nil, nil
}

func TestEnvironmentResponseEmbedsExplicitOrImplicitDeploymentUnits(t *testing.T) {
	envID := uuid.New()
	env := &domain.Environment{
		ID:            envID,
		Name:          "production",
		RuntimeConfig: map[string]any{"type": "compose", "endpoint_ref": "max"},
		Targeting: domain.EnvironmentTargeting{
			DefaultUnitKey:       "default",
			DefaultReconcileMode: domain.ReconcileModeAutoApply,
		},
	}

	t.Run("explicit", func(t *testing.T) {
		expected := domain.DeploymentUnit{ID: uuid.New(), EnvironmentID: envID, Key: "max", RuntimeType: domain.RuntimeTypeCompose}
		handler := &EnvironmentHandler{units: environmentReadUnitRepo{units: []domain.DeploymentUnit{expected}}}
		response, err := handler.environmentResponse(httptest.NewRequest("GET", "/", nil), env)
		if err != nil {
			t.Fatalf("environmentResponse() error = %v", err)
		}
		if len(response.DeploymentUnits) != 1 || response.DeploymentUnits[0].ID != expected.ID || response.DeploymentUnits[0].Implicit {
			t.Fatalf("deployment units = %#v", response.DeploymentUnits)
		}
	})

	t.Run("implicit", func(t *testing.T) {
		handler := &EnvironmentHandler{units: environmentReadUnitRepo{}}
		response, err := handler.environmentResponse(httptest.NewRequest("GET", "/", nil), env)
		if err != nil {
			t.Fatalf("environmentResponse() error = %v", err)
		}
		if len(response.DeploymentUnits) != 1 || !response.DeploymentUnits[0].Implicit || response.DeploymentUnits[0].Key != "default" || response.DeploymentUnits[0].RuntimeType != domain.RuntimeTypeCompose {
			t.Fatalf("deployment units = %#v", response.DeploymentUnits)
		}
	})

	t.Run("repository error", func(t *testing.T) {
		handler := &EnvironmentHandler{units: environmentReadUnitRepo{err: errors.New("read failed")}}
		if _, err := handler.environmentResponse(httptest.NewRequest("GET", "/", nil), env); err == nil {
			t.Fatal("environmentResponse() expected repository error")
		}
	})
}

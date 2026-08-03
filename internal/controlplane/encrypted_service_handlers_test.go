package controlplane

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestValidateManagedDeployReviewHashRequiresExpectedHash(t *testing.T) {
	svc := &domain.Service{
		RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{
			Managed: &domain.ManagedRuntimeConfig{},
		},
	}
	if err := validateManagedDeployReviewHash(svc, ""); err == nil || !strings.Contains(err.Error(), "expected_desired_state_hash is required") {
		t.Fatalf("blank managed deploy hash error = %v", err)
	}
	if err := validateManagedDeployReviewHash(svc, "sha256:reviewed"); err != nil {
		t.Fatalf("reviewed managed deploy rejected: %v", err)
	}
}

func TestCarryForwardRollbackPublicRouteIncludesRouteInSignedHash(t *testing.T) {
	serviceID, environmentID, unitID := uuid.New(), uuid.New(), uuid.New()
	route := &domain.DesiredPublicRoutePlan{
		SchemaVersion:    domain.PublicRouteSchemaVersion,
		ServiceID:        serviceID,
		EnvironmentID:    environmentID,
		DeploymentUnitID: unitID,
		Hostname:         "arcana.example.com",
	}
	supersededState := &domain.DesiredServiceSpec{
		SchemaVersion:     domain.DesiredStateSchemaVersion,
		ServiceID:         serviceID,
		EnvironmentID:     environmentID,
		DeploymentUnitID:  &unitID,
		DeploymentUnitKey: "arcana",
		ArtifactID:        uuid.New(),
		PublicRoute:       route,
	}
	desiredState := &domain.DesiredServiceSpec{
		SchemaVersion:     domain.DesiredStateSchemaVersion,
		ServiceID:         serviceID,
		EnvironmentID:     environmentID,
		DeploymentUnitID:  &unitID,
		DeploymentUnitKey: "arcana",
		ArtifactID:        uuid.New(),
	}
	withoutRoute := desiredState.ComputeDesiredHash()

	carryForwardRollbackPublicRoute(desiredState, &domain.DeploymentIntent{DesiredState: supersededState})

	if desiredState.PublicRoute != route {
		t.Fatal("rollback desired state did not carry the superseded public route")
	}
	if desiredState.DesiredHash == withoutRoute {
		t.Fatal("rollback desired-state hash did not include the public route")
	}
}

package reconcile

import (
	"testing"

	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestLLMReconcilerObservationCarriesBackendAndGatewayState(t *testing.T) {
	routeID, releaseID, runID, envID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	route := &domain.LLMRoute{ID: routeID, Name: "drydock-review"}
	release := &domain.LLMRelease{ID: releaseID}
	run := &domain.LLMDeploymentRun{ID: runID, BackendKind: domain.LLMBackendKindVLLM, BackendEndpoint: "http://old"}
	backend := &llmadapter.BackendObservation{BackendKind: domain.LLMBackendKindVLLM, BackendEndpoint: "http://t7920:8000", HealthStatus: domain.HealthStatusHealthy}
	gateway := &llmadapter.GatewayRouteObservation{Status: domain.GatewayRouteStatusSynced, TargetURL: "http://t7920:8000", GatewayConfigHash: "hash"}
	obs := observationFromReconcile(route, release, run, envID, backend, gateway, "desired")
	if obs.RouteID != routeID || obs.EnvironmentID != envID || obs.ObservedReleaseID == nil || *obs.ObservedReleaseID != releaseID || obs.ObservedRunID == nil || *obs.ObservedRunID != runID {
		t.Fatalf("observation identifiers not populated: %#v", obs)
	}
	if obs.BackendEndpoint != "http://t7920:8000" || obs.BackendHealth != domain.HealthStatusHealthy || obs.GatewayStatus != domain.GatewayRouteStatusSynced || obs.GatewayConfigHash != "hash" {
		t.Fatalf("observation state not populated: %#v", obs)
	}
}

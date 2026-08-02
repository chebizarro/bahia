package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// TestDeployStatusEventsIncludeStepTags verifies that step-progression status
// events published during DeployWithStatus use canonical NIP-38 status events and carry the expected step
// tag, action tag, and correlation tags (e, p, service, environment, artifact).
func TestDeployStatusEventsIncludeStepTags(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()

	// Stub that records status callback steps and publishes them.
	var capturedSteps []service.DeployStep
	runtimeStub := &stubRuntimeLifecycleOperatorService{
		deployResp: &domain.RuntimeObservation{
			ID:                  obsID,
			ServiceID:           serviceID,
			EnvironmentID:       envID,
			ObservedImageDigest: "sha256:abc",
			ObservedContainerID: "container-1",
			HealthStatus:        domain.HealthStatusHealthy,
			Source:              "direct_runtime",
		},
	}

	// Override DeployWithStatus to actually invoke the callback.
	runtimeStub.deployResp = &domain.RuntimeObservation{
		ID:            obsID,
		ServiceID:     serviceID,
		EnvironmentID: envID,
		HealthStatus:  domain.HealthStatusHealthy,
		Source:        "direct_runtime",
	}

	operatorKey := nostr.Generate().Hex()
	operatorPubkey := testNostrPubKeyHexFromPrivateKey(t, operatorKey)
	reactor := newOperatorActionTestReactor(t,
		Config{DirectRuntimeAuthorizedPubkeys: []string{operatorPubkey}},
		capture, nil, &statusCapturingRuntimeStub{
			obs:           runtimeStub.deployResp,
			capturedSteps: &capturedSteps,
		},
	)

	requestEventID := testNostrID("deploy-step-test")
	requestEvent := &nostr.Event{
		ID:      requestEventID,
		PubKey:  testNostrPubKeyFromPrivateKey(t, operatorKey),
		Kind:    KindServiceAction,
		Content: fmt.Sprintf(`{"action":"deploy","service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, envID, artifactID),
	}

	reactor.handleServiceAction(context.Background(), requestEvent)

	// Find all canonical status/result reply events.
	var statusEvents []nostr.Event
	var resultEvents []nostr.Event
	for _, ev := range capture.events {
		switch ev.Kind {
		case KindNIP38Status:
			statusEvents = append(statusEvents, ev)
		case KindContextVMMessage:
			resultEvents = append(resultEvents, ev)
		}
	}

	if len(statusEvents) == 0 {
		t.Fatal("expected at least one canonical NIP-38 status event")
	}

	// The first status event should be the initial "executing" step from handleDirectRuntimeActionRequest.
	first := statusEvents[0]
	assertReactorTag(t, first.Tags, "e", requestEventID.Hex())
	assertReactorTag(t, first.Tags, "p", operatorPubkey)
	assertReactorTag(t, first.Tags, "status", "processing")
	assertReactorTag(t, first.Tags, "action", "deploy")
	assertReactorTag(t, first.Tags, "step", "executing")
	assertSignedEvent(t, first)

	// All status events must have correlation tags.
	for i, ev := range statusEvents {
		assertReactorTag(t, ev.Tags, "e", requestEventID.Hex())
		assertReactorTag(t, ev.Tags, "p", operatorPubkey)
		assertReactorTag(t, ev.Tags, "action", "deploy")
		foundStep := false
		for _, tag := range ev.Tags {
			if len(tag) >= 2 && tag[0] == "step" {
				foundStep = true
				break
			}
		}
		if !foundStep {
			t.Fatalf("status event %d missing step tag", i)
		}
		// Resource tags should be present (from appendRequestResourceTags parsing content).
		assertReactorTag(t, ev.Tags, "service", serviceID.String())
		assertReactorTag(t, ev.Tags, "environment", envID.String())
		assertReactorTag(t, ev.Tags, "artifact", artifactID.String())
	}

	// Must have exactly one result event.
	if len(resultEvents) != 1 {
		t.Fatalf("expected 1 result event, got %d", len(resultEvents))
	}
}

// TestResultEventsCorrelateToOriginalRequest verifies that ContextVM response
// events carry the correct correlation tags linking back to the request event.
func TestResultEventsCorrelateToOriginalRequest(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()

	runtimeStub := &stubRuntimeLifecycleOperatorService{
		deployResp: &domain.RuntimeObservation{
			ID:                  obsID,
			ServiceID:           serviceID,
			EnvironmentID:       envID,
			ObservedImageDigest: "sha256:def456",
			ObservedContainerID: "container-2",
			HealthStatus:        domain.HealthStatusHealthy,
			Source:              "direct_runtime",
		},
	}

	operatorKey := nostr.Generate().Hex()
	operatorPubkey := testNostrPubKeyHexFromPrivateKey(t, operatorKey)
	reactor := newOperatorActionTestReactor(t,
		Config{DirectRuntimeAuthorizedPubkeys: []string{operatorPubkey}},
		capture, nil, runtimeStub,
	)

	requestEventID := testNostrID("request-correlation-test-" + uuid.NewString()[:8])
	reactor.handleServiceAction(context.Background(), &nostr.Event{
		ID:      requestEventID,
		PubKey:  testNostrPubKeyFromPrivateKey(t, operatorKey),
		Kind:    KindServiceAction,
		Content: fmt.Sprintf(`{"action":"deploy","service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, envID, artifactID),
	})

	// Find the result event.
	var result *nostr.Event
	for i := range capture.events {
		if capture.events[i].Kind == KindContextVMMessage {
			result = &capture.events[i]
			break
		}
	}
	if result == nil {
		t.Fatal("no ContextVM result event published")
	}

	// Verify correlation tags.
	assertReactorTag(t, result.Tags, "e", requestEventID.Hex())
	assertReactorTag(t, result.Tags, "p", operatorPubkey)
	assertReactorTag(t, result.Tags, "status", "success")
	assertReactorTag(t, result.Tags, "action", "deploy")
	assertReactorTag(t, result.Tags, "service", serviceID.String())
	assertReactorTag(t, result.Tags, "environment", envID.String())
	assertSignedEvent(t, *result)

	// Verify the e tag has the "reply" marker (NIP-10 style).
	for _, tag := range result.Tags {
		if len(tag) >= 2 && tag[0] == "e" && tag[1] == requestEventID.Hex() {
			if len(tag) < 4 || tag[3] != "reply" {
				t.Fatalf("e tag missing reply marker: %v", tag)
			}
			break
		}
	}

	// Verify result content is valid and contains the expected payload.
	var payload dto.RuntimeActionResponse
	decodeContextVMResult(t, *result, &payload)
	if payload.Action != "deploy" {
		t.Fatalf("result action = %q, want deploy", payload.Action)
	}
	if payload.ServiceID != serviceID {
		t.Fatalf("result service_id = %s, want %s", payload.ServiceID, serviceID)
	}
	if payload.EnvironmentID != envID {
		t.Fatalf("result environment_id = %s, want %s", payload.EnvironmentID, envID)
	}
	if payload.Observation == nil || payload.Observation.ID != obsID {
		t.Fatalf("result observation missing or wrong ID")
	}
}

// TestFailurePathsPublishTerminalResults verifies that when the runtime
// lifecycle service returns an error, a terminal ContextVM error response is
// still published with status=failed and the error message.
func TestFailurePathsPublishTerminalResults(t *testing.T) {
	tests := []struct {
		name     string
		action   string
		errMsg   string
		setupErr func(*stubRuntimeLifecycleOperatorService)
	}{
		{
			name:   "deploy failure",
			action: "deploy",
			errMsg: "image pull timeout",
			setupErr: func(s *stubRuntimeLifecycleOperatorService) {
				s.deployErr = errors.New("image pull timeout")
			},
		},
		{
			name:   "restart failure",
			action: "restart",
			errMsg: "container not found",
			setupErr: func(s *stubRuntimeLifecycleOperatorService) {
				s.restartErr = errors.New("container not found")
			},
		},
		{
			name:   "stop failure",
			action: "stop",
			errMsg: "permission denied",
			setupErr: func(s *stubRuntimeLifecycleOperatorService) {
				s.stopErr = errors.New("permission denied")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := &captureNostrPublisher{published: 1}
			serviceID := uuid.New()
			envID := uuid.New()
			runtimeStub := &stubRuntimeLifecycleOperatorService{}
			tc.setupErr(runtimeStub)

			operatorKey := nostr.Generate().Hex()
			operatorPubkey := testNostrPubKeyHexFromPrivateKey(t, operatorKey)
			reactor := newOperatorActionTestReactor(t,
				Config{DirectRuntimeAuthorizedPubkeys: []string{operatorPubkey}},
				capture, nil, runtimeStub,
			)

			content := fmt.Sprintf(`{"action":"%s","service_id":"%s","environment_id":"%s"}`, tc.action, serviceID, envID)
			if tc.action == "deploy" {
				artifactID := uuid.New()
				content = fmt.Sprintf(`{"action":"deploy","service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, envID, artifactID)
			}

			requestEventID := testNostrID(tc.action + "-fail-" + uuid.NewString()[:8])
			reactor.handleServiceAction(context.Background(), &nostr.Event{
				ID:      requestEventID,
				PubKey:  testNostrPubKeyFromPrivateKey(t, operatorKey),
				Kind:    KindServiceAction,
				Content: content,
			})

			// Find the terminal result event.
			var result *nostr.Event
			for i := range capture.events {
				if capture.events[i].Kind == KindContextVMMessage {
					ev := capture.events[i]
					// Check if this is the terminal result (not the status event).
					for _, tag := range ev.Tags {
						if len(tag) >= 2 && tag[0] == "status" && tag[1] == "failed" {
							result = &ev
							break
						}
					}
				}
			}
			if result == nil {
				t.Fatal("no terminal ContextVM failure result event status=failed published")
			}

			// Verify correlation tags on failure result.
			assertReactorTag(t, result.Tags, "e", requestEventID.Hex())
			assertReactorTag(t, result.Tags, "p", operatorPubkey)
			assertReactorTag(t, result.Tags, "status", "failed")
			assertReactorTag(t, result.Tags, "action", tc.action)
			assertSignedEvent(t, *result)

			// Verify the error is included.
			hasError := false
			for _, tag := range result.Tags {
				if len(tag) >= 2 && tag[0] == "error" {
					hasError = true
					break
				}
			}
			if !hasError {
				t.Fatal("failure result missing error tag")
			}

			var response ContextVMJSONRPCResponse
			if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
				t.Fatalf("decode failure ContextVM response: %v", err)
			}
			if response.Error == nil || response.Error.Message == "" {
				t.Fatalf("failure response missing error: %+v", response)
			}

			// Verify resource correlation tags are present.
			assertReactorTag(t, result.Tags, "service", serviceID.String())
			assertReactorTag(t, result.Tags, "environment", envID.String())
			assertNoLegacyStatusResultEvents(t, capture.events)
		})
	}
}

// TestDeployStatusStepProgressionPublishesAllSteps verifies that the
// deployStatusCallbackFor mechanism publishes a status event for each step
// reported by the lifecycle service, preserving correct tags.
func TestDeployStatusStepProgressionPublishesAllSteps(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()

	expectedSteps := []service.DeployStep{
		service.DeployStepBuildingDesiredState,
		service.DeployStepLockingEnvironment,
		service.DeployStepRendering,
		service.DeployStepApplying,
		service.DeployStepObserving,
		service.DeployStepProjecting,
	}

	operatorKey := nostr.Generate().Hex()
	operatorPubkey := testNostrPubKeyHexFromPrivateKey(t, operatorKey)
	reactor := newOperatorActionTestReactor(t,
		Config{DirectRuntimeAuthorizedPubkeys: []string{operatorPubkey}},
		capture, nil, &allStepsRuntimeStub{
			obs:   &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: envID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"},
			steps: expectedSteps,
		},
	)

	reactor.handleServiceAction(context.Background(), &nostr.Event{
		ID:      testNostrID("step-progression-test"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, operatorKey),
		Kind:    KindServiceAction,
		Content: fmt.Sprintf(`{"action":"deploy","service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, envID, artifactID),
	})

	// Collect all status events.
	var statusSteps []string
	for _, ev := range capture.events {
		if ev.Kind == KindNIP38Status {
			for _, tag := range ev.Tags {
				if len(tag) >= 2 && tag[0] == "step" {
					statusSteps = append(statusSteps, tag[1])
					break
				}
			}
		}
	}

	// Expect "executing" initial + each deploy step.
	expectedCount := 1 + len(expectedSteps)
	if len(statusSteps) != expectedCount {
		t.Fatalf("expected %d status step events, got %d: %v", expectedCount, len(statusSteps), statusSteps)
	}

	// First should be "executing" from handleDirectRuntimeActionRequest.
	if statusSteps[0] != "executing" {
		t.Fatalf("first step = %q, want executing", statusSteps[0])
	}

	// Remaining should match the deploy lifecycle steps.
	for i, expected := range expectedSteps {
		got := statusSteps[i+1]
		if got != string(expected) {
			t.Fatalf("step %d = %q, want %q", i+1, got, expected)
		}
	}
}

// TestProductionReplyKindsAreCanonical verifies production command reply paths
// use canonical NIP-38 status and ContextVM response kinds instead of legacy 696x/796x replies.
func TestProductionReplyKindsAreCanonical(t *testing.T) {
	if KindNIP38Status >= 6961 && KindNIP38Status <= 6999 {
		t.Fatalf("KindNIP38Status = %d must not be legacy 696x", KindNIP38Status)
	}
	if KindContextVMMessage >= 7961 && KindContextVMMessage <= 7999 {
		t.Fatalf("KindContextVMMessage = %d must not be legacy 796x", KindContextVMMessage)
	}
	if KindActionStatus != 6963 || KindActionResult != 7962 {
		t.Fatalf("legacy constants changed unexpectedly; keep them migration/test-only")
	}
}

// --- Test helpers ---

// statusCapturingRuntimeStub invokes the status callback with all deploy steps.
type statusCapturingRuntimeStub struct {
	obs           *domain.RuntimeObservation
	capturedSteps *[]service.DeployStep
}

func (s *statusCapturingRuntimeStub) BuildDesiredStateSnapshot(_ context.Context, serviceID, envID, artifactID uuid.UUID, _ *uuid.UUID) (*domain.DesiredServiceSpec, error) {
	spec := &domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: envID, ArtifactID: artifactID, StableServiceKey: "api", ImageRef: "registry.example.com/api:latest"}
	spec.ComputeDesiredHash()
	return spec, nil
}

func (s *statusCapturingRuntimeStub) DeployDesiredStateSnapshot(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID, _ *domain.DesiredServiceSpec, statusFn service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	return s.DeployWithStatus(ctx, serviceID, envID, artifactID, statusFn)
}

func (s *statusCapturingRuntimeStub) Deploy(_ context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.obs, nil
}

func (s *statusCapturingRuntimeStub) DeployWithStatus(_ context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID, statusFn service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	if statusFn != nil {
		steps := []service.DeployStep{
			service.DeployStepBuildingDesiredState,
			service.DeployStepLockingEnvironment,
			service.DeployStepRendering,
			service.DeployStepApplying,
			service.DeployStepObserving,
			service.DeployStepProjecting,
		}
		for _, step := range steps {
			*s.capturedSteps = append(*s.capturedSteps, step)
			statusFn(context.Background(), step, "test")
		}
	}
	return s.obs, nil
}

func (s *statusCapturingRuntimeStub) Restart(_ context.Context, _, _ uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.obs, nil
}

func (s *statusCapturingRuntimeStub) Stop(_ context.Context, _, _ uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.obs, nil
}

// allStepsRuntimeStub is like statusCapturingRuntimeStub but takes expected steps.
type allStepsRuntimeStub struct {
	obs   *domain.RuntimeObservation
	steps []service.DeployStep
}

func (s *allStepsRuntimeStub) BuildDesiredStateSnapshot(_ context.Context, serviceID, envID, artifactID uuid.UUID, _ *uuid.UUID) (*domain.DesiredServiceSpec, error) {
	spec := &domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: envID, ArtifactID: artifactID, StableServiceKey: "api", ImageRef: "registry.example.com/api:latest"}
	spec.ComputeDesiredHash()
	return spec, nil
}

func (s *allStepsRuntimeStub) DeployDesiredStateSnapshot(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID, _ *domain.DesiredServiceSpec, statusFn service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	return s.DeployWithStatus(ctx, serviceID, envID, artifactID, statusFn)
}

func (s *allStepsRuntimeStub) Deploy(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.obs, nil
}

func (s *allStepsRuntimeStub) DeployWithStatus(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID, statusFn service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	if statusFn != nil {
		for _, step := range s.steps {
			statusFn(context.Background(), step, "step: "+string(step))
		}
	}
	return s.obs, nil
}

func (s *allStepsRuntimeStub) Restart(_ context.Context, _, _ uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.obs, nil
}

func (s *allStepsRuntimeStub) Stop(_ context.Context, _, _ uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.obs, nil
}

// hasTagKey checks if any tag with the given key exists.
func hasTagKey(tags nostr.Tags, key string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return true
		}
	}
	return false
}

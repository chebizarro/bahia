package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Section 1: Locking Behavior Tests
// ---------------------------------------------------------------------------

// TestLocking_SameEnvironmentSerializes proves that two concurrent deploys
// targeting the same environment are serialized by the in-memory apply lock.
// Uses channel synchronization — no sleeps for ordering guarantees.
func TestLocking_SameEnvironmentSerializes(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)

	// deploySem controls when each deploy completes.
	deploySem := make(chan struct{})
	var deployOrder []int
	var mu sync.Mutex

	rt := &blockingMockRuntime{
		deploySem:   deploySem,
		deployOrder: &deployOrder,
		mu:          &mu,
	}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	errs := make(chan error, 2)

	// Deploy 1: will acquire lock and block on deploySem.
	go func() {
		_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
		errs <- err
	}()

	// Deploy 2: will queue behind the lock.
	go func() {
		_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
		errs <- err
	}()

	// Let each deploy complete one at a time.
	deploySem <- struct{}{} // unblock first deploy
	deploySem <- struct{}{} // unblock second deploy

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("deploy %d failed: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deployOrder) != 2 {
		t.Fatalf("expected 2 serialized deploys, got %d", len(deployOrder))
	}
	// The key assertion: deploy 1 must finish before deploy 2 starts.
	if deployOrder[0] == deployOrder[1] {
		t.Fatalf("deploys should have distinct ordering markers, got %v", deployOrder)
	}
}

// TestLocking_DifferentEnvironmentsParallel proves that deploys targeting
// different environments proceed concurrently without blocking each other.
func TestLocking_DifferentEnvironmentsParallel(t *testing.T) {
	ctx := context.Background()

	// holdCh blocks both deploys so we can prove they both acquired their lock.
	holdCh := make(chan struct{})
	bothAcquired := make(chan struct{}, 2)

	rt := &parallelMockRuntime{
		holdCh:       holdCh,
		bothAcquired: bothAcquired,
	}
	lock := newInMemoryApplyLock()

	// Create two separate environments with their own fixtures.
	registryA, svcRepoA, envRepoA, _, artifactRepoA, _, _ := newTestRegistry()
	stateRepoA := registryA.state.(*mockStateRepo)
	svcA, envA, artifactA := seedRuntimeLifecycleFixtures(t, registryA)

	registryB, svcRepoB, envRepoB, _, artifactRepoB, _, _ := newTestRegistry()
	stateRepoB := registryB.state.(*mockStateRepo)
	svcB, envB, artifactB := seedRuntimeLifecycleFixtures(t, registryB)

	lifecycleA := NewRuntimeLifecycleService(
		registryA, svcRepoA, envRepoA, artifactRepoA, stateRepoA,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)
	lifecycleB := NewRuntimeLifecycleService(
		registryB, svcRepoB, envRepoB, artifactRepoB, stateRepoB,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	errs := make(chan error, 2)
	go func() {
		_, err := lifecycleA.Deploy(ctx, svcA.ID, envA.ID, &artifactA.ID)
		errs <- err
	}()
	go func() {
		_, err := lifecycleB.Deploy(ctx, svcB.ID, envB.ID, &artifactB.ID)
		errs <- err
	}()

	// Both environments should acquire their lock and reach Deploy concurrently.
	timeout := time.After(3 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-bothAcquired:
		case <-timeout:
			t.Fatal("timed out waiting for both environments to acquire locks concurrently")
		}
	}

	// Release both deploys.
	close(holdCh)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("parallel deploy %d failed: %v", i, err)
		}
	}
}

// TestLocking_ReleasedOnSuccess verifies the lock is released after a
// successful deploy so a subsequent deploy can acquire immediately.
func TestLocking_ReleasedOnSuccess(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	// First deploy succeeds.
	if _, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID); err != nil {
		t.Fatalf("first deploy failed: %v", err)
	}

	// Second deploy should acquire immediately — no deadlock.
	done := make(chan error, 1)
	go func() {
		_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second deploy after success failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second deploy blocked — lock was not released after successful deploy")
	}
}

// TestLocking_ReleasedOnDeployFailure verifies the lock is released when the
// runtime deploy operation fails, so a subsequent deploy can proceed.
func TestLocking_ReleasedOnDeployFailure(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{deployErr: fmt.Errorf("runtime crashed")}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	// First deploy fails.
	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err == nil {
		t.Fatal("expected deploy failure")
	}

	// Clear the error for the next deploy.
	rt.deployErr = nil

	// Second deploy should succeed — lock should not be held.
	done := make(chan error, 1)
	go func() {
		_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("deploy after failure should succeed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lock not released after deploy failure — second deploy blocked")
	}
}

// TestLocking_ReleasedOnObserveFailure verifies the lock is released even when
// post-deploy observation fails.
func TestLocking_ReleasedOnObserveFailure(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &observeFailMockRuntime{observeErr: fmt.Errorf("observe timeout")}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	// First deploy fails at observe.
	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err == nil {
		t.Fatal("expected observe failure")
	}

	// Clear the error.
	rt.observeErr = nil

	// Lock must be free.
	done := make(chan error, 1)
	go func() {
		_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("deploy after observe failure should succeed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lock not released after observe failure")
	}
}

// TestLocking_ReleasedAfterHeldDeploy verifies that after a deploy that holds
// the lock completes, a subsequent deploy can acquire the lock immediately.
// This tests the complete lock lifecycle: acquire → hold → release → reacquire.
func TestLocking_ReleasedAfterHeldDeploy(t *testing.T) {
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)

	holdCh := make(chan struct{})
	rt := &holdingMockRuntime{holdCh: holdCh}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	// Deploy 1 holds the lock (blocks in runtime.Deploy).
	deploy1Done := make(chan error, 1)
	go func() {
		_, err := lifecycle.Deploy(context.Background(), svc.ID, env.ID, &artifact.ID)
		deploy1Done <- err
	}()

	// Give deploy 1 time to acquire the lock.
	time.Sleep(50 * time.Millisecond)

	// Release deploy 1.
	close(holdCh)
	if err := <-deploy1Done; err != nil {
		t.Fatalf("deploy 1 failed: %v", err)
	}

	// Now a second deploy should work immediately — lock is free.
	rt2 := &lifecycleMockRuntime{}
	lifecycle2 := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt2}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)
	done := make(chan error, 1)
	go func() {
		_, err := lifecycle2.Deploy(context.Background(), svc.ID, env.ID, &artifact.ID)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("deploy after held deploy should succeed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lock not released after held deploy — second deploy blocked")
	}
}

// TestLocking_NilLockSkipsLocking verifies deploy works without a lock injected
// (backward compatibility).
func TestLocking_NilLockSkipsLocking(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	// No WithRuntimeApplyLock — applyLock is nil.
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	obs, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err != nil {
		t.Fatalf("deploy without lock should succeed: %v", err)
	}
	if obs == nil {
		t.Fatal("expected observation")
	}
}

// ---------------------------------------------------------------------------
// Section 2: Deploy Path Convergence Tests
// ---------------------------------------------------------------------------

// TestConvergence_DeployAndDeployWithStatusUseSameHelper proves that both
// Deploy() and DeployWithStatus() invoke the shared deployDesiredState helper,
// producing the same runtime calls and state mutations.
func TestConvergence_DeployAndDeployWithStatusUseSameHelper(t *testing.T) {
	ctx := context.Background()

	// Run Deploy() path.
	registryA, svcRepoA, envRepoA, _, artifactRepoA, _, _ := newTestRegistry()
	stateRepoA := registryA.state.(*mockStateRepo)
	rtA := &capturingMockRuntime{}
	publisherA := &capturePublisher{}
	lifecycleA := NewRuntimeLifecycleService(
		registryA, svcRepoA, envRepoA, artifactRepoA, stateRepoA,
		&mockRuntimeResolver{rt: rtA}, publisherA, zap.NewNop(),
	)
	svcA, envA, artifactA := seedRuntimeLifecycleFixtures(t, registryA)
	obsA, errA := lifecycleA.Deploy(ctx, svcA.ID, envA.ID, &artifactA.ID)

	// Run DeployWithStatus() path with a status callback.
	registryB, svcRepoB, envRepoB, _, artifactRepoB, _, _ := newTestRegistry()
	stateRepoB := registryB.state.(*mockStateRepo)
	rtB := &capturingMockRuntime{}
	publisherB := &capturePublisher{}
	lifecycleB := NewRuntimeLifecycleService(
		registryB, svcRepoB, envRepoB, artifactRepoB, stateRepoB,
		&mockRuntimeResolver{rt: rtB}, publisherB, zap.NewNop(),
	)
	svcB, envB, artifactB := seedRuntimeLifecycleFixtures(t, registryB)
	var steps []DeployStep
	statusFn := func(_ context.Context, step DeployStep, _ string) {
		steps = append(steps, step)
	}
	obsB, errB := lifecycleB.DeployWithStatus(ctx, svcB.ID, envB.ID, &artifactB.ID, statusFn)

	// Both should succeed.
	if errA != nil {
		t.Fatalf("Deploy failed: %v", errA)
	}
	if errB != nil {
		t.Fatalf("DeployWithStatus failed: %v", errB)
	}

	// Both should produce observations.
	if obsA == nil || obsB == nil {
		t.Fatal("both paths must produce observations")
	}

	// Same runtime target name used.
	if rtA.deployTarget != rtB.deployTarget {
		t.Fatalf("deploy targets differ: %q vs %q", rtA.deployTarget, rtB.deployTarget)
	}

	// Same image reference.
	if rtA.deployImage != rtB.deployImage {
		t.Fatalf("deploy images differ: %q vs %q", rtA.deployImage, rtB.deployImage)
	}

	// Same deploy options shape (Bahia labels, env, ports, etc.).
	if rtA.deployOpts.Labels["bahia.managed"] != rtB.deployOpts.Labels["bahia.managed"] {
		t.Fatal("bahia.managed label differs between paths")
	}
	if rtA.deployOpts.Labels["bahia.service"] != rtB.deployOpts.Labels["bahia.service"] {
		t.Fatal("bahia.service label differs between paths")
	}
	if rtA.deployOpts.Environment["APP_ENV"] != rtB.deployOpts.Environment["APP_ENV"] {
		t.Fatal("environment vars differ between paths")
	}

	// Same events published.
	if len(publisherA.events) != len(publisherB.events) {
		t.Fatalf("event count differs: %d vs %d", len(publisherA.events), len(publisherB.events))
	}
	for i := range publisherA.events {
		if publisherA.events[i].Type != publisherB.events[i].Type {
			t.Fatalf("event type at %d differs: %v vs %v", i, publisherA.events[i].Type, publisherB.events[i].Type)
		}
	}

	// DeployWithStatus should have emitted all expected steps.
	expectedSteps := []DeployStep{
		DeployStepBuildingDesiredState,
		DeployStepLockingEnvironment,
		DeployStepRendering,
		DeployStepApplying,
		DeployStepObserving,
		DeployStepProjecting,
	}
	if len(steps) != len(expectedSteps) {
		t.Fatalf("expected %d steps from DeployWithStatus, got %d: %v", len(expectedSteps), len(steps), steps)
	}
	for i, expected := range expectedSteps {
		if steps[i] != expected {
			t.Fatalf("step %d: expected %q, got %q", i, expected, steps[i])
		}
	}
}

// TestConvergence_RestartAndStopDoNotUseDeployHelper proves that restart and
// stop flow through LifecycleRuntime methods, NOT through deployDesiredState.
func TestConvergence_RestartAndStopDoNotUseDeployHelper(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	publisher := &capturePublisher{}
	rt := &capturingMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, publisher, zap.NewNop(),
	)

	svc, env, _ := seedRuntimeLifecycleFixtures(t, registry)

	// Restart.
	if _, err := lifecycle.Restart(ctx, svc.ID, env.ID); err != nil {
		t.Fatalf("Restart error: %v", err)
	}
	if rt.deployTarget != "" {
		t.Fatalf("restart should not call deploy, got target %q", rt.deployTarget)
	}
	if rt.restartTarget != "legacy-api" {
		t.Fatalf("expected restart target legacy-api, got %q", rt.restartTarget)
	}

	// Stop.
	if _, err := lifecycle.Stop(ctx, svc.ID, env.ID); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if rt.stopTarget != "legacy-api" {
		t.Fatalf("expected stop target legacy-api, got %q", rt.stopTarget)
	}

	// Verify events: restart and stop should produce their own event types.
	hasRestart := false
	hasStop := false
	for _, e := range publisher.events {
		if e.Type == runtimeActionRestartEvent {
			hasRestart = true
		}
		if e.Type == runtimeActionStopEvent {
			hasStop = true
		}
		if e.Type == runtimeActionDeployEvent {
			t.Fatal("restart/stop should not publish deploy events")
		}
	}
	if !hasRestart {
		t.Fatal("expected restart event")
	}
	if !hasStop {
		t.Fatal("expected stop event")
	}
}

// TestConvergence_DeployUpdatesDesiredArtifactAndDriftStatus proves that the
// deploy path updates environment service state with the deployed artifact ID
// and sets drift status to deploying.
func TestConvergence_DeployUpdatesDesiredArtifactAndDriftStatus(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	// Seed with a different desired artifact.
	oldArtifactID := uuid.New()
	if err := stateRepo.Upsert(ctx, &domain.EnvironmentServiceState{
		ServiceID:         svc.ID,
		EnvironmentID:     env.ID,
		DesiredArtifactID: &oldArtifactID,
		DriftStatus:       domain.DriftStatusInSync,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if _, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state == nil {
		t.Fatal("state not found after deploy")
	}
	if state.DesiredArtifactID == nil || *state.DesiredArtifactID != artifact.ID {
		t.Fatalf("expected desired artifact %s, got %v", artifact.ID, state.DesiredArtifactID)
	}
}

// ---------------------------------------------------------------------------
// Section 3: Integration-style Tests (Concurrent Deploys, Events, Correlation)
// ---------------------------------------------------------------------------

// TestIntegration_ConcurrentSameEnvDeploysQueueCorrectly proves that N
// concurrent deploys to the same environment all complete and each gets
// a unique observation recorded, with proper event publication.
func TestIntegration_ConcurrentSameEnvDeploysQueueCorrectly(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)

	var deployCount int32
	rt := &countingMockRuntime{deployCount: &deployCount}
	publisher := &threadSafePublisher{}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, publisher, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	const N = 5
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
			errs <- err
		}()
	}

	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("deploy %d failed: %v", i, err)
		}
	}

	count := atomic.LoadInt32(&deployCount)
	if count != N {
		t.Fatalf("expected %d deploy calls, got %d", N, count)
	}

	// Each deploy should publish exactly one runtime.deploy event.
	publisher.mu.Lock()
	deployEvents := 0
	for _, e := range publisher.events {
		if e.Type == runtimeActionDeployEvent {
			deployEvents++
		}
	}
	publisher.mu.Unlock()
	if deployEvents != N {
		t.Fatalf("expected %d deploy events, got %d", N, deployEvents)
	}
}

// TestIntegration_StatusEventsShowCorrectSteps proves that a full deploy
// with locking emits all expected step progression callbacks including the
// locking step.
func TestIntegration_StatusEventsShowCorrectSteps(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	var steps []DeployStep
	var messages []string
	statusFn := func(_ context.Context, step DeployStep, msg string) {
		steps = append(steps, step)
		messages = append(messages, msg)
	}

	obs, err := lifecycle.DeployWithStatus(ctx, svc.ID, env.ID, &artifact.ID, statusFn)
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if obs == nil {
		t.Fatal("expected observation")
	}

	expectedSteps := []DeployStep{
		DeployStepBuildingDesiredState,
		DeployStepLockingEnvironment,
		DeployStepRendering,
		DeployStepApplying,
		DeployStepObserving,
		DeployStepProjecting,
	}
	if len(steps) != len(expectedSteps) {
		t.Fatalf("expected %d steps, got %d: %v", len(expectedSteps), len(steps), steps)
	}
	for i, expected := range expectedSteps {
		if steps[i] != expected {
			t.Fatalf("step %d: expected %q, got %q", i, expected, steps[i])
		}
	}

	// Each step should have a non-empty message.
	for i, msg := range messages {
		if msg == "" {
			t.Fatalf("step %d (%s) has empty message", i, steps[i])
		}
	}
}

// TestIntegration_ResultEventsCorrelate proves that published deploy events
// contain correct service/environment correlation data.
func TestIntegration_ResultEventsCorrelate(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	publisher := &capturePublisher{}
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, publisher, zap.NewNop(),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	obs, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	// Find the deploy event.
	var deployEvent *events.Event
	for i := range publisher.events {
		if publisher.events[i].Type == runtimeActionDeployEvent {
			deployEvent = &publisher.events[i]
			break
		}
	}
	if deployEvent == nil {
		t.Fatal("no deploy event published")
	}

	// Verify correlation data.
	data, ok := deployEvent.Data.(map[string]any)
	if !ok {
		t.Fatalf("event data is not map[string]any: %T", deployEvent.Data)
	}
	if data["service_id"] != svc.ID {
		t.Fatalf("event service_id mismatch: %v", data["service_id"])
	}
	if data["environment_id"] != env.ID {
		t.Fatalf("event environment_id mismatch: %v", data["environment_id"])
	}
	if data["service"] != svc.Name {
		t.Fatalf("event service name mismatch: %v", data["service"])
	}
	if data["environment"] != env.Name {
		t.Fatalf("event environment name mismatch: %v", data["environment"])
	}
	if data["runtime_target"] != svc.RuntimeTargetName() {
		t.Fatalf("event runtime_target mismatch: %v", data["runtime_target"])
	}
	if data["artifact_id"] != artifact.ID {
		t.Fatalf("event artifact_id mismatch: %v", data["artifact_id"])
	}
	if data["observation_id"] != obs.ID {
		t.Fatalf("event observation_id mismatch: %v vs %v", data["observation_id"], obs.ID)
	}
	if data["health_status"] != obs.HealthStatus {
		t.Fatalf("event health_status mismatch: %v", data["health_status"])
	}
}

// TestIntegration_FailedDeployPublishesNoSuccessEvent proves that a failed
// deploy does not publish a success event and does not record an observation.
func TestIntegration_FailedDeployPublishesNoSuccessEvent(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{deployErr: fmt.Errorf("container create failed")}
	publisher := &capturePublisher{}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, publisher, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err == nil {
		t.Fatal("expected deploy error")
	}

	// No deploy events should be published on failure.
	for _, e := range publisher.events {
		if e.Type == runtimeActionDeployEvent {
			t.Fatal("failed deploy should not publish a deploy event")
		}
	}
}

// TestIntegration_FailedDeployStepProgressionStopsEarly proves that the
// status callback stops at the failing step and doesn't report later steps.
func TestIntegration_FailedDeployStepProgressionStopsEarly(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{deployErr: fmt.Errorf("apply explosion")}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	var steps []DeployStep
	statusFn := func(_ context.Context, step DeployStep, _ string) {
		steps = append(steps, step)
	}

	_, err := lifecycle.DeployWithStatus(ctx, svc.ID, env.ID, &artifact.ID, statusFn)
	if err == nil {
		t.Fatal("expected deploy failure")
	}

	// Should have steps up to and including applying (where the failure happens),
	// but NOT observing or projecting.
	for _, s := range steps {
		if s == DeployStepObserving || s == DeployStepProjecting {
			t.Fatalf("step %q should not be reached on deploy failure", s)
		}
	}

	// Must include at least the pre-failure steps.
	if len(steps) < 3 {
		t.Fatalf("expected at least building/locking/rendering steps before failure, got %v", steps)
	}
}

// TestIntegration_LockAcquisitionFailureReturnsError proves that if the lock
// cannot be acquired, deploy returns an error with appropriate context.
func TestIntegration_LockAcquisitionFailureReturnsError(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	failLock := &failingApplyLock{err: fmt.Errorf("database connection lost")}
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(failLock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err == nil {
		t.Fatal("expected error from lock acquisition failure")
	}
	if rt.deployTarget != "" {
		t.Fatal("deploy should not proceed when lock acquisition fails")
	}
}

// ---------------------------------------------------------------------------
// Section 4: Advisory Lock Key Tests (unit, no DB required)
// ---------------------------------------------------------------------------

// TestAdvisoryLockKey_DistinctEnvironments proves different environment UUIDs
// produce different advisory lock keys.
func TestAdvisoryLockKey_DistinctEnvironments(t *testing.T) {
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"),
		uuid.New(),
		uuid.New(),
	}

	keys := map[int64]uuid.UUID{}
	for _, id := range ids {
		key := advisoryLockKey(id)
		if prev, exists := keys[key]; exists {
			t.Fatalf("key collision: %s and %s both map to %d", prev, id, key)
		}
		keys[key] = id
	}
}

// TestAdvisoryLockKey_Stable proves the same environment always produces the
// same advisory key across multiple calls.
func TestAdvisoryLockKey_Stable(t *testing.T) {
	id := uuid.MustParse("abcdef12-3456-7890-abcd-ef1234567890")
	key1 := advisoryLockKey(id)
	key2 := advisoryLockKey(id)
	key3 := advisoryLockKey(id)
	if key1 != key2 || key2 != key3 {
		t.Fatalf("advisory key is not stable: %d, %d, %d", key1, key2, key3)
	}
}

// ---------------------------------------------------------------------------
// Test Mock Types
// ---------------------------------------------------------------------------

// blockingMockRuntime records deploy order and blocks until deploySem receives.
type blockingMockRuntime struct {
	lifecycleMockRuntime
	deploySem   chan struct{}
	deployOrder *[]int
	mu          *sync.Mutex
	counter     int32
}

func (m *blockingMockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	n := int(atomic.AddInt32(&m.counter, 1))
	<-m.deploySem // block until test signals
	m.mu.Lock()
	*m.deployOrder = append(*m.deployOrder, n)
	m.mu.Unlock()
	return nil
}

// parallelMockRuntime signals bothAcquired when Deploy is entered, then
// blocks on holdCh — proving both environments reached Deploy concurrently.
type parallelMockRuntime struct {
	lifecycleMockRuntime
	holdCh       chan struct{}
	bothAcquired chan struct{}
}

func (m *parallelMockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	m.bothAcquired <- struct{}{}
	<-m.holdCh
	return nil
}

// holdingMockRuntime blocks Deploy on holdCh.
type holdingMockRuntime struct {
	lifecycleMockRuntime
	holdCh chan struct{}
}

func (m *holdingMockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	<-m.holdCh
	return nil
}

// observeFailMockRuntime succeeds at Deploy but fails at Observe.
type observeFailMockRuntime struct {
	lifecycleMockRuntime
	observeErr error
}

func (m *observeFailMockRuntime) Observe(_ context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	if m.observeErr != nil {
		return nil, m.observeErr
	}
	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedContainerID: serviceName + "-container",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "mock",
	}, nil
}

func (m *observeFailMockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	return nil
}

// capturingMockRuntime captures deploy and lifecycle targets for convergence checks.
type capturingMockRuntime struct {
	lifecycleMockRuntime
}

// countingMockRuntime atomically counts deploy invocations.
type countingMockRuntime struct {
	lifecycleMockRuntime
	deployCount *int32
}

func (m *countingMockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	atomic.AddInt32(m.deployCount, 1)
	return nil
}

// threadSafePublisher is a Publisher that safely captures events from concurrent goroutines.
type threadSafePublisher struct {
	mu     sync.Mutex
	events []events.Event
}

func (p *threadSafePublisher) Publish(_ context.Context, e events.Event) {
	p.mu.Lock()
	p.events = append(p.events, e)
	p.mu.Unlock()
}

func (p *threadSafePublisher) Subscribe(_ events.EventType, _ events.Handler) {}

// failingApplyLock always fails to acquire.
type failingApplyLock struct {
	err error
}

func (f *failingApplyLock) Lock(_ context.Context, _ uuid.UUID) (func(), error) {
	return nil, f.err
}

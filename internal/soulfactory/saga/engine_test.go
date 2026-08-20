package saga

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryDriver struct {
	stage             Stage
	mu                sync.Mutex
	resource          *Resource
	extraResources    []Resource
	applyExtraKinds   []string
	conflict          bool
	applyErr          error
	applyLeavesAbsent bool
	postApplySpec     string
	inspectErr        error
	applyCount        int
	compensateCount   int
	order             *[]Stage
}

func (d *memoryDriver) Stage() Stage { return d.stage }
func (d *memoryDriver) Inspect(_ context.Context, snap Snapshot, target *Resource) (Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inspectErr != nil {
		return Observation{}, d.inspectErr
	}
	if d.conflict {
		return Observation{Reality: RealityConflict, Detail: "owned by another run"}, nil
	}
	if d.resource == nil && len(d.extraResources) == 0 {
		return Observation{Reality: RealityAbsent}, nil
	}
	resources := append([]Resource(nil), d.extraResources...)
	if d.resource != nil {
		resources = append([]Resource{*d.resource}, resources...)
	}
	if d.stage == StageRunning {
		soul := resources[0]
		soul.Kind = ResourceAgentSoul
		soul.ExternalID = string(d.stage) + "-soul-id"
		resources = append(resources, soul)
	}
	if target != nil {
		for _, resource := range resources {
			if PublicResourceRef(resource.System, resource.Kind, resource.ExternalID) == target.ExternalID {
				return Observation{Reality: RealityMatching, Resources: []Resource{resource}}, nil
			}
		}
		return Observation{Reality: RealityAbsent}, nil
	}
	return Observation{Reality: RealityMatching, Resources: resources}, nil
}
func (d *memoryDriver) Apply(_ context.Context, snap Snapshot, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyCount++
	kind := "resource"
	if d.stage == StageRunning {
		kind = ResourceProvisioningResult
	}
	resource := Resource{Stage: d.stage, System: systemFor(d.stage), Kind: kind, ExternalID: string(d.stage) + "-id", SpecHash: snap.SpecHash, Ownership: OwnershipCreated, OwnerRunID: snap.RunID, IdempotencyKey: key, CorrelationID: snap.RequestID, CompensationOrder: rankFor(d.stage)}
	if d.postApplySpec != "" {
		resource.SpecHash = d.postApplySpec
	}
	if d.stage == StageRunning {
		resource.AuthoritativeStage = StageRunning
	}
	if !d.applyLeavesAbsent {
		d.resource = &resource
		for _, extraKind := range d.applyExtraKinds {
			extra := resource
			extra.Kind = extraKind
			extra.ExternalID = string(d.stage) + "-" + extraKind + "-id"
			d.extraResources = append(d.extraResources, extra)
		}
	}
	return d.applyErr
}

func (d *memoryDriver) InspectTerminal(ctx context.Context, snap Snapshot, stage Stage, failure *Failure) (Observation, error) {
	obs, err := d.Inspect(ctx, snap, nil)
	if err != nil || obs.Reality != RealityMatching {
		return obs, err
	}
	for _, resource := range obs.Resources {
		if resource.AuthoritativeStage != stage {
			return Observation{Reality: RealityConflict}, nil
		}
	}
	return obs, nil
}

func (d *memoryDriver) PublishTerminal(_ context.Context, snap Snapshot, stage Stage, _ *Failure, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyCount++
	d.resource = &Resource{Stage: stage, System: SystemBahiaProjection, Kind: ResourceProvisioningResult, ExternalID: string(stage) + "-result-id", SpecHash: snap.SpecHash, Ownership: OwnershipCreated, OwnerRunID: snap.RunID, IdempotencyKey: key, CorrelationID: snap.RequestID, AuthoritativeStage: stage, CompensationOrder: CompensateProjection}
	return d.applyErr
}
func (d *memoryDriver) Compensate(_ context.Context, _ Snapshot, resource Resource, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.compensateCount++
	if d.order != nil {
		*d.order = append(*d.order, d.stage)
	}
	if d.resource != nil && PublicResourceRef(d.resource.System, d.resource.Kind, d.resource.ExternalID) == resource.ExternalID {
		d.resource = nil
	} else {
		filtered := d.extraResources[:0]
		for _, extra := range d.extraResources {
			if PublicResourceRef(extra.System, extra.Kind, extra.ExternalID) != resource.ExternalID {
				filtered = append(filtered, extra)
			}
		}
		d.extraResources = filtered
	}
	return nil
}

func systemFor(stage Stage) string {
	if stage == StageSignerEnrolled {
		return "signet"
	}
	if stage == StageRuntimeAllocated {
		return "runtime"
	}
	if stage == StageRunning {
		return "bahia_projection"
	}
	return "openclaw"
}
func rankFor(stage Stage) CompensationRank {
	switch stage {
	case StageSignerEnrolled:
		return CompensateSignetPolicy
	case StageRuntimeAllocated:
		return CompensateContainer
	case StageRunning:
		return CompensateProjection
	default:
		return CompensateCredentials
	}
}

func fixtureEngine(t *testing.T, mutate func(map[Stage]*memoryDriver)) (*Engine, map[Stage]*memoryDriver, Store) {
	t.Helper()
	drivers := map[Stage]*memoryDriver{}
	interfaces := make([]StageDriver, 0, len(forwardStages))
	for _, stage := range forwardStages {
		driver := &memoryDriver{stage: stage}
		drivers[stage] = driver
		interfaces = append(interfaces, driver)
	}
	if mutate != nil {
		mutate(drivers)
	}
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store, interfaces)
	if err != nil {
		t.Fatal(err)
	}
	return engine, drivers, store
}

func TestReconcilePersistsEveryStageAndReplayIsSideEffectFree(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, nil)
	ctx := context.Background()
	if _, err := engine.Start(ctx, "request-1", "run-1", "agent-1", "sha256:spec"); err != nil {
		t.Fatal(err)
	}
	report, err := engine.Reconcile(ctx, "request-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stage != StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	run, err := store.Load(ctx, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Resources) != len(forwardStages)+1 {
		t.Fatalf("resources = %d", len(run.Resources))
	}
	if len(run.Transitions) != len(forwardStages) {
		t.Fatalf("transitions = %d", len(run.Transitions))
	}
	version := run.Version
	if _, err := engine.Reconcile(ctx, "request-1", false); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Load(ctx, "request-1")
	if after.Version != version {
		t.Fatalf("replay changed version %d -> %d", version, after.Version)
	}
	for stage, driver := range drivers {
		if driver.applyCount != 1 {
			t.Fatalf("%s apply count = %d", stage, driver.applyCount)
		}
	}
}

func TestAmbiguousApplyRefetchesAndCorrelatesWithoutBlindRepeat(t *testing.T) {
	ambiguous := &SafeError{Code: "response_lost", Message: "runtime response was not observed", Retryable: true}
	engine, drivers, _ := fixtureEngine(t, func(ds map[Stage]*memoryDriver) { ds[StageRuntimeAllocated].applyErr = ambiguous })
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-2", "run-2", "agent-2", "sha256:spec")
	report, err := engine.Reconcile(ctx, "request-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stage != StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	if drivers[StageRuntimeAllocated].applyCount != 1 {
		t.Fatalf("runtime apply count = %d", drivers[StageRuntimeAllocated].applyCount)
	}
}

func TestConflictIsRejectedBeforeMutationAndCreatedResourcesRollBack(t *testing.T) {
	var order []Stage
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		for _, driver := range ds {
			driver.order = &order
		}
		ds[StageSignerEnrolled].conflict = true
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-3", "run-3", "agent-3", "sha256:spec")
	_, err := engine.Reconcile(ctx, "request-3", false)
	if err == nil {
		t.Fatal("expected conflict")
	}
	run, _ := store.Load(ctx, "request-3")
	if run.Stage != StageRolledBack {
		t.Fatalf("stage = %s", run.Stage)
	}
	if drivers[StageSignerEnrolled].applyCount != 0 {
		t.Fatal("conflicting stage mutated")
	}
	if fmt.Sprint(order) != fmt.Sprint([]Stage{StageIdentityReserved, StageRuntimeAllocated}) {
		t.Fatalf("compensation order = %v", order)
	}
}

func TestSafeAbortNeverDeletesAdoptedResource(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageIdentityReserved].resource = &Resource{Stage: StageIdentityReserved, System: "signet", Kind: "identity", ExternalID: "incumbent", SpecHash: "sha256:spec", Ownership: OwnershipAdopted, IdempotencyKey: "external:lineage", CorrelationID: "request-4", CompensationOrder: CompensateSignetPolicy}
		ds[StageRuntimeAllocated].inspectErr = errors.New("runtime unavailable")
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-4", "run-4", "agent-4", "sha256:spec")
	_, _ = engine.Reconcile(ctx, "request-4", false)
	run, _ := store.Load(ctx, "request-4")
	if run.Stage != StageFailedRecoverable {
		t.Fatalf("stage = %s", run.Stage)
	}
	if _, err := engine.SafeAbort(ctx, "request-4", false); err != nil {
		t.Fatal(err)
	}
	if drivers[StageIdentityReserved].compensateCount != 0 {
		t.Fatal("adopted identity was compensated")
	}
	if drivers[StageIdentityReserved].resource == nil {
		t.Fatal("adopted identity was deleted")
	}
}

func TestStaleCheckpointCannotOverwriteNewerSuccess(t *testing.T) {
	engine, _, store := fixtureEngine(t, nil)
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-5", "run-5", "agent-5", "sha256:spec")
	stale, _ := store.Load(ctx, "request-5")
	if _, err := engine.Reconcile(ctx, "request-5", false); err != nil {
		t.Fatal(err)
	}
	stale.Stage = StageFailedTerminal
	stale.Failure = &Failure{Stage: StageRuntimeAllocated, Code: "old", Message: "old failure", At: time.Now()}
	stale.Version++
	if err := store.Save(ctx, stale, stale.Version-1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	current, _ := store.Load(ctx, "request-5")
	if current.Stage != StageRunning {
		t.Fatalf("newer success overwritten: %s", current.Stage)
	}
}

func TestDryRunInspectsWithoutMutation(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, nil)
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-6", "run-6", "agent-6", "sha256:spec")
	report, err := engine.Reconcile(ctx, "request-6", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Actions) != 1 || report.Actions[0].Operation != "create" {
		t.Fatalf("actions = %#v", report.Actions)
	}
	run, _ := store.Load(ctx, "request-6")
	if run.Stage != StageRequested || run.Version != 1 {
		t.Fatalf("dry run mutated state: %#v", run)
	}
	if drivers[StageIdentityReserved].applyCount != 0 {
		t.Fatal("dry run applied mutation")
	}
}

func TestRetentionPurgesOnlyExpiredFailedState(t *testing.T) {
	engine, _, store := fixtureEngine(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	run, _ := engine.Start(ctx, "request-7", "run-7", "agent-7", "sha256:spec")
	run.Stage = StageFailedTerminal
	expired := now.Add(-time.Hour)
	run.RetainUntil = &expired
	run.Version++
	if err := store.Save(ctx, run, run.Version-1); err != nil {
		t.Fatal(err)
	}
	removed, err := PurgeExpired(ctx, store, now)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if _, err := store.Load(ctx, "request-7"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load error = %v", err)
	}
}

func TestRetryResumesFailedStageInsteadOfSkippingIt(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) { ds[StageRuntimeAllocated].inspectErr = errors.New("runtime offline") })
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-retry", "run-retry", "agent-retry", "sha256:spec")
	_, _ = engine.Reconcile(ctx, "request-retry", false)
	drivers[StageRuntimeAllocated].mu.Lock()
	drivers[StageRuntimeAllocated].inspectErr = nil
	drivers[StageRuntimeAllocated].mu.Unlock()
	report, err := engine.Retry(ctx, "request-retry", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stage != StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	run, _ := store.Load(ctx, "request-retry")
	found := false
	for _, resource := range run.Resources {
		if resource.Stage == StageRuntimeAllocated {
			found = true
		}
	}
	if !found {
		t.Fatal("failed runtime stage was skipped")
	}
}

func TestNonRetryableMutationFailureCompensatesPartialRun(t *testing.T) {
	denied := &SafeError{Code: "policy_denied", Message: "signer policy denied", Retryable: false}
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageSignerEnrolled].applyErr = denied
		ds[StageSignerEnrolled].applyLeavesAbsent = true
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-denied", "run-denied", "agent-denied", "sha256:spec")
	_, err := engine.Reconcile(ctx, "request-denied", false)
	if err == nil {
		t.Fatal("expected denial")
	}
	run, _ := store.Load(ctx, "request-denied")
	if run.Stage != StageRolledBack {
		t.Fatalf("stage = %s", run.Stage)
	}
	if drivers[StageRuntimeAllocated].compensateCount != 1 || drivers[StageIdentityReserved].compensateCount != 1 {
		t.Fatalf("partial resources were not compensated")
	}
}

func TestMultiResourceStageCompensatesEachResourceIndependently(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageRuntimeAllocated].applyExtraKinds = []string{"account", "route"}
		ds[StageSignerEnrolled].conflict = true
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-multi", "run-multi", "agent-multi", "sha256:spec")
	_, err := engine.Reconcile(ctx, "request-multi", false)
	if err == nil {
		t.Fatal("expected conflict")
	}
	run, _ := store.Load(ctx, "request-multi")
	if run.Stage != StageRolledBack {
		t.Fatalf("stage = %s", run.Stage)
	}
	if drivers[StageRuntimeAllocated].compensateCount != 3 {
		t.Fatalf("runtime compensation count = %d", drivers[StageRuntimeAllocated].compensateCount)
	}
}

func TestMatchingWrongSpecIsRejectedBeforeMutation(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageIdentityReserved].resource = &Resource{Stage: StageIdentityReserved, System: "signet", Kind: "identity", ExternalID: "wrong-spec", SpecHash: "sha256:other", Ownership: OwnershipAdopted, IdempotencyKey: "external", CorrelationID: "request-spec", CompensationOrder: CompensateSignetPolicy}
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-spec", "run-spec", "agent-spec", "sha256:spec")
	_, err := engine.Reconcile(ctx, "request-spec", false)
	if err == nil {
		t.Fatal("expected spec conflict")
	}
	run, _ := store.Load(ctx, "request-spec")
	if run.Stage != StageRolledBack {
		t.Fatalf("stage = %s", run.Stage)
	}
	if drivers[StageIdentityReserved].applyCount != 0 {
		t.Fatal("wrong-spec match triggered mutation")
	}
}

func TestStoreRejectsCurrentVersionLineageRewrite(t *testing.T) {
	engine, _, store := fixtureEngine(t, nil)
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-immutable", "run-immutable", "agent-immutable", "sha256:spec")
	run, _ := store.Load(ctx, "request-immutable")
	run.AgentID = "rewritten-agent"
	run.Version++
	if err := store.Save(ctx, run, run.Version-1); !errors.Is(err, ErrConflict) {
		t.Fatalf("rewrite error = %v", err)
	}
}

func TestRolledBackTerminalProjectionContainsCorrelated7950And31951(t *testing.T) {
	engine, _, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) { ds[StageSignerEnrolled].conflict = true })
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-terminal", "run-terminal", "agent-terminal", "sha256:spec")
	_, _ = engine.Reconcile(ctx, "request-terminal", false)
	run, _ := store.Load(ctx, "request-terminal")
	if run.Stage != StageRolledBack {
		t.Fatalf("stage = %s", run.Stage)
	}
	seen := map[string]bool{}
	for _, resource := range run.Resources {
		if resource.System == SystemBahiaProjection && resource.AuthoritativeStage == StageRolledBack && resource.CorrelationID == run.RequestID && resource.SpecHash == run.SpecHash {
			seen[resource.Kind] = true
		}
	}
	if !seen[ResourceProvisioningResult] || !seen[ResourceAgentSoul] {
		t.Fatalf("terminal lineage = %#v", seen)
	}
}

func TestPublicFailureAndOperatorReportDoNotEchoAdapterSecrets(t *testing.T) {
	secret := "token=should-not-appear"
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageIdentityReserved].inspectErr = &SafeError{Code: secret, Message: secret, Retryable: true}
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-secret", "run-secret", "agent-secret", "sha256:spec")
	_, returned := engine.Reconcile(ctx, "request-secret", false)
	if returned == nil || strings.Contains(returned.Error(), secret) {
		t.Fatalf("unsafe returned error = %v", returned)
	}
	run, _ := store.Load(ctx, "request-secret")
	if run.Failure == nil || run.Failure.Code != "stage_failed" || run.Failure.Message == secret {
		t.Fatalf("unsafe failure = %#v", run.Failure)
	}
	drivers[StageIdentityReserved].inspectErr = nil
	report, _ := engine.Inspect(ctx, "request-secret")
	encoded := fmt.Sprintf("%#v", report)
	if strings.Contains(encoded, secret) {
		t.Fatalf("operator report leaked secret: %s", encoded)
	}
}

func TestRestartLoadsCheckpointAndResumesFromInspectedReality(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) { ds[StageRuntimeAllocated].inspectErr = errors.New("runtime restart") })
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-restart", "run-restart", "agent-restart", "sha256:spec")
	_, _ = engine.Reconcile(ctx, "request-restart", false)
	drivers[StageRuntimeAllocated].inspectErr = nil
	fileStore := store.(*FileStore)
	reopened, err := NewFileStore(fileStore.dir)
	if err != nil {
		t.Fatal(err)
	}
	interfaces := make([]StageDriver, 0, len(forwardStages))
	for _, stage := range forwardStages {
		interfaces = append(interfaces, drivers[stage])
	}
	restarted, err := NewEngine(reopened, interfaces)
	if err != nil {
		t.Fatal(err)
	}
	report, err := restarted.Retry(ctx, "request-restart", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stage != StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	if drivers[StageIdentityReserved].applyCount != 1 {
		t.Fatal("restart replayed completed identity side effect")
	}
}

func TestConcurrentRunsRemainIsolatedInSharedDurableStore(t *testing.T) {
	dir := t.TempDir()
	storeA, _ := NewFileStore(dir)
	storeB, _ := NewFileStore(dir)
	newIsolated := func(store Store) *Engine {
		interfaces := make([]StageDriver, 0, len(forwardStages))
		for _, stage := range forwardStages {
			interfaces = append(interfaces, &memoryDriver{stage: stage})
		}
		engine, err := NewEngine(store, interfaces)
		if err != nil {
			t.Fatal(err)
		}
		return engine
	}
	engineA, engineB := newIsolated(storeA), newIsolated(storeB)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, item := range []struct {
		engine              *Engine
		request, run, agent string
	}{{engineA, "request-a", "run-a", "agent-a"}, {engineB, "request-b", "run-b", "agent-b"}} {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := item.engine.Start(ctx, item.request, item.run, item.agent, "sha256:spec"); err != nil {
				errs <- err
				return
			}
			_, err := item.engine.Reconcile(ctx, item.request, false)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	runA, _ := storeA.Load(ctx, "request-a")
	runB, _ := storeB.Load(ctx, "request-b")
	if runA.Stage != StageRunning || runB.Stage != StageRunning || runA.AgentID == runB.AgentID {
		t.Fatalf("isolated runs = %#v %#v", runA, runB)
	}
}

func TestFailureMatrixCompensatesAtEveryForwardStage(t *testing.T) {
	for _, failedStage := range forwardStages {
		t.Run(string(failedStage), func(t *testing.T) {
			denial := &SafeError{Code: "policy_denied", Message: "not persisted", Retryable: false}
			engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
				ds[failedStage].applyErr = denial
				ds[failedStage].applyLeavesAbsent = true
			})
			ctx := context.Background()
			request := "matrix-" + string(failedStage)
			_, _ = engine.Start(ctx, request, "run-"+string(failedStage), "agent-"+string(failedStage), "sha256:spec")
			_, err := engine.Reconcile(ctx, request, false)
			if err == nil {
				t.Fatal("expected injected failure")
			}
			run, _ := store.Load(ctx, request)
			if run.Stage != StageRolledBack {
				t.Fatalf("stage = %s", run.Stage)
			}
			for stage, driver := range drivers {
				if stage == StageRunning {
					continue
				}
				if driver.resource != nil || len(driver.extraResources) > 0 {
					t.Fatalf("orphan at %s", stage)
				}
			}
		})
	}
}

func TestRecoverableRunWithOwnedResourcesIsNotPurged(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) { ds[StageRuntimeAllocated].inspectErr = errors.New("offline") })
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-retain", "run-retain", "agent-retain", "sha256:spec")
	_, _ = engine.Reconcile(ctx, "request-retain", false)
	run, _ := store.Load(ctx, "request-retain")
	if run.Stage != StageFailedRecoverable || len(run.Resources) == 0 || run.RetainUntil != nil {
		t.Fatalf("recoverable retention = %#v", run)
	}
	removed, err := PurgeExpired(ctx, store, time.Now().Add(365*24*time.Hour))
	if err != nil || removed != 0 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if drivers[StageIdentityReserved].resource == nil {
		t.Fatal("test did not retain external owned resource")
	}
}

func TestWrongPostApplyRealityIsCheckpointedAndCompensated(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) { ds[StageRuntimeAllocated].postApplySpec = "sha256:wrong" })
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-post-conflict", "run-post-conflict", "agent-post-conflict", "sha256:spec")
	_, err := engine.Reconcile(ctx, "request-post-conflict", false)
	if err == nil {
		t.Fatal("expected post-apply conflict")
	}
	run, _ := store.Load(ctx, "request-post-conflict")
	if run.Stage != StageRolledBack {
		t.Fatalf("stage=%s", run.Stage)
	}
	found := false
	for _, resource := range run.Resources {
		if resource.Stage == StageRuntimeAllocated && resource.Conflict {
			found = true
		}
	}
	if !found {
		t.Fatal("conflicting created lineage was not checkpointed")
	}
	if drivers[StageRuntimeAllocated].resource != nil {
		t.Fatal("conflicting created runtime was orphaned")
	}
}

func TestResponseLossAfterEverySideEffectRefetchesWithoutDuplicate(t *testing.T) {
	for _, stage := range forwardStages {
		t.Run(string(stage), func(t *testing.T) {
			lost := &SafeError{Code: "response_lost", Message: "not persisted", Retryable: true}
			engine, drivers, _ := fixtureEngine(t, func(ds map[Stage]*memoryDriver) { ds[stage].applyErr = lost })
			ctx := context.Background()
			request := "lost-" + string(stage)
			_, _ = engine.Start(ctx, request, "run-"+string(stage), "agent-"+string(stage), "sha256:spec")
			report, err := engine.Reconcile(ctx, request, false)
			if err != nil {
				t.Fatal(err)
			}
			if report.Stage != StageRunning {
				t.Fatalf("stage=%s", report.Stage)
			}
			if drivers[stage].applyCount != 1 {
				t.Fatalf("apply count=%d", drivers[stage].applyCount)
			}
		})
	}
}

func TestProductionShapedFailureClassesRemainRecoverableOrCompensated(t *testing.T) {
	cases := []struct {
		name    string
		stage   Stage
		inspect bool
		cause   error
	}{
		{"identity_timeout", StageIdentityReserved, true, errors.New("timeout")},
		{"container_failure", StageRuntimeAllocated, false, &SafeError{Code: "stage_failed", Retryable: false}},
		{"signet_denial", StageSignerEnrolled, false, &SafeError{Code: "policy_denied", Retryable: false}},
		{"relay_disconnect", StageNostrConfigured, true, errors.New("relay disconnected")},
		{"invalid_model", StageLLMVerified, false, &SafeError{Code: "stage_failed", Retryable: false}},
		{"dm_relay_disconnect", StageDMVerified, true, errors.New("relay disconnected")},
		{"bahia_publish_timeout", StageRunning, true, errors.New("publish timeout")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
				if tc.inspect {
					ds[tc.stage].inspectErr = tc.cause
				} else {
					ds[tc.stage].applyErr = tc.cause
					ds[tc.stage].applyLeavesAbsent = true
				}
			})
			ctx := context.Background()
			request := "class-" + tc.name
			_, _ = engine.Start(ctx, request, "run-"+tc.name, "agent-"+tc.name, "sha256:spec")
			_, err := engine.Reconcile(ctx, request, false)
			if err == nil {
				t.Fatal("expected injected failure")
			}
			run, _ := store.Load(ctx, request)
			if tc.inspect {
				if run.Stage != StageFailedRecoverable {
					t.Fatalf("stage=%s", run.Stage)
				}
				drivers[tc.stage].inspectErr = nil
				if _, abortErr := engine.SafeAbort(ctx, request, false); abortErr != nil {
					t.Fatal(abortErr)
				}
				run, _ = store.Load(ctx, request)
			}
			if run.Stage != StageRolledBack {
				t.Fatalf("final stage=%s", run.Stage)
			}
		})
	}
}

func TestRestartPreinspectionCheckpointsAndCompensatesOwnedConflict(t *testing.T) {
	engine, drivers, store := fixtureEngine(t, func(ds map[Stage]*memoryDriver) {
		ds[StageIdentityReserved].resource = &Resource{Stage: StageIdentityReserved, System: "signet", Kind: "identity", ExternalID: "created-before-crash", SpecHash: "sha256:wrong", Ownership: OwnershipCreated, OwnerRunID: "run-crash-conflict", IdempotencyKey: "lost", CorrelationID: "wrong-correlation", CompensationOrder: CompensateSignetPolicy}
	})
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-crash-conflict", "run-crash-conflict", "agent-crash-conflict", "sha256:spec")
	_, err := engine.Reconcile(ctx, "request-crash-conflict", false)
	if err == nil {
		t.Fatal("expected conflict")
	}
	run, _ := store.Load(ctx, "request-crash-conflict")
	if run.Stage != StageRolledBack {
		t.Fatalf("stage=%s", run.Stage)
	}
	found := false
	for _, resource := range run.Resources {
		if resource.Stage == StageIdentityReserved && resource.Conflict {
			found = true
		}
	}
	if !found {
		t.Fatal("restart conflict lineage was not checkpointed")
	}
	if drivers[StageIdentityReserved].resource != nil {
		t.Fatal("created-before-crash resource was orphaned")
	}
}

func TestTerminalDriverErrorsAreRedactedFromOperator(t *testing.T) {
	secret := "token=terminal-secret"
	engine, drivers, _ := fixtureEngine(t, nil)
	ctx := context.Background()
	_, _ = engine.Start(ctx, "request-terminal-secret", "run-terminal-secret", "agent-terminal-secret", "sha256:spec")
	if _, err := engine.Reconcile(ctx, "request-terminal-secret", false); err != nil {
		t.Fatal(err)
	}
	drivers[StageRunning].inspectErr = errors.New(secret)
	_, err := engine.Reconcile(ctx, "request-terminal-secret", false)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe terminal error=%v", err)
	}
}

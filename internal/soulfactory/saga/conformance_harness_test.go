package saga

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type conformanceDriver struct {
	stage          Stage
	mu             sync.Mutex
	resources      map[string][]Resource
	applyCount     map[string]int
	failRequest    string
	disconnectOnce string
	disconnected   bool
}

func newConformanceDrivers() ([]StageDriver, map[Stage]*conformanceDriver) {
	drivers := make([]StageDriver, 0, len(forwardStages))
	byStage := make(map[Stage]*conformanceDriver, len(forwardStages))
	for _, stage := range forwardStages {
		driver := &conformanceDriver{stage: stage, resources: map[string][]Resource{}, applyCount: map[string]int{}}
		drivers = append(drivers, driver)
		byStage[stage] = driver
	}
	return drivers, byStage
}

func (d *conformanceDriver) Stage() Stage { return d.stage }

func (d *conformanceDriver) Inspect(_ context.Context, snap Snapshot, target *Resource) (Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disconnected {
		return Observation{}, errors.New("disposable relay disconnected")
	}
	if d.disconnectOnce == snap.RequestID {
		d.disconnectOnce = ""
		return Observation{}, errors.New("disposable relay disconnected before EOSE")
	}
	resources := append([]Resource(nil), d.resources[snap.RequestID]...)
	if target != nil {
		for _, resource := range resources {
			if PublicResourceRef(resource.System, resource.Kind, resource.ExternalID) == target.ExternalID {
				return Observation{Reality: RealityMatching, Resources: []Resource{resource}}, nil
			}
		}
		return Observation{Reality: RealityAbsent}, nil
	}
	if len(resources) == 0 {
		return Observation{Reality: RealityAbsent}, nil
	}
	return Observation{Reality: RealityMatching, Resources: resources}, nil
}

func (d *conformanceDriver) Apply(_ context.Context, snap Snapshot, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyCount[snap.RequestID]++
	if d.failRequest == snap.RequestID {
		return &SafeError{Code: "stage_failed", Retryable: false}
	}
	resource := Resource{
		Stage: d.stage, System: systemFor(d.stage), Kind: "resource",
		ExternalID: fmt.Sprintf("%s/%s/%s", d.stage, snap.AgentID, snap.RunID),
		SpecHash:   snap.SpecHash, Ownership: OwnershipCreated, OwnerRunID: snap.RunID,
		IdempotencyKey: key, CorrelationID: snap.RequestID, CompensationOrder: rankFor(d.stage),
	}
	if d.stage == StageDMVerified {
		resource.Kind = "nip17_dm_roundtrip"
		resource.ExternalID = "dm/" + snap.AgentID + "/reply-to-" + snap.RequestID
	}
	if d.stage == StageRunning {
		resource.System = SystemBahiaProjection
		resource.Kind = ResourceProvisioningResult
		resource.AuthoritativeStage = StageRunning
		soul := resource
		soul.Kind = ResourceAgentSoul
		soul.ExternalID = "soul/" + snap.AgentID + "/" + snap.RunID
		d.resources[snap.RequestID] = []Resource{resource, soul}
		return nil
	}
	d.resources[snap.RequestID] = []Resource{resource}
	return nil
}

func (d *conformanceDriver) Compensate(_ context.Context, snap Snapshot, _ Resource, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.resources, snap.RequestID)
	return nil
}

func (d *conformanceDriver) InspectTerminal(ctx context.Context, snap Snapshot, _ Stage, _ *Failure) (Observation, error) {
	return d.Inspect(ctx, snap, nil)
}

func (d *conformanceDriver) PublishTerminal(_ context.Context, snap Snapshot, stage Stage, _ *Failure, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := Resource{
		Stage: stage, System: SystemBahiaProjection, Kind: ResourceProvisioningResult,
		ExternalID: "terminal/" + snap.RequestID, SpecHash: snap.SpecHash,
		Ownership: OwnershipCreated, OwnerRunID: snap.RunID, IdempotencyKey: key,
		CorrelationID: snap.RequestID, AuthoritativeStage: stage, CompensationOrder: CompensateProjection,
	}
	soul := result
	soul.Kind = ResourceAgentSoul
	soul.ExternalID = "terminal-soul/" + snap.AgentID
	d.resources[snap.RequestID] = []Resource{result, soul}
	return nil
}

func newConformanceEngine(t *testing.T, dir string, drivers []StageDriver) (*Engine, Store) {
	t.Helper()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store, drivers)
	if err != nil {
		t.Fatal(err)
	}
	return engine, store
}

func provisionConformanceSoul(ctx context.Context, engine *Engine, request, run, agent string) error {
	if _, err := engine.Start(ctx, request, run, agent, "sha256:"+agent); err != nil {
		return err
	}
	report, err := engine.Reconcile(ctx, request, false)
	if err != nil {
		return err
	}
	if report.Stage != StageRunning {
		return fmt.Errorf("terminal stage = %s", report.Stage)
	}
	return nil
}

func TestOpenClawProvisioningConformanceDisposableEnvironment(t *testing.T) {
	ctx := context.Background()
	interfaces, drivers := newConformanceDrivers()
	dir := t.TempDir()
	engine, store := newConformanceEngine(t, dir, interfaces)

	t.Run("two souls sequentially remain isolated and pass DM round trips", func(t *testing.T) {
		for _, soul := range []string{"sequential-a", "sequential-b"} {
			if err := provisionConformanceSoul(ctx, engine, "request-"+soul, "run-"+soul, "agent-"+soul); err != nil {
				t.Fatal(err)
			}
		}
		dmA := drivers[StageDMVerified].resources["request-sequential-a"][0]
		dmB := drivers[StageDMVerified].resources["request-sequential-b"][0]
		if dmA.ExternalID == dmB.ExternalID || dmA.CorrelationID == dmB.CorrelationID {
			t.Fatalf("DM routes crossed: %#v %#v", dmA, dmB)
		}
	})

	t.Run("two souls concurrently remain isolated", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, soul := range []string{"concurrent-a", "concurrent-b"} {
			soul := soul
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- provisionConformanceSoul(ctx, engine, "request-"+soul, "run-"+soul, "agent-"+soul)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		runA, _ := store.Load(ctx, "request-concurrent-a")
		runB, _ := store.Load(ctx, "request-concurrent-b")
		if runA.AgentID == runB.AgentID || runA.RunID == runB.RunID || runA.Stage != StageRunning || runB.Stage != StageRunning {
			t.Fatalf("concurrent isolation failed: %#v %#v", runA, runB)
		}
	})

	t.Run("exact replay and Bahia Signet runtime restarts do not repeat effects", func(t *testing.T) {
		request := "request-sequential-a"
		before := drivers[StageSignerEnrolled].applyCount[request]
		restarted, _ := newConformanceEngine(t, dir, interfaces)
		if _, err := restarted.Reconcile(ctx, request, false); err != nil {
			t.Fatal(err)
		}
		if drivers[StageSignerEnrolled].applyCount[request] != before {
			t.Fatal("restart replayed Signet enrollment")
		}
		if _, err := restarted.Start(ctx, request, "conflicting-run", "agent-sequential-a", "sha256:agent-sequential-a"); !errors.Is(err, ErrConflict) {
			t.Fatalf("conflicting replay error = %v", err)
		}
	})

	t.Run("relay disconnect resumes from backfill without duplicate mutation", func(t *testing.T) {
		request := "request-backfill"
		drivers[StageNostrConfigured].disconnectOnce = request
		if _, err := engine.Start(ctx, request, "run-backfill", "agent-backfill", "sha256:agent-backfill"); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Reconcile(ctx, request, false); err == nil {
			t.Fatal("expected relay disconnect")
		}
		report, err := engine.Retry(ctx, request, false)
		if err != nil {
			t.Fatal(err)
		}
		if report.Stage != StageRunning || drivers[StageNostrConfigured].applyCount[request] != 1 {
			t.Fatalf("backfill recovery report=%#v apply=%d", report, drivers[StageNostrConfigured].applyCount[request])
		}
	})

	t.Run("every stage failure compensates without orphan candidates", func(t *testing.T) {
		for _, failedStage := range forwardStages {
			failedStage := failedStage
			t.Run(string(failedStage), func(t *testing.T) {
				stageInterfaces, stageDrivers := newConformanceDrivers()
				stageEngine, stageStore := newConformanceEngine(t, t.TempDir(), stageInterfaces)
				request := "failure-" + string(failedStage)
				stageDrivers[failedStage].failRequest = request
				_, _ = stageEngine.Start(ctx, request, "run-"+string(failedStage), "agent-"+string(failedStage), "sha256:failure")
				if _, err := stageEngine.Reconcile(ctx, request, false); err == nil {
					t.Fatal("expected injected failure")
				}
				monitor, err := NewMonitor(MonitorConfig{Store: stageStore, Instance: "ci-disposable", Build: "test"})
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := monitor.Collect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Runs[0].Stage != StageRolledBack || snapshot.Runs[0].OrphanCandidates != 0 {
					t.Fatalf("failure compensation = %#v", snapshot.Runs[0])
				}
			})
		}
	})
}

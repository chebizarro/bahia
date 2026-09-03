package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeMaintenancePublisher struct {
	calls        []string // method:worker[:paths]
	afterPublish func(method string)
}

func (f *fakeMaintenancePublisher) record(method string, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error) {
	entry := method + ":" + cmd.WorkerPubKey
	if len(cmd.Paths) > 0 {
		entry += ":" + strings.Join(cmd.Paths, ",")
	}
	f.calls = append(f.calls, entry)
	if f.afterPublish != nil {
		f.afterPublish(method)
	}
	return &controlplane.WorkerCommandReceipt{Command: method, WorkerPubKey: cmd.WorkerPubKey}, nil
}

func (f *fakeMaintenancePublisher) PublishScan(_ context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error) {
	return f.record("maintenance/scan", cmd)
}
func (f *fakeMaintenancePublisher) PublishQuarantine(_ context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error) {
	return f.record("maintenance/quarantine", cmd)
}
func (f *fakeMaintenancePublisher) PublishRelocate(_ context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error) {
	return f.record("maintenance/relocate", cmd)
}
func (f *fakeMaintenancePublisher) PublishGC(_ context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error) {
	return f.record("maintenance/gc", cmd)
}
func (f *fakeMaintenancePublisher) PublishPressure(_ context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error) {
	return f.record("maintenance/pressure", cmd)
}

type fakeObservationSource struct {
	observations map[string]*HygieneObservation
}

func (f *fakeObservationSource) Latest(_ context.Context, worker string) (*HygieneObservation, error) {
	return f.observations[worker], nil
}

type fakeHygieneMetrics struct {
	scans, breaches int
	candidates      map[string]int
	actions         map[string]int
}

func newFakeHygieneMetrics() *fakeHygieneMetrics {
	return &fakeHygieneMetrics{candidates: map[string]int{}, actions: map[string]int{}}
}
func (m *fakeHygieneMetrics) RecordHygieneScan() { m.scans++ }
func (m *fakeHygieneMetrics) RecordHygieneCandidates(class string, count int) {
	m.candidates[class] += count
}
func (m *fakeHygieneMetrics) RecordHygieneAction(method, status string) {
	m.actions[method+":"+status]++
}
func (m *fakeHygieneMetrics) RecordHygienePressureBreach() { m.breaches++ }

func testHygienePolicy(mutate func(*domain.HygienePolicy)) domain.HygienePolicy {
	policy := domain.HygienePolicy{
		SchemaVersion:  domain.HygienePolicySchemaVersion,
		ID:             "test-policy",
		Enabled:        true,
		ScanRoots:      []string{"/home/agents/work"},
		ProtectedPaths: []string{"/home/agents/.signet"},
		AutoQuarantine: true,
		AutoGC:         true,
	}
	if mutate != nil {
		mutate(&policy)
	}
	return policy
}

func TestHygieneReconcilerIssuesScanAndTier1Actions(t *testing.T) {
	publisher := &fakeMaintenancePublisher{}
	metrics := newFakeHygieneMetrics()
	source := &fakeObservationSource{observations: map[string]*HygieneObservation{
		"worker-a": {
			WorkerPubKey: "worker-a",
			Candidates: []HygieneCandidate{
				{Path: "/home/agents/work/dup", Class: domain.HygieneClassDupClone, Canonical: "/home/agents/canonical/dup"},
				{Path: "/home/agents/work/node_modules", Class: domain.HygieneClassCruft},
				{Path: "/home/agents/work/dirty", Class: domain.HygieneClassDupClone, Canonical: "/x", Blocked: "uncommitted changes"},
				{Path: "/home/agents/work/dump.bak", Class: domain.HygieneClassMisplacedBackup},
				{Path: "/home/agents/work/scratch", Class: domain.HygieneClassOrphanClone, Blocked: "report-only"},
			},
			ObservedAt: time.Now(),
		},
	}}
	rec, err := NewHygieneReconciler(testHygienePolicy(nil), []string{"worker-a"}, publisher, source, metrics, time.Minute, zap.NewNop())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	result, err := rec.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.ScansIssued != 1 || metrics.scans != 1 {
		t.Fatalf("scans issued = %d / %d", result.ScansIssued, metrics.scans)
	}
	// Tier-1: exactly one quarantine intent covering dup + cruft, NOT the
	// blocked/backup/orphan candidates.
	var quarantines []string
	for _, call := range publisher.calls {
		if strings.HasPrefix(call, "maintenance/quarantine:") {
			quarantines = append(quarantines, call)
		}
	}
	if len(quarantines) != 1 {
		t.Fatalf("quarantine calls = %v", publisher.calls)
	}
	if !strings.Contains(quarantines[0], "/home/agents/work/dup") || !strings.Contains(quarantines[0], "node_modules") {
		t.Fatalf("quarantine paths wrong: %s", quarantines[0])
	}
	if strings.Contains(quarantines[0], "dirty") || strings.Contains(quarantines[0], "dump.bak") || strings.Contains(quarantines[0], "scratch") {
		t.Fatalf("blocked/tier-2 candidate leaked into Tier-1 quarantine: %s", quarantines[0])
	}
	// Tier-2: relocation of the misplaced backup is pending, never issued.
	for _, call := range publisher.calls {
		if strings.HasPrefix(call, "maintenance/relocate:") {
			t.Fatalf("tier-2 relocate must not be auto-issued: %v", publisher.calls)
		}
	}
	foundPending := false
	for _, pending := range result.PendingTier2 {
		if pending.Method == controlplane.ContextVMMethodMaintenanceRelocate && pending.Tier == 2 {
			foundPending = true
		}
	}
	if !foundPending {
		t.Fatalf("expected pending tier-2 relocate, got %+v", result.PendingTier2)
	}
	if metrics.candidates[domain.HygieneClassDupClone] != 2 || metrics.candidates[domain.HygieneClassMisplacedBackup] != 1 {
		t.Fatalf("candidate metrics = %+v", metrics.candidates)
	}
}

func TestHygieneReconcilerPressureBreachTriggersGC(t *testing.T) {
	publisher := &fakeMaintenancePublisher{}
	metrics := newFakeHygieneMetrics()
	source := &fakeObservationSource{observations: map[string]*HygieneObservation{
		"worker-a": {
			WorkerPubKey: "worker-a",
			Pressure: []HygieneMountPressure{
				{Path: "/", UsedPct: 91.5, TotalInodes: 100, FreeInodes: 50},
			},
			ObservedAt: time.Now(),
		},
	}}
	rec, err := NewHygieneReconciler(testHygienePolicy(nil), []string{"worker-a"}, publisher, source, metrics, time.Minute, zap.NewNop())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	result, err := rec.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	gcIssued := false
	for _, call := range publisher.calls {
		if strings.HasPrefix(call, "maintenance/gc:worker-a") {
			gcIssued = true
		}
	}
	if !gcIssued {
		t.Fatalf("expected gc intent on pressure breach: %v", publisher.calls)
	}
	if metrics.breaches != 1 || len(result.PressureAlerts) != 1 {
		t.Fatalf("breaches=%d alerts=%v", metrics.breaches, result.PressureAlerts)
	}
}

func TestHygieneReconcilerRespectsAutoFlagsAndDisabledPolicy(t *testing.T) {
	publisher := &fakeMaintenancePublisher{}
	source := &fakeObservationSource{observations: map[string]*HygieneObservation{
		"worker-a": {
			WorkerPubKey: "worker-a",
			Candidates:   []HygieneCandidate{{Path: "/home/agents/work/dup", Class: domain.HygieneClassDupClone, Canonical: "/c"}},
			Pressure:     []HygieneMountPressure{{Path: "/", UsedPct: 99}},
		},
	}}
	rec, err := NewHygieneReconciler(testHygienePolicy(func(p *domain.HygienePolicy) {
		p.AutoQuarantine = false
		p.AutoGC = false
	}), []string{"worker-a"}, publisher, source, newFakeHygieneMetrics(), time.Minute, zap.NewNop())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	result, err := rec.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, call := range publisher.calls {
		if strings.HasPrefix(call, "maintenance/quarantine") || strings.HasPrefix(call, "maintenance/gc") {
			t.Fatalf("auto flags disabled but action issued: %v", publisher.calls)
		}
	}
	if len(result.PendingTier2) != 2 {
		t.Fatalf("expected 2 pending actions, got %+v", result.PendingTier2)
	}

	// Disabled policy: nothing at all.
	publisher2 := &fakeMaintenancePublisher{}
	rec2, err := NewHygieneReconciler(testHygienePolicy(func(p *domain.HygienePolicy) { p.Enabled = false }), []string{"worker-a"}, publisher2, source, nil, time.Minute, zap.NewNop())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	if _, err := rec2.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(publisher2.calls) != 0 {
		t.Fatalf("disabled policy must be inert: %v", publisher2.calls)
	}
}

func TestHygieneReconcilerFreshnessUsesPassStartBoundary(t *testing.T) {
	const worker = "worker-a"
	interval := time.Minute
	passStartedAt := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name           string
		observedAt     time.Time
		wantQuarantine bool
	}{
		{
			name:           "previous pass boundary accepted",
			observedAt:     passStartedAt.Add(-interval),
			wantQuarantine: true,
		},
		{
			name:           "older observation rejected",
			observedAt:     passStartedAt.Add(-interval - time.Nanosecond),
			wantQuarantine: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			currentTime := passStartedAt
			publisher := &fakeMaintenancePublisher{afterPublish: func(method string) {
				if method == "maintenance/scan" || method == "maintenance/pressure" {
					currentTime = currentTime.Add(15 * time.Second)
				}
			}}
			source := &fakeObservationSource{observations: map[string]*HygieneObservation{
				worker: {
					WorkerPubKey: worker,
					Candidates:   []HygieneCandidate{{Path: "/home/agents/work/cruft", Class: domain.HygieneClassCruft}},
					ObservedAt:   tc.observedAt,
				},
			}}
			rec, err := NewHygieneReconciler(testHygienePolicy(nil), []string{worker}, publisher, source, nil, interval, zap.NewNop())
			if err != nil {
				t.Fatalf("new reconciler: %v", err)
			}
			rec.now = func() time.Time { return currentTime }

			if _, err := rec.ReconcileOnce(context.Background()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if elapsed := currentTime.Sub(passStartedAt); elapsed != 30*time.Second {
				t.Fatalf("simulated scan round-trip = %v, want 30s", elapsed)
			}
			gotQuarantine := false
			for _, call := range publisher.calls {
				if strings.HasPrefix(call, "maintenance/quarantine:") {
					gotQuarantine = true
				}
			}
			if gotQuarantine != tc.wantQuarantine {
				t.Fatalf("quarantine issued = %v, want %v; calls=%v", gotQuarantine, tc.wantQuarantine, publisher.calls)
			}
		})
	}
}

func TestHygienePolicyValidation(t *testing.T) {
	if err := (domain.HygienePolicy{}).WithDefaults().Validate(); err == nil {
		t.Fatal("empty policy must fail validation")
	}
	if err := testHygienePolicy(func(p *domain.HygienePolicy) { p.ProtectedPaths = nil }).WithDefaults().Validate(); err == nil {
		t.Fatal("policy without protected paths must fail")
	}
	if err := testHygienePolicy(func(p *domain.HygienePolicy) { p.ScanRoots = []string{"relative"} }).WithDefaults().Validate(); err == nil {
		t.Fatal("relative scan root must fail")
	}
	if err := testHygienePolicy(nil).WithDefaults().Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
}

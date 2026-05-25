package service

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestWorkerPressureMonitorSamePressureNoChange(t *testing.T) {
	monitor := NewWorkerPressureMonitor()
	first := workerPressureObservation("worker-a", time.Unix(100, 0), domain.WorkerCapacityOpen)
	_, _, changed := monitor.Observe(first)
	if changed {
		t.Fatal("first observation should establish baseline without reporting a transition")
	}

	previous, current, changed := monitor.Observe(workerPressureObservation("worker-a", time.Unix(101, 0), domain.WorkerCapacityOpen))
	if changed {
		t.Fatal("same capacity class should not report changed=true")
	}
	if previous == nil || current == nil || previous.CapacityClass != domain.WorkerCapacityOpen || current.CapacityClass != domain.WorkerCapacityOpen {
		t.Fatalf("unexpected pressure snapshots: previous=%#v current=%#v", previous, current)
	}
}

func TestWorkerPressureMonitorCapacityClassTransitionChanged(t *testing.T) {
	monitor := NewWorkerPressureMonitor()
	monitor.Observe(workerPressureObservation("worker-a", time.Unix(100, 0), domain.WorkerCapacityOpen))

	previous, current, changed := monitor.Observe(workerPressureObservation("worker-a", time.Unix(101, 0), domain.WorkerCapacityBlocked))
	if !changed {
		t.Fatal("capacity class transition should report changed=true")
	}
	if previous == nil || previous.CapacityClass != domain.WorkerCapacityOpen || current == nil || current.CapacityClass != domain.WorkerCapacityBlocked {
		t.Fatalf("unexpected transition snapshots: previous=%#v current=%#v", previous, current)
	}
}

func TestWorkerPressureMonitorIgnoresOlderObservation(t *testing.T) {
	monitor := NewWorkerPressureMonitor()
	monitor.Observe(workerPressureObservation("worker-a", time.Unix(200, 0), domain.WorkerCapacityReduced))

	previous, current, changed := monitor.Observe(workerPressureObservation("worker-a", time.Unix(199, 0), domain.WorkerCapacityBlocked))
	if changed {
		t.Fatal("older observation should be ignored without reporting changed=true")
	}
	if previous == nil || current == nil || previous.CapacityClass != domain.WorkerCapacityReduced || current.CapacityClass != domain.WorkerCapacityReduced {
		t.Fatalf("older observation should return stored pressure: previous=%#v current=%#v", previous, current)
	}

	_, current, changed = monitor.Observe(workerPressureObservation("worker-a", time.Unix(201, 0), domain.WorkerCapacityReduced))
	if changed || current == nil || current.CapacityClass != domain.WorkerCapacityReduced {
		t.Fatalf("stored snapshot was not preserved after older observation: changed=%v current=%#v", changed, current)
	}
}

func workerPressureObservation(pubkey string, advertisedAt time.Time, class domain.WorkerCapacityClass) domain.Worker {
	return domain.Worker{
		PubKey:              pubkey,
		LastAdvertisementAt: advertisedAt.UTC(),
		Pressure: &domain.WorkerPressureAssessment{
			OverallLevel:      domain.WorkerPressureNominal,
			CapacityClass:     class,
			RecommendedAction: domain.WorkerPressureActionNone,
			AssessedAt:        advertisedAt.UTC(),
		},
	}
}

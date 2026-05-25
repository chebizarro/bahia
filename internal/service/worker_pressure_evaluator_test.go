package service

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const testGiB int64 = 1024 * 1024 * 1024

func TestAssessWorkerPressureNominalOpen(t *testing.T) {
	assessment := Assess(nominalWorker(), fixedAssessmentTime())
	assertAssessment(t, assessment, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
}

func TestAssessWorkerPressureWarningMemoryReduced(t *testing.T) {
	worker := nominalWorker()
	worker.Telemetry.Memory.AvailableBytes = 12 * testGiB

	assessment := Assess(worker, fixedAssessmentTime())
	assertAssessment(t, assessment, domain.WorkerPressureWarning, domain.WorkerCapacityReduced, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, assessment, "memory", domain.WorkerPressureWarning, domain.WorkerPressureActionOperatorIntervention)
}

func TestAssessWorkerPressureCriticalDiskWithReclaimableCleanupOnly(t *testing.T) {
	worker := nominalWorker()
	worker.Telemetry.Disk.AvailableBytes = 10 * testGiB
	worker.Telemetry.Disk.DockerReclaimableBytes = 100 * testGiB

	assessment := Assess(worker, fixedAssessmentTime())
	assertAssessment(t, assessment, domain.WorkerPressureCritical, domain.WorkerCapacityCleanupOnly, domain.WorkerPressureActionCleanupRecommended)
	assertSignal(t, assessment, "disk", domain.WorkerPressureCritical, domain.WorkerPressureActionCleanupRecommended)
}

func TestAssessWorkerPressureCriticalThermalBlocked(t *testing.T) {
	worker := nominalWorker()
	worker.Telemetry.Thermal.MaxTemperatureC = 93

	assessment := Assess(worker, fixedAssessmentTime())
	assertAssessment(t, assessment, domain.WorkerPressureCritical, domain.WorkerCapacityBlocked, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, assessment, "thermal", domain.WorkerPressureCritical, domain.WorkerPressureActionOperatorIntervention)
}

func TestAssessWorkerPressureMissingTelemetryReduced(t *testing.T) {
	worker := nominalWorker()
	worker.Telemetry = nil

	assessment := Assess(worker, fixedAssessmentTime())
	assertAssessment(t, assessment, domain.WorkerPressureWarning, domain.WorkerCapacityReduced, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, assessment, "telemetry", domain.WorkerPressureUnknown, domain.WorkerPressureActionOperatorIntervention)
}

func TestAssessWorkerPressureStandbyWorkerUsesConservativeThresholds(t *testing.T) {
	worker := nominalWorker()
	worker.Telemetry.Memory.AvailableBytes = 20 * testGiB
	worker.StandbyAssignments = []domain.WorkerStandbyAssignment{{
		ServiceKey: "svc-a:prod",
		Tier:       domain.StandbyTierWarm,
		UpdatedAt:  fixedAssessmentTime(),
	}}

	assessment := Assess(worker, fixedAssessmentTime())
	assertAssessment(t, assessment, domain.WorkerPressureWarning, domain.WorkerCapacityReduced, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, assessment, "memory", domain.WorkerPressureWarning, domain.WorkerPressureActionOperatorIntervention)
}

func nominalWorker() domain.Worker {
	return domain.Worker{
		PubKey:            "worker-pubkey",
		MaxConcurrentJobs: 4,
		CurrentQueueDepth: 1,
		Telemetry: &domain.WorkerTelemetry{
			SampledAt: fixedAssessmentTime().Add(-time.Minute),
			Memory: &domain.WorkerMemoryTelemetry{
				TotalBytes:     64 * testGiB,
				AvailableBytes: 40 * testGiB,
			},
			Disk: &domain.WorkerDiskTelemetry{
				Path:                   "/",
				TotalBytes:             1000 * testGiB,
				AvailableBytes:         300 * testGiB,
				DockerReclaimableBytes: 0,
			},
			Accelerators: []domain.WorkerAcceleratorTelemetry{{
				Index:            0,
				MemoryTotalBytes: 24 * testGiB,
				MemoryFreeBytes:  16 * testGiB,
				TemperatureC:     55,
			}},
			Thermal: &domain.WorkerThermalTelemetry{MaxTemperatureC: 60},
		},
	}
}

func fixedAssessmentTime() time.Time {
	return time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
}

func assertAssessment(t *testing.T, assessment *domain.WorkerPressureAssessment, level domain.WorkerPressureLevel, class domain.WorkerCapacityClass, action domain.WorkerPressureAction) {
	t.Helper()
	if assessment == nil {
		t.Fatal("assessment is nil")
	}
	if assessment.OverallLevel != level || assessment.CapacityClass != class || assessment.RecommendedAction != action {
		t.Fatalf("assessment = level %s class %s action %s, want level %s class %s action %s", assessment.OverallLevel, assessment.CapacityClass, assessment.RecommendedAction, level, class, action)
	}
}

func assertSignal(t *testing.T, assessment *domain.WorkerPressureAssessment, name string, level domain.WorkerPressureLevel, action domain.WorkerPressureAction) {
	t.Helper()
	for _, signal := range assessment.Signals {
		if signal.Name == name {
			if signal.Level != level || signal.RecommendedAction != action {
				t.Fatalf("signal %s = level %s action %s, want level %s action %s", name, signal.Level, signal.RecommendedAction, level, action)
			}
			return
		}
	}
	t.Fatalf("signal %s not found in %#v", name, assessment.Signals)
}

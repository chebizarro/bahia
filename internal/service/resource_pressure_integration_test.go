package service

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestMixedVersionWorkerPressureBehavior(t *testing.T) {
	now := fixedAssessmentTime()

	oldWorker := makePressureIntegrationWorker("old-worker")
	oldWorker.Telemetry = nil
	oldAssessment := Assess(oldWorker, now)
	assertAssessment(t, oldAssessment, domain.WorkerPressureWarning, domain.WorkerCapacityReduced, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, oldAssessment, "telemetry", domain.WorkerPressureUnknown, domain.WorkerPressureActionOperatorIntervention)
	oldWorker.Pressure = oldAssessment
	oldAdmission := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &oldWorker})
	if !oldAdmission.Eligible || oldAdmission.CapacityClass != domain.WorkerCapacityReduced {
		t.Fatalf("old worker admission = %#v, want eligible reduced", oldAdmission)
	}

	newWorker := makePressureIntegrationWorker("new-worker")
	newAssessment := Assess(newWorker, now)
	assertAssessment(t, newAssessment, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
	newWorker.Pressure = newAssessment
	newAdmission := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &newWorker})
	if !newAdmission.Eligible || newAdmission.CapacityClass != domain.WorkerCapacityOpen {
		t.Fatalf("new worker admission = %#v, want eligible open", newAdmission)
	}

	partialWorker := makePressureIntegrationWorker("partial-worker")
	partialWorker.Telemetry.Disk = nil
	partialWorker.Telemetry.Accelerators = nil
	partialWorker.Telemetry.Thermal = nil
	partialAssessment := Assess(partialWorker, now)
	assertAssessment(t, partialAssessment, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
	assertSignal(t, partialAssessment, "memory", domain.WorkerPressureNominal, domain.WorkerPressureActionNone)
	assertNoSignal(t, partialAssessment, "disk")
	assertNoSignal(t, partialAssessment, "vram[0]")
	assertNoSignal(t, partialAssessment, "thermal")

	staleWorker := makePressureIntegrationWorker("stale-worker")
	staleWorker.Telemetry.SampledAt = now.Add(-(workerTelemetryStaleThreshold + time.Second))
	staleAssessment := Assess(staleWorker, now)
	assertAssessment(t, staleAssessment, domain.WorkerPressureWarning, domain.WorkerCapacityReduced, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, staleAssessment, "telemetry_stale", domain.WorkerPressureUnknown, domain.WorkerPressureActionOperatorIntervention)
}

func TestTelemetryDegradationOmitsUnavailableCollectorSignals(t *testing.T) {
	now := fixedAssessmentTime()

	memoryMissing := makePressureIntegrationWorker("memory-missing")
	memoryMissing.Telemetry.Memory = nil
	memoryMissingAssessment := Assess(memoryMissing, now)
	assertAssessment(t, memoryMissingAssessment, domain.WorkerPressureWarning, domain.WorkerCapacityReduced, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, memoryMissingAssessment, "memory", domain.WorkerPressureUnknown, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, memoryMissingAssessment, "disk", domain.WorkerPressureNominal, domain.WorkerPressureActionNone)
	assertSignal(t, memoryMissingAssessment, "thermal", domain.WorkerPressureNominal, domain.WorkerPressureActionNone)

	diskMissing := makePressureIntegrationWorker("disk-missing")
	diskMissing.Telemetry.Disk = nil
	diskMissingAssessment := Assess(diskMissing, now)
	assertAssessment(t, diskMissingAssessment, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
	assertSignal(t, diskMissingAssessment, "memory", domain.WorkerPressureNominal, domain.WorkerPressureActionNone)
	assertNoSignal(t, diskMissingAssessment, "disk")

	noAccelerators := makePressureIntegrationWorker("no-accelerators")
	noAccelerators.Telemetry.Accelerators = nil
	noAcceleratorsAssessment := Assess(noAccelerators, now)
	assertAssessment(t, noAcceleratorsAssessment, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
	assertNoSignal(t, noAcceleratorsAssessment, "vram[0]")

	noThermal := makePressureIntegrationWorker("no-thermal")
	noThermal.Telemetry.Thermal = nil
	noThermalAssessment := Assess(noThermal, now)
	assertAssessment(t, noThermalAssessment, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
	assertNoSignal(t, noThermalAssessment, "thermal")

	allTelemetryMissing := makePressureIntegrationWorker("all-telemetry-missing")
	allTelemetryMissing.Telemetry = nil
	allTelemetryMissingAssessment := Assess(allTelemetryMissing, now)
	assertAssessment(t, allTelemetryMissingAssessment, domain.WorkerPressureWarning, domain.WorkerCapacityReduced, domain.WorkerPressureActionOperatorIntervention)
	assertSignal(t, allTelemetryMissingAssessment, "telemetry", domain.WorkerPressureUnknown, domain.WorkerPressureActionOperatorIntervention)
}

func TestEndToEndPressureLifecycle(t *testing.T) {
	now := fixedAssessmentTime()
	worker := makePressureIntegrationWorker("lifecycle-worker")

	worker.Pressure = Assess(worker, now)
	assertAssessment(t, worker.Pressure, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
	assertAdmission(t, Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker}), true, "eligible", domain.WorkerCapacityOpen)

	worker.Telemetry.Disk.AvailableBytes = 10 * testGiB
	worker.Telemetry.Disk.DockerReclaimableBytes = 100 * testGiB
	worker.Pressure = Assess(worker, now.Add(time.Minute))
	assertAssessment(t, worker.Pressure, domain.WorkerPressureCritical, domain.WorkerCapacityCleanupOnly, domain.WorkerPressureActionCleanupRecommended)

	standardAdmission := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker})
	assertAdmission(t, standardAdmission, false, "capacity_class_rejected", domain.WorkerCapacityCleanupOnly)
	cleanupAdmission := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeCleanup, Worker: &worker})
	assertAdmission(t, cleanupAdmission, true, "eligible", domain.WorkerCapacityCleanupOnly)
	if !cleanupAdmission.CleanupSuggested {
		t.Fatalf("cleanup admission should carry cleanup suggestion: %#v", cleanupAdmission)
	}

	worker.Telemetry.SampledAt = now.Add(2 * time.Minute)
	worker.Telemetry.Disk.AvailableBytes = 300 * testGiB
	worker.Telemetry.Disk.DockerReclaimableBytes = 0
	worker.Pressure = Assess(worker, now.Add(2*time.Minute))
	assertAssessment(t, worker.Pressure, domain.WorkerPressureNominal, domain.WorkerCapacityOpen, domain.WorkerPressureActionNone)
	assertAdmission(t, Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker}), true, "eligible", domain.WorkerCapacityOpen)
}

func makePressureIntegrationWorker(pubkey string) domain.Worker {
	worker := nominalWorker()
	worker.PubKey = pubkey
	worker.Name = pubkey
	worker.Status = domain.WorkerStatusOnline
	worker.SchedulingState = domain.WorkerSchedulingActive
	worker.LastAdvertisementAt = fixedAssessmentTime()
	worker.CreatedAt = fixedAssessmentTime()
	worker.UpdatedAt = fixedAssessmentTime()
	return worker
}

func assertNoSignal(t *testing.T, assessment *domain.WorkerPressureAssessment, name string) {
	t.Helper()
	for _, signal := range assessment.Signals {
		if signal.Name == name {
			t.Fatalf("signal %s found in %#v", name, assessment.Signals)
		}
	}
}

func assertAdmission(t *testing.T, decision WorkerAdmissionDecision, eligible bool, code string, class domain.WorkerCapacityClass) {
	t.Helper()
	if decision.Eligible != eligible || decision.Code != code || decision.CapacityClass != class {
		t.Fatalf("admission = eligible %v code %s class %s, want eligible %v code %s class %s: %#v", decision.Eligible, decision.Code, decision.CapacityClass, eligible, code, class, decision)
	}
}

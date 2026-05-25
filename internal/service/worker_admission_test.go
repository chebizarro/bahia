package service

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestWorkerAdmissionAllowsTelemetryMissingForNonDynamicPlacement(t *testing.T) {
	worker := makeWorker("pk", "worker", 0, 0, "", "linux/amd64")
	worker.Telemetry = nil
	worker.Pressure = nil

	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker})
	if !decision.Eligible {
		t.Fatalf("expected missing telemetry to be eligible without dynamic headroom, got %#v", decision)
	}
	if decision.CapacityClass != domain.WorkerCapacityReduced {
		t.Fatalf("expected reduced capacity for telemetry-missing worker, got %s", decision.CapacityClass)
	}
}

func TestWorkerAdmissionRejectsTelemetryMissingForDynamicHeadroom(t *testing.T) {
	worker := makeWorker("pk", "worker", 0, 0, "", "linux/amd64")
	worker.Telemetry = nil
	worker.Pressure = nil

	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeMLDeploy, Worker: &worker, MinSystemMemoryBytes: gibibyte})
	if decision.Eligible {
		t.Fatalf("expected dynamic request to reject missing telemetry")
	}
	if decision.Code != "dynamic_headroom_insufficient" || !strings.Contains(decision.Reason, "telemetry unavailable") {
		t.Fatalf("expected telemetry headroom rejection, got %#v", decision)
	}
}

func TestWorkerAdmissionRejectsBlockedCapacityForStandardPlacement(t *testing.T) {
	worker := makeWorker("pk", "worker", 0, 0, "", "linux/amd64")
	worker.Pressure = &domain.WorkerPressureAssessment{CapacityClass: domain.WorkerCapacityBlocked, OverallLevel: domain.WorkerPressureCritical}

	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker})
	if decision.Eligible || decision.Code != "capacity_class_rejected" {
		t.Fatalf("expected blocked capacity rejection, got %#v", decision)
	}
}

func TestWorkerAdmissionAssessesMissingPressureWithConfiguredThresholds(t *testing.T) {
	worker := dynamicAdmissionWorker(40*gibibyte, 64*gibibyte, 64*gibibyte)
	worker.Pressure = nil
	thresholds := dynamicAdmissionThresholds()
	thresholds.MemoryWarningMinBytes = 48 * gibibyte
	thresholds.MemoryCriticalMinBytes = 24 * gibibyte

	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker, PressureThresholds: thresholds})
	if !decision.Eligible || decision.CapacityClass != domain.WorkerCapacityReduced || decision.PressureLevel != domain.WorkerPressureWarning {
		t.Fatalf("expected configured fallback pressure assessment to reduce capacity, got %#v", decision)
	}
}

func TestWorkerAdmissionWorkerlessBypass(t *testing.T) {
	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeWorkerless})
	if !decision.Eligible || decision.Code != "workerless" || !strings.Contains(decision.Reason, "bypasses worker admission") {
		t.Fatalf("expected explicit workerless bypass, got %#v", decision)
	}
}

func TestDynamicHeadroomRequiresWorkloadPlusReserveForMemoryScenarios(t *testing.T) {
	thresholds := dynamicAdmissionThresholds()
	reserve := 4 * gibibyte
	scenarios := []struct {
		name string
		min  int64
	}{
		{name: "min below reserve", min: 2 * gibibyte},
		{name: "min equals reserve", min: reserve},
		{name: "min above reserve", min: 6 * gibibyte},
	}
	scopes := []AdmissionScope{AdmissionScopeServiceDeploy, AdmissionScopeLLMDeploy, AdmissionScopeMLDeploy, AdmissionScopeBackup}
	for _, scenario := range scenarios {
		for _, scope := range scopes {
			t.Run(string(scope)+"/"+scenario.name, func(t *testing.T) {
				required := requiredWithReserve(scenario.min, reserve)
				worker := dynamicAdmissionWorker(required-1, 64*gibibyte, 64*gibibyte)
				decision := Evaluate(WorkerAdmissionRequest{Scope: scope, Worker: &worker, MinSystemMemoryBytes: scenario.min, PressureThresholds: thresholds})
				if decision.Eligible || decision.Code != "dynamic_headroom_insufficient" {
					t.Fatalf("expected memory reserve rejection below %d bytes, got %#v", required, decision)
				}

				worker = dynamicAdmissionWorker(required, 64*gibibyte, 64*gibibyte)
				decision = Evaluate(WorkerAdmissionRequest{Scope: scope, Worker: &worker, MinSystemMemoryBytes: scenario.min, PressureThresholds: thresholds})
				if !decision.Eligible {
					t.Fatalf("expected admission at workload+reserve %d bytes, got %#v", required, decision)
				}
			})
		}
	}
}

func TestDynamicHeadroomPreservesDiskAndVRAMReserve(t *testing.T) {
	thresholds := dynamicAdmissionThresholds()
	reserve := 4 * gibibyte
	min := 2 * gibibyte
	required := requiredWithReserve(min, reserve)

	worker := dynamicAdmissionWorker(64*gibibyte, required-1, 64*gibibyte)
	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker, MinFreeDiskBytes: min, PressureThresholds: thresholds})
	if decision.Eligible || decision.Code != "dynamic_headroom_insufficient" {
		t.Fatalf("expected disk reserve rejection below %d bytes, got %#v", required, decision)
	}
	worker = dynamicAdmissionWorker(64*gibibyte, required, 64*gibibyte)
	decision = Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeServiceDeploy, Worker: &worker, MinFreeDiskBytes: min, PressureThresholds: thresholds})
	if !decision.Eligible {
		t.Fatalf("expected disk admission at workload+reserve %d bytes, got %#v", required, decision)
	}

	worker = dynamicAdmissionWorker(64*gibibyte, 64*gibibyte, required-1)
	decision = Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeLLMDeploy, Worker: &worker, MinVRAMBytes: min, PressureThresholds: thresholds})
	if decision.Eligible || decision.Code != "dynamic_headroom_insufficient" {
		t.Fatalf("expected VRAM reserve rejection below %d bytes, got %#v", required, decision)
	}
	worker = dynamicAdmissionWorker(64*gibibyte, 64*gibibyte, required)
	decision = Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeLLMDeploy, Worker: &worker, MinVRAMBytes: min, PressureThresholds: thresholds})
	if !decision.Eligible {
		t.Fatalf("expected VRAM admission at workload+reserve %d bytes, got %#v", required, decision)
	}
}

func dynamicAdmissionThresholds() WorkerPressureThresholds {
	thresholds := DefaultWorkerPressureThresholds()
	thresholds.MemoryWarningMinBytes = 4 * gibibyte
	thresholds.MemoryWarningMinRatio = 0.01
	thresholds.DiskWarningMinBytes = 4 * gibibyte
	thresholds.DiskWarningMinRatio = 0.01
	thresholds.VRAMWarningMinBytes = 4 * gibibyte
	thresholds.VRAMWarningMinRatio = 0.01
	return thresholds
}

func dynamicAdmissionWorker(memoryAvailable, diskAvailable, vramAvailable int64) domain.Worker {
	worker := makeWorker("pk", "worker", 0, 0, "", "linux/amd64")
	worker.Pressure = &domain.WorkerPressureAssessment{CapacityClass: domain.WorkerCapacityOpen, OverallLevel: domain.WorkerPressureNominal}
	worker.Telemetry = &domain.WorkerTelemetry{
		Memory: &domain.WorkerMemoryTelemetry{TotalBytes: 100 * gibibyte, AvailableBytes: memoryAvailable},
		Disk:   &domain.WorkerDiskTelemetry{Path: "/", TotalBytes: 100 * gibibyte, AvailableBytes: diskAvailable},
		Accelerators: []domain.WorkerAcceleratorTelemetry{{
			Index:            0,
			MemoryTotalBytes: 100 * gibibyte,
			MemoryFreeBytes:  vramAvailable,
		}},
	}
	return worker
}

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

func TestWorkerAdmissionWorkerlessBypass(t *testing.T) {
	decision := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeWorkerless})
	if !decision.Eligible || decision.Code != "workerless" || !strings.Contains(decision.Reason, "bypasses worker admission") {
		t.Fatalf("expected explicit workerless bypass, got %#v", decision)
	}
}

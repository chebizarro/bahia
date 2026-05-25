package service

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AdmissionScope identifies the placement family being admitted.
type AdmissionScope string

const (
	AdmissionScopeServiceDeploy AdmissionScope = "service_deploy"
	AdmissionScopeLLMDeploy     AdmissionScope = "llm_deploy"
	AdmissionScopeMLDeploy      AdmissionScope = "ml_deploy"
	AdmissionScopeBackup        AdmissionScope = "backup"
	AdmissionScopeCleanup       AdmissionScope = "cleanup"
	AdmissionScopeWorkerless    AdmissionScope = "workerless"
)

// WorkerAdmissionRequest contains placement-independent worker admission inputs.
type WorkerAdmissionRequest struct {
	Scope                AdmissionScope
	Worker               *domain.Worker
	RequireRuntimeTarget bool
	Selector             map[string]any
	LabelSelector        map[string]string
	MaxPrice             int
	PinnedWorker         string
	MinSystemMemoryBytes int64
	MinFreeDiskBytes     int64
	MinVRAMBytes         int64
}

// WorkerAdmissionDecision explains whether a worker can receive a placement.
type WorkerAdmissionDecision struct {
	Eligible         bool
	Code             string
	Reason           string
	CapacityClass    domain.WorkerCapacityClass
	PressureLevel    domain.WorkerPressureLevel
	CleanupSuggested bool
}

// Evaluate applies Bahia's ordered, placement-independent worker admission policy.
func Evaluate(req WorkerAdmissionRequest) WorkerAdmissionDecision {
	if req.Scope == AdmissionScopeWorkerless {
		return WorkerAdmissionDecision{Eligible: true, Code: "workerless", Reason: "workerless placement bypasses worker admission by policy", CapacityClass: domain.WorkerCapacityOpen, PressureLevel: domain.WorkerPressureNominal}
	}
	if req.Worker == nil {
		return rejectAdmission("worker_missing", "worker is required for worker-backed placement", domain.WorkerCapacityBlocked, domain.WorkerPressureUnknown, false)
	}

	w := req.Worker
	capacityClass, pressureLevel, cleanupSuggested := workerAdmissionPressure(*w)
	cleanupScope := req.Scope == AdmissionScopeCleanup

	if cleanupScope {
		if w.Status != domain.WorkerStatusOnline && w.Status != domain.WorkerStatusStale {
			return rejectAdmission("worker_not_live", fmt.Sprintf("worker status %s is not online or stale for cleanup", w.Status), capacityClass, pressureLevel, cleanupSuggested)
		}
	} else if w.Status != domain.WorkerStatusOnline {
		return rejectAdmission("worker_not_online", fmt.Sprintf("worker status %s is not online", w.Status), capacityClass, pressureLevel, cleanupSuggested)
	}

	if cleanupScope {
		if normalizedWorkerSchedulingState(w.SchedulingState) == domain.WorkerSchedulingDisabled {
			return rejectAdmission("worker_scheduling", "worker is disabled", capacityClass, pressureLevel, cleanupSuggested)
		}
	} else if !workerSchedulingStateAllowsNewPlacement(w.SchedulingState) {
		return rejectAdmission("worker_scheduling", workerSchedulingStateRejectionReason(w.SchedulingState), capacityClass, pressureLevel, cleanupSuggested)
	}

	if req.PinnedWorker != "" && w.PubKey != req.PinnedWorker {
		return rejectAdmission("pinned_worker_mismatch", fmt.Sprintf("worker does not match pinned_worker %s", req.PinnedWorker), capacityClass, pressureLevel, cleanupSuggested)
	}
	if !matchesSelector(*w, req.Selector) {
		return rejectAdmission("selector_mismatch", "worker does not match selector", capacityClass, pressureLevel, cleanupSuggested)
	}
	if reason, ok := workerLabelsMatchReason(*w, req.LabelSelector); !ok {
		return rejectAdmission("label_mismatch", reason, capacityClass, pressureLevel, cleanupSuggested)
	}
	if req.RequireRuntimeTarget && !workerHasRuntimeTarget(*w) {
		return rejectAdmission("runtime_target_missing", "runtime target missing", capacityClass, pressureLevel, cleanupSuggested)
	}
	if req.MaxPrice > 0 {
		if price := lowestPrice(*w); price > 0 && price > req.MaxPrice {
			return rejectAdmission("price_above_max", fmt.Sprintf("worker price %d exceeds max %d", price, req.MaxPrice), capacityClass, pressureLevel, cleanupSuggested)
		}
	}
	if !cleanupScope && (capacityClass == domain.WorkerCapacityCleanupOnly || capacityClass == domain.WorkerCapacityBlocked) {
		return rejectAdmission("capacity_class_rejected", fmt.Sprintf("worker capacity class %s rejects standard placement", capacityClass), capacityClass, pressureLevel, cleanupSuggested)
	}
	if reason, ok := dynamicHeadroomAdmission(*w, req); !ok {
		return rejectAdmission("dynamic_headroom_insufficient", reason, capacityClass, pressureLevel, cleanupSuggested)
	}

	return WorkerAdmissionDecision{Eligible: true, Code: "eligible", Reason: "worker satisfies admission policy", CapacityClass: capacityClass, PressureLevel: pressureLevel, CleanupSuggested: cleanupSuggested}
}

func rejectAdmission(code, reason string, capacityClass domain.WorkerCapacityClass, pressureLevel domain.WorkerPressureLevel, cleanupSuggested bool) WorkerAdmissionDecision {
	return WorkerAdmissionDecision{Eligible: false, Code: code, Reason: reason, CapacityClass: capacityClass, PressureLevel: pressureLevel, CleanupSuggested: cleanupSuggested}
}

func workerAdmissionPressure(w domain.Worker) (domain.WorkerCapacityClass, domain.WorkerPressureLevel, bool) {
	if w.Pressure == nil {
		if w.Telemetry == nil {
			return domain.WorkerCapacityReduced, domain.WorkerPressureWarning, false
		}
		return domain.WorkerCapacityOpen, domain.WorkerPressureNominal, false
	}
	return w.Pressure.CapacityClass, w.Pressure.OverallLevel, w.Pressure.RecommendedAction == domain.WorkerPressureActionCleanupRecommended
}

func workerHasRuntimeTarget(w domain.Worker) bool {
	return w.RuntimeTarget != nil && strings.TrimSpace(w.RuntimeTarget.PublicBaseURL) != ""
}

func dynamicHeadroomAdmission(w domain.Worker, req WorkerAdmissionRequest) (string, bool) {
	if req.MinSystemMemoryBytes <= 0 && req.MinFreeDiskBytes <= 0 && req.MinVRAMBytes <= 0 {
		return "", true
	}
	if w.Telemetry == nil {
		return "worker telemetry unavailable for dynamic headroom admission", false
	}
	thresholds := DefaultWorkerPressureThresholds()
	if req.MinSystemMemoryBytes > 0 {
		if w.Telemetry.Memory == nil || w.Telemetry.Memory.TotalBytes <= 0 {
			return "memory telemetry unavailable for dynamic headroom admission", false
		}
		reserve := maxInt64(thresholds.MemoryWarningMinBytes, int64(float64(w.Telemetry.Memory.TotalBytes)*thresholds.MemoryWarningMinRatio))
		if w.Telemetry.Memory.AvailableBytes < requiredAfterReserve(req.MinSystemMemoryBytes, reserve) {
			return "available system memory below requested dynamic headroom", false
		}
	}
	if req.MinFreeDiskBytes > 0 {
		if w.Telemetry.Disk == nil || w.Telemetry.Disk.TotalBytes <= 0 {
			return "disk telemetry unavailable for dynamic headroom admission", false
		}
		reserve := maxInt64(thresholds.DiskWarningMinBytes, int64(float64(w.Telemetry.Disk.TotalBytes)*thresholds.DiskWarningMinRatio))
		if w.Telemetry.Disk.AvailableBytes < requiredAfterReserve(req.MinFreeDiskBytes, reserve) {
			return "available disk below requested dynamic headroom", false
		}
	}
	if req.MinVRAMBytes > 0 {
		if !acceleratorHasVRAMHeadroom(w.Telemetry.Accelerators, req.MinVRAMBytes, thresholds) {
			return "available VRAM below requested dynamic headroom", false
		}
	}
	return "", true
}

func acceleratorHasVRAMHeadroom(accelerators []domain.WorkerAcceleratorTelemetry, minBytes int64, thresholds WorkerPressureThresholds) bool {
	for _, accelerator := range accelerators {
		if accelerator.MemoryTotalBytes <= 0 {
			continue
		}
		reserve := maxInt64(thresholds.VRAMWarningMinBytes, int64(float64(accelerator.MemoryTotalBytes)*thresholds.VRAMWarningMinRatio))
		if accelerator.MemoryFreeBytes >= requiredAfterReserve(minBytes, reserve) {
			return true
		}
	}
	return false
}

func requiredAfterReserve(minBytes, reserveBytes int64) int64 {
	if minBytes <= reserveBytes {
		return 1
	}
	return minBytes - reserveBytes
}

func bytesFromGiB(gib int) int64 {
	if gib <= 0 {
		return 0
	}
	return int64(gib) * gibibyte
}

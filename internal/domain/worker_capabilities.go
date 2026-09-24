package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// NormalizeWorkerMLCapabilities returns a de-duplicated, lowercase capability view.
func NormalizeWorkerMLCapabilities(w Worker) WorkerMLCapabilities {
	caps := w.MLCapabilities
	for _, sw := range w.Software {
		name := normalizeCapabilityToken(sw.Name)
		if runtime := MLRuntimeKind(name); runtime.IsValid() {
			caps.Runtimes = append(caps.Runtimes, runtime)
		}
		if isKnownToolchain(name) {
			caps.Toolchains = append(caps.Toolchains, name)
		}
	}
	for _, accel := range w.Accelerators {
		caps.Accelerators = append(caps.Accelerators, acceleratorClasses(accel)...)
	}
	caps.Tasks = dedupeMLTaskKinds(caps.Tasks)
	caps.Runtimes = dedupeMLRuntimeKinds(caps.Runtimes)
	caps.ArtifactFormats = dedupeMLArtifactFormats(caps.ArtifactFormats)
	caps.Accelerators = dedupeStrings(caps.Accelerators)
	caps.Toolchains = dedupeStrings(caps.Toolchains)
	caps.CachedArtifacts = dedupeStrings(caps.CachedArtifacts)
	return caps
}

// NormalizeVerifiedExecutionPlaneCapabilities returns only unexpired, structurally
// valid contributions. This does not authenticate evidence: callers must source
// these records exclusively from the reconciler's current live sessions.
func NormalizeVerifiedExecutionPlaneCapabilities(w Worker, now time.Time) []VerifiedExecutionPlaneCapabilities {
	out := make([]VerifiedExecutionPlaneCapabilities, 0, len(w.VerifiedExecutionPlanes))
	seen := map[uuid.UUID]bool{}
	for _, verified := range w.VerifiedExecutionPlanes {
		if verified.PlaneID == uuid.Nil || verified.SessionID == uuid.Nil || verified.Generation < 1 || verified.ProbeSequence < 1 || !now.Before(verified.ExpiresAt) || seen[verified.PlaneID] {
			continue
		}
		if vmPlaneCapabilities(verified.Capabilities, []VMLifecycleClass{VMLifecycleLoomFirecracker, VMLifecycleLoomQEMU}) != nil {
			continue
		}
		seen[verified.PlaneID] = true
		verified.Capabilities = append([]ExecutionPlaneCapability(nil), verified.Capabilities...)
		out = append(out, verified)
	}
	return out
}

// HasVerifiedExecutionPlaneCapability deliberately ignores Software and generic
// WorkloadKinds/Features, including any Windows or persistent-VM advertisements.
func HasVerifiedExecutionPlaneCapability(w Worker, required ExecutionPlaneCapability, now time.Time) bool {
	if vmPlaneCapabilities([]ExecutionPlaneCapability{required}, []VMLifecycleClass{VMLifecycleLoomFirecracker, VMLifecycleLoomQEMU}) != nil ||
		w.Status != WorkerStatusOnline || w.SchedulingState != WorkerSchedulingActive ||
		w.MaxConcurrentJobs <= 0 || w.CurrentQueueDepth < 0 || w.CurrentQueueDepth >= w.MaxConcurrentJobs ||
		(w.Pressure != nil && (w.Pressure.CapacityClass == WorkerCapacityBlocked || w.Pressure.CapacityClass == WorkerCapacityCleanupOnly)) {
		return false
	}
	for _, verified := range NormalizeVerifiedExecutionPlaneCapabilities(w, now) {
		for _, capability := range verified.Capabilities {
			if capability == required {
				return true
			}
		}
	}
	return false
}

func normalizeCapabilityToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isKnownToolchain(value string) bool {
	switch value {
	case "rknn_toolkit2", "tensorrt", "onnxruntime", "cuda", "openvino", "tensorflow", "torch", "vllm":
		return true
	default:
		return false
	}
}

func acceleratorClasses(accel WorkerAccelerator) []string {
	vendor := normalizeCapabilityToken(accel.Vendor)
	model := normalizeCapabilityToken(accel.Model)
	var out []string
	if vendor == "nvidia" || strings.Contains(model, "nvidia") || strings.Contains(normalizeCapabilityToken(accel.Driver), "cuda") {
		out = append(out, "gpu_nvidia_cuda")
	}
	if strings.Contains(model, "rk3588") || vendor == "rockchip" {
		out = append(out, "npu_rk3588")
	}
	return out
}

func dedupeMLTaskKinds(values []MLTaskKind) []MLTaskKind {
	seen := map[MLTaskKind]bool{}
	out := make([]MLTaskKind, 0, len(values))
	for _, value := range values {
		value = MLTaskKind(normalizeCapabilityToken(string(value)))
		if value == "" || !value.IsValid() || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func dedupeMLRuntimeKinds(values []MLRuntimeKind) []MLRuntimeKind {
	seen := map[MLRuntimeKind]bool{}
	out := make([]MLRuntimeKind, 0, len(values))
	for _, value := range values {
		value = MLRuntimeKind(normalizeCapabilityToken(string(value)))
		if value == "" || !value.IsValid() || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func dedupeMLArtifactFormats(values []MLArtifactFormat) []MLArtifactFormat {
	seen := map[MLArtifactFormat]bool{}
	out := make([]MLArtifactFormat, 0, len(values))
	for _, value := range values {
		value = MLArtifactFormat(normalizeCapabilityToken(string(value)))
		if value == "" || !value.IsValid() || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeCapabilityToken(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

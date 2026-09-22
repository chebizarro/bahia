package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestVerifiedPlaneCapabilityIsSeparateFromAdvertisements(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	capability := ExecutionPlaneCapability{LifecycleClass: VMLifecycleLoomQEMU, OS: VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "3"}
	worker := Worker{Status: WorkerStatusOnline, SchedulingState: WorkerSchedulingActive, MaxConcurrentJobs: 2, Software: []WorkerSoftware{{Name: "qemu"}}, Capabilities: WorkerCapabilities{Features: []string{"qemu", "windows"}, WorkloadKinds: []string{string(VMLifecycleLoomQEMU)}}}
	require.False(t, HasVerifiedExecutionPlaneCapability(worker, capability, now))
	worker.VerifiedExecutionPlanes = []VerifiedExecutionPlaneCapabilities{{PlaneID: uuid.New(), Generation: 1, SessionID: uuid.New(), ProbeSequence: 1, ExpiresAt: now.Add(90 * time.Second), Capabilities: []ExecutionPlaneCapability{capability}}}
	require.True(t, HasVerifiedExecutionPlaneCapability(worker, capability, now))
	require.False(t, HasVerifiedExecutionPlaneCapability(worker, capability, now.Add(90*time.Second)))
	copy := NormalizeVerifiedExecutionPlaneCapabilities(worker, now)
	copy[0].Capabilities[0].OS = VMOSWindows
	require.Equal(t, VMOSLinux, worker.VerifiedExecutionPlanes[0].Capabilities[0].OS)
	worker.VerifiedExecutionPlanes = nil
	require.Equal(t, []string{"qemu", "windows"}, worker.Capabilities.Features)
	require.True(t, worker.HasSoftware("qemu"))
	encoded, err := json.Marshal(worker)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "verified_execution_planes")
}

func TestVerifiedPlaneCapabilityFailsClosed(t *testing.T) {
	now := time.Now()
	capability := ExecutionPlaneCapability{LifecycleClass: VMLifecycleLoomFirecracker, OS: VMOSLinux, Architecture: "arm64", AgentProtocolVersion: "3"}
	base := Worker{Status: WorkerStatusOnline, SchedulingState: WorkerSchedulingActive, MaxConcurrentJobs: 2}
	verified := VerifiedExecutionPlaneCapabilities{PlaneID: uuid.New(), Generation: 1, SessionID: uuid.New(), ProbeSequence: 1, ExpiresAt: now.Add(time.Minute), Capabilities: []ExecutionPlaneCapability{capability}}
	for name, mutate := range map[string]func(*Worker){
		"cordoned":               func(w *Worker) { w.SchedulingState = WorkerSchedulingCordoned },
		"unspecified scheduling": func(w *Worker) { w.SchedulingState = "" },
		"offline":                func(w *Worker) { w.Status = WorkerStatusOffline },
		"full":                   func(w *Worker) { w.CurrentQueueDepth = 2 },
		"unknown capacity":       func(w *Worker) { w.MaxConcurrentJobs = 0 },
		"pressure":               func(w *Worker) { w.Pressure = &WorkerPressureAssessment{CapacityClass: WorkerCapacityBlocked} },
		"session missing":        func(w *Worker) { w.VerifiedExecutionPlanes[0].SessionID = uuid.Nil },
		"generation missing":     func(w *Worker) { w.VerifiedExecutionPlanes[0].Generation = 0 },
		"expired":                func(w *Worker) { w.VerifiedExecutionPlanes[0].ExpiresAt = now },
		"windows": func(w *Worker) {
			w.VerifiedExecutionPlanes[0].Capabilities = []ExecutionPlaneCapability{{LifecycleClass: VMLifecycleLoomQEMU, OS: VMOSWindows, Architecture: "amd64", AgentProtocolVersion: "3"}}
		},
		"persistent": func(w *Worker) {
			w.VerifiedExecutionPlanes[0].Capabilities = []ExecutionPlaneCapability{{LifecycleClass: VMLifecyclePersistent, OS: VMOSLinux, Architecture: "arm64", AgentProtocolVersion: "3"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := base
			w.VerifiedExecutionPlanes = []VerifiedExecutionPlaneCapabilities{verified}
			mutate(&w)
			require.False(t, HasVerifiedExecutionPlaneCapability(w, capability, now))
		})
	}
}

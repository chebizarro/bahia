package domain

import (
	"encoding/json"
	"time"
)

// WorkerStatus represents liveness derived from worker advertisement freshness.
type WorkerStatus string

const (
	WorkerStatusOnline  WorkerStatus = "online"
	WorkerStatusStale   WorkerStatus = "stale"   // no ad for >5 minutes
	WorkerStatusOffline WorkerStatus = "offline" // no ad for >30 minutes
)

// WorkerSchedulingState represents operator scheduling intent for a worker.
type WorkerSchedulingState string

const (
	WorkerSchedulingActive      WorkerSchedulingState = "active"
	WorkerSchedulingCordoned    WorkerSchedulingState = "cordoned"
	WorkerSchedulingDraining    WorkerSchedulingState = "draining"
	WorkerSchedulingMaintenance WorkerSchedulingState = "maintenance"
	WorkerSchedulingDisabled    WorkerSchedulingState = "disabled"
)

// WorkerSoftware describes an installed software entry from the S tag.
type WorkerSoftware struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
}

// WorkerPricing describes pricing from a price tag.
type WorkerPricing struct {
	MintURL        string `json:"mint_url"`
	PricePerSecond int    `json:"price_per_second"`
	Unit           string `json:"unit"` // e.g. "sat"
}

// WorkerResources describes host-level resources advertised by a worker.
type WorkerResources struct {
	CPUCores int `json:"cpu_cores,omitempty"`
	MemoryGB int `json:"memory_gb,omitempty"`
	DiskGB   int `json:"disk_gb,omitempty"`
}

// WorkerAccelerator describes one accelerator class available on a worker.
type WorkerAccelerator struct {
	Vendor   string `json:"vendor,omitempty"`
	Model    string `json:"model,omitempty"`
	Count    int    `json:"count,omitempty"`
	MemoryGB int    `json:"memory_gb,omitempty"`
	Driver   string `json:"driver,omitempty"`
}

// WorkerRuntimeTarget describes where Bahia may deploy runtime-managed work for a worker.
type WorkerRuntimeTarget struct {
	Type          RuntimeType `json:"type,omitempty"`
	EndpointRef   string      `json:"endpoint_ref,omitempty"`
	ComposeDir    string      `json:"compose_dir,omitempty"`
	KubeNamespace string      `json:"kube_namespace,omitempty"`
	PublicBaseURL string      `json:"public_base_url,omitempty"`
}

// WorkerMLCapabilities is Bahia's normalized AI/ML placement capability view.
type WorkerMLCapabilities struct {
	Tasks           []MLTaskKind       `json:"tasks,omitempty"`
	Runtimes        []MLRuntimeKind    `json:"runtimes,omitempty"`
	ArtifactFormats []MLArtifactFormat `json:"artifact_formats,omitempty"`
	Accelerators    []string           `json:"accelerators,omitempty"`
	Toolchains      []string           `json:"toolchains,omitempty"`
	CachedArtifacts []string           `json:"cached_artifacts,omitempty"`
}

// WorkerCapabilities is Bahia's generic placement capability view for all worker-backed workloads.
type WorkerCapabilities struct {
	WorkloadKinds   []string `json:"workload_kinds,omitempty"`
	Runtimes        []string `json:"runtimes,omitempty"`
	ArtifactFormats []string `json:"artifact_formats,omitempty"`
	Accelerators    []string `json:"accelerators,omitempty"`
	Toolchains      []string `json:"toolchains,omitempty"`
	Features        []string `json:"features,omitempty"`
}

// Worker represents a Loom compute worker discovered via Kind 10100 events.
type Worker struct {
	PubKey              string                `json:"pubkey"`
	Name                string                `json:"name"`
	Description         string                `json:"description,omitempty"`
	Architecture        string                `json:"architecture,omitempty"` // e.g. "linux/amd64"
	MaxConcurrentJobs   int                   `json:"max_concurrent_jobs"`
	CurrentQueueDepth   int                   `json:"current_queue_depth"`
	Software            []WorkerSoftware      `json:"software,omitempty"`
	Pricing             []WorkerPricing       `json:"pricing,omitempty"`
	Resources           *WorkerResources      `json:"resources,omitempty"`
	Accelerators        []WorkerAccelerator   `json:"accelerators,omitempty"`
	MLCapabilities      WorkerMLCapabilities  `json:"ml_capabilities,omitempty"`
	Capabilities        WorkerCapabilities    `json:"capabilities,omitempty"`
	RuntimeTarget       *WorkerRuntimeTarget  `json:"runtime_target,omitempty"`
	MinDurationSecs     int                   `json:"min_duration_secs,omitempty"`
	MaxDurationSecs     int                   `json:"max_duration_secs,omitempty"`
	Geohash             string                `json:"geohash,omitempty"`
	PreferredRelays     []string              `json:"preferred_relays,omitempty"`
	LastAdvertisementAt time.Time             `json:"last_advertisement_at"`
	Status              WorkerStatus          `json:"status"`
	SchedulingState     WorkerSchedulingState `json:"scheduling_state"`
	SchedulingNote      string                `json:"scheduling_note,omitempty"`
	Labels              map[string]string     `json:"labels,omitempty"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

// MarshalJSON emits an active scheduling state when older in-memory callers have not set one.
func (w Worker) MarshalJSON() ([]byte, error) {
	type workerAlias Worker
	if w.SchedulingState == "" {
		w.SchedulingState = WorkerSchedulingActive
	}
	return json.Marshal(workerAlias(w))
}

// StaleThreshold is the duration after which a worker is considered stale.
const StaleThreshold = 5 * time.Minute

// OfflineThreshold is the duration after which a worker is considered offline.
const OfflineThreshold = 30 * time.Minute

// ComputeStatus derives the worker's current status from its last advertisement time.
func (w *Worker) ComputeStatus(now time.Time) WorkerStatus {
	age := now.Sub(w.LastAdvertisementAt)
	switch {
	case age > OfflineThreshold:
		return WorkerStatusOffline
	case age > StaleThreshold:
		return WorkerStatusStale
	default:
		return WorkerStatusOnline
	}
}

// HasSoftware checks if the worker advertises the given software name.
func (w *Worker) HasSoftware(name string) bool {
	for _, s := range w.Software {
		if s.Name == name {
			return true
		}
	}
	return false
}

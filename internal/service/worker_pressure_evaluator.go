package service

import (
	"fmt"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const gibibyte int64 = 1024 * 1024 * 1024

const workerTelemetryStaleThreshold = 10 * time.Minute

// WorkerPressureThresholds contains Bahia-owned pressure policy defaults.
type WorkerPressureThresholds struct {
	MemoryWarningMinBytes  int64
	MemoryWarningMinRatio  float64
	MemoryCriticalMinBytes int64
	MemoryCriticalMinRatio float64
	DiskWarningMinBytes    int64
	DiskWarningMinRatio    float64
	DiskCriticalMinBytes   int64
	DiskCriticalMinRatio   float64
	VRAMWarningMinBytes    int64
	VRAMWarningMinRatio    float64
	VRAMCriticalMinBytes   int64
	VRAMCriticalMinRatio   float64
	ThermalWarningC        float64
	ThermalCriticalC       float64
	QueueWarningRatio      float64
	QueueCriticalRatio     float64
}

// DefaultWorkerPressureThresholds returns the initial resource-pressure policy
// from the resource pressure orchestration plan.
func DefaultWorkerPressureThresholds() WorkerPressureThresholds {
	return WorkerPressureThresholds{
		MemoryWarningMinBytes:  4 * gibibyte,
		MemoryWarningMinRatio:  0.20,
		MemoryCriticalMinBytes: 2 * gibibyte,
		MemoryCriticalMinRatio: 0.10,
		DiskWarningMinBytes:    40 * gibibyte,
		DiskWarningMinRatio:    0.15,
		DiskCriticalMinBytes:   20 * gibibyte,
		DiskCriticalMinRatio:   0.08,
		VRAMWarningMinBytes:    4 * gibibyte,
		VRAMWarningMinRatio:    0.20,
		VRAMCriticalMinBytes:   2 * gibibyte,
		VRAMCriticalMinRatio:   0.10,
		ThermalWarningC:        85,
		ThermalCriticalC:       92,
		QueueWarningRatio:      0.80,
		QueueCriticalRatio:     1.0,
	}
}

// Assess computes a worker pressure assessment using Bahia's default thresholds.
func Assess(worker domain.Worker, now time.Time) *domain.WorkerPressureAssessment {
	return AssessWithThresholds(worker, now, DefaultWorkerPressureThresholds())
}

// AssessWithThresholds computes a worker pressure assessment from only the
// worker snapshot, the supplied clock value, and explicit thresholds.
func AssessWithThresholds(worker domain.Worker, now time.Time, thresholds WorkerPressureThresholds) *domain.WorkerPressureAssessment {
	thresholds = EffectiveWorkerPressureThresholds(thresholds)
	assessment := &domain.WorkerPressureAssessment{
		OverallLevel:      domain.WorkerPressureNominal,
		CapacityClass:     domain.WorkerCapacityOpen,
		RecommendedAction: domain.WorkerPressureActionNone,
		AssessedAt:        now.UTC(),
	}
	if worker.Telemetry == nil {
		assessment.OverallLevel = domain.WorkerPressureWarning
		assessment.CapacityClass = domain.WorkerCapacityReduced
		assessment.RecommendedAction = domain.WorkerPressureActionOperatorIntervention
		assessment.Signals = append(assessment.Signals, domain.WorkerPressureSignal{
			Name:              "telemetry",
			Level:             domain.WorkerPressureUnknown,
			RecommendedAction: domain.WorkerPressureActionOperatorIntervention,
			Reason:            "worker telemetry unavailable",
		})
		return assessment
	}

	telemetry := worker.Telemetry
	if !telemetry.SampledAt.IsZero() && now.UTC().Sub(telemetry.SampledAt.UTC()) > workerTelemetryStaleThreshold {
		assessment.OverallLevel = domain.WorkerPressureWarning
		assessment.CapacityClass = domain.WorkerCapacityReduced
		assessment.RecommendedAction = domain.WorkerPressureActionOperatorIntervention
		assessment.Signals = append(assessment.Signals, domain.WorkerPressureSignal{
			Name:              "telemetry_stale",
			Level:             domain.WorkerPressureUnknown,
			RecommendedAction: domain.WorkerPressureActionOperatorIntervention,
			Reason:            "worker telemetry sample is stale",
		})
		return assessment
	}

	standbyReserve := hasHotOrWarmStandby(worker)
	if telemetry.Memory != nil && telemetry.Memory.TotalBytes > 0 {
		warningFloor := maxInt64(thresholds.MemoryWarningMinBytes, int64(float64(telemetry.Memory.TotalBytes)*thresholds.MemoryWarningMinRatio))
		criticalFloor := maxInt64(thresholds.MemoryCriticalMinBytes, int64(float64(telemetry.Memory.TotalBytes)*thresholds.MemoryCriticalMinRatio))
		if standbyReserve {
			warningFloor += warningFloor
			criticalFloor += warningFloor / 2
		}
		assessment.Signals = append(assessment.Signals, freeBytesSignal("memory", telemetry.Memory.AvailableBytes, warningFloor, criticalFloor, false, 0))
	} else {
		assessment.Signals = append(assessment.Signals, domain.WorkerPressureSignal{
			Name:              "memory",
			Level:             domain.WorkerPressureUnknown,
			RecommendedAction: domain.WorkerPressureActionOperatorIntervention,
			Reason:            "memory telemetry unavailable",
		})
	}

	if telemetry.Disk != nil && telemetry.Disk.TotalBytes > 0 {
		warningFloor := maxInt64(thresholds.DiskWarningMinBytes, int64(float64(telemetry.Disk.TotalBytes)*thresholds.DiskWarningMinRatio))
		criticalFloor := maxInt64(thresholds.DiskCriticalMinBytes, int64(float64(telemetry.Disk.TotalBytes)*thresholds.DiskCriticalMinRatio))
		if standbyReserve {
			warningFloor += warningFloor
			criticalFloor += warningFloor / 2
		}
		assessment.Signals = append(assessment.Signals, freeBytesSignal("disk", telemetry.Disk.AvailableBytes, warningFloor, criticalFloor, true, telemetry.Disk.DockerReclaimableBytes))
	}

	for _, accelerator := range telemetry.Accelerators {
		if accelerator.MemoryTotalBytes <= 0 {
			continue
		}
		warningFloor := maxInt64(thresholds.VRAMWarningMinBytes, int64(float64(accelerator.MemoryTotalBytes)*thresholds.VRAMWarningMinRatio))
		criticalFloor := maxInt64(thresholds.VRAMCriticalMinBytes, int64(float64(accelerator.MemoryTotalBytes)*thresholds.VRAMCriticalMinRatio))
		if standbyReserve {
			warningFloor += warningFloor
			criticalFloor += warningFloor / 2
		}
		name := fmt.Sprintf("vram[%d]", accelerator.Index)
		assessment.Signals = append(assessment.Signals, freeBytesSignal(name, accelerator.MemoryFreeBytes, warningFloor, criticalFloor, false, 0))
	}

	if telemetry.Thermal != nil {
		level := domain.WorkerPressureNominal
		action := domain.WorkerPressureActionNone
		reason := "thermal nominal"
		if telemetry.Thermal.Throttled || telemetry.Thermal.MaxTemperatureC >= thresholds.ThermalCriticalC {
			level = domain.WorkerPressureCritical
			action = domain.WorkerPressureActionOperatorIntervention
			reason = "thermal critical or throttled"
		} else if telemetry.Thermal.MaxTemperatureC >= thresholds.ThermalWarningC {
			level = domain.WorkerPressureWarning
			action = domain.WorkerPressureActionOperatorIntervention
			reason = "thermal warning threshold exceeded"
		}
		assessment.Signals = append(assessment.Signals, domain.WorkerPressureSignal{Name: "thermal", Level: level, RecommendedAction: action, Reason: reason})
	}

	if worker.MaxConcurrentJobs > 0 {
		utilization := float64(worker.CurrentQueueDepth) / float64(worker.MaxConcurrentJobs)
		level := domain.WorkerPressureNominal
		reason := "queue nominal"
		if utilization >= thresholds.QueueCriticalRatio {
			level = domain.WorkerPressureCritical
			reason = "queue full"
		} else if utilization > thresholds.QueueWarningRatio {
			level = domain.WorkerPressureWarning
			reason = "queue warning threshold exceeded"
		}
		assessment.Signals = append(assessment.Signals, domain.WorkerPressureSignal{Name: "queue", Level: level, RecommendedAction: domain.WorkerPressureActionNone, Reason: reason})
	}

	classifyAssessment(assessment)
	return assessment
}

func freeBytesSignal(name string, availableBytes, warningFloor, criticalFloor int64, cleanupable bool, reclaimableBytes int64) domain.WorkerPressureSignal {
	signal := domain.WorkerPressureSignal{Name: name, Level: domain.WorkerPressureNominal, RecommendedAction: domain.WorkerPressureActionNone, Reason: name + " nominal"}
	if availableBytes < criticalFloor {
		signal.Level = domain.WorkerPressureCritical
		if cleanupable && reclaimableBytes >= criticalFloor-availableBytes {
			signal.RecommendedAction = domain.WorkerPressureActionCleanupRecommended
			signal.Reason = name + " critical with sufficient reclaimable docker data"
		} else {
			signal.RecommendedAction = domain.WorkerPressureActionOperatorIntervention
			signal.Reason = name + " critical without automatic cleanup path"
		}
	} else if availableBytes < warningFloor {
		signal.Level = domain.WorkerPressureWarning
		if cleanupable && reclaimableBytes > 0 {
			signal.RecommendedAction = domain.WorkerPressureActionCleanupRecommended
			signal.Reason = name + " warning with reclaimable docker data"
		} else if cleanupable {
			signal.RecommendedAction = domain.WorkerPressureActionOperatorIntervention
			signal.Reason = name + " warning without docker reclaim data"
		} else {
			signal.RecommendedAction = domain.WorkerPressureActionOperatorIntervention
			signal.Reason = name + " warning"
		}
	}
	return signal
}

func classifyAssessment(assessment *domain.WorkerPressureAssessment) {
	criticalNonCleanup := false
	criticalDiskCleanup := false
	warning := false
	cleanupRecommended := false
	operatorIntervention := false
	for _, signal := range assessment.Signals {
		if signal.RecommendedAction == domain.WorkerPressureActionCleanupRecommended {
			cleanupRecommended = true
		}
		if signal.RecommendedAction == domain.WorkerPressureActionOperatorIntervention {
			operatorIntervention = true
		}
		if signal.Level == domain.WorkerPressureUnknown && signal.RecommendedAction == domain.WorkerPressureActionOperatorIntervention {
			if assessment.OverallLevel != domain.WorkerPressureCritical {
				assessment.OverallLevel = domain.WorkerPressureWarning
			}
			warning = true
		}
		switch signal.Level {
		case domain.WorkerPressureCritical:
			assessment.OverallLevel = domain.WorkerPressureCritical
			if signal.Name == "disk" && signal.RecommendedAction == domain.WorkerPressureActionCleanupRecommended {
				criticalDiskCleanup = true
			} else {
				criticalNonCleanup = true
			}
		case domain.WorkerPressureWarning:
			if assessment.OverallLevel != domain.WorkerPressureCritical {
				assessment.OverallLevel = domain.WorkerPressureWarning
			}
			warning = true
		}
	}

	switch {
	case criticalNonCleanup:
		assessment.CapacityClass = domain.WorkerCapacityBlocked
	case criticalDiskCleanup:
		assessment.CapacityClass = domain.WorkerCapacityCleanupOnly
	case warning:
		assessment.CapacityClass = domain.WorkerCapacityReduced
	default:
		assessment.CapacityClass = domain.WorkerCapacityOpen
	}

	switch {
	case cleanupRecommended:
		assessment.RecommendedAction = domain.WorkerPressureActionCleanupRecommended
	case operatorIntervention:
		assessment.RecommendedAction = domain.WorkerPressureActionOperatorIntervention
	default:
		assessment.RecommendedAction = domain.WorkerPressureActionNone
	}
}

func hasHotOrWarmStandby(worker domain.Worker) bool {
	for _, assignment := range worker.StandbyAssignments {
		if assignment.Tier == domain.StandbyTierHot || assignment.Tier == domain.StandbyTierWarm {
			return true
		}
	}
	return false
}

func EffectiveWorkerPressureThresholds(thresholds WorkerPressureThresholds) WorkerPressureThresholds {
	defaults := DefaultWorkerPressureThresholds()
	if thresholds.MemoryWarningMinBytes <= 0 {
		thresholds.MemoryWarningMinBytes = defaults.MemoryWarningMinBytes
	}
	if thresholds.MemoryWarningMinRatio <= 0 {
		thresholds.MemoryWarningMinRatio = defaults.MemoryWarningMinRatio
	}
	if thresholds.MemoryCriticalMinBytes <= 0 {
		thresholds.MemoryCriticalMinBytes = defaults.MemoryCriticalMinBytes
	}
	if thresholds.MemoryCriticalMinRatio <= 0 {
		thresholds.MemoryCriticalMinRatio = defaults.MemoryCriticalMinRatio
	}
	if thresholds.DiskWarningMinBytes <= 0 {
		thresholds.DiskWarningMinBytes = defaults.DiskWarningMinBytes
	}
	if thresholds.DiskWarningMinRatio <= 0 {
		thresholds.DiskWarningMinRatio = defaults.DiskWarningMinRatio
	}
	if thresholds.DiskCriticalMinBytes <= 0 {
		thresholds.DiskCriticalMinBytes = defaults.DiskCriticalMinBytes
	}
	if thresholds.DiskCriticalMinRatio <= 0 {
		thresholds.DiskCriticalMinRatio = defaults.DiskCriticalMinRatio
	}
	if thresholds.VRAMWarningMinBytes <= 0 {
		thresholds.VRAMWarningMinBytes = defaults.VRAMWarningMinBytes
	}
	if thresholds.VRAMWarningMinRatio <= 0 {
		thresholds.VRAMWarningMinRatio = defaults.VRAMWarningMinRatio
	}
	if thresholds.VRAMCriticalMinBytes <= 0 {
		thresholds.VRAMCriticalMinBytes = defaults.VRAMCriticalMinBytes
	}
	if thresholds.VRAMCriticalMinRatio <= 0 {
		thresholds.VRAMCriticalMinRatio = defaults.VRAMCriticalMinRatio
	}
	if thresholds.ThermalWarningC <= 0 {
		thresholds.ThermalWarningC = defaults.ThermalWarningC
	}
	if thresholds.ThermalCriticalC <= 0 {
		thresholds.ThermalCriticalC = defaults.ThermalCriticalC
	}
	if thresholds.QueueWarningRatio <= 0 {
		thresholds.QueueWarningRatio = defaults.QueueWarningRatio
	}
	if thresholds.QueueCriticalRatio <= 0 {
		thresholds.QueueCriticalRatio = defaults.QueueCriticalRatio
	}
	return thresholds
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

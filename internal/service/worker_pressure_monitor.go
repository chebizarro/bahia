package service

import (
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

type pressureSnapshot struct {
	lastAdvertisementAt time.Time
	pressure            *domain.WorkerPressureAssessment
}

// WorkerPressureMonitor tracks worker pressure observations and reports
// capacity-class transitions.
type WorkerPressureMonitor struct {
	mu        sync.Mutex
	snapshots map[string]pressureSnapshot
}

func NewWorkerPressureMonitor() *WorkerPressureMonitor {
	return &WorkerPressureMonitor{snapshots: make(map[string]pressureSnapshot)}
}

func (m *WorkerPressureMonitor) Observe(worker domain.Worker) (previous, current *domain.WorkerPressureAssessment, changed bool) {
	if m == nil {
		return nil, copyPressureAssessment(worker.Pressure), false
	}
	pubkey := strings.TrimSpace(worker.PubKey)
	if pubkey == "" {
		return nil, copyPressureAssessment(worker.Pressure), false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	incoming := copyPressureAssessment(worker.Pressure)
	stored, exists := m.snapshots[pubkey]
	if exists && worker.LastAdvertisementAt.Before(stored.lastAdvertisementAt) {
		storedPressure := copyPressureAssessment(stored.pressure)
		return storedPressure, storedPressure, false
	}

	if !exists {
		m.snapshots[pubkey] = pressureSnapshot{lastAdvertisementAt: worker.LastAdvertisementAt, pressure: incoming}
		return nil, copyPressureAssessment(incoming), false
	}

	previous = copyPressureAssessment(stored.pressure)
	current = copyPressureAssessment(incoming)
	changed = capacityClass(previous) != capacityClass(current)
	m.snapshots[pubkey] = pressureSnapshot{lastAdvertisementAt: worker.LastAdvertisementAt, pressure: incoming}
	return previous, current, changed
}

func capacityClass(pressure *domain.WorkerPressureAssessment) domain.WorkerCapacityClass {
	if pressure == nil {
		return ""
	}
	return pressure.CapacityClass
}

func copyPressureAssessment(in *domain.WorkerPressureAssessment) *domain.WorkerPressureAssessment {
	if in == nil {
		return nil
	}
	out := *in
	if in.Signals != nil {
		out.Signals = append([]domain.WorkerPressureSignal(nil), in.Signals...)
	}
	return &out
}

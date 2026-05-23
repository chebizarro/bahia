package service

import (
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// HeartbeatMonitor records worker heartbeat observations and reports freshness state.
type HeartbeatMonitor interface {
	Observe(obs domain.HeartbeatObservation)
	Snapshot(pubkey string) (HeartbeatSnapshot, bool)
	ListExpired(now time.Time) []HeartbeatSnapshot
}

// HeartbeatSnapshot is the latest observed heartbeat state for one worker.
type HeartbeatSnapshot struct {
	WorkerPubKey   string
	LastObservedAt time.Time
	Sequence       uint64
	Interval       time.Duration
	ExpiresAfter   time.Duration
	Status         domain.HeartbeatStatus
}

// InMemoryHeartbeatMonitor stores heartbeat observations in process memory.
type InMemoryHeartbeatMonitor struct {
	mu        sync.RWMutex
	clock     func() time.Time
	snapshots map[string]HeartbeatSnapshot
}

// NewInMemoryHeartbeatMonitor returns an empty heartbeat monitor.
func NewInMemoryHeartbeatMonitor() *InMemoryHeartbeatMonitor {
	return &InMemoryHeartbeatMonitor{
		clock:     func() time.Time { return time.Now().UTC() },
		snapshots: make(map[string]HeartbeatSnapshot),
	}
}

// Observe records an observation unless it is older than the current state.
func (m *InMemoryHeartbeatMonitor) Observe(obs domain.HeartbeatObservation) {
	if m == nil || obs.WorkerPubKey == "" || obs.ObservedAt.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshots == nil {
		m.snapshots = make(map[string]HeartbeatSnapshot)
	}
	current, ok := m.snapshots[obs.WorkerPubKey]
	if ok && !heartbeatObservationNewer(current, obs) {
		return
	}
	expiresAfter := obs.ExpiresAfter
	if expiresAfter <= 0 && obs.Interval > 0 {
		expiresAfter = 3 * obs.Interval
	}
	m.snapshots[obs.WorkerPubKey] = HeartbeatSnapshot{
		WorkerPubKey:   obs.WorkerPubKey,
		LastObservedAt: obs.ObservedAt,
		Sequence:       obs.Sequence,
		Interval:       obs.Interval,
		ExpiresAfter:   expiresAfter,
		Status:         heartbeatStatus(obs.ObservedAt, expiresAfter, m.now()),
	}
}

// Snapshot returns the latest heartbeat snapshot for a worker with freshness computed at read time.
func (m *InMemoryHeartbeatMonitor) Snapshot(pubkey string) (HeartbeatSnapshot, bool) {
	if m == nil {
		return HeartbeatSnapshot{}, false
	}
	m.mu.RLock()
	snapshot, ok := m.snapshots[pubkey]
	m.mu.RUnlock()
	if !ok {
		return HeartbeatSnapshot{}, false
	}
	snapshot.Status = heartbeatStatus(snapshot.LastObservedAt, snapshot.ExpiresAfter, m.now())
	return snapshot, true
}

// ListExpired returns all snapshots whose heartbeat freshness is expired at now.
func (m *InMemoryHeartbeatMonitor) ListExpired(now time.Time) []HeartbeatSnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	expired := make([]HeartbeatSnapshot, 0)
	for _, snapshot := range m.snapshots {
		snapshot.Status = heartbeatStatus(snapshot.LastObservedAt, snapshot.ExpiresAfter, now)
		if snapshot.Status == domain.HeartbeatStatusExpired {
			expired = append(expired, snapshot)
		}
	}
	return expired
}

func (m *InMemoryHeartbeatMonitor) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now().UTC()
}

func heartbeatObservationNewer(current HeartbeatSnapshot, obs domain.HeartbeatObservation) bool {
	if obs.Sequence == 0 || current.Sequence == 0 {
		return obs.ObservedAt.After(current.LastObservedAt)
	}
	if obs.Sequence < current.Sequence {
		return false
	}
	if obs.Sequence > current.Sequence {
		return true
	}
	return obs.ObservedAt.After(current.LastObservedAt)
}

func heartbeatStatus(observedAt time.Time, expiresAfter time.Duration, now time.Time) domain.HeartbeatStatus {
	if observedAt.IsZero() || expiresAfter <= 0 {
		return domain.HeartbeatStatusUnknown
	}
	age := now.Sub(observedAt)
	if age < 0 {
		age = 0
	}
	if age > expiresAfter {
		return domain.HeartbeatStatusExpired
	}
	if age > expiresAfter/2 {
		return domain.HeartbeatStatusStale
	}
	return domain.HeartbeatStatusFresh
}

package service

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestInMemoryHeartbeatMonitorObserveDefaultsExpiryAndReportsFreshness(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }

	monitor.Observe(domain.HeartbeatObservation{
		WorkerPubKey: "worker-a",
		ObservedAt:   now.Add(-5 * time.Second),
		Sequence:     1,
		Interval:     10 * time.Second,
	})

	snapshot, ok := monitor.Snapshot("worker-a")
	require.True(t, ok)
	require.Equal(t, "worker-a", snapshot.WorkerPubKey)
	require.Equal(t, uint64(1), snapshot.Sequence)
	require.Equal(t, 10*time.Second, snapshot.Interval)
	require.Equal(t, 30*time.Second, snapshot.ExpiresAfter)
	require.Equal(t, domain.HeartbeatStatusFresh, snapshot.Status)
}

func TestInMemoryHeartbeatMonitorDeduplicatesBySequence(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }

	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "worker-a", ObservedAt: now.Add(-20 * time.Second), Sequence: 5, Interval: 10 * time.Second})
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "worker-a", ObservedAt: now, Sequence: 4, Interval: 10 * time.Second})

	snapshot, ok := monitor.Snapshot("worker-a")
	require.True(t, ok)
	require.Equal(t, uint64(5), snapshot.Sequence)
	require.True(t, snapshot.LastObservedAt.Equal(now.Add(-20*time.Second)))

	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "worker-a", ObservedAt: now.Add(-10 * time.Second), Sequence: 5, Interval: 10 * time.Second})
	snapshot, ok = monitor.Snapshot("worker-a")
	require.True(t, ok)
	require.Equal(t, uint64(5), snapshot.Sequence)
	require.True(t, snapshot.LastObservedAt.Equal(now.Add(-10*time.Second)))
}

func TestInMemoryHeartbeatMonitorSequenceZeroUsesObservedAt(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }

	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "worker-a", ObservedAt: now.Add(-10 * time.Second), Interval: 10 * time.Second})
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "worker-a", ObservedAt: now.Add(-20 * time.Second), Interval: 10 * time.Second})
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "worker-a", ObservedAt: now.Add(-5 * time.Second), Interval: 10 * time.Second})

	snapshot, ok := monitor.Snapshot("worker-a")
	require.True(t, ok)
	require.Equal(t, uint64(0), snapshot.Sequence)
	require.True(t, snapshot.LastObservedAt.Equal(now.Add(-5*time.Second)))
}

func TestInMemoryHeartbeatMonitorStatusTransitionsAndListExpired(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }

	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "fresh", ObservedAt: now.Add(-10 * time.Second), Sequence: 1, ExpiresAfter: 30 * time.Second})
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "stale", ObservedAt: now.Add(-20 * time.Second), Sequence: 1, ExpiresAfter: 30 * time.Second})
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "expired", ObservedAt: now.Add(-31 * time.Second), Sequence: 1, ExpiresAfter: 30 * time.Second})

	fresh, ok := monitor.Snapshot("fresh")
	require.True(t, ok)
	require.Equal(t, domain.HeartbeatStatusFresh, fresh.Status)
	stale, ok := monitor.Snapshot("stale")
	require.True(t, ok)
	require.Equal(t, domain.HeartbeatStatusStale, stale.Status)
	expired, ok := monitor.Snapshot("expired")
	require.True(t, ok)
	require.Equal(t, domain.HeartbeatStatusExpired, expired.Status)

	expiredList := monitor.ListExpired(now)
	require.Len(t, expiredList, 1)
	require.Equal(t, "expired", expiredList[0].WorkerPubKey)
	require.Equal(t, domain.HeartbeatStatusExpired, expiredList[0].Status)
}

func TestInMemoryHeartbeatMonitorUnknownSnapshot(t *testing.T) {
	monitor := NewInMemoryHeartbeatMonitor()

	_, ok := monitor.Snapshot("missing")
	require.False(t, ok)
}

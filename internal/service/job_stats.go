// Package service provides domain services.
package service

import (
	"sync"
	"time"
)

// JobStats contains aggregated job statistics for a worker.
type JobStats struct {
	WorkerPubkey   string    `json:"worker_pubkey"`
	TotalCompleted int64     `json:"total_completed"`
	TotalFailed    int64     `json:"total_failed"`
	AvgDurationMs  int64     `json:"avg_duration_ms"`
	LastJobAt      time.Time `json:"last_job_at,omitempty"`
}

// jobRecord stores a single job completion record.
type jobRecord struct {
	durationMs int64
	success    bool
	timestamp  time.Time
}

// workerStats holds per-worker statistics with a ring buffer for recent jobs.
type workerStats struct {
	totalCompleted int64
	totalFailed    int64
	lastJobAt      time.Time

	// Ring buffer for recent job durations (for rolling average)
	recentJobs []jobRecord
	nextIdx    int
}

// JobStatsTracker tracks job completion statistics per worker.
// It uses an in-memory ring buffer to calculate rolling averages.
type JobStatsTracker struct {
	mu           sync.RWMutex
	workers      map[string]*workerStats
	ringCapacity int // Number of recent jobs to track per worker
}

// NewJobStatsTracker creates a new JobStatsTracker.
// ringCapacity determines how many recent jobs are used for average calculation.
func NewJobStatsTracker(ringCapacity int) *JobStatsTracker {
	if ringCapacity <= 0 {
		ringCapacity = 100 // Default to last 100 jobs
	}
	return &JobStatsTracker{
		workers:      make(map[string]*workerStats),
		ringCapacity: ringCapacity,
	}
}

// RecordJobCompletion records a job completion for a worker.
func (t *JobStatsTracker) RecordJobCompletion(workerPubkey string, durationMs int64, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ws, ok := t.workers[workerPubkey]
	if !ok {
		ws = &workerStats{
			recentJobs: make([]jobRecord, 0, t.ringCapacity),
		}
		t.workers[workerPubkey] = ws
	}

	now := time.Now().UTC()
	ws.lastJobAt = now

	if success {
		ws.totalCompleted++
	} else {
		ws.totalFailed++
	}

	// Add to ring buffer
	record := jobRecord{
		durationMs: durationMs,
		success:    success,
		timestamp:  now,
	}

	if len(ws.recentJobs) < t.ringCapacity {
		ws.recentJobs = append(ws.recentJobs, record)
	} else {
		ws.recentJobs[ws.nextIdx] = record
		ws.nextIdx = (ws.nextIdx + 1) % t.ringCapacity
	}
}

// GetStats returns aggregated statistics for a worker.
func (t *JobStatsTracker) GetStats(workerPubkey string) JobStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ws, ok := t.workers[workerPubkey]
	if !ok {
		return JobStats{WorkerPubkey: workerPubkey}
	}

	stats := JobStats{
		WorkerPubkey:   workerPubkey,
		TotalCompleted: ws.totalCompleted,
		TotalFailed:    ws.totalFailed,
		LastJobAt:      ws.lastJobAt,
	}

	// Calculate average duration from successful jobs in ring buffer
	var totalDuration int64
	var count int64
	for _, job := range ws.recentJobs {
		if job.success && job.durationMs > 0 {
			totalDuration += job.durationMs
			count++
		}
	}
	if count > 0 {
		stats.AvgDurationMs = totalDuration / count
	}

	return stats
}

// GetAllStats returns statistics for all tracked workers.
func (t *JobStatsTracker) GetAllStats() []JobStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]JobStats, 0, len(t.workers))
	for pubkey := range t.workers {
		// Release read lock temporarily to call GetStats (which acquires its own lock)
		t.mu.RUnlock()
		stats := t.GetStats(pubkey)
		t.mu.RLock()
		result = append(result, stats)
	}
	return result
}

// SuccessRate returns the success rate for a worker (0.0 to 1.0).
func (t *JobStatsTracker) SuccessRate(workerPubkey string) float64 {
	stats := t.GetStats(workerPubkey)
	total := stats.TotalCompleted + stats.TotalFailed
	if total == 0 {
		return 0
	}
	return float64(stats.TotalCompleted) / float64(total)
}

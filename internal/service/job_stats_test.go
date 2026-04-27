package service

import (
	"testing"
)

func TestJobStatsTracker_RecordJobCompletion(t *testing.T) {
	tracker := NewJobStatsTracker(10)

	// Record some successful jobs
	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker1", 2000, true)
	tracker.RecordJobCompletion("worker1", 3000, true)

	stats := tracker.GetStats("worker1")
	if stats.TotalCompleted != 3 {
		t.Errorf("TotalCompleted = %d, want 3", stats.TotalCompleted)
	}
	if stats.TotalFailed != 0 {
		t.Errorf("TotalFailed = %d, want 0", stats.TotalFailed)
	}
	if stats.AvgDurationMs != 2000 {
		t.Errorf("AvgDurationMs = %d, want 2000", stats.AvgDurationMs)
	}
}

func TestJobStatsTracker_FailedJobs(t *testing.T) {
	tracker := NewJobStatsTracker(10)

	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker1", 0, false)
	tracker.RecordJobCompletion("worker1", 2000, true)
	tracker.RecordJobCompletion("worker1", 0, false)

	stats := tracker.GetStats("worker1")
	if stats.TotalCompleted != 2 {
		t.Errorf("TotalCompleted = %d, want 2", stats.TotalCompleted)
	}
	if stats.TotalFailed != 2 {
		t.Errorf("TotalFailed = %d, want 2", stats.TotalFailed)
	}
	// Average should only include successful jobs
	if stats.AvgDurationMs != 1500 {
		t.Errorf("AvgDurationMs = %d, want 1500", stats.AvgDurationMs)
	}
}

func TestJobStatsTracker_RingBuffer(t *testing.T) {
	tracker := NewJobStatsTracker(3) // Small capacity for testing

	// Fill the ring buffer
	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker1", 2000, true)
	tracker.RecordJobCompletion("worker1", 3000, true)

	stats := tracker.GetStats("worker1")
	if stats.AvgDurationMs != 2000 {
		t.Errorf("AvgDurationMs = %d, want 2000 (avg of 1000, 2000, 3000)", stats.AvgDurationMs)
	}

	// Add more jobs - should overwrite oldest
	tracker.RecordJobCompletion("worker1", 6000, true)
	tracker.RecordJobCompletion("worker1", 6000, true)
	tracker.RecordJobCompletion("worker1", 6000, true)

	stats = tracker.GetStats("worker1")
	// Should now average only the last 3 jobs (all 6000ms)
	if stats.AvgDurationMs != 6000 {
		t.Errorf("AvgDurationMs = %d, want 6000", stats.AvgDurationMs)
	}
	// Total should still reflect all jobs
	if stats.TotalCompleted != 6 {
		t.Errorf("TotalCompleted = %d, want 6", stats.TotalCompleted)
	}
}

func TestJobStatsTracker_UnknownWorker(t *testing.T) {
	tracker := NewJobStatsTracker(10)

	stats := tracker.GetStats("unknown")
	if stats.TotalCompleted != 0 {
		t.Errorf("TotalCompleted = %d, want 0", stats.TotalCompleted)
	}
	if stats.AvgDurationMs != 0 {
		t.Errorf("AvgDurationMs = %d, want 0", stats.AvgDurationMs)
	}
}

func TestJobStatsTracker_SuccessRate(t *testing.T) {
	tracker := NewJobStatsTracker(10)

	// 75% success rate
	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker1", 0, false)

	rate := tracker.SuccessRate("worker1")
	if rate != 0.75 {
		t.Errorf("SuccessRate = %f, want 0.75", rate)
	}

	// Unknown worker
	rate = tracker.SuccessRate("unknown")
	if rate != 0 {
		t.Errorf("SuccessRate for unknown worker = %f, want 0", rate)
	}
}

func TestJobStatsTracker_MultipleWorkers(t *testing.T) {
	tracker := NewJobStatsTracker(10)

	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker2", 5000, true)
	tracker.RecordJobCompletion("worker1", 2000, true)

	stats1 := tracker.GetStats("worker1")
	stats2 := tracker.GetStats("worker2")

	if stats1.TotalCompleted != 2 {
		t.Errorf("worker1 TotalCompleted = %d, want 2", stats1.TotalCompleted)
	}
	if stats2.TotalCompleted != 1 {
		t.Errorf("worker2 TotalCompleted = %d, want 1", stats2.TotalCompleted)
	}
	if stats1.AvgDurationMs != 1500 {
		t.Errorf("worker1 AvgDurationMs = %d, want 1500", stats1.AvgDurationMs)
	}
	if stats2.AvgDurationMs != 5000 {
		t.Errorf("worker2 AvgDurationMs = %d, want 5000", stats2.AvgDurationMs)
	}
}

func TestJobStatsTracker_GetAllStats(t *testing.T) {
	tracker := NewJobStatsTracker(10)

	tracker.RecordJobCompletion("worker1", 1000, true)
	tracker.RecordJobCompletion("worker2", 2000, true)
	tracker.RecordJobCompletion("worker3", 3000, true)

	allStats := tracker.GetAllStats()
	if len(allStats) != 3 {
		t.Errorf("GetAllStats returned %d workers, want 3", len(allStats))
	}
}

func TestJobStatsTracker_DefaultCapacity(t *testing.T) {
	tracker := NewJobStatsTracker(0) // Should default to 100

	if tracker.ringCapacity != 100 {
		t.Errorf("ringCapacity = %d, want 100 (default)", tracker.ringCapacity)
	}
}

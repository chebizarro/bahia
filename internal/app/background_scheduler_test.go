package app

import (
	"context"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// --- BackupSchedulerRunner tests -------------------------------------------

func TestBackupSchedulerRunner_NextDueTime(t *testing.T) {
	now := time.Now().UTC()
	fiveMinFromNow := now.Add(5 * time.Minute)

	sched := &fakeBackupScheduler{
		nextDue: &fiveMinFromNow,
	}
	runner := NewBackupSchedulerRunner(sched, 10*time.Minute, zap.NewNop())
	ctx := context.Background()

	interval := runner.nextDueInterval(ctx)
	// Should be approximately 5 minutes (within tolerance).
	if interval < 4*time.Minute || interval > 6*time.Minute {
		t.Errorf("expected ~5m interval, got %v", interval)
	}
}

func TestBackupSchedulerRunner_NextDueTimeNil(t *testing.T) {
	sched := &fakeBackupScheduler{nextDue: nil}
	runner := NewBackupSchedulerRunner(sched, 7*time.Minute, zap.NewNop())
	ctx := context.Background()

	interval := runner.nextDueInterval(ctx)
	if interval != 7*time.Minute {
		t.Errorf("expected 7m fallback interval, got %v", interval)
	}
}

func TestBackupSchedulerRunner_NextDueTimeAlreadyPast(t *testing.T) {
	past := time.Now().UTC().Add(-1 * time.Minute)
	sched := &fakeBackupScheduler{nextDue: &past}
	runner := NewBackupSchedulerRunner(sched, 10*time.Minute, zap.NewNop())
	ctx := context.Background()

	interval := runner.nextDueInterval(ctx)
	if interval != time.Second {
		t.Errorf("expected 1s for past due time, got %v", interval)
	}
}

func TestBackupSchedulerRunner_TriggerWakesRunner(t *testing.T) {
	runner := NewBackupSchedulerRunner(&fakeBackupScheduler{}, 0, zap.NewNop())

	// Trigger should not block.
	runner.Trigger()
	runner.Trigger() // second trigger should not block either (buffered channel)

	// Drain the trigger channel.
	select {
	case <-runner.triggerCh:
		// Good.
	default:
		t.Error("expected trigger to be present in channel")
	}
}

type fakeBackupScheduler struct {
	nextDue   *time.Time
	processed int
}

func (f *fakeBackupScheduler) ProcessDueSchedules(_ context.Context) (*service.BackupScheduleProcessResult, error) {
	f.processed++
	return &service.BackupScheduleProcessResult{}, nil
}

func (f *fakeBackupScheduler) NextDueTime(_ context.Context) (*time.Time, error) {
	return f.nextDue, nil
}

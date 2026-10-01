package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type mockScheduler struct {
	processFunc func(ctx context.Context) (*service.BackupScheduleProcessResult, error)
}

func (m *mockScheduler) ProcessDueSchedules(ctx context.Context) (*service.BackupScheduleProcessResult, error) {
	return m.processFunc(ctx)
}

func TestBackupSchedulerRunnerName(t *testing.T) {
	r := NewBackupSchedulerRunner(nil, 0, nil)
	if got := r.Name(); got != "backup-scheduler" {
		t.Errorf("Name() = %q, want %q", got, "backup-scheduler")
	}
}

func TestBackupSchedulerRunnerNilScheduler(t *testing.T) {
	r := NewBackupSchedulerRunner(nil, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Run(ctx); err != nil {
		t.Errorf("Run() with nil scheduler = %v, want nil", err)
	}
}

func TestBackupSchedulerRunnerInitialRunFailureVisibility(t *testing.T) {
	called := int32(0)
	mock := &mockScheduler{
		processFunc: func(ctx context.Context) (*service.BackupScheduleProcessResult, error) {
			atomic.AddInt32(&called, 1)
			return nil, errors.New("scheduler failure")
		},
	}
	r := NewBackupSchedulerRunner(mock, time.Hour, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.Run(ctx); err != nil {
		t.Errorf("Run() = %v, want nil (errors logged, not returned)", err)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("ProcessDueSchedules called %d times, want 1 (initial run only)", atomic.LoadInt32(&called))
	}
}

func TestBackupSchedulerRunnerProcessesDueSchedulesOnStartup(t *testing.T) {
	called := int32(0)
	mock := &mockScheduler{
		processFunc: func(ctx context.Context) (*service.BackupScheduleProcessResult, error) {
			atomic.AddInt32(&called, 1)
			return &service.BackupScheduleProcessResult{
				Checked:    5,
				Dispatched: 2,
				Skipped:    1,
				MissedRuns: 3,
				Dispatches: []service.BackupScheduleDispatch{
					{DefinitionID: uuid.New(), RunID: uuid.New(), MissedRuns: 1},
					{DefinitionID: uuid.New(), RunID: uuid.New(), MissedRuns: 2},
				},
			}, nil
		},
	}
	r := NewBackupSchedulerRunner(mock, time.Hour, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.Run(ctx); err != nil {
		t.Errorf("Run() = %v, want nil", err)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("ProcessDueSchedules called %d times, want 1", atomic.LoadInt32(&called))
	}
}

// cancelAfterCalls returns a scheduler that cancels its context from inside
// the nth call. Cancellation happens exactly once (bahia-fz9gs: the old fake
// closed a channel on every call from the 4th on, so a 5th tick racing the
// cancellation panicked), and the runner's return is driven by that call,
// not by elapsed time.
func cancelAfterCalls(n int32, cancel context.CancelFunc, calls *atomic.Int32) *mockScheduler {
	var once sync.Once
	return &mockScheduler{
		processFunc: func(ctx context.Context) (*service.BackupScheduleProcessResult, error) {
			if calls.Add(1) >= n {
				once.Do(cancel)
			}
			return &service.BackupScheduleProcessResult{Checked: 1}, nil
		},
	}
}

func TestBackupSchedulerRunnerPeriodicProcessing(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewBackupSchedulerRunner(cancelAfterCalls(4, cancel, &calls), time.Millisecond, zap.NewNop())

	if err := r.Run(ctx); err != nil {
		t.Errorf("Run() = %v, want nil", err)
	}
	if count := calls.Load(); count < 4 {
		t.Errorf("ProcessDueSchedules called %d times, want at least 4 (initial + 3 periodic)", count)
	}
}

func TestBackupSchedulerRunnerIdempotentRestart(t *testing.T) {
	var total int32
	for i := 0; i < 3; i++ {
		var calls atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		r := NewBackupSchedulerRunner(cancelAfterCalls(3, cancel, &calls), time.Millisecond, zap.NewNop())
		if err := r.Run(ctx); err != nil {
			t.Errorf("Run iteration %d: %v", i, err)
		}
		cancel()
		if count := calls.Load(); count < 3 {
			t.Errorf("Run iteration %d: ProcessDueSchedules called %d times, want at least 3 (initial + 2 periodic)", i, count)
		}
		total += calls.Load()
	}
	if total < 9 {
		t.Errorf("ProcessDueSchedules called %d times across 3 restarts, want at least 9", total)
	}
}

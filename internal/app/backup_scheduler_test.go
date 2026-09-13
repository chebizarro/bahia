package app

import (
	"context"
	"errors"
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

func TestBackupSchedulerRunnerPeriodicProcessing(t *testing.T) {
	var callCount int32
	mock := &mockScheduler{
		processFunc: func(ctx context.Context) (*service.BackupScheduleProcessResult, error) {
			atomic.AddInt32(&callCount, 1)
			return &service.BackupScheduleProcessResult{Checked: 1}, nil
		},
	}
	r := NewBackupSchedulerRunner(mock, 10*time.Millisecond, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if err := r.Run(ctx); err != nil {
		t.Errorf("Run() = %v, want nil", err)
	}
	count := atomic.LoadInt32(&callCount)
	if count < 2 {
		t.Errorf("ProcessDueSchedules called %d times, want at least 2 (initial + periodic)", count)
	}
}

func TestBackupSchedulerRunnerIdempotentRestart(t *testing.T) {
	var callCount int32
	mock := &mockScheduler{
		processFunc: func(ctx context.Context) (*service.BackupScheduleProcessResult, error) {
			atomic.AddInt32(&callCount, 1)
			return &service.BackupScheduleProcessResult{Checked: 1, Dispatched: 0}, nil
		},
	}
	r := NewBackupSchedulerRunner(mock, 10*time.Millisecond, zap.NewNop())

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()
		if err := r.Run(ctx); err != nil {
			t.Errorf("Run iteration %d: %v", i, err)
		}
	}
	if atomic.LoadInt32(&callCount) < 3 {
		t.Errorf("ProcessDueSchedules called %d times across 3 restarts, want at least 3", atomic.LoadInt32(&callCount))
	}
}

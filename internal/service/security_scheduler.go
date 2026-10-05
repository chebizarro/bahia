package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type SecurityScheduleDeriver interface {
	DeriveSecurityScanSchedules(ctx context.Context) error
}

type SecurityScheduledScanner interface {
	SubmitScan(ctx context.Context, req SecurityScanRequest) (*SecurityScanAccepted, error)
}

type SecuritySchedulerConfig struct {
	// Repo is the canonical security store: schedules, targets and run
	// claims are read from and published to canonical cp-state.
	Repo     repository.SecurityRepository
	Scanner  SecurityScheduledScanner
	Deriver  SecurityScheduleDeriver
	Interval time.Duration
	// Ready, when set, returns a channel that is closed once the local event
	// store has caught up with the relays. It is asked when the scheduler
	// starts; the scheduler does not derive or dispatch before the channel
	// closes, so it never acts on a partial view of policy and target state.
	Ready     func() <-chan struct{}
	BatchSize int
	WorkerID  string
	Logger    *zap.Logger
	Now       func() time.Time
}

// SecurityScheduler dispatches policy-derived periodic scans (audit B-32).
//
// Everything it acts on is canonical cp-state in the local event store: on
// each wakeup it re-derives the schedules from the retained policy and target
// records, then dispatches the schedules that are due. There is no SQL lease.
// A due schedule is claimed by publishing the scan's run record under an id
// derived from the schedule and its due time, so any number of wakeups, and a
// restart between the claim and the schedule update, resolve to one run.
type SecurityScheduler struct {
	mu        sync.Mutex
	repo      repository.SecurityRepository
	scanner   SecurityScheduledScanner
	deriver   SecurityScheduleDeriver
	interval  time.Duration
	ready     func() <-chan struct{}
	batchSize int
	workerID  string
	logger    *zap.Logger
	now       func() time.Time
}

func NewSecurityScheduler(cfg SecuritySchedulerConfig) *SecurityScheduler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 100
	}
	worker := cfg.WorkerID
	if worker == "" {
		worker = "security-scheduler"
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SecurityScheduler{repo: cfg.Repo, scanner: cfg.Scanner, deriver: cfg.Deriver, interval: interval, ready: cfg.Ready, batchSize: batch, workerID: worker, logger: logger.Named("security-scheduler"), now: now}
}

func (s *SecurityScheduler) Name() string { return "security-osv-scheduler" }

func (s *SecurityScheduler) Run(ctx context.Context) error {
	if err := s.configured(); err != nil {
		return err
	}
	if s.ready != nil {
		select {
		case <-s.ready():
		case <-ctx.Done():
			return nil
		}
	}
	s.wake(ctx)
	// The ticker is the due-time wakeup: schedule intervals are hours or
	// days, and nothing is polled for completion here.
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.wake(ctx)
		}
	}
}

// wake re-derives the schedules from canonical policy and target state and
// dispatches the ones that are due.
func (s *SecurityScheduler) wake(ctx context.Context) {
	if s.deriver != nil {
		if err := s.deriver.DeriveSecurityScanSchedules(ctx); err != nil {
			s.logger.Warn("security schedule derivation failed", zap.Error(err))
		}
	}
	if err := s.Tick(ctx); err != nil {
		s.logger.Warn("security scheduler tick failed", zap.Error(err))
	}
}

// Tick dispatches every enabled schedule that is due, oldest first, up to the
// batch size. A schedule that cannot be dispatched does not hold back the
// others; it stays due and is retried on the next wakeup. Concurrent ticks are
// serialized, and the run claim keeps a tick of another scheduler idempotent.
func (s *SecurityScheduler) Tick(ctx context.Context) error {
	if err := s.configured(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	schedules, err := s.repo.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{EnabledOnly: true})
	if err != nil {
		return err
	}
	var failed []error
	dispatched := 0
	for _, schedule := range schedules {
		if !schedule.Enabled || schedule.NextDueAt.After(now) {
			continue
		}
		if dispatched >= s.batchSize {
			break
		}
		dispatched++
		if err := s.dispatchSchedule(ctx, schedule, now); err != nil {
			failed = append(failed, fmt.Errorf("schedule %s: %w", schedule.ID, err))
		}
	}
	return errors.Join(failed...)
}

// scheduledRunID is the claim of one scheduled scan: the same schedule and due
// time always name the same run coordinate.
func scheduledRunID(schedule domain.SecurityScanSchedule) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("security:scheduled-run:"+schedule.ID.String()+":"+schedule.NextDueAt.UTC().Format(time.RFC3339Nano)))
}

func (s *SecurityScheduler) dispatchSchedule(ctx context.Context, schedule domain.SecurityScanSchedule, dispatchedAt time.Time) error {
	nextDue := dispatchedAt.Add(time.Duration(schedule.IntervalSeconds) * time.Second)
	if active, err := s.repo.GetActiveSecurityScanRunByTargetHash(ctx, schedule.TargetKeyHash); err == nil {
		// The target is already being scanned (by this schedule's claim or
		// another trigger); that run satisfies this due time.
		return s.repo.MarkSecurityScheduleDispatched(ctx, schedule.ID, active.ID, dispatchedAt, nextDue)
	} else if !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	target, err := s.repo.GetSecurityTargetByHash(ctx, schedule.TargetKeyHash)
	if err != nil {
		return fmt.Errorf("security target %s: %w", schedule.TargetKeyHash, err)
	}
	accepted, err := s.scanner.SubmitScan(ctx, SecurityScanRequest{Target: targetInputFromStored(target), Trigger: domain.SecurityTriggerScheduled, RequestedBy: s.workerID, ScheduledRunID: scheduledRunID(schedule)})
	if err != nil {
		return err
	}
	if accepted.RunID == uuid.Nil {
		return errors.New("scheduled security scan accepted without run id")
	}
	return s.repo.MarkSecurityScheduleDispatched(ctx, schedule.ID, accepted.RunID, dispatchedAt, nextDue)
}

func (s *SecurityScheduler) configured() error {
	if s == nil {
		return errors.New("security scheduler is nil")
	}
	if s.repo == nil {
		return errors.New("security repository is not configured")
	}
	if s.scanner == nil {
		return errors.New("security scanner is not configured")
	}
	return nil
}

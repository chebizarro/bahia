package service

import (
	"context"
	"fmt"
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
	Repo          repository.SecurityRepository
	Scanner       SecurityScheduledScanner
	Deriver       SecurityScheduleDeriver
	Interval      time.Duration
	LeaseDuration time.Duration
	BatchSize     int
	WorkerID      string
	Logger        *zap.Logger
	Now           func() time.Time
}

type SecurityScheduler struct {
	repo          repository.SecurityRepository
	scanner       SecurityScheduledScanner
	deriver       SecurityScheduleDeriver
	interval      time.Duration
	leaseDuration time.Duration
	batchSize     int
	workerID      string
	logger        *zap.Logger
	now           func() time.Time
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
	lease := cfg.LeaseDuration
	if lease <= 0 {
		lease = 15 * time.Minute
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
	return &SecurityScheduler{repo: cfg.Repo, scanner: cfg.Scanner, deriver: cfg.Deriver, interval: interval, leaseDuration: lease, batchSize: batch, workerID: worker, logger: logger.Named("security-scheduler"), now: now}
}

func (s *SecurityScheduler) Name() string { return "security-osv-scheduler" }

func (s *SecurityScheduler) Run(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	if s.deriver != nil {
		if err := s.deriver.DeriveSecurityScanSchedules(ctx); err != nil {
			s.logger.Warn("security schedule derivation failed", zap.Error(err))
		}
	}
	if err := s.Tick(ctx); err != nil {
		s.logger.Warn("security scheduler startup tick failed", zap.Error(err))
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				s.logger.Warn("security scheduler tick failed", zap.Error(err))
			}
		}
	}
}

func (s *SecurityScheduler) Tick(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	now := s.now().UTC()
	due, err := s.repo.ClaimDueSecurityScanSchedules(ctx, now, s.batchSize, s.workerID, now.Add(s.leaseDuration))
	if err != nil {
		return err
	}
	for _, schedule := range due {
		if err := s.dispatchSchedule(ctx, schedule, now); err != nil {
			s.logger.Warn("security scheduled scan dispatch failed", zap.String("schedule_id", schedule.ID.String()), zap.Error(err))
		}
	}
	return nil
}

func (s *SecurityScheduler) dispatchSchedule(ctx context.Context, schedule domain.SecurityScanSchedule, dispatchedAt time.Time) error {
	if !schedule.Enabled {
		return nil
	}
	nextDue := dispatchedAt.Add(time.Duration(schedule.IntervalSeconds) * time.Second)
	if active, err := s.repo.GetActiveSecurityScanRunByTargetHash(ctx, schedule.TargetKeyHash); err == nil {
		return s.repo.MarkSecurityScheduleDispatched(ctx, schedule.ID, active.ID, dispatchedAt, nextDue)
	} else if err != nil && !errorsIsNotFound(err) {
		return err
	}
	target, err := s.repo.GetSecurityTargetByHash(ctx, schedule.TargetKeyHash)
	if err != nil {
		return err
	}
	accepted, err := s.scanner.SubmitScan(ctx, SecurityScanRequest{Target: targetInputFromStored(target), Trigger: domain.SecurityTriggerScheduled, RequestedBy: s.workerID})
	if err != nil {
		return err
	}
	runID := accepted.RunID
	if runID == uuid.Nil {
		return fmt.Errorf("scheduled Security scan accepted without run id")
	}
	return s.repo.MarkSecurityScheduleDispatched(ctx, schedule.ID, runID, dispatchedAt, nextDue)
}

func (s *SecurityScheduler) ready() error {
	if s == nil {
		return fmt.Errorf("security scheduler is nil")
	}
	if s.repo == nil {
		return fmt.Errorf("security repository is not configured")
	}
	if s.scanner == nil {
		return fmt.Errorf("security scanner is not configured")
	}
	return nil
}

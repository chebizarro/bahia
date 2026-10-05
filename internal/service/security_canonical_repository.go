package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// SecurityCanonicalStore publishes state through the signed outbox and reads
// retained, authored events from the local event store. SQL is not consulted.
type SecurityCanonicalStore interface {
	PublishTarget(context.Context, *domain.SecurityTarget) error
	PublishRun(context.Context, *domain.SecurityScanRun) error
	PublishSchedule(context.Context, *domain.SecurityScanSchedule) error
	PublishFinding(context.Context, domain.SecurityOSVFinding) error
	PublishFindingDetail(context.Context, domain.SecurityOSVFinding) error
	ListSecurityTargets(context.Context) ([]domain.SecurityTarget, error)
	ListSecurityRuns(context.Context) ([]domain.SecurityScanRun, error)
	ListSecuritySchedules(context.Context) ([]domain.SecurityScanSchedule, error)
	ListSecurityFindings(context.Context) ([]domain.SecurityOSVFinding, error)
}

// CanonicalSecurityRepository implements SecurityRepository from the retained
// event view. The embedded SQL repository is an optional write-behind index
// for compatibility reads and auxiliary caches only.
type CanonicalSecurityRepository struct {
	index     repository.SecurityRepository
	canonical SecurityCanonicalStore
	mu        sync.Mutex
	logger    *zap.Logger
}

var _ repository.SecurityRepository = (*CanonicalSecurityRepository)(nil)

func NewCanonicalSecurityRepository(index repository.SecurityRepository, canonical SecurityCanonicalStore, logger *zap.Logger) *CanonicalSecurityRepository {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CanonicalSecurityRepository{index: index, canonical: canonical, logger: logger.Named("security-canonical-store")}
}
func (r *CanonicalSecurityRepository) available() error {
	if r == nil || r.canonical == nil {
		return fmt.Errorf("security canonical store is not configured")
	}
	return nil
}
func (r *CanonicalSecurityRepository) indexError(op string, err error) {
	if err != nil {
		r.logger.Warn("security SQL index write failed; canonical state retained", zap.String("operation", op), zap.Error(err))
	}
}
func (r *CanonicalSecurityRepository) UpsertSecurityTarget(ctx context.Context, target *domain.SecurityTarget) (*domain.SecurityTarget, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("security target is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, err := r.GetSecurityTargetByHash(ctx, target.TargetKeyHash)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	if existing != nil {
		target.ID = existing.ID
		target.CreatedAt = existing.CreatedAt
	} else if target.ID == uuid.Nil {
		target.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:security-target:"+target.TargetKeyHash))
	}
	if target.CreatedAt.IsZero() {
		target.CreatedAt = now
	}
	target.UpdatedAt = now
	if err := r.canonical.PublishTarget(ctx, target); err != nil {
		return nil, err
	}
	if r.index != nil {
		_, err = r.index.UpsertSecurityTarget(ctx, target)
		r.indexError("target", err)
	}
	copyTarget := *target
	return &copyTarget, nil
}
func (r *CanonicalSecurityRepository) GetSecurityTargetByHash(ctx context.Context, hash string) (*domain.SecurityTarget, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecurityTargets(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].TargetKeyHash == hash {
			return &rows[i], nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *CanonicalSecurityRepository) ListSecurityTargets(ctx context.Context, typ domain.SecurityTargetType, limit int) ([]domain.SecurityTarget, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecurityTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityTarget, 0)
	for _, v := range rows {
		if typ == "" || v.Type == typ {
			out = append(out, v)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *CanonicalSecurityRepository) CreateSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error {
	if err := r.available(); err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("security run is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.ID == uuid.Nil {
		run.ID = domain.NewEntityID()
	}
	if existing, err := r.GetSecurityScanRun(ctx, run.ID); err == nil && existing != nil {
		return repository.ErrAlreadyExists
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if active, err := r.GetActiveSecurityScanRunByTargetHash(ctx, run.TargetKeyHash); err == nil && active.ID != run.ID {
		return repository.ErrAlreadyExists
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	if err := r.canonical.PublishRun(ctx, run); err != nil {
		return err
	}
	if r.index != nil {
		r.indexError("run", r.index.CreateSecurityScanRun(ctx, run))
	}
	return nil
}
func (r *CanonicalSecurityRepository) GetSecurityScanRun(ctx context.Context, id uuid.UUID) (*domain.SecurityScanRun, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecurityRuns(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i], nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *CanonicalSecurityRepository) GetActiveSecurityScanRunByTargetHash(ctx context.Context, hash string) (*domain.SecurityScanRun, error) {
	rows, err := r.ListSecurityScanRuns(ctx, hash, 0)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if !rows[i].Status.IsTerminal() {
			return &rows[i], nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *CanonicalSecurityRepository) ListSecurityScanRuns(ctx context.Context, hash string, limit int) ([]domain.SecurityScanRun, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecurityRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityScanRun, 0)
	for _, v := range rows {
		if hash == "" || v.TargetKeyHash == hash {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *CanonicalSecurityRepository) ListSecurityScanRunsByStatus(ctx context.Context, statuses []domain.SecurityScanStatus, limit int) ([]domain.SecurityScanRun, error) {
	rows, err := r.ListSecurityScanRuns(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityScanRun, 0)
	for _, v := range rows {
		for _, status := range statuses {
			if v.Status == status {
				out = append(out, v)
				break
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *CanonicalSecurityRepository) MarkSecurityScanRunStarted(ctx context.Context, id uuid.UUID, started time.Time) error {
	run, err := r.GetSecurityScanRun(ctx, id)
	if err != nil {
		return err
	}
	run.Status = domain.SecurityScanRunning
	run.StartedAt = &started
	run.UpdatedAt = started
	if err := r.canonical.PublishRun(ctx, run); err != nil {
		return err
	}
	if r.index != nil {
		r.indexError("run-start", r.index.MarkSecurityScanRunStarted(ctx, id, started))
	}
	return nil
}
func (r *CanonicalSecurityRepository) CompleteSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error {
	if err := r.available(); err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("security run is required")
	}
	run.UpdatedAt = time.Now().UTC()
	if err := r.canonical.PublishRun(ctx, run); err != nil {
		return err
	}
	if r.index != nil {
		r.indexError("run-complete", r.index.CompleteSecurityScanRun(ctx, run))
	}
	return nil
}
func (r *CanonicalSecurityRepository) UpdateSecurityScanRunStatus(ctx context.Context, id uuid.UUID, status domain.SecurityScanStatus, message string, finished *time.Time) error {
	run, err := r.GetSecurityScanRun(ctx, id)
	if err != nil {
		return err
	}
	run.Status = status
	run.Error = message
	run.FinishedAt = finished
	run.UpdatedAt = time.Now().UTC()
	if err := r.canonical.PublishRun(ctx, run); err != nil {
		return err
	}
	if r.index != nil {
		r.indexError("run-status", r.index.UpdateSecurityScanRunStatus(ctx, id, status, message, finished))
	}
	return nil
}
func (r *CanonicalSecurityRepository) UpsertSecurityTargetLatest(ctx context.Context, latest *domain.SecurityTargetLatest) error {
	if r.index != nil {
		r.indexError("target-latest", r.index.UpsertSecurityTargetLatest(ctx, latest))
	}
	return nil
}
func (r *CanonicalSecurityRepository) GetSecurityTargetLatestByHash(ctx context.Context, hash string) (*domain.SecurityTargetLatest, error) {
	rows, err := r.ListSecurityScanRuns(ctx, hash, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, repository.ErrNotFound
	}
	run := rows[0]
	if !run.Status.IsTerminal() {
		return nil, repository.ErrNotFound
	}
	at := run.UpdatedAt
	if run.FinishedAt != nil {
		at = *run.FinishedAt
	}
	return &domain.SecurityTargetLatest{TargetID: run.TargetID, TargetKeyHash: hash, RunID: run.ID, Status: run.Status, SeverityCounts: run.SeverityCounts, FindingCount: run.FindingCount, ScannedAt: at, UpdatedAt: at}, nil
}
func (r *CanonicalSecurityRepository) GetLatestSecurityTargetLatestForArtifact(ctx context.Context, id uuid.UUID) (*domain.SecurityTargetLatest, error) {
	targets, err := r.ListSecurityTargets(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if target.Subject != nil && target.Subject.ID == id.String() {
			return r.GetSecurityTargetLatestByHash(ctx, target.TargetKeyHash)
		}
	}
	return nil, repository.ErrNotFound
}
func (r *CanonicalSecurityRepository) UpsertSecurityFindings(ctx context.Context, findings []domain.SecurityOSVFinding) error {
	if err := r.available(); err != nil {
		return err
	}
	existing, err := r.canonical.ListSecurityFindings(ctx)
	if err != nil {
		return err
	}
	byRunAndHash := make(map[string]domain.SecurityOSVFinding, len(existing))
	for _, finding := range existing {
		byRunAndHash[finding.RunID.String()+":"+finding.FindingKeyHash] = finding
	}
	now := time.Now().UTC()
	for i := range findings {
		key := findings[i].RunID.String() + ":" + findings[i].FindingKeyHash
		if previous, ok := byRunAndHash[key]; ok {
			findings[i].ID = previous.ID
			findings[i].CreatedAt = previous.CreatedAt
		}
		if findings[i].ID == uuid.Nil {
			findings[i].ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:security-finding:"+key))
		}
		if findings[i].CreatedAt.IsZero() {
			findings[i].CreatedAt = now
		}
		findings[i].UpdatedAt = now
		if err := r.canonical.PublishFinding(ctx, findings[i]); err != nil {
			return err
		}
		if err := r.canonical.PublishFindingDetail(ctx, findings[i]); err != nil {
			return err
		}
	}
	if r.index != nil {
		r.indexError("findings", r.index.UpsertSecurityFindings(ctx, findings))
	}
	return nil
}
func (r *CanonicalSecurityRepository) ListSecurityFindings(ctx context.Context, runID uuid.UUID) ([]domain.SecurityOSVFinding, error) {
	return r.ListSecurityFindingsFiltered(ctx, repository.SecurityFindingFilter{RunID: &runID})
}
func (r *CanonicalSecurityRepository) ListSecurityFindingsFiltered(ctx context.Context, f repository.SecurityFindingFilter) ([]domain.SecurityOSVFinding, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecurityFindings(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityOSVFinding, 0)
	for _, v := range rows {
		if f.RunID != nil && v.RunID != *f.RunID {
			continue
		}
		if f.TargetKeyHash != "" && v.TargetKeyHash != f.TargetKeyHash {
			continue
		}
		if f.Severity != "" && v.Severity != f.Severity {
			continue
		}
		if f.OSVID != "" && v.OSVID != f.OSVID {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return []domain.SecurityOSVFinding{}, nil
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}
func (r *CanonicalSecurityRepository) UpsertSecurityScanSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error {
	if err := r.available(); err != nil {
		return err
	}
	if schedule == nil {
		return fmt.Errorf("security schedule is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return err
	}
	for _, v := range rows {
		if v.PolicyID == schedule.PolicyID && v.TargetKeyHash == schedule.TargetKeyHash {
			schedule.ID = v.ID
			schedule.CreatedAt = v.CreatedAt
			schedule.NextDueAt = v.NextDueAt
			schedule.LastDispatchedAt = v.LastDispatchedAt
			schedule.LastRunID = v.LastRunID
			break
		}
	}
	now := time.Now().UTC()
	if schedule.ID == uuid.Nil {
		schedule.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("security:schedule:"+schedule.PolicyID.String()+":"+schedule.TargetKeyHash))
	}
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = now
	}
	if schedule.NextDueAt.IsZero() {
		schedule.NextDueAt = now
	}
	schedule.UpdatedAt = now
	if err := r.canonical.PublishSchedule(ctx, schedule); err != nil {
		return err
	}
	if r.index != nil {
		r.indexError("schedule", r.index.UpsertSecurityScanSchedule(ctx, schedule))
	}
	return nil
}
func (r *CanonicalSecurityRepository) ListSecurityScanSchedulesFiltered(ctx context.Context, f repository.SecurityScheduleFilter) ([]domain.SecurityScanSchedule, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityScanSchedule, 0)
	for _, v := range rows {
		if f.PolicyID != nil && v.PolicyID != *f.PolicyID {
			continue
		}
		if f.TargetKeyHash != "" && v.TargetKeyHash != f.TargetKeyHash {
			continue
		}
		if f.EnabledOnly && !v.Enabled {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextDueAt.Before(out[j].NextDueAt) })
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return []domain.SecurityScanSchedule{}, nil
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}
func (r *CanonicalSecurityRepository) ClaimDueSecurityScanSchedules(context.Context, time.Time, int, string, time.Time) ([]domain.SecurityScanSchedule, error) {
	return nil, fmt.Errorf("SQL schedule claims are not canonical; use signed run records")
}
func (r *CanonicalSecurityRepository) MarkSecurityScheduleDispatched(ctx context.Context, id, runID uuid.UUID, dispatched, next time.Time) error {
	if err := r.available(); err != nil {
		return err
	}
	rows, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].ID != id {
			continue
		}
		schedule := rows[i]
		schedule.LastRunID = &runID
		schedule.LastDispatchedAt = &dispatched
		schedule.NextDueAt = next
		schedule.LeaseUntil = nil
		schedule.LeasedBy = ""
		schedule.UpdatedAt = dispatched
		if err := r.canonical.PublishSchedule(ctx, &schedule); err != nil {
			return err
		}
		if r.index != nil {
			r.indexError("schedule-dispatch", r.index.MarkSecurityScheduleDispatched(ctx, id, runID, dispatched, next))
		}
		return nil
	}
	return repository.ErrNotFound
}
func (r *CanonicalSecurityRepository) DisableSecurityScanSchedulesForPolicy(ctx context.Context, policyID uuid.UUID, at time.Time) error {
	if err := r.available(); err != nil {
		return err
	}
	rows, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return err
	}
	for _, v := range rows {
		if v.PolicyID != policyID || !v.Enabled {
			continue
		}
		v.Enabled = false
		v.LeaseUntil = nil
		v.LeasedBy = ""
		v.UpdatedAt = at
		if err := r.canonical.PublishSchedule(ctx, &v); err != nil {
			return err
		}
	}
	if r.index != nil {
		r.indexError("schedule-disable", r.index.DisableSecurityScanSchedulesForPolicy(ctx, policyID, at))
	}
	return nil
}
func (r *CanonicalSecurityRepository) RecordSecurityPolicyBreach(ctx context.Context, b *domain.SecurityPolicyBreach) (domain.SecurityBreachRecordResult, error) {
	if r.index != nil {
		return r.index.RecordSecurityPolicyBreach(ctx, b)
	}
	return "", fmt.Errorf("security breach index is unavailable")
}
func (r *CanonicalSecurityRepository) ResolveSecurityPolicyBreach(ctx context.Context, id uuid.UUID, hash string, at time.Time) error {
	if r.index != nil {
		return r.index.ResolveSecurityPolicyBreach(ctx, id, hash, at)
	}
	return nil
}
func (r *CanonicalSecurityRepository) GetActiveSecurityPolicyBreach(ctx context.Context, id uuid.UUID, hash string) (*domain.SecurityPolicyBreach, error) {
	if r.index != nil {
		return r.index.GetActiveSecurityPolicyBreach(ctx, id, hash)
	}
	return nil, repository.ErrNotFound
}
func (r *CanonicalSecurityRepository) UpsertOSVVulnerabilityCache(ctx context.Context, c *domain.OSVVulnerabilityCache) error {
	if r.index != nil {
		return r.index.UpsertOSVVulnerabilityCache(ctx, c)
	}
	return nil
}
func (r *CanonicalSecurityRepository) GetOSVVulnerabilityCache(ctx context.Context, id string, now time.Time) (*domain.OSVVulnerabilityCache, error) {
	if r.index != nil {
		return r.index.GetOSVVulnerabilityCache(ctx, id, now)
	}
	return nil, repository.ErrNotFound
}
func (r *CanonicalSecurityRepository) PruneExpiredOSVVulnerabilityCache(ctx context.Context, now time.Time) (int64, error) {
	if r.index != nil {
		return r.index.PruneExpiredOSVVulnerabilityCache(ctx, now)
	}
	return 0, nil
}
func (r *CanonicalSecurityRepository) UpsertSecurityPublication(ctx context.Context, p *domain.SecurityObservablePublication) error {
	if r.index != nil {
		return r.index.UpsertSecurityPublication(ctx, p)
	}
	return nil
}
func (r *CanonicalSecurityRepository) UpdateSecurityPublicationState(ctx context.Context, id uuid.UUID, state domain.SecurityPublicationState, eventID, lastError string, next, published *time.Time) error {
	if r.index != nil {
		return r.index.UpdateSecurityPublicationState(ctx, id, state, eventID, lastError, next, published)
	}
	return nil
}
func (r *CanonicalSecurityRepository) AbandonSecurityPublication(ctx context.Context, eventID, reason string) (int64, error) {
	if r.index != nil {
		return r.index.AbandonSecurityPublication(ctx, eventID, reason)
	}
	return 0, nil
}
func (r *CanonicalSecurityRepository) DeliverSecurityPublication(ctx context.Context, eventID string) (int64, error) {
	if r.index != nil {
		return r.index.DeliverSecurityPublication(ctx, eventID)
	}
	return 0, nil
}

// RebuildIndex replays current canonical coordinates into the optional SQL index.
func (r *CanonicalSecurityRepository) RebuildIndex(ctx context.Context) error {
	if r.index == nil {
		return nil
	}
	if err := r.available(); err != nil {
		return err
	}
	targets, err := r.canonical.ListSecurityTargets(ctx)
	if err != nil {
		return err
	}
	for i := range targets {
		if _, err := r.index.UpsertSecurityTarget(ctx, &targets[i]); err != nil {
			return err
		}
	}
	runs, err := r.canonical.ListSecurityRuns(ctx)
	if err != nil {
		return err
	}
	for i := range runs {
		if _, err := r.index.GetSecurityScanRun(ctx, runs[i].ID); errors.Is(err, repository.ErrNotFound) {
			if err := r.index.CreateSecurityScanRun(ctx, &runs[i]); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if runs[i].Status.IsTerminal() {
			if err := r.index.CompleteSecurityScanRun(ctx, &runs[i]); err != nil {
				return err
			}
		}
	}
	schedules, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return err
	}
	for i := range schedules {
		if err := r.index.UpsertSecurityScanSchedule(ctx, &schedules[i]); err != nil {
			return err
		}
	}
	findings, err := r.canonical.ListSecurityFindings(ctx)
	if err != nil {
		return err
	}
	if len(findings) > 0 {
		return r.index.UpsertSecurityFindings(ctx, findings)
	}
	return nil
}

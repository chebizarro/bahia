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

// SecurityCanonicalStore is the canonical security state: signed,
// replaceable cp-state records admitted to the durable publish outbox and read
// back from the daemon's own retained events in the local event store. A
// Publish method returns nil once the record is accepted or durably queued and
// an error when it was not admitted. SQL is never consulted.
type SecurityCanonicalStore interface {
	PublishTarget(context.Context, *domain.SecurityTarget) error
	PublishRun(context.Context, *domain.SecurityScanRun) error
	PublishSchedule(context.Context, *domain.SecurityScanSchedule) error
	PublishFinding(context.Context, domain.SecurityOSVFinding) error
	PublishFindingDetail(context.Context, domain.SecurityOSVFinding) error
	// RetireRun tombstones a run record with a NIP-40 expiration so the
	// coordinate is dropped by readers now and swept by retention later.
	RetireRun(context.Context, *domain.SecurityScanRun, time.Time) error
	// The List methods narrow by the records' public tags: an empty hash or
	// uuid.Nil means "any".
	ListSecurityTargets(ctx context.Context, targetKeyHash string) ([]domain.SecurityTarget, error)
	ListSecurityRuns(ctx context.Context, runID uuid.UUID, targetKeyHash string) ([]domain.SecurityScanRun, error)
	ListSecuritySchedules(ctx context.Context) ([]domain.SecurityScanSchedule, error)
	ListSecurityFindings(ctx context.Context, runID uuid.UUID, targetKeyHash string) ([]domain.SecurityOSVFinding, error)
}

// securityBreachFingerprintsKey is the run metadata key holding the policy
// breach fingerprints a completed scan established, keyed by policy id. The
// breach lifecycle (new, changed, unchanged, resolved) is derived by comparing
// a run's fingerprints with those of the target's previous terminal run.
const securityBreachFingerprintsKey = "policy_breach_fingerprints"

// CanonicalSecurityRepository serves the scanner, scheduler and policy service
// from canonical security cp-state. Every write publishes the signed record
// first and returns the publish error; only then does it mirror the change
// into index, the optional SQL repository, whose failures are logged and
// repaired by RebuildIndex. Every read of targets, runs, schedules, findings
// and breach state comes from the local event store, so the security domain
// runs with no database.
//
// It has the shape of repository.SecurityRepository because that is what its
// callers already speak, but it carries no SQL semantics: there are no
// leases or claims (a scheduled scan is claimed by its run record, whose id
// the scheduler derives from the schedule and due time), no sequences (ids are
// derived from the coordinate), and no multi-row transactions (each record is
// one replaceable event, and every multi-record write is safe to repeat).
//
// index additionally backs three things that are not canonical state: the OSV
// vulnerability cache, the mirror of observable publish outcomes, and the
// notification bookkeeping of the breach table. Without a database the first
// two are no-ops.
type CanonicalSecurityRepository struct {
	index     repository.SecurityRepository
	canonical SecurityCanonicalStore
	// mu serializes read-check-publish sequences on this process, so two
	// wakeups cannot both decide a coordinate is free.
	mu     sync.Mutex
	logger *zap.Logger
}

var _ repository.SecurityRepository = (*CanonicalSecurityRepository)(nil)

// NewCanonicalSecurityRepository returns the canonical security store over
// canonical. index may be nil.
func NewCanonicalSecurityRepository(index repository.SecurityRepository, canonical SecurityCanonicalStore, logger *zap.Logger) *CanonicalSecurityRepository {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CanonicalSecurityRepository{index: index, canonical: canonical, logger: logger.Named("security-canonical-store")}
}

func (r *CanonicalSecurityRepository) available() error {
	if r == nil || r.canonical == nil {
		return errors.New("security canonical store is not configured")
	}
	return nil
}

// mirror records the outcome of a derived SQL index write. The canonical
// record is already published, so a failure is logged, never returned.
func (r *CanonicalSecurityRepository) mirror(op string, err error) {
	if err != nil {
		r.logger.Warn("security SQL index write failed; canonical state retained", zap.String("operation", op), zap.Error(err))
	}
}

// Targets ---------------------------------------------------------------------

func securityTargetID(targetKeyHash string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:security-target:"+targetKeyHash))
}

func (r *CanonicalSecurityRepository) UpsertSecurityTarget(ctx context.Context, target *domain.SecurityTarget) (*domain.SecurityTarget, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	if target == nil {
		return nil, errors.New("security target is required")
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
		target.ID = securityTargetID(target.TargetKeyHash)
	}
	if target.CreatedAt.IsZero() {
		target.CreatedAt = now
	}
	target.UpdatedAt = now
	if err := r.canonical.PublishTarget(ctx, target); err != nil {
		return nil, fmt.Errorf("publish security target: %w", err)
	}
	if r.index != nil {
		indexed := *target
		_, err := r.index.UpsertSecurityTarget(ctx, &indexed)
		r.mirror("target", err)
	}
	stored := *target
	return &stored, nil
}

func (r *CanonicalSecurityRepository) GetSecurityTargetByHash(ctx context.Context, hash string) (*domain.SecurityTarget, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, repository.ErrNotFound
	}
	rows, err := r.canonical.ListSecurityTargets(ctx, hash)
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
	rows, err := r.canonical.ListSecurityTargets(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityTarget, 0, len(rows))
	for _, v := range rows {
		if typ == "" || v.Type == typ {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].TargetKeyHash < out[j].TargetKeyHash
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Runs ------------------------------------------------------------------------

// CreateSecurityScanRun publishes the run record that claims a scan. It is the
// idempotent claim: a run id that is already retained, or a target that
// already has a non-terminal run, yields ErrAlreadyExists and nothing is
// published, so the loser of two concurrent wakeups adopts the winner's run.
func (r *CanonicalSecurityRepository) CreateSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error {
	if err := r.available(); err != nil {
		return err
	}
	if run == nil {
		return errors.New("security run is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.ID == uuid.Nil {
		run.ID = domain.NewEntityID()
	}
	if _, err := r.GetSecurityScanRun(ctx, run.ID); err == nil {
		return repository.ErrAlreadyExists
	} else if !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if _, err := r.GetActiveSecurityScanRunByTargetHash(ctx, run.TargetKeyHash); err == nil {
		return repository.ErrAlreadyExists
	} else if !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if run.Status == "" {
		run.Status = domain.SecurityScanAccepted
	}
	if run.PublishState == "" {
		run.PublishState = domain.SecurityPublicationPending
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	if err := r.canonical.PublishRun(ctx, run); err != nil {
		return fmt.Errorf("publish security run claim: %w", err)
	}
	if r.index != nil {
		indexed := *run
		r.mirror("run", r.index.CreateSecurityScanRun(ctx, &indexed))
	}
	return nil
}

func (r *CanonicalSecurityRepository) GetSecurityScanRun(ctx context.Context, id uuid.UUID) (*domain.SecurityScanRun, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, repository.ErrNotFound
	}
	rows, err := r.canonical.ListSecurityRuns(ctx, id, "")
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

// ListSecurityScanRuns returns runs newest first; hash "" lists every target.
func (r *CanonicalSecurityRepository) ListSecurityScanRuns(ctx context.Context, hash string, limit int) ([]domain.SecurityScanRun, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecurityRuns(ctx, uuid.Nil, hash)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityScanRun, 0, len(rows))
	for _, v := range rows {
		if hash == "" || v.TargetKeyHash == hash {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
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

// updateRun replaces a run's record on its coordinate. A terminal run is never
// reopened: a late update from a duplicate execution is rejected.
func (r *CanonicalSecurityRepository) updateRun(ctx context.Context, id uuid.UUID, apply func(*domain.SecurityScanRun)) (*domain.SecurityScanRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, err := r.GetSecurityScanRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.Status.IsTerminal() {
		return nil, fmt.Errorf("security run %s is already %s: %w", id, run.Status, repository.ErrConflict)
	}
	apply(run)
	if err := r.canonical.PublishRun(ctx, run); err != nil {
		return nil, fmt.Errorf("publish security run: %w", err)
	}
	return run, nil
}

func (r *CanonicalSecurityRepository) MarkSecurityScanRunStarted(ctx context.Context, id uuid.UUID, started time.Time) error {
	if _, err := r.updateRun(ctx, id, func(run *domain.SecurityScanRun) {
		run.Status = domain.SecurityScanRunning
		run.StartedAt = &started
		run.UpdatedAt = started
	}); err != nil {
		return err
	}
	if r.index != nil {
		r.mirror("run-start", r.index.MarkSecurityScanRunStarted(ctx, id, started))
	}
	return nil
}

// CompleteSecurityScanRun publishes the run's terminal record. The scanner
// calls it again for the same run when only the publish state of its legacy
// observables changes, so a terminal run may be replaced by itself.
func (r *CanonicalSecurityRepository) CompleteSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error {
	if err := r.available(); err != nil {
		return err
	}
	if run == nil {
		return errors.New("security run is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run.UpdatedAt = time.Now().UTC()
	if err := r.canonical.PublishRun(ctx, run); err != nil {
		return fmt.Errorf("publish security run: %w", err)
	}
	if r.index != nil {
		indexed := *run
		r.mirror("run-complete", r.index.CompleteSecurityScanRun(ctx, &indexed))
	}
	return nil
}

func (r *CanonicalSecurityRepository) UpdateSecurityScanRunStatus(ctx context.Context, id uuid.UUID, status domain.SecurityScanStatus, message string, finished *time.Time) error {
	if _, err := r.updateRun(ctx, id, func(run *domain.SecurityScanRun) {
		run.Status = status
		run.Error = message
		run.FinishedAt = finished
		run.UpdatedAt = time.Now().UTC()
	}); err != nil {
		return err
	}
	if r.index != nil {
		r.mirror("run-status", r.index.UpdateSecurityScanRunStatus(ctx, id, status, message, finished))
	}
	return nil
}

// Latest-per-target summaries are derived from run records; only the SQL
// mirror is written here.
func (r *CanonicalSecurityRepository) UpsertSecurityTargetLatest(ctx context.Context, latest *domain.SecurityTargetLatest) error {
	if r.index != nil {
		r.mirror("target-latest", r.index.UpsertSecurityTargetLatest(ctx, latest))
	}
	return nil
}

func latestFromRun(run domain.SecurityScanRun) *domain.SecurityTargetLatest {
	at := run.UpdatedAt
	if run.FinishedAt != nil {
		at = *run.FinishedAt
	}
	return &domain.SecurityTargetLatest{TargetID: run.TargetID, TargetKeyHash: run.TargetKeyHash, RunID: run.ID, Status: run.Status, SeverityCounts: run.SeverityCounts, FindingCount: run.FindingCount, ScannedAt: at, UpdatedAt: at}
}

// GetSecurityTargetLatestByHash returns the summary of the target's newest
// terminal run. A scan in progress does not hide the last finished one.
func (r *CanonicalSecurityRepository) GetSecurityTargetLatestByHash(ctx context.Context, hash string) (*domain.SecurityTargetLatest, error) {
	rows, err := r.ListSecurityScanRuns(ctx, hash, 0)
	if err != nil {
		return nil, err
	}
	for _, run := range rows {
		if run.Status.IsTerminal() {
			return latestFromRun(run), nil
		}
	}
	return nil, repository.ErrNotFound
}

// GetLatestSecurityTargetLatestForArtifact returns the most recently scanned
// summary across the artifact's SBOM targets (an artifact has one target per
// SBOM payload).
func (r *CanonicalSecurityRepository) GetLatestSecurityTargetLatestForArtifact(ctx context.Context, id uuid.UUID) (*domain.SecurityTargetLatest, error) {
	targets, err := r.ListSecurityTargets(ctx, domain.SecurityTargetSBOM, 0)
	if err != nil {
		return nil, err
	}
	var newest *domain.SecurityTargetLatest
	for _, target := range targets {
		if target.Subject == nil || target.Subject.Type != domain.SBOMSubjectArtifact || target.Subject.ID != id.String() {
			continue
		}
		latest, err := r.GetSecurityTargetLatestByHash(ctx, target.TargetKeyHash)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if newest == nil || latest.ScannedAt.After(newest.ScannedAt) {
			newest = latest
		}
	}
	if newest == nil {
		return nil, repository.ErrNotFound
	}
	return newest, nil
}

// Findings --------------------------------------------------------------------

func securityFindingID(runID uuid.UUID, findingKeyHash string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:security-finding:"+runID.String()+":"+findingKeyHash))
}

// UpsertSecurityFindings publishes each finding and its detail record. The
// scanner stamps ids and creation times from the run, so repeating the call
// after a partial failure re-asserts identical records and signs nothing new
// for those already admitted.
func (r *CanonicalSecurityRepository) UpsertSecurityFindings(ctx context.Context, findings []domain.SecurityOSVFinding) error {
	if err := r.available(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range findings {
		if findings[i].ID == uuid.Nil {
			findings[i].ID = securityFindingID(findings[i].RunID, findings[i].FindingKeyHash)
		}
		if findings[i].CreatedAt.IsZero() {
			findings[i].CreatedAt = now
		}
		findings[i].UpdatedAt = now
		if err := r.canonical.PublishFinding(ctx, findings[i]); err != nil {
			return fmt.Errorf("publish security finding %s: %w", findings[i].FindingKeyHash, err)
		}
		if err := r.canonical.PublishFindingDetail(ctx, findings[i]); err != nil {
			return fmt.Errorf("publish security finding detail %s: %w", findings[i].FindingKeyHash, err)
		}
	}
	if r.index != nil && len(findings) > 0 {
		indexed := append([]domain.SecurityOSVFinding(nil), findings...)
		r.mirror("findings", r.index.UpsertSecurityFindings(ctx, indexed))
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
	runID := uuid.Nil
	if f.RunID != nil {
		runID = *f.RunID
	}
	rows, err := r.canonical.ListSecurityFindings(ctx, runID, f.TargetKeyHash)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityOSVFinding, 0, len(rows))
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].FindingKeyHash < out[j].FindingKeyHash
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
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

// Schedules -------------------------------------------------------------------

func securityScheduleID(policyID uuid.UUID, targetKeyHash string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("security:schedule:"+policyID.String()+":"+targetKeyHash))
}

// publishSchedule publishes a schedule and mirrors it. Callers hold mu.
func (r *CanonicalSecurityRepository) publishSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error {
	schedule.LeaseUntil, schedule.LeasedBy = nil, ""
	if err := r.canonical.PublishSchedule(ctx, schedule); err != nil {
		return fmt.Errorf("publish security schedule: %w", err)
	}
	if r.index != nil {
		indexed := *schedule
		r.mirror("schedule", r.index.UpsertSecurityScanSchedule(ctx, &indexed))
	}
	return nil
}

// UpsertSecurityScanSchedule publishes the schedule for (policy, target). The
// progress of an existing schedule (next due time, last dispatch) is kept, so
// re-deriving schedules from policy state never resets when a scan is due.
func (r *CanonicalSecurityRepository) UpsertSecurityScanSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error {
	if err := r.available(); err != nil {
		return err
	}
	if schedule == nil {
		return errors.New("security schedule is required")
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
		schedule.ID = securityScheduleID(schedule.PolicyID, schedule.TargetKeyHash)
	}
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = now
	}
	if schedule.NextDueAt.IsZero() {
		schedule.NextDueAt = now
	}
	schedule.UpdatedAt = now
	return r.publishSchedule(ctx, schedule)
}

// ListSecurityScanSchedulesFiltered returns schedules ordered by due time.
func (r *CanonicalSecurityRepository) ListSecurityScanSchedulesFiltered(ctx context.Context, f repository.SecurityScheduleFilter) ([]domain.SecurityScanSchedule, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SecurityScanSchedule, 0, len(rows))
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].NextDueAt.Equal(out[j].NextDueAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].NextDueAt.Before(out[j].NextDueAt)
	})
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

func (r *CanonicalSecurityRepository) MarkSecurityScheduleDispatched(ctx context.Context, id, runID uuid.UUID, dispatched, next time.Time) error {
	if err := r.available(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
		schedule.UpdatedAt = dispatched
		return r.publishSchedule(ctx, &schedule)
	}
	return repository.ErrNotFound
}

func (r *CanonicalSecurityRepository) DisableSecurityScanSchedulesForPolicy(ctx context.Context, policyID uuid.UUID, at time.Time) error {
	if err := r.available(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return err
	}
	for _, v := range rows {
		if v.PolicyID != policyID || !v.Enabled {
			continue
		}
		v.Enabled = false
		v.UpdatedAt = at
		if err := r.publishSchedule(ctx, &v); err != nil {
			return err
		}
	}
	return nil
}

// Policy breaches ---------------------------------------------------------------

func breachRunID(breach *domain.SecurityPolicyBreach) uuid.UUID {
	if breach == nil || breach.Metadata == nil {
		return uuid.Nil
	}
	raw, _ := breach.Metadata["run_id"].(string)
	id, _ := uuid.Parse(raw)
	return id
}

func runBreachFingerprints(run *domain.SecurityScanRun) map[string]string {
	out := map[string]string{}
	if run == nil || run.Metadata == nil {
		return out
	}
	switch stored := run.Metadata[securityBreachFingerprintsKey].(type) {
	case map[string]string:
		for policy, fingerprint := range stored {
			out[policy] = fingerprint
		}
	case map[string]any:
		for policy, fingerprint := range stored {
			if s, ok := fingerprint.(string); ok {
				out[policy] = s
			}
		}
	}
	return out
}

// previousCompletedRun returns the target's newest completed run other than
// current, or nil. Only a completed scan evaluates policies, so a failed or
// cancelled run in between neither resolves nor re-announces a breach.
func (r *CanonicalSecurityRepository) previousCompletedRun(ctx context.Context, targetKeyHash string, current uuid.UUID) (*domain.SecurityScanRun, error) {
	rows, err := r.ListSecurityScanRuns(ctx, targetKeyHash, 0)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID != current && rows[i].Status == domain.SecurityScanCompleted {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// RecordSecurityPolicyBreach decides whether a breach is new, changed or
// unchanged from signed run records: the run named in the breach metadata
// records the fingerprint, and the target's previous completed run supplies
// the one to compare with. A completed run that records no fingerprint for a
// policy is that breach's resolution. The SQL breach table is mirrored
// afterwards.
func (r *CanonicalSecurityRepository) RecordSecurityPolicyBreach(ctx context.Context, breach *domain.SecurityPolicyBreach) (domain.SecurityBreachRecordResult, error) {
	if err := r.available(); err != nil {
		return "", err
	}
	if breach == nil {
		return "", errors.New("security policy breach is required")
	}
	runID := breachRunID(breach)
	if runID == uuid.Nil {
		return "", errors.New("security policy breach does not name its scan run")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, err := r.GetSecurityScanRun(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("security policy breach run %s: %w", runID, err)
	}
	policy := breach.PolicyID.String()
	recorded := runBreachFingerprints(run)
	previousFingerprint := ""
	if previous, err := r.previousCompletedRun(ctx, breach.TargetKeyHash, runID); err != nil {
		return "", err
	} else if previous != nil {
		previousFingerprint = runBreachFingerprints(previous)[policy]
	}
	result := domain.SecurityBreachRecordNew
	switch {
	case recorded[policy] == breach.Fingerprint:
		// This run already recorded the breach: a repeated evaluation.
		result = domain.SecurityBreachRecordUnchanged
	case previousFingerprint == breach.Fingerprint:
		result = domain.SecurityBreachRecordUnchanged
	case previousFingerprint != "":
		result = domain.SecurityBreachRecordChanged
		breach.PreviousFingerprint = previousFingerprint
	}
	if breach.ID == uuid.Nil {
		breach.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:security-breach:"+policy+":"+breach.TargetKeyHash))
	}
	if recorded[policy] != breach.Fingerprint {
		recorded[policy] = breach.Fingerprint
		if run.Metadata == nil {
			run.Metadata = map[string]any{}
		}
		run.Metadata[securityBreachFingerprintsKey] = recorded
		run.UpdatedAt = time.Now().UTC()
		if err := r.canonical.PublishRun(ctx, run); err != nil {
			return "", fmt.Errorf("publish security run breach state: %w", err)
		}
	}
	if r.index != nil {
		indexed := *breach
		_, err := r.index.RecordSecurityPolicyBreach(ctx, &indexed)
		r.mirror("breach", err)
	}
	return result, nil
}

// ResolveSecurityPolicyBreach needs no canonical write: the completed run that
// records no fingerprint for the policy is the resolution. Only the SQL mirror
// is updated, and a breach it does not hold is not an error.
func (r *CanonicalSecurityRepository) ResolveSecurityPolicyBreach(ctx context.Context, id uuid.UUID, hash string, at time.Time) error {
	if r.index != nil {
		if err := r.index.ResolveSecurityPolicyBreach(ctx, id, hash, at); !errors.Is(err, repository.ErrNotFound) {
			r.mirror("breach-resolve", err)
		}
	}
	return nil
}

// Non-canonical auxiliaries: SQL only -----------------------------------------

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

// Index rebuild and migration ---------------------------------------------------

// RebuildIndex replays the retained canonical coordinates into the optional
// SQL index: every target and schedule, and each run the index lacks or holds
// in an older state, together with that run's findings. It is safe to repeat
// and never removes rows; one failed row does not stop the rest.
func (r *CanonicalSecurityRepository) RebuildIndex(ctx context.Context) error {
	if r.index == nil {
		return nil
	}
	if err := r.available(); err != nil {
		return err
	}
	var failed []error
	targets, err := r.canonical.ListSecurityTargets(ctx, "")
	if err != nil {
		return err
	}
	for i := range targets {
		if _, err := r.index.UpsertSecurityTarget(ctx, &targets[i]); err != nil {
			failed = append(failed, fmt.Errorf("target %s: %w", targets[i].TargetKeyHash, err))
		}
	}
	runs, err := r.ListSecurityScanRuns(ctx, "", 0)
	if err != nil {
		return err
	}
	// Oldest first, so a target's finished runs are indexed before its active one.
	for i := len(runs) - 1; i >= 0; i-- {
		if err := r.rebuildRun(ctx, runs[i]); err != nil {
			failed = append(failed, fmt.Errorf("run %s: %w", runs[i].ID, err))
		}
	}
	schedules, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return err
	}
	for i := range schedules {
		if err := r.index.UpsertSecurityScanSchedule(ctx, &schedules[i]); err != nil {
			failed = append(failed, fmt.Errorf("schedule %s: %w", schedules[i].ID, err))
		}
	}
	return errors.Join(failed...)
}

// rebuildRun brings the index row of one run, and the findings it reported,
// up to the canonical record. A run the index already holds in the same state
// is left alone.
func (r *CanonicalSecurityRepository) rebuildRun(ctx context.Context, run domain.SecurityScanRun) error {
	indexed, err := r.index.GetSecurityScanRun(ctx, run.ID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		created := run
		if err := r.index.CreateSecurityScanRun(ctx, &created); err != nil {
			return err
		}
	case err != nil:
		return err
	case indexed.Status == run.Status && indexed.PublishState == run.PublishState:
		return nil
	}
	if !run.Status.IsTerminal() {
		if run.Status == domain.SecurityScanRunning && run.StartedAt != nil {
			return r.index.MarkSecurityScanRunStarted(ctx, run.ID, *run.StartedAt)
		}
		return nil
	}
	findings, err := r.canonical.ListSecurityFindings(ctx, run.ID, run.TargetKeyHash)
	if err != nil {
		return err
	}
	if len(findings) > 0 {
		if err := r.index.UpsertSecurityFindings(ctx, findings); err != nil {
			return err
		}
	}
	if err := r.index.CompleteSecurityScanRun(ctx, &run); err != nil {
		return err
	}
	return r.index.UpsertSecurityTargetLatest(ctx, latestFromRun(run))
}

// securityBackfillMarker names the one-time migration of SQL-era security
// state into canonical cp-state.
const securityBackfillMarker = "security-canonical-v1"

// securityBackfillLimit bounds the rows read per listing during the backfill.
const securityBackfillLimit = 100000

// BackfillFromIndex publishes, once, the security state that exists only in
// the SQL index because it was written before the domain became canonical:
// each target, its newest finished run with that run's findings (what
// deployment gates and breach comparison read), and each schedule. State that
// already has a canonical record is left alone, and SQL ids are kept. The
// marker is written only after everything was admitted, so a failed pass
// repeats on the next start. Call it after the local store has caught up.
func (r *CanonicalSecurityRepository) BackfillFromIndex(ctx context.Context, marker F74aBackfillMarker) error {
	if r.index == nil || marker == nil {
		return nil
	}
	if err := r.available(); err != nil {
		return err
	}
	if done, err := marker.GetControlRecord("bootstrap", securityBackfillMarker); err != nil || string(done) == "1" {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, err := r.index.ListSecurityTargets(ctx, "", securityBackfillLimit)
	if err != nil {
		return err
	}
	for i := range targets {
		if err := r.backfillTarget(ctx, &targets[i]); err != nil {
			return fmt.Errorf("backfill security target %s: %w", targets[i].TargetKeyHash, err)
		}
	}
	retained, err := r.canonical.ListSecuritySchedules(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(retained))
	for _, schedule := range retained {
		known[schedule.PolicyID.String()+":"+schedule.TargetKeyHash] = struct{}{}
	}
	for offset := 0; ; {
		page, err := r.index.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{Limit: securityBackfillLimit, Offset: offset})
		if err != nil {
			return err
		}
		for i := range page {
			key := page[i].PolicyID.String() + ":" + page[i].TargetKeyHash
			if _, ok := known[key]; ok {
				continue
			}
			known[key] = struct{}{}
			page[i].LeaseUntil, page[i].LeasedBy = nil, ""
			if err := r.canonical.PublishSchedule(ctx, &page[i]); err != nil {
				return fmt.Errorf("backfill security schedule %s: %w", page[i].ID, err)
			}
		}
		if len(page) == 0 {
			break
		}
		offset += len(page)
	}
	return marker.PutControlRecord("bootstrap", securityBackfillMarker, []byte("1"))
}

// backfillTarget publishes one SQL-era target and its newest finished run.
func (r *CanonicalSecurityRepository) backfillTarget(ctx context.Context, target *domain.SecurityTarget) error {
	if _, err := r.GetSecurityTargetByHash(ctx, target.TargetKeyHash); errors.Is(err, repository.ErrNotFound) {
		if err := r.canonical.PublishTarget(ctx, target); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if runs, err := r.canonical.ListSecurityRuns(ctx, uuid.Nil, target.TargetKeyHash); err != nil || len(runs) > 0 {
		return err
	}
	indexed, err := r.index.ListSecurityScanRuns(ctx, target.TargetKeyHash, 20)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range indexed {
		if !indexed[i].Status.IsTerminal() {
			// A scan that was in flight under SQL leases is not resumed: the
			// next due time or SBOM observable scans the target again. Its
			// index row is closed so it does not block the target's next run.
			r.mirror("backfill-cancel-run", r.index.UpdateSecurityScanRunStatus(ctx, indexed[i].ID, domain.SecurityScanCancelled, "superseded: security state became canonical cp-state", &now))
			continue
		}
		findings, err := r.index.ListSecurityFindings(ctx, indexed[i].ID)
		if err != nil {
			return err
		}
		for _, finding := range findings {
			if err := r.canonical.PublishFinding(ctx, finding); err != nil {
				return err
			}
			if err := r.canonical.PublishFindingDetail(ctx, finding); err != nil {
				return err
			}
		}
		return r.canonical.PublishRun(ctx, &indexed[i])
	}
	return nil
}

// Run retention ----------------------------------------------------------------

// Run records are one coordinate per run. Latest-wins
// retention never sweeps a coordinate, so growth is bounded here: per target,
// the newest securityRunsRetainedPerTarget terminal runs are kept (the breach
// lifecycle compares a run with the target's previous terminal run, and the
// SQL index historically listed about this many) and older terminal runs are
// retired with a tombstone that expires after securityRunTombstoneTTL, the
// NIP-40 lifetime intent-status and sidecar-status records already use. Runs
// that are not terminal are never retired.
const (
	securityRunsRetainedPerTarget = 20
	securityRunTombstoneTTL       = 7 * 24 * time.Hour
)

// PruneSecurityScanRuns retires, per target, every terminal run beyond the
// newest securityRunsRetainedPerTarget. It is level triggered and safe to
// repeat; the security scheduler calls it on its existing wakeup. It returns
// how many runs were retired; a run that cannot be retired does not stop the
// others.
func (r *CanonicalSecurityRepository) PruneSecurityScanRuns(ctx context.Context) (int, error) {
	if err := r.available(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	runs, err := r.canonical.ListSecurityRuns(ctx, uuid.Nil, "")
	if err != nil {
		return 0, err
	}
	byTarget := map[string][]domain.SecurityScanRun{}
	for _, run := range runs {
		if run.Status.IsTerminal() {
			byTarget[run.TargetKeyHash] = append(byTarget[run.TargetKeyHash], run)
		}
	}
	expiresAt := time.Now().UTC().Add(securityRunTombstoneTTL)
	retired := 0
	var failed []error
	for _, terminal := range byTarget {
		if len(terminal) <= securityRunsRetainedPerTarget {
			continue
		}
		sort.Slice(terminal, func(i, j int) bool {
			if a, b := securityRunSettledAt(terminal[i]), securityRunSettledAt(terminal[j]); !a.Equal(b) {
				return a.After(b)
			}
			return terminal[i].ID.String() < terminal[j].ID.String()
		})
		for i := securityRunsRetainedPerTarget; i < len(terminal); i++ {
			run := terminal[i]
			if err := r.canonical.RetireRun(ctx, &run, expiresAt); err != nil {
				failed = append(failed, fmt.Errorf("retire security run %s: %w", run.ID, err))
				continue
			}
			retired++
		}
	}
	if retired > 0 {
		r.logger.Info("retired security scan runs beyond per-target retention", zap.Int("runs", retired), zap.Int("retained_per_target", securityRunsRetainedPerTarget))
	}
	return retired, errors.Join(failed...)
}

// securityRunSettledAt orders terminal runs: when they finished, else when
// they were created.
func securityRunSettledAt(run domain.SecurityScanRun) time.Time {
	if run.FinishedAt != nil {
		return *run.FinishedAt
	}
	return run.CreatedAt
}

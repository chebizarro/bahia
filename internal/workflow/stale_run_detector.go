package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	defaultStaleRunAfter         = 5 * time.Minute
	maximumStaleRunCheckInterval = time.Minute
	minimumStaleRunCheckInterval = time.Second
	staleRunHealthSchema         = "bahia.deployment-run-health.v1"
)

// DeploymentRunHealthSource is the deployment-run registry surface used by the
// stale-run detector. PostgreSQL-backed deployment run repositories implement it.
type DeploymentRunHealthSource interface {
	ListNonTerminal(context.Context) ([]domain.DeploymentRun, error)
	GetByID(context.Context, uuid.UUID) (*domain.DeploymentRun, error)
}

// StaleRunEventPublisher publishes the detector's replaceable status events.
// The application supplies the outbox-backed Nostr Publisher.
type StaleRunEventPublisher interface {
	PublishSignedEvent(context.Context, *nostr.Event) error
}

type staleRunSignal struct {
	loomJobID    string
	lastStatusAt *time.Time
}

// StaleRunDetector publishes domain-health transitions for Loom-backed
// deployment runs whose kind-30100 status stream has gone quiet.
type StaleRunDetector struct {
	runs          DeploymentRunHealthSource
	events        repository.NostrEventRepository
	publisher     StaleRunEventPublisher
	staleAfter    time.Duration
	checkInterval time.Duration
	logger        *zap.Logger
	now           func() time.Time
	active        map[uuid.UUID]staleRunSignal
	hydrated      bool
}

func NewStaleRunDetector(
	runs DeploymentRunHealthSource,
	events repository.NostrEventRepository,
	publisher StaleRunEventPublisher,
	staleAfter time.Duration,
	logger *zap.Logger,
) *StaleRunDetector {
	if staleAfter <= 0 {
		staleAfter = defaultStaleRunAfter
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &StaleRunDetector{
		runs:          runs,
		events:        events,
		publisher:     publisher,
		staleAfter:    staleAfter,
		checkInterval: staleRunCheckInterval(staleAfter),
		logger:        logger,
		now:           func() time.Time { return time.Now().UTC() },
		active:        make(map[uuid.UUID]staleRunSignal),
	}
}

// Name implements app.BackgroundRunner.
func (d *StaleRunDetector) Name() string { return "deployment-run-stale-detector" }

// Run performs an immediate check, then continues until application shutdown.
func (d *StaleRunDetector) Run(ctx context.Context) error {
	d.checkAndLog(ctx)
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			d.checkAndLog(ctx)
		}
	}
}

func (d *StaleRunDetector) checkAndLog(ctx context.Context) {
	if err := d.check(ctx); err != nil && !errors.Is(err, context.Canceled) {
		d.logger.Warn("deployment run stale-status check failed", zap.Error(err))
	}
}

func (d *StaleRunDetector) check(ctx context.Context) error {
	if d.runs == nil || d.events == nil || d.publisher == nil {
		return fmt.Errorf("stale-run detector dependencies are not configured")
	}

	if !d.hydrated {
		if err := d.hydrateActiveSignals(ctx); err != nil {
			return err
		}
		d.hydrated = true
	}

	now := d.now().UTC()
	runs, err := d.runs.ListNonTerminal(ctx)
	if err != nil {
		return fmt.Errorf("list non-terminal deployment runs: %w", err)
	}

	present := make(map[uuid.UUID]domain.DeploymentRun, len(runs))
	var checkErrors []error
	for i := range runs {
		run := runs[i]
		if !isLoomBackedRun(run) {
			continue
		}
		present[run.ID] = run

		if active, ok := d.active[run.ID]; ok && active.loomJobID != run.LoomJobID {
			transitionAt := run.UpdatedAt
			if transitionAt.IsZero() {
				transitionAt = now
			}
			if err := d.publishHealth(ctx, run, "recovered", "loom_job_changed", active.lastStatusAt, transitionAt); err != nil {
				checkErrors = append(checkErrors, err)
			}
			delete(d.active, run.ID)
		}

		latest, err := d.latestLoomStatus(ctx, run.LoomJobID)
		if err != nil {
			checkErrors = append(checkErrors, fmt.Errorf("run %s: %w", run.ID, err))
			continue
		}
		reference := deploymentRunStatusReference(run)
		var lastStatusAt *time.Time
		if latest != nil {
			createdAt := latest.CreatedAt.UTC()
			lastStatusAt = &createdAt
			if createdAt.After(reference) {
				reference = createdAt
			}
		}

		staleAt := reference.Add(d.staleAfter)
		_, wasActive := d.active[run.ID]
		if now.Before(staleAt) {
			if wasActive {
				reason := "loom_status_resumed"
				transitionAt := reference
				if lastStatusAt == nil {
					reason = "run_no_longer_stale"
					transitionAt = now
				}
				if err := d.publishHealth(ctx, run, "recovered", reason, lastStatusAt, transitionAt); err != nil {
					checkErrors = append(checkErrors, err)
				}
				delete(d.active, run.ID)
			}
			continue
		}
		if wasActive {
			continue
		}

		if err := d.publishHealth(ctx, run, "stale", "loom_status_missing", lastStatusAt, staleAt); err != nil {
			checkErrors = append(checkErrors, err)
		}
		// The production publisher persists before attempting relay delivery. Track
		// the transition even when the immediate relay attempt fails so the outbox
		// retry owns redelivery and the detector does not enqueue periodic duplicates.
		d.active[run.ID] = staleRunSignal{
			loomJobID:    run.LoomJobID,
			lastStatusAt: lastStatusAt,
		}
	}

	for runID, active := range d.active {
		if _, ok := present[runID]; ok {
			continue
		}
		run, err := d.runs.GetByID(ctx, runID)
		if err != nil {
			checkErrors = append(checkErrors, fmt.Errorf("get formerly stale run %s: %w", runID, err))
			continue
		}
		if run == nil || !isTerminalRunStatus(run.Status) {
			continue
		}
		transitionAt := run.UpdatedAt
		if run.FinishedAt != nil {
			transitionAt = run.FinishedAt.UTC()
		}
		if transitionAt.IsZero() {
			transitionAt = now
		}
		if err := d.publishHealth(ctx, *run, "recovered", "run_terminal", active.lastStatusAt, transitionAt); err != nil {
			checkErrors = append(checkErrors, err)
		}
		delete(d.active, runID)
	}

	return errors.Join(checkErrors...)
}

func (d *StaleRunDetector) hydrateActiveSignals(ctx context.Context) error {
	records, err := d.events.FindByTag(ctx, "t", "deployment.run.health", []int{kinds.NIP38Status}, 10000)
	if err != nil {
		return fmt.Errorf("load prior stale-run health events: %w", err)
	}
	seen := make(map[uuid.UUID]struct{})
	for _, record := range records {
		var payload struct {
			Schema           string `json:"schema"`
			RunID            string `json:"run_id"`
			LoomJobID        string `json:"loom_job_id"`
			State            string `json:"state"`
			LastLoomStatusAt string `json:"last_loom_status_at"`
		}
		if err := json.Unmarshal([]byte(record.Content), &payload); err != nil || payload.Schema != staleRunHealthSchema {
			continue
		}
		runID, err := uuid.Parse(payload.RunID)
		if err != nil {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		if payload.State != "stale" {
			continue
		}
		var lastStatusAt *time.Time
		if parsed, err := time.Parse(time.RFC3339, payload.LastLoomStatusAt); err == nil {
			parsed = parsed.UTC()
			lastStatusAt = &parsed
		}
		d.active[runID] = staleRunSignal{loomJobID: payload.LoomJobID, lastStatusAt: lastStatusAt}
	}
	return nil
}

func (d *StaleRunDetector) latestLoomStatus(ctx context.Context, loomJobID string) (*repository.NostrEventRecord, error) {
	records, err := d.events.FindByTag(ctx, "e", loomJobID, []int{kinds.LoomJobStatusUpdate}, 1)
	if err != nil {
		return nil, fmt.Errorf("find latest Loom kind-30100 status: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

func (d *StaleRunDetector) publishHealth(
	ctx context.Context,
	run domain.DeploymentRun,
	state string,
	reason string,
	lastStatusAt *time.Time,
	transitionAt time.Time,
) error {
	transitionAt = transitionAt.UTC()
	payload := map[string]any{
		"schema":              staleRunHealthSchema,
		"run_id":              run.ID.String(),
		"loom_job_id":         run.LoomJobID,
		"state":               state,
		"reason":              reason,
		"run_status":          string(run.Status),
		"stale_after_seconds": int64(d.staleAfter / time.Second),
		"changed_at":          transitionAt.Format(time.RFC3339),
	}
	if lastStatusAt != nil {
		payload["last_loom_status_at"] = lastStatusAt.UTC().Format(time.RFC3339)
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal stale-run health event: %w", err)
	}

	event := &nostr.Event{
		Kind:      nostr.Kind(kinds.NIP38Status),
		CreatedAt: nostr.Timestamp(transitionAt.Unix()),
		Tags: nostr.Tags{
			{"d", run.ID.String()},
			{"e", run.LoomJobID},
			{"t", "deployment.run.health"},
			{"domain", "deployment"},
			{"entity", "run"},
			{"run", run.ID.String()},
			{"status", state},
		},
		Content: string(content),
	}
	if err := d.publisher.PublishSignedEvent(ctx, event); err != nil {
		return fmt.Errorf("publish %s health for deployment run %s: %w", state, run.ID, err)
	}
	return nil
}

func staleRunCheckInterval(staleAfter time.Duration) time.Duration {
	interval := staleAfter / 2
	if interval > maximumStaleRunCheckInterval {
		interval = maximumStaleRunCheckInterval
	}
	if interval < minimumStaleRunCheckInterval {
		interval = minimumStaleRunCheckInterval
	}
	return interval
}

func deploymentRunStatusReference(run domain.DeploymentRun) time.Time {
	if run.StartedAt != nil && !run.StartedAt.IsZero() {
		return run.StartedAt.UTC()
	}
	return run.CreatedAt.UTC()
}

func isLoomBackedRun(run domain.DeploymentRun) bool {
	jobID := strings.TrimSpace(run.LoomJobID)
	return jobID != "" && jobID != "runtime:direct" && !strings.HasPrefix(jobID, "admission:")
}

func isTerminalRunStatus(status domain.DeploymentRunStatus) bool {
	switch status {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

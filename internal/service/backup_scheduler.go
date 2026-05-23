package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	BackupSchedulerInternalRequester = "bahia-backup-scheduler"
	BackupSchedulerRunRequestKind    = 38400

	backupRunMetadataScheduled              = "scheduled"
	backupRunMetadataScheduleDefinitionID   = "schedule_definition_id"
	backupRunMetadataScheduleDefinitionName = "schedule_definition_name"
	backupRunMetadataScheduleDueAt          = "schedule_due_at"
	backupRunMetadataScheduleDispatchAfter  = "schedule_dispatch_after"
	backupRunMetadataScheduleJitterWindow   = "schedule_jitter_window"
	backupRunMetadataScheduleMaintenance    = "schedule_maintenance_window"
)

// BackupScheduleStateRepository persists Bahia-owned schedule dispatch state.
type BackupScheduleStateRepository interface {
	UpsertBackupScheduleState(ctx context.Context, state *domain.BackupScheduleState) error
	GetBackupScheduleState(ctx context.Context, definitionID uuid.UUID) (*domain.BackupScheduleState, error)
}

// BackupSchedulerService evaluates BackupDefinition schedules and dispatches durable backup runs.
type BackupSchedulerService struct {
	registry        *BackupRegistryService
	schedules       BackupScheduleStateRepository
	logger          *zap.Logger
	clock           func() time.Time
	schedulerPubkey string
	requestKind     int
	pageSize        int
	maxCatchUp      int
}

type BackupSchedulerOption func(*BackupSchedulerService)

func WithBackupSchedulerClock(clock func() time.Time) BackupSchedulerOption {
	return func(s *BackupSchedulerService) {
		if clock != nil {
			s.clock = clock
		}
	}
}

func WithBackupSchedulerIdentity(pubkey string) BackupSchedulerOption {
	return func(s *BackupSchedulerService) {
		if strings.TrimSpace(pubkey) != "" {
			s.schedulerPubkey = strings.TrimSpace(pubkey)
		}
	}
}

func WithBackupSchedulerRequestKind(kind int) BackupSchedulerOption {
	return func(s *BackupSchedulerService) {
		if kind != 0 {
			s.requestKind = kind
		}
	}
}

func WithBackupSchedulerCatchUpLimit(limit int) BackupSchedulerOption {
	return func(s *BackupSchedulerService) {
		if limit > 0 {
			s.maxCatchUp = limit
		}
	}
}

func NewBackupSchedulerService(registry *BackupRegistryService, logger *zap.Logger, opts ...BackupSchedulerOption) *BackupSchedulerService {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &BackupSchedulerService{
		registry:        registry,
		logger:          logger.Named("backup-scheduler"),
		clock:           func() time.Time { return time.Now().UTC() },
		schedulerPubkey: BackupSchedulerInternalRequester,
		requestKind:     BackupSchedulerRunRequestKind,
		pageSize:        250,
		maxCatchUp:      1000,
	}
	if registry != nil && registry.repo != nil {
		if schedules, ok := registry.repo.(BackupScheduleStateRepository); ok {
			s.schedules = schedules
		}
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type BackupScheduleDispatch struct {
	DefinitionID          uuid.UUID                   `json:"definition_id"`
	RunID                 uuid.UUID                   `json:"run_id"`
	ScheduledRunDueAt     time.Time                   `json:"scheduled_run_due_at"`
	EffectiveDispatchTime time.Time                   `json:"effective_dispatch_time"`
	Created               bool                        `json:"created"`
	MissedRuns            int                         `json:"missed_runs"`
	NextScheduledRun      *time.Time                  `json:"next_scheduled_run,omitempty"`
	State                 *domain.BackupScheduleState `json:"state,omitempty"`
}

type BackupScheduleProcessError struct {
	DefinitionID uuid.UUID `json:"definition_id"`
	Message      string    `json:"message"`
}

type BackupScheduleProcessResult struct {
	Checked    int                          `json:"checked"`
	Dispatched int                          `json:"dispatched"`
	Skipped    int                          `json:"skipped"`
	MissedRuns int                          `json:"missed_runs"`
	Dispatches []BackupScheduleDispatch     `json:"dispatches,omitempty"`
	Errors     []BackupScheduleProcessError `json:"errors,omitempty"`
}

func (s *BackupSchedulerService) ProcessDueSchedules(ctx context.Context) (*BackupScheduleProcessResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	result := &BackupScheduleProcessResult{}
	now := s.clock().UTC()
	for offset := 0; ; offset += s.pageSize {
		definitions, err := s.registry.repo.ListBackupDefinitions(ctx, s.pageSize, offset)
		if err != nil {
			return nil, err
		}
		if len(definitions) == 0 {
			break
		}
		for i := range definitions {
			result.Checked++
			dispatch, skipped, err := s.processDefinitionAt(ctx, &definitions[i], now)
			if err != nil {
				result.Skipped++
				result.Errors = append(result.Errors, BackupScheduleProcessError{DefinitionID: definitions[i].ID, Message: err.Error()})
				s.logger.Warn("backup schedule definition skipped", zap.String("definition_id", definitions[i].ID.String()), zap.Error(err))
				continue
			}
			if skipped {
				result.Skipped++
				continue
			}
			if dispatch != nil {
				result.Dispatched++
				result.MissedRuns += dispatch.MissedRuns
				result.Dispatches = append(result.Dispatches, *dispatch)
			}
		}
		if len(definitions) < s.pageSize {
			break
		}
	}
	return result, nil
}

func (s *BackupSchedulerService) ProcessDefinition(ctx context.Context, definitionID uuid.UUID) (*BackupScheduleDispatch, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	definition, err := s.registry.repo.GetBackupDefinition(ctx, definitionID)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, fmt.Errorf("backup definition %s not found: %w", definitionID, repository.ErrNotFound)
	}
	dispatch, _, err := s.processDefinitionAt(ctx, definition, s.clock().UTC())
	return dispatch, err
}

func (s *BackupSchedulerService) PauseSchedule(ctx context.Context, definitionID uuid.UUID, reason, actor string) error {
	if err := s.ready(); err != nil {
		return err
	}
	definition, err := s.registry.repo.GetBackupDefinition(ctx, definitionID)
	if err != nil {
		return err
	}
	if definition == nil {
		return fmt.Errorf("backup definition %s not found: %w", definitionID, repository.ErrNotFound)
	}
	reason = strings.TrimSpace(reason)
	if err := domain.ValidateRequiredString(reason, "pause_reason"); err != nil {
		return err
	}
	state, err := s.stateForDefinition(ctx, definitionID)
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	state.PauseReason = reason
	state.PausedBy = strings.TrimSpace(actor)
	state.PausedAt = &now
	state.NextScheduledRun = nil
	return s.schedules.UpsertBackupScheduleState(ctx, state)
}

func (s *BackupSchedulerService) ResumeSchedule(ctx context.Context, definitionID uuid.UUID) error {
	if err := s.ready(); err != nil {
		return err
	}
	definition, err := s.registry.repo.GetBackupDefinition(ctx, definitionID)
	if err != nil {
		return err
	}
	if definition == nil {
		return fmt.Errorf("backup definition %s not found: %w", definitionID, repository.ErrNotFound)
	}
	state, err := s.stateForDefinition(ctx, definitionID)
	if err != nil {
		return err
	}
	state.PauseReason = ""
	state.PausedBy = ""
	state.PausedAt = nil
	if definition.ScheduleEnabled && strings.TrimSpace(definition.ScheduleExpression) != "" {
		next, err := s.nextEffectiveRun(definition, s.clock().UTC())
		if err != nil {
			return err
		}
		state.NextScheduledRun = next
	}
	return s.schedules.UpsertBackupScheduleState(ctx, state)
}

func (s *BackupSchedulerService) SetScheduleEnabled(ctx context.Context, definitionID uuid.UUID, enabled bool, actor, reason string) error {
	if err := s.ready(); err != nil {
		return err
	}
	definition, err := s.registry.repo.GetBackupDefinition(ctx, definitionID)
	if err != nil {
		return err
	}
	if definition == nil {
		return fmt.Errorf("backup definition %s not found: %w", definitionID, repository.ErrNotFound)
	}
	definition.ScheduleEnabled = enabled
	if err := s.registry.repo.UpsertBackupDefinition(ctx, definition); err != nil {
		return err
	}
	state, err := s.stateForDefinition(ctx, definitionID)
	if err != nil {
		return err
	}
	if enabled {
		state.DisabledBy = ""
		state.DisableReason = ""
		state.DisabledAt = nil
		if strings.TrimSpace(definition.ScheduleExpression) != "" {
			next, err := s.nextEffectiveRun(definition, s.clock().UTC())
			if err != nil {
				return err
			}
			state.NextScheduledRun = next
		}
	} else {
		now := s.clock().UTC()
		state.DisabledBy = strings.TrimSpace(actor)
		state.DisableReason = strings.TrimSpace(reason)
		state.DisabledAt = &now
		state.NextScheduledRun = nil
	}
	return s.schedules.UpsertBackupScheduleState(ctx, state)
}

func (s *BackupSchedulerService) processDefinitionAt(ctx context.Context, definition *domain.BackupDefinition, now time.Time) (*BackupScheduleDispatch, bool, error) {
	if definition == nil {
		return nil, true, nil
	}
	state, err := s.stateForDefinition(ctx, definition.ID)
	if err != nil {
		return nil, false, err
	}
	state.MaintenanceWindow = domain.BackupDefinitionMaintenanceWindow(definition)
	state.MaintenanceWindowTimeZone = domain.BackupDefinitionMaintenanceWindowTimeZone(definition)
	if !definition.ScheduleEnabled || strings.TrimSpace(definition.ScheduleExpression) == "" || state.Paused() || state.Disabled() {
		state.NextScheduledRun = nil
		if err := s.schedules.UpsertBackupScheduleState(ctx, state); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	due, err := s.dueRuns(definition, state, now)
	if err != nil {
		return nil, false, err
	}
	if len(due) == 0 {
		next, err := s.nextEffectiveRunAfterState(definition, state, now)
		if err != nil {
			return nil, false, err
		}
		state.NextScheduledRun = next
		if err := s.schedules.UpsertBackupScheduleState(ctx, state); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	selected := due[len(due)-1]
	missed := len(due) - 1
	run, created, err := s.dispatchRun(ctx, definition, selected)
	if err != nil {
		return nil, false, err
	}
	next, err := s.nextEffectiveRun(definition, selected.scheduledAt)
	if err != nil {
		return nil, false, err
	}
	state.LastScheduledDispatch = &now
	state.LastScheduledRunDueAt = &selected.scheduledAt
	state.MissedRunCount += missed
	state.NextScheduledRun = next
	if err := s.schedules.UpsertBackupScheduleState(ctx, state); err != nil {
		return nil, false, err
	}
	return &BackupScheduleDispatch{DefinitionID: definition.ID, RunID: run.ID, ScheduledRunDueAt: selected.scheduledAt, EffectiveDispatchTime: selected.effectiveAt, Created: created, MissedRuns: missed, NextScheduledRun: next, State: state}, false, nil
}

func (s *BackupSchedulerService) dispatchRun(ctx context.Context, definition *domain.BackupDefinition, due backupScheduleDue) (*domain.BackupRun, bool, error) {
	recipe, err := s.registry.GetRecipe(ctx, definition.RecipeID)
	if err != nil {
		return nil, false, err
	}
	if recipe == nil {
		return nil, false, fmt.Errorf("backup recipe %s not found for definition %s: %w", definition.RecipeID, definition.ID, repository.ErrNotFound)
	}
	policyID := recipe.PolicyID
	run := &domain.BackupRun{
		ID:                 uuid.New(),
		RecipeID:           recipe.ID,
		RepositoryID:       recipe.RepositoryID,
		PolicyID:           policyID,
		RequestedBy:        s.schedulerPubkey,
		RequestEventID:     scheduledRunEventID(definition.ID, due.scheduledAt),
		RequestKind:        s.requestKind,
		RequestDTag:        scheduledRunDTag(definition.ID, due.scheduledAt),
		Status:             domain.RunStatusQueued,
		Backend:            recipe.Backend,
		TargetRef:          recipe.TargetRef,
		VerificationMode:   recipe.VerificationMode,
		VerificationStatus: domain.BackupVerificationPending,
		Metadata: map[string]any{
			backupRunMetadataScheduled:              true,
			backupRunMetadataScheduleDefinitionID:   definition.ID.String(),
			backupRunMetadataScheduleDefinitionName: definition.Name,
			backupRunMetadataScheduleDueAt:          due.scheduledAt.UTC().Format(time.RFC3339Nano),
			backupRunMetadataScheduleDispatchAfter:  due.effectiveAt.UTC().Format(time.RFC3339Nano),
			backupRunMetadataScheduleJitterWindow:   definition.ScheduleJitterWindow,
			backupRunMetadataScheduleMaintenance:    domain.BackupDefinitionMaintenanceWindow(definition),
		},
	}
	return s.registry.CreateBackupRunIfAbsent(ctx, run)
}

func (s *BackupSchedulerService) dueRuns(definition *domain.BackupDefinition, state *domain.BackupScheduleState, now time.Time) ([]backupScheduleDue, error) {
	plan, err := parseBackupSchedule(definition.ScheduleExpression)
	if err != nil {
		return nil, err
	}
	anchor := scheduleAnchor(definition, state, now)
	due := []backupScheduleDue{}
	for i := 0; i < s.maxCatchUp; i++ {
		scheduledAt, err := plan.Next(anchor)
		if err != nil {
			return nil, err
		}
		effectiveAt, err := effectiveScheduledTime(definition, scheduledAt)
		if err != nil {
			return nil, err
		}
		if effectiveAt.After(now) {
			return due, nil
		}
		due = append(due, backupScheduleDue{scheduledAt: scheduledAt, effectiveAt: effectiveAt})
		anchor = scheduledAt
	}
	return nil, fmt.Errorf("%w: backup schedule %q exceeded catch-up limit %d", domain.ErrInvalidValue, definition.ScheduleExpression, s.maxCatchUp)
}

func (s *BackupSchedulerService) nextEffectiveRunAfterState(definition *domain.BackupDefinition, state *domain.BackupScheduleState, now time.Time) (*time.Time, error) {
	anchor := scheduleAnchor(definition, state, now)
	if state != nil && state.NextScheduledRun != nil && state.NextScheduledRun.After(now) {
		return state.NextScheduledRun, nil
	}
	return s.nextEffectiveRun(definition, anchor)
}

func (s *BackupSchedulerService) nextEffectiveRun(definition *domain.BackupDefinition, after time.Time) (*time.Time, error) {
	plan, err := parseBackupSchedule(definition.ScheduleExpression)
	if err != nil {
		return nil, err
	}
	scheduledAt, err := plan.Next(after)
	if err != nil {
		return nil, err
	}
	effectiveAt, err := effectiveScheduledTime(definition, scheduledAt)
	if err != nil {
		return nil, err
	}
	return &effectiveAt, nil
}

func (s *BackupSchedulerService) stateForDefinition(ctx context.Context, definitionID uuid.UUID) (*domain.BackupScheduleState, error) {
	state, err := s.schedules.GetBackupScheduleState(ctx, definitionID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = &domain.BackupScheduleState{DefinitionID: definitionID}
	}
	return state, nil
}

func (s *BackupSchedulerService) ready() error {
	if s == nil || s.registry == nil || s.registry.repo == nil {
		return fmt.Errorf("backup scheduler registry is not configured")
	}
	if s.schedules == nil {
		return fmt.Errorf("backup schedule state repository is not configured")
	}
	if s.requestKind == 0 {
		return fmt.Errorf("%w: backup scheduler request kind must not be zero", domain.ErrInvalidValue)
	}
	return nil
}

type backupScheduleDue struct {
	scheduledAt time.Time
	effectiveAt time.Time
}

func scheduleAnchor(definition *domain.BackupDefinition, state *domain.BackupScheduleState, now time.Time) time.Time {
	if state != nil && state.LastScheduledRunDueAt != nil {
		return state.LastScheduledRunDueAt.UTC()
	}
	if definition != nil && !definition.CreatedAt.IsZero() {
		return definition.CreatedAt.UTC()
	}
	return now.UTC()
}

func scheduledRunEventID(definitionID uuid.UUID, due time.Time) string {
	return "scheduled:" + definitionID.String() + ":" + due.UTC().Format(time.RFC3339Nano)
}

func scheduledRunDTag(definitionID uuid.UUID, due time.Time) string {
	return "backup-scheduled:" + definitionID.String() + ":" + due.UTC().Format("20060102T150405Z")
}

func effectiveScheduledTime(definition *domain.BackupDefinition, scheduledAt time.Time) (time.Time, error) {
	effective := scheduledAt.UTC().Truncate(time.Second)
	jitterWindow := strings.TrimSpace(definition.ScheduleJitterWindow)
	if jitterWindow != "" {
		jitter, err := time.ParseDuration(jitterWindow)
		if err != nil || jitter < 0 {
			return time.Time{}, fmt.Errorf("%w: invalid schedule_jitter_window %q", domain.ErrInvalidValue, definition.ScheduleJitterWindow)
		}
		effective = effective.Add(stableJitter(definition.ID, scheduledAt, jitter))
	}
	window := domain.BackupDefinitionMaintenanceWindow(definition)
	zone := domain.BackupDefinitionMaintenanceWindowTimeZone(definition)
	if window != "" {
		adjusted, err := applyMaintenanceWindow(effective, window, zone)
		if err != nil {
			return time.Time{}, err
		}
		effective = adjusted
	}
	return effective.UTC(), nil
}

func stableJitter(definitionID uuid.UUID, scheduledAt time.Time, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	hash := sha256.Sum256([]byte(definitionID.String() + "|" + scheduledAt.UTC().Format(time.RFC3339Nano)))
	n := binary.BigEndian.Uint64(hash[:8])
	return time.Duration(n % (uint64(window) + 1))
}

type maintenanceWindow struct {
	startHour int
	startMin  int
	endHour   int
	endMin    int
}

func applyMaintenanceWindow(t time.Time, expression, zone string) (time.Time, error) {
	window, err := parseMaintenanceWindow(expression)
	if err != nil {
		return time.Time{}, err
	}
	loc := time.UTC
	if strings.TrimSpace(zone) != "" {
		loaded, err := time.LoadLocation(strings.TrimSpace(zone))
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: invalid maintenance window timezone %q", domain.ErrInvalidValue, zone)
		}
		loc = loaded
	}
	local := t.In(loc)
	if window.contains(local) {
		return t.UTC(), nil
	}
	candidate := window.startOn(local)
	if !candidate.After(local) {
		candidate = window.startOn(local.AddDate(0, 0, 1))
	}
	return candidate.UTC(), nil
}

func parseMaintenanceWindow(expression string) (maintenanceWindow, error) {
	parts := strings.Split(strings.TrimSpace(expression), "-")
	if len(parts) != 2 {
		return maintenanceWindow{}, fmt.Errorf("%w: maintenance window must use HH:MM-HH:MM", domain.ErrInvalidValue)
	}
	sh, sm, err := parseClock(parts[0])
	if err != nil {
		return maintenanceWindow{}, err
	}
	eh, em, err := parseClock(parts[1])
	if err != nil {
		return maintenanceWindow{}, err
	}
	if sh == eh && sm == em {
		return maintenanceWindow{}, fmt.Errorf("%w: maintenance window start and end must differ", domain.ErrInvalidValue)
	}
	return maintenanceWindow{startHour: sh, startMin: sm, endHour: eh, endMin: em}, nil
}

func parseClock(raw string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%w: maintenance window clock %q must use HH:MM", domain.ErrInvalidValue, raw)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("%w: maintenance window hour %q is invalid", domain.ErrInvalidValue, parts[0])
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("%w: maintenance window minute %q is invalid", domain.ErrInvalidValue, parts[1])
	}
	return hour, minute, nil
}

func (w maintenanceWindow) contains(t time.Time) bool {
	minute := t.Hour()*60 + t.Minute()
	start := w.startHour*60 + w.startMin
	end := w.endHour*60 + w.endMin
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

func (w maintenanceWindow) startOn(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), w.startHour, w.startMin, 0, 0, t.Location())
}

type backupSchedulePlan interface {
	Next(after time.Time) (time.Time, error)
}

func parseBackupSchedule(expression string) (backupSchedulePlan, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return nil, fmt.Errorf("%w: schedule expression must not be empty", domain.ErrEmptyField)
	}
	switch expression {
	case "@hourly":
		return cadenceSchedulePlan{interval: time.Hour}, nil
	case "@daily":
		return cadenceSchedulePlan{interval: 24 * time.Hour}, nil
	case "@weekly":
		return cadenceSchedulePlan{interval: 7 * 24 * time.Hour}, nil
	}
	if strings.HasPrefix(expression, "@every ") || strings.HasPrefix(expression, "every ") {
		parts := strings.Fields(expression)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%w: cadence schedule must be '@every <duration>'", domain.ErrInvalidValue)
		}
		interval, err := time.ParseDuration(parts[1])
		if err != nil || interval <= 0 || interval < time.Second || interval%time.Second != 0 {
			return nil, fmt.Errorf("%w: cadence schedule duration %q must be a positive whole-second duration", domain.ErrInvalidValue, parts[1])
		}
		return cadenceSchedulePlan{interval: interval}, nil
	}
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w: cron schedule %q must have five fields", domain.ErrInvalidValue, expression)
	}
	cron, err := newCronSchedulePlan(fields)
	if err != nil {
		return nil, err
	}
	return cron, nil
}

type cadenceSchedulePlan struct {
	interval time.Duration
}

func (p cadenceSchedulePlan) Next(after time.Time) (time.Time, error) {
	return after.UTC().Add(p.interval).Truncate(time.Second), nil
}

type cronSchedulePlan struct {
	minutes cronField
	hours   cronField
	dom     cronField
	months  cronField
	dow     cronField
}

func newCronSchedulePlan(fields []string) (cronSchedulePlan, error) {
	minutes, err := parseCronField(fields[0], 0, 59, nil)
	if err != nil {
		return cronSchedulePlan{}, err
	}
	hours, err := parseCronField(fields[1], 0, 23, nil)
	if err != nil {
		return cronSchedulePlan{}, err
	}
	dom, err := parseCronField(fields[2], 1, 31, nil)
	if err != nil {
		return cronSchedulePlan{}, err
	}
	months, err := parseCronField(fields[3], 1, 12, nil)
	if err != nil {
		return cronSchedulePlan{}, err
	}
	dow, err := parseCronField(fields[4], 0, 7, map[int]int{7: 0})
	if err != nil {
		return cronSchedulePlan{}, err
	}
	return cronSchedulePlan{minutes: minutes, hours: hours, dom: dom, months: months, dow: dow}, nil
}

func (p cronSchedulePlan) Next(after time.Time) (time.Time, error) {
	candidate := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(5, 0, 0)
	for !candidate.After(limit) {
		if p.matches(candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("%w: cron schedule has no run within five years", domain.ErrInvalidValue)
}

func (p cronSchedulePlan) matches(t time.Time) bool {
	if !p.minutes.matches(t.Minute()) || !p.hours.matches(t.Hour()) || !p.months.matches(int(t.Month())) {
		return false
	}
	domMatches := p.dom.matches(t.Day())
	dowMatches := p.dow.matches(int(t.Weekday()))
	if !p.dom.wildcard && !p.dow.wildcard {
		return domMatches || dowMatches
	}
	return domMatches && dowMatches
}

type cronField struct {
	wildcard bool
	values   map[int]bool
}

func (f cronField) matches(value int) bool {
	if f.values[value] {
		return true
	}
	return false
}

func parseCronField(raw string, min, max int, aliases map[int]int) (cronField, error) {
	raw = strings.TrimSpace(raw)
	field := cronField{wildcard: raw == "*", values: map[int]bool{}}
	if raw == "" {
		return field, fmt.Errorf("%w: cron field must not be empty", domain.ErrEmptyField)
	}
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		if err := addCronPart(&field, strings.TrimSpace(part), min, max, aliases); err != nil {
			return field, err
		}
	}
	return field, nil
}

func addCronPart(field *cronField, part string, min, max int, aliases map[int]int) error {
	if part == "*" {
		for i := min; i <= max; i++ {
			field.values[aliasValue(i, aliases)] = true
		}
		return nil
	}
	step := 1
	base := part
	if strings.Contains(part, "/") {
		pieces := strings.Split(part, "/")
		if len(pieces) != 2 {
			return fmt.Errorf("%w: invalid cron field %q", domain.ErrInvalidValue, part)
		}
		base = pieces[0]
		parsedStep, err := strconv.Atoi(pieces[1])
		if err != nil || parsedStep <= 0 {
			return fmt.Errorf("%w: invalid cron step %q", domain.ErrInvalidValue, pieces[1])
		}
		step = parsedStep
	}
	start, end, err := cronRange(base, min, max)
	if err != nil {
		return err
	}
	for i := start; i <= end; i += step {
		field.values[aliasValue(i, aliases)] = true
	}
	return nil
}

func cronRange(raw string, min, max int) (int, int, error) {
	if raw == "*" {
		return min, max, nil
	}
	if strings.Contains(raw, "-") {
		pieces := strings.Split(raw, "-")
		if len(pieces) != 2 {
			return 0, 0, fmt.Errorf("%w: invalid cron range %q", domain.ErrInvalidValue, raw)
		}
		start, err := strconv.Atoi(pieces[0])
		if err != nil {
			return 0, 0, fmt.Errorf("%w: invalid cron value %q", domain.ErrInvalidValue, pieces[0])
		}
		end, err := strconv.Atoi(pieces[1])
		if err != nil {
			return 0, 0, fmt.Errorf("%w: invalid cron value %q", domain.ErrInvalidValue, pieces[1])
		}
		if start > end || start < min || end > max {
			return 0, 0, fmt.Errorf("%w: cron range %q outside %d-%d", domain.ErrInvalidValue, raw, min, max)
		}
		return start, end, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, 0, fmt.Errorf("%w: cron value %q outside %d-%d", domain.ErrInvalidValue, raw, min, max)
	}
	return value, value, nil
}

func aliasValue(value int, aliases map[int]int) int {
	if aliases == nil {
		return value
	}
	if mapped, ok := aliases[value]; ok {
		return mapped
	}
	return value
}

func sortedCronValues(field cronField) []int {
	values := make([]int, 0, len(field.values))
	for value := range field.values {
		values = append(values, value)
	}
	sort.Ints(values)
	return values
}

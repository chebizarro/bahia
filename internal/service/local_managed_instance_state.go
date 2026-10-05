package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	// managedRecoveryLedgerSchema is the canonical recovery ledger of one
	// managed instance: its recent recovery attempts, which are the restart
	// budget and backoff history.
	managedRecoveryLedgerSchema = "bahia.state.managed-instance-recovery.v1"
	// managedMaintenanceSchema is the canonical maintenance override of one
	// managed instance.
	managedMaintenanceSchema = "bahia.state.managed-instance-maintenance.v1"

	managedRecoveryLedgerEntity = "managed-instance-recovery"
	managedMaintenanceEntity    = "managed-instance-maintenance"

	// managedRecoveryLedgerLimit bounds a ledger record. The ledger is one
	// replaceable record per instance, so canonical state grows with the
	// number of instances and never with the number of attempts.
	managedRecoveryLedgerLimit = 100
)

// LocalManagedInstanceState is the state the managed-instance supervisor
// decides from. Health, the recovery ledger and maintenance overrides are read
// from the daemon's canonical records in the local event store, so a restarted
// or PostgreSQL-less daemon keeps its restart budget, its pending attempt and
// its overrides. The ledger and the override are published here, before the
// optional SQL index is written; health records are published by the
// ManagedInstanceHealthProjector. A failed index write is logged and never
// fails the caller.
type LocalManagedInstanceState struct {
	state     LocalSupervisionState
	index     repository.ManagedInstanceHealthRepository
	publisher NostrEventPublisher
	logger    *zap.Logger
	now       func() time.Time

	// writeMu serializes the read-modify-publish of ledger and maintenance
	// records.
	writeMu sync.Mutex

	mu          sync.Mutex
	health      map[string]domain.ManagedInstanceHealth
	ledgers     map[string]managedRecoveryLedger
	maintenance map[string]managedMaintenance
}

// managedRecoveryLedger is one instance's ledger and the created_at of the
// record that carries it.
type managedRecoveryLedger struct {
	createdAt gonostr.Timestamp
	attempts  []domain.RecoveryAttempt
}

type managedMaintenance struct {
	createdAt gonostr.Timestamp
	active    bool
	override  domain.MaintenanceOverride
}

// NewLocalManagedInstanceState builds the state. index may be nil; publisher
// is required for recovery and maintenance writes.
func NewLocalManagedInstanceState(state LocalSupervisionState, index repository.ManagedInstanceHealthRepository, publisher NostrEventPublisher, logger *zap.Logger) *LocalManagedInstanceState {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LocalManagedInstanceState{
		state: state, index: index, publisher: publisher, logger: logger.Named("managed-instance-state"),
		now:         func() time.Time { return time.Now().UTC() },
		health:      map[string]domain.ManagedInstanceHealth{},
		ledgers:     map[string]managedRecoveryLedger{},
		maintenance: map[string]managedMaintenance{},
	}
}

// GetHealth returns the instance's latest health, or nil before its first
// observation.
func (r *LocalManagedInstanceState) GetHealth(ctx context.Context, key domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	record, err := r.state.coordinate(ctx, managedInstanceDTag(key))
	if err != nil {
		return nil, err
	}
	var latest *domain.ManagedInstanceHealth
	if record != nil {
		if health, ok := decodeManagedHealthRecord(*record); ok {
			latest = &health
		}
	}
	r.mu.Lock()
	written, ok := r.health[instanceKeyString(key)]
	r.mu.Unlock()
	if ok && (latest == nil || !latest.UpdatedAt.After(written.UpdatedAt)) {
		latest = &written
	}
	return latest, nil
}

// UpsertHealth records health that changed no status.
func (r *LocalManagedInstanceState) UpsertHealth(ctx context.Context, health *domain.ManagedInstanceHealth) error {
	r.rememberHealth(*health)
	if r.index != nil {
		if err := r.index.UpsertHealth(ctx, health); err != nil {
			r.indexFailed("health", health.ManagedInstanceKey, err)
		}
	}
	return nil
}

// UpsertHealthWithEvent records a material health observation.
func (r *LocalManagedInstanceState) UpsertHealthWithEvent(ctx context.Context, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) error {
	r.rememberHealth(*health)
	if r.index != nil {
		if err := r.index.UpsertHealthWithEvent(ctx, health, event); err != nil {
			r.indexFailed("health", health.ManagedInstanceKey, err)
		}
	}
	return nil
}

func (r *LocalManagedInstanceState) rememberHealth(health domain.ManagedInstanceHealth) {
	r.mu.Lock()
	r.health[instanceKeyString(health.ManagedInstanceKey)] = health
	r.mu.Unlock()
}

// ListRecentRecoveryAttempts returns the instance's ledger, newest first.
func (r *LocalManagedInstanceState) ListRecentRecoveryAttempts(ctx context.Context, key domain.ManagedInstanceKey, limit int) ([]domain.RecoveryAttempt, error) {
	ledger, err := r.ledger(ctx, key)
	if err != nil {
		return nil, err
	}
	attempts := append([]domain.RecoveryAttempt(nil), ledger.attempts...)
	sortRecoveryAttempts(attempts)
	if limit > 0 && len(attempts) > limit {
		attempts = attempts[:limit]
	}
	return attempts, nil
}

// RecordRecoveryAttempt adds attempt to the instance's ledger. It reports
// false, and publishes nothing, when the ledger already holds the attempt's
// correlation: a retried evaluation of one failure generation is one attempt.
func (r *LocalManagedInstanceState) RecordRecoveryAttempt(ctx context.Context, attempt *domain.RecoveryAttempt) (bool, error) {
	if attempt == nil || strings.TrimSpace(attempt.CorrelationID) == "" {
		return false, fmt.Errorf("recovery attempt and correlation are required")
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	ledger, err := r.ledger(ctx, attempt.ManagedInstanceKey)
	if err != nil {
		return false, err
	}
	for _, known := range ledger.attempts {
		if known.CorrelationID == attempt.CorrelationID {
			return false, nil
		}
	}
	attempts := append(append([]domain.RecoveryAttempt(nil), ledger.attempts...), sanitizeAttempt(*attempt))
	if err := r.publishLedger(ctx, attempt.ManagedInstanceKey, attempts, ledger.createdAt); err != nil {
		return false, err
	}
	if r.index != nil {
		if _, err := r.index.RecordRecoveryAttempt(ctx, attempt); err != nil {
			r.indexFailed("recovery attempt", attempt.ManagedInstanceKey, err)
		}
	}
	return true, nil
}

// CompleteRecoveryAttemptWithHealthEvent settles the pending attempt with
// correlation in the ledger. It reports false when the ledger holds no such
// pending attempt, so a settled attempt is never published twice.
func (r *LocalManagedInstanceState) CompleteRecoveryAttemptWithHealthEvent(ctx context.Context, correlation string, result domain.RecoveryAttemptResult, evidence string, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) (bool, error) {
	if health == nil {
		return false, fmt.Errorf("health is required to complete a recovery attempt")
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	ledger, err := r.ledger(ctx, health.ManagedInstanceKey)
	if err != nil {
		return false, err
	}
	attempts := append([]domain.RecoveryAttempt(nil), ledger.attempts...)
	settled := false
	for i := range attempts {
		if attempts[i].CorrelationID == correlation && attempts[i].Result == domain.RecoveryAttemptPending {
			attempts[i].Result, attempts[i].Evidence = result, domain.SanitizeEvidence(evidence)
			settled = true
			break
		}
	}
	if !settled {
		return false, nil
	}
	if err := r.publishLedger(ctx, health.ManagedInstanceKey, attempts, ledger.createdAt); err != nil {
		return false, err
	}
	r.rememberHealth(*health)
	if r.index != nil {
		if _, err := r.index.CompleteRecoveryAttemptWithHealthEvent(ctx, correlation, result, evidence, health, event); err != nil {
			r.indexFailed("recovery attempt", health.ManagedInstanceKey, err)
		}
	}
	return true, nil
}

// GetActiveMaintenanceOverride returns the instance's override when it applies
// at the given instant.
func (r *LocalManagedInstanceState) GetActiveMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey, at time.Time) (*domain.MaintenanceOverride, error) {
	current, _, err := r.maintenanceRecord(ctx, key)
	if err != nil {
		return nil, err
	}
	if !current.active || !current.override.ActiveAt(at) {
		return nil, nil
	}
	override := current.override
	return &override, nil
}

// CreateMaintenanceOverride publishes override as the instance's maintenance
// record, replacing any earlier one.
func (r *LocalManagedInstanceState) CreateMaintenanceOverride(ctx context.Context, override *domain.MaintenanceOverride) error {
	if override == nil {
		return fmt.Errorf("maintenance override is required")
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	current, _, err := r.maintenanceRecord(ctx, override.ManagedInstanceKey)
	if err != nil {
		return err
	}
	if err := r.publishMaintenance(ctx, managedMaintenance{active: true, override: *override}, current.createdAt); err != nil {
		return err
	}
	if r.index != nil {
		if err := r.index.CreateMaintenanceOverride(ctx, override); err != nil {
			r.indexFailed("maintenance override", override.ManagedInstanceKey, err)
		}
	}
	return nil
}

// ClearMaintenanceOverride publishes the instance's maintenance record as
// inactive.
func (r *LocalManagedInstanceState) ClearMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	current, found, err := r.maintenanceRecord(ctx, key)
	if err != nil {
		return err
	}
	if found && current.active {
		if err := r.publishMaintenance(ctx, managedMaintenance{active: false, override: current.override}, current.createdAt); err != nil {
			return err
		}
	}
	if r.index != nil {
		if err := r.index.ClearMaintenanceOverride(ctx, key); err != nil {
			r.indexFailed("maintenance override", key, err)
		}
	}
	return nil
}

// BackfillFromIndex publishes the canonical ledger and maintenance record of
// every instance the SQL index knows and the local event store does not: the
// state a deployment recorded before these records existed. It is the one-shot
// PostgreSQL-to-relay backfill; an instance that already has a canonical
// record is never read from the index. It is a no-op without an index.
func (r *LocalManagedInstanceState) BackfillFromIndex(ctx context.Context) error {
	if r.index == nil {
		return nil
	}
	known, err := r.index.ListAllHealth(ctx)
	if err != nil {
		return fmt.Errorf("list indexed managed instances: %w", err)
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	var errs []error
	for _, health := range known {
		key := health.ManagedInstanceKey
		if err := r.backfillInstance(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("backfill %s: %w", instanceKeyString(key), err))
		}
	}
	return errors.Join(errs...)
}

func (r *LocalManagedInstanceState) backfillInstance(ctx context.Context, key domain.ManagedInstanceKey) error {
	_, hasMaintenance, err := r.maintenanceRecord(ctx, key)
	if err != nil {
		return err
	}
	if !hasMaintenance {
		override, err := r.index.GetActiveMaintenanceOverride(ctx, key, r.now())
		if err != nil {
			return err
		}
		if override != nil {
			if err := r.publishMaintenance(ctx, managedMaintenance{active: true, override: *override}, 0); err != nil {
				return err
			}
		}
	}
	ledger, err := r.ledger(ctx, key)
	if err != nil {
		return err
	}
	if ledger.createdAt == 0 {
		attempts, err := r.index.ListRecentRecoveryAttempts(ctx, key, managedRecoveryLedgerLimit)
		if err != nil {
			return err
		}
		if len(attempts) > 0 {
			for i := range attempts {
				attempts[i] = sanitizeAttempt(attempts[i])
			}
			if err := r.publishLedger(ctx, key, attempts, 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// ledger returns the instance's ledger: the newer of its canonical record and
// the one published in this process. A zero createdAt means it has none.
func (r *LocalManagedInstanceState) ledger(ctx context.Context, key domain.ManagedInstanceKey) (managedRecoveryLedger, error) {
	record, err := r.state.coordinate(ctx, managedRecoveryLedgerDTag(key))
	if err != nil {
		return managedRecoveryLedger{}, err
	}
	var latest managedRecoveryLedger
	if record != nil && supervisionTag(*record, kinds.CASControlStateTagSchema) == managedRecoveryLedgerSchema {
		var content struct {
			Attempts []domain.RecoveryAttempt `json:"attempts"`
		}
		if json.Unmarshal([]byte(record.Content), &content) == nil {
			latest = managedRecoveryLedger{createdAt: record.CreatedAt, attempts: content.Attempts}
		}
	}
	r.mu.Lock()
	written, ok := r.ledgers[instanceKeyString(key)]
	r.mu.Unlock()
	if ok && written.createdAt >= latest.createdAt {
		latest = written
	}
	return latest, nil
}

func (r *LocalManagedInstanceState) publishLedger(ctx context.Context, key domain.ManagedInstanceKey, attempts []domain.RecoveryAttempt, replaces gonostr.Timestamp) error {
	attempts = trimRecoveryLedger(attempts)
	createdAt, err := r.publishRecord(ctx, managedRecoveryLedgerSchema, managedRecoveryLedgerEntity, managedRecoveryLedgerDTag(key), key,
		map[string]any{"schema": managedRecoveryLedgerSchema, "attempts": attempts}, replaces)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.ledgers[instanceKeyString(key)] = managedRecoveryLedger{createdAt: createdAt, attempts: attempts}
	r.mu.Unlock()
	return nil
}

// maintenanceRecord returns the instance's maintenance record and whether it
// has one.
func (r *LocalManagedInstanceState) maintenanceRecord(ctx context.Context, key domain.ManagedInstanceKey) (managedMaintenance, bool, error) {
	record, err := r.state.coordinate(ctx, managedMaintenanceDTag(key))
	if err != nil {
		return managedMaintenance{}, false, err
	}
	var latest managedMaintenance
	if record != nil && supervisionTag(*record, kinds.CASControlStateTagSchema) == managedMaintenanceSchema {
		var content struct {
			Active   bool                       `json:"active"`
			Override domain.MaintenanceOverride `json:"override"`
		}
		if json.Unmarshal([]byte(record.Content), &content) == nil {
			latest = managedMaintenance{createdAt: record.CreatedAt, active: content.Active, override: content.Override}
		}
	}
	r.mu.Lock()
	written, ok := r.maintenance[instanceKeyString(key)]
	r.mu.Unlock()
	if ok && written.createdAt >= latest.createdAt {
		latest = written
	}
	return latest, latest.createdAt != 0, nil
}

func (r *LocalManagedInstanceState) publishMaintenance(ctx context.Context, record managedMaintenance, replaces gonostr.Timestamp) error {
	record.override.Actor = domain.SanitizeEvidence(record.override.Actor)
	record.override.Reason = domain.SanitizeEvidence(record.override.Reason)
	key := record.override.ManagedInstanceKey
	createdAt, err := r.publishRecord(ctx, managedMaintenanceSchema, managedMaintenanceEntity, managedMaintenanceDTag(key), key,
		map[string]any{"schema": managedMaintenanceSchema, "active": record.active, "override": record.override}, replaces)
	if err != nil {
		return err
	}
	record.createdAt = createdAt
	r.mu.Lock()
	r.maintenance[instanceKeyString(key)] = record
	r.mu.Unlock()
	return nil
}

// publishRecord signs and publishes one replaceable supervision record. Its
// created_at is strictly after the version it replaces, so a record rewritten
// within one second still replaces its predecessor on relays and in the local
// event store. A publish the outbox keeps for retry counts as published.
func (r *LocalManagedInstanceState) publishRecord(ctx context.Context, schema, entity, d string, key domain.ManagedInstanceKey, content any, replaces gonostr.Timestamp) (gonostr.Timestamp, error) {
	if r.publisher == nil {
		return 0, fmt.Errorf("managed instance state has no canonical publisher")
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return 0, fmt.Errorf("encode %s record: %w", entity, err)
	}
	createdAt := gonostr.Timestamp(r.now().Unix())
	if createdAt <= replaces {
		createdAt = replaces + 1
	}
	event := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: createdAt, Content: string(encoded), Tags: gonostr.Tags{
		{kinds.CASControlStateTagD, d},
		{kinds.CASControlStateTagDomain, "runtime"},
		{kinds.CASControlStateTagSchema, schema},
		{kinds.CASControlStateTagEntity, entity},
		{"t", kinds.CPStateTopicManagedInstanceHealth},
		{kinds.CASControlStateTagLegacyKind, strconv.Itoa(kinds.ManagedInstanceHealthRecord)},
		{"service", key.ServiceID.String()},
		{"environment", key.EnvironmentID.String()},
		{"deployment_unit", key.DeploymentUnitID.String()},
		{"target", strings.TrimSpace(key.RuntimeTargetName)},
	}}
	if err := r.publisher.PublishSignedEvent(ctx, &event); err != nil && !nostrutil.IsPublishQueued(err) {
		return 0, fmt.Errorf("publish %s record for %s: %w", entity, instanceKeyString(key), err)
	}
	return createdAt, nil
}

func (r *LocalManagedInstanceState) indexFailed(what string, key domain.ManagedInstanceKey, err error) {
	r.logger.Warn("managed instance SQL index write failed", zap.String("record", what), zap.String("instance", instanceKeyString(key)), zap.Error(err))
}

func managedRecoveryLedgerDTag(key domain.ManagedInstanceKey) string {
	return "runtime:recovery:" + strings.TrimPrefix(managedInstanceDTag(key), "runtime:instance:")
}

func managedMaintenanceDTag(key domain.ManagedInstanceKey) string {
	return "runtime:maintenance:" + strings.TrimPrefix(managedInstanceDTag(key), "runtime:instance:")
}

func sortRecoveryAttempts(attempts []domain.RecoveryAttempt) {
	sort.SliceStable(attempts, func(i, j int) bool { return attempts[i].RequestedAt.After(attempts[j].RequestedAt) })
}

// trimRecoveryLedger keeps the newest attempts up to the ledger limit. A
// pending attempt is always kept: it is the claim a restarted daemon must
// reconcile.
func trimRecoveryLedger(attempts []domain.RecoveryAttempt) []domain.RecoveryAttempt {
	out := append([]domain.RecoveryAttempt(nil), attempts...)
	sortRecoveryAttempts(out)
	if len(out) <= managedRecoveryLedgerLimit {
		return out
	}
	kept := out[:managedRecoveryLedgerLimit]
	for _, attempt := range out[managedRecoveryLedgerLimit:] {
		if attempt.Result == domain.RecoveryAttemptPending {
			kept = append(kept, attempt)
		}
	}
	return kept
}

var _ ManagedInstanceState = (*LocalManagedInstanceState)(nil)

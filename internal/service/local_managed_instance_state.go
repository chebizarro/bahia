package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	managedRecoveryStateSchema    = "bahia.state.managed-instance-recovery.v1"
	managedMaintenanceStateSchema = "bahia.state.managed-instance-maintenance.v1"
)

// LocalManagedInstanceState reads canonical observations and ledgers from the
// subscribed store. Its overlay covers the interval before bus projections
// arrive; the optional SQL repository is a write-behind query index only.
type LocalManagedInstanceState struct {
	State     LocalSupervisionState
	Index     repository.ManagedInstanceHealthRepository
	Publisher NostrEventPublisher
	Logger    *zap.Logger
	mu        sync.Mutex
	health    map[string]domain.ManagedInstanceHealth
	attempts  map[string]domain.RecoveryAttempt
	overrides map[string]*domain.MaintenanceOverride
}

func NewLocalManagedInstanceState(state LocalSupervisionState, index repository.ManagedInstanceHealthRepository, publisher NostrEventPublisher, logger *zap.Logger) *LocalManagedInstanceState {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LocalManagedInstanceState{State: state, Index: index, Publisher: publisher, Logger: logger, health: map[string]domain.ManagedInstanceHealth{}, attempts: map[string]domain.RecoveryAttempt{}, overrides: map[string]*domain.MaintenanceOverride{}}
}

func (r *LocalManagedInstanceState) GetHealth(_ context.Context, key domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	return r.healthSnapshot(key)
}

func (r *LocalManagedInstanceState) healthSnapshot(key domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	records, err := r.State.records(kinds.CPStateTopicManagedInstanceHealth)
	if err != nil {
		return nil, err
	}
	var latest *domain.ManagedInstanceHealth
	for _, event := range records {
		if localTag(event, "schema") != managedHealthStateSchema || localTag(event, "d") != managedInstanceDTag(key) {
			continue
		}
		var content struct {
			Health domain.ManagedInstanceHealth `json:"health"`
		}
		if !localStateContent(event, &content) || content.Health.ServiceID == (domain.ManagedInstanceKey{}).ServiceID {
			continue
		}
		latest = &content.Health
		break
	}
	r.mu.Lock()
	if overlay, ok := r.health[instanceKeyString(key)]; ok && (latest == nil || !latest.UpdatedAt.After(overlay.UpdatedAt)) {
		value := overlay
		latest = &value
	}
	r.mu.Unlock()
	return latest, nil
}

func (r *LocalManagedInstanceState) UpsertHealth(ctx context.Context, health *domain.ManagedInstanceHealth) error {
	r.mu.Lock()
	r.health[instanceKeyString(health.ManagedInstanceKey)] = *health
	r.mu.Unlock()
	if r.Index != nil {
		if err := r.Index.UpsertHealth(ctx, health); err != nil {
			r.Logger.Warn("managed health SQL index unavailable", zap.Error(err))
		}
	}
	return nil
}

func (r *LocalManagedInstanceState) UpsertHealthWithEvent(ctx context.Context, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) error {
	r.mu.Lock()
	r.health[instanceKeyString(health.ManagedInstanceKey)] = *health
	r.mu.Unlock()
	if r.Index != nil {
		if err := r.Index.UpsertHealthWithEvent(ctx, health, event); err != nil {
			r.Logger.Warn("managed health SQL index unavailable", zap.Error(err))
		}
	}
	return nil
}

func (r *LocalManagedInstanceState) ListRecentRecoveryAttempts(_ context.Context, key domain.ManagedInstanceKey, limit int) ([]domain.RecoveryAttempt, error) {
	records, err := r.State.records(kinds.CPStateTopicManagedInstanceHealth)
	if err != nil {
		return nil, err
	}
	byID := map[string]domain.RecoveryAttempt{}
	for _, event := range records {
		if localTag(event, "schema") != managedRecoveryStateSchema {
			continue
		}
		var content struct {
			Attempt domain.RecoveryAttempt `json:"attempt"`
		}
		if !localStateContent(event, &content) || instanceKeyString(content.Attempt.ManagedInstanceKey) != instanceKeyString(key) {
			continue
		}
		byID[content.Attempt.CorrelationID] = content.Attempt
	}
	r.mu.Lock()
	for correlation, attempt := range r.attempts {
		if instanceKeyString(attempt.ManagedInstanceKey) == instanceKeyString(key) {
			byID[correlation] = attempt
		}
	}
	r.mu.Unlock()
	result := make([]domain.RecoveryAttempt, 0, len(byID))
	for _, attempt := range byID {
		result = append(result, attempt)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RequestedAt.After(result[j].RequestedAt) })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (r *LocalManagedInstanceState) RecordRecoveryAttempt(ctx context.Context, attempt *domain.RecoveryAttempt) (bool, error) {
	if attempt == nil || strings.TrimSpace(attempt.CorrelationID) == "" {
		return false, fmt.Errorf("recovery attempt and correlation are required")
	}
	known, err := r.ListRecentRecoveryAttempts(ctx, attempt.ManagedInstanceKey, 0)
	if err != nil {
		return false, err
	}
	for _, previous := range known {
		if previous.CorrelationID == attempt.CorrelationID {
			return false, nil
		}
	}
	if err := r.publishRecord(ctx, managedRecoveryStateSchema, "runtime:recovery:"+attempt.CorrelationID, attempt.ManagedInstanceKey, map[string]any{"attempt": attempt}, attempt.RequestedAt.Unix()); err != nil {
		return false, err
	}
	r.mu.Lock()
	r.attempts[attempt.CorrelationID] = *attempt
	r.mu.Unlock()
	if r.Index != nil {
		if _, err := r.Index.RecordRecoveryAttempt(ctx, attempt); err != nil {
			r.Logger.Warn("recovery SQL index unavailable", zap.Error(err))
		}
	}
	return true, nil
}

func (r *LocalManagedInstanceState) CompleteRecoveryAttemptWithHealthEvent(ctx context.Context, correlation string, result domain.RecoveryAttemptResult, evidence string, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) (bool, error) {
	attempts, err := r.ListRecentRecoveryAttempts(ctx, health.ManagedInstanceKey, 0)
	if err != nil {
		return false, err
	}
	var attempt *domain.RecoveryAttempt
	for i := range attempts {
		if attempts[i].CorrelationID == correlation {
			attempt = &attempts[i]
			break
		}
	}
	if attempt == nil || attempt.Result != domain.RecoveryAttemptPending {
		return false, nil
	}
	attempt.Result, attempt.Evidence = result, domain.SanitizeEvidence(evidence)
	if err := r.publishRecord(ctx, managedRecoveryStateSchema, "runtime:recovery:"+correlation, attempt.ManagedInstanceKey, map[string]any{"attempt": attempt}, max(time.Now().UTC().Unix(), attempt.RequestedAt.Unix()+1)); err != nil {
		return false, err
	}
	r.mu.Lock()
	r.attempts[correlation] = *attempt
	r.health[instanceKeyString(health.ManagedInstanceKey)] = *health
	r.mu.Unlock()
	if r.Index != nil {
		if _, err := r.Index.CompleteRecoveryAttemptWithHealthEvent(ctx, correlation, result, evidence, health, event); err != nil {
			r.Logger.Warn("recovery SQL index unavailable", zap.Error(err))
		}
	}
	return true, nil
}

func (r *LocalManagedInstanceState) GetActiveMaintenanceOverride(_ context.Context, key domain.ManagedInstanceKey, at time.Time) (*domain.MaintenanceOverride, error) {
	records, err := r.State.records(kinds.CPStateTopicManagedInstanceHealth)
	if err != nil {
		return nil, err
	}
	var active *domain.MaintenanceOverride
	for _, event := range records {
		if localTag(event, "schema") != managedMaintenanceStateSchema || localTag(event, "d") != "runtime:maintenance:"+managedInstanceDTag(key) {
			continue
		}
		var content struct {
			Active   bool                       `json:"active"`
			Override domain.MaintenanceOverride `json:"override"`
		}
		if localStateContent(event, &content) && content.Active {
			active = &content.Override
		}
		break
	}
	r.mu.Lock()
	if overlay, ok := r.overrides[instanceKeyString(key)]; ok {
		active = overlay
	}
	r.mu.Unlock()
	if active != nil && active.ActiveAt(at) {
		return active, nil
	}
	return nil, nil
}

func (r *LocalManagedInstanceState) CreateMaintenanceOverride(ctx context.Context, override *domain.MaintenanceOverride) error {
	if override == nil {
		return fmt.Errorf("maintenance override is required")
	}
	if err := r.publishRecord(ctx, managedMaintenanceStateSchema, "runtime:maintenance:"+managedInstanceDTag(override.ManagedInstanceKey), override.ManagedInstanceKey, map[string]any{"active": true, "override": override}, override.CreatedAt.Unix()); err != nil {
		return err
	}
	r.mu.Lock()
	r.overrides[instanceKeyString(override.ManagedInstanceKey)] = override
	r.mu.Unlock()
	if r.Index != nil {
		if err := r.Index.CreateMaintenanceOverride(ctx, override); err != nil {
			r.Logger.Warn("maintenance SQL index unavailable", zap.Error(err))
		}
	}
	return nil
}

func (r *LocalManagedInstanceState) ClearMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey) error {
	current, err := r.GetActiveMaintenanceOverride(ctx, key, time.Now().UTC())
	if err != nil || current == nil {
		return err
	}
	if err := r.publishRecord(ctx, managedMaintenanceStateSchema, "runtime:maintenance:"+managedInstanceDTag(key), key, map[string]any{"active": false, "override": current}, max(time.Now().UTC().Unix(), current.CreatedAt.Unix()+1)); err != nil {
		return err
	}
	r.mu.Lock()
	r.overrides[instanceKeyString(key)] = nil
	r.mu.Unlock()
	if r.Index != nil {
		if err := r.Index.ClearMaintenanceOverride(ctx, key); err != nil {
			r.Logger.Warn("maintenance SQL index unavailable", zap.Error(err))
		}
	}
	return nil
}

func (r *LocalManagedInstanceState) publishRecord(ctx context.Context, schema, coordinate string, key domain.ManagedInstanceKey, content any, createdAt int64) error {
	if r.Publisher == nil {
		return fmt.Errorf("canonical managed-instance publisher is required")
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return err
	}
	tags := append(gonostr.Tags{{"d", coordinate}, {"domain", "runtime"}, {"schema", schema}, {"entity", "managed-instance"}, {"t", kinds.CPStateTopicManagedInstanceHealth}}, managedInstanceResourceTags(domain.ManagedInstanceHealth{ManagedInstanceKey: key})...)
	event := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: gonostr.Timestamp(createdAt), Tags: tags, Content: string(encoded)}
	if err := r.Publisher.PublishSignedEvent(ctx, &event); err != nil && !nostrutil.IsPublishQueued(err) {
		return err
	}
	return nil
}

var _ ManagedInstanceState = (*LocalManagedInstanceState)(nil)

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

const (
	EventContinuityStatusChanged           events.EventType = "continuity.status_changed"
	EventContinuityRecoveryProgressChanged events.EventType = "continuity.recovery_progress_changed"
)

const (
	KindContinuityStatusReadModel        = 30351
	KindContinuityDegradedModeActivation = 30352
	KindContinuityRecoveryProgress       = 30353
)

// ContinuityNostrPublishFunc publishes a relay-visible continuity read model.
// The injected implementation owns signing and OK verification.
type ContinuityNostrPublishFunc func(ctx context.Context, kind int, tags gonostr.Tags, content string) error

// ContinuityStatusProjector consumes continuity runtime events, updates the
// query store, and projects Nostr read models for observers.
type ContinuityStatusProjector struct {
	store   *InMemoryContinuityStatusStore
	publish ContinuityNostrPublishFunc
	logger  *zap.Logger
	clock   func() time.Time

	mu                    sync.Mutex
	lastStatusPublished   map[string]continuityStatusFingerprint
	lastRecoveryPublished map[string]continuityRecoveryFingerprint
}

type continuityStatusFingerprint struct {
	ActiveProfile     domain.ContinuityMode
	OperationState    string
	ActiveWorker      string
	RunID             string
	CurrentStepIndex  int
	CurrentStepCount  int
	CurrentStepAction string
}

type continuityRecoveryFingerprint struct {
	OperationState    string
	CurrentStepIndex  int
	CurrentStepCount  int
	CurrentStepAction string
	ActiveProfile     domain.ContinuityMode
	ActiveWorker      string
	Reason            string
}

func NewContinuityStatusProjector(pub events.Publisher, store *InMemoryContinuityStatusStore, publish ContinuityNostrPublishFunc, logger *zap.Logger) *ContinuityStatusProjector {
	if store == nil {
		store = NewInMemoryContinuityStatusStore()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &ContinuityStatusProjector{
		store:                 store,
		publish:               publish,
		logger:                logger.Named("continuity-status-projector"),
		clock:                 func() time.Time { return time.Now().UTC() },
		lastStatusPublished:   make(map[string]continuityStatusFingerprint),
		lastRecoveryPublished: make(map[string]continuityRecoveryFingerprint),
	}
	if pub != nil {
		pub.Subscribe(EventContinuityStatusChanged, p.handleStatusEvent)
		pub.Subscribe(EventContinuityRecoveryProgressChanged, p.handleRecoveryProgressEvent)
	}
	return p
}

func (p *ContinuityStatusProjector) Store() ContinuityStatusReader {
	if p == nil {
		return nil
	}
	return p.store
}

func (p *ContinuityStatusProjector) ProjectStatus(ctx context.Context, status ContinuityStatus) error {
	return p.project(ctx, status, false)
}

func (p *ContinuityStatusProjector) ProjectRecoveryProgress(ctx context.Context, status ContinuityStatus) error {
	return p.project(ctx, status, true)
}

func (p *ContinuityStatusProjector) handleStatusEvent(ctx context.Context, e events.Event) {
	status, ok := continuityStatusFromEvent(e.Data)
	if !ok {
		p.logger.Warn("continuity status event carried unsupported payload", zap.String("event_type", string(e.Type)))
		return
	}
	if err := p.ProjectStatus(ctx, status); err != nil {
		p.logger.Warn("project continuity status failed", zap.String("service_key", status.ServiceKey), zap.Error(err))
	}
}

func (p *ContinuityStatusProjector) handleRecoveryProgressEvent(ctx context.Context, e events.Event) {
	status, ok := continuityStatusFromEvent(e.Data)
	if !ok {
		p.logger.Warn("continuity recovery progress event carried unsupported payload", zap.String("event_type", string(e.Type)))
		return
	}
	if err := p.ProjectRecoveryProgress(ctx, status); err != nil {
		p.logger.Warn("project continuity recovery progress failed", zap.String("service_key", status.ServiceKey), zap.String("run_id", status.CurrentRunID), zap.Error(err))
	}
}

func (p *ContinuityStatusProjector) project(ctx context.Context, status ContinuityStatus, recoveryProgress bool) error {
	if p == nil {
		return nil
	}
	status = normalizeContinuityStatus(status)
	if status.ChangedAt.IsZero() {
		status.ChangedAt = p.now()
	}
	if err := validateContinuityStatus(status); err != nil {
		return err
	}
	previous, hadPrevious := p.store.GetServiceStatus(status.ServiceKey)
	p.store.Update(status)

	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error
	if p.shouldPublishStatus(status) {
		if err := p.publishContinuityStatus(ctx, status); err != nil {
			errs = append(errs, err)
		} else {
			p.lastStatusPublished[status.ServiceKey] = statusFingerprint(status)
		}
	}
	if shouldPublishDegradedActivation(previous, hadPrevious, status) {
		if err := p.publishDegradedActivation(ctx, previous, hadPrevious, status); err != nil {
			errs = append(errs, err)
		}
	}
	if recoveryProgress || status.OperationState == ContinuityOperationRecoveryInProgress {
		if p.shouldPublishRecoveryProgress(status) {
			if err := p.publishRecoveryProgress(ctx, status); err != nil {
				errs = append(errs, err)
			} else {
				p.lastRecoveryPublished[recoveryProgressKey(status)] = recoveryFingerprint(status)
			}
		}
	}
	return errors.Join(errs...)
}

func (p *ContinuityStatusProjector) now() time.Time {
	if p.clock != nil {
		return p.clock()
	}
	return time.Now().UTC()
}

func (p *ContinuityStatusProjector) shouldPublishStatus(status ContinuityStatus) bool {
	last, ok := p.lastStatusPublished[status.ServiceKey]
	return !ok || last != statusFingerprint(status)
}

func (p *ContinuityStatusProjector) shouldPublishRecoveryProgress(status ContinuityStatus) bool {
	if status.CurrentRunID == "" {
		return false
	}
	key := recoveryProgressKey(status)
	last, ok := p.lastRecoveryPublished[key]
	return !ok || last != recoveryFingerprint(status)
}

func shouldPublishDegradedActivation(previous ContinuityStatus, hadPrevious bool, next ContinuityStatus) bool {
	if next.ActiveProfile == domain.ContinuityModeFull {
		return false
	}
	return !hadPrevious || previous.ActiveProfile != next.ActiveProfile
}

func (p *ContinuityStatusProjector) publishContinuityStatus(ctx context.Context, status ContinuityStatus) error {
	return p.publishJSON(ctx, KindContinuityStatusReadModel, continuityStatusTags(status), continuityStatusContent(status))
}

func (p *ContinuityStatusProjector) publishDegradedActivation(ctx context.Context, previous ContinuityStatus, hadPrevious bool, status ContinuityStatus) error {
	content := continuityStatusContent(status)
	if hadPrevious {
		content["previous_profile"] = string(previous.ActiveProfile)
	}
	return p.publishJSON(ctx, KindContinuityDegradedModeActivation, degradedActivationTags(previous, hadPrevious, status), content)
}

func (p *ContinuityStatusProjector) publishRecoveryProgress(ctx context.Context, status ContinuityStatus) error {
	return p.publishJSON(ctx, KindContinuityRecoveryProgress, recoveryProgressTags(status), continuityStatusContent(status))
}

func (p *ContinuityStatusProjector) publishJSON(ctx context.Context, kind int, tags gonostr.Tags, value any) error {
	if p.publish == nil {
		return nil
	}
	content, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode continuity projection: %w", err)
	}
	if err := p.publish(ctx, kind, tags, string(content)); err != nil {
		return fmt.Errorf("publish continuity projection kind %d: %w", kind, err)
	}
	return nil
}

func validateContinuityStatus(status ContinuityStatus) error {
	if status.ServiceKey == "" {
		return fmt.Errorf("continuity status service key must not be empty")
	}
	if !status.ActiveProfile.IsValid() {
		return fmt.Errorf("continuity status active profile %q is invalid", status.ActiveProfile)
	}
	if !validContinuityOperationState(status.OperationState) {
		return fmt.Errorf("continuity status operation state %q is invalid", status.OperationState)
	}
	if status.CurrentStepIndex < 0 {
		return fmt.Errorf("continuity status current step index must not be negative")
	}
	if status.CurrentStepCount < 0 {
		return fmt.Errorf("continuity status current step count must not be negative")
	}
	if status.CurrentStepCount > 0 && status.CurrentStepIndex > status.CurrentStepCount {
		return fmt.Errorf("continuity status current step index must not exceed current step count")
	}
	return nil
}

func continuityStatusFromEvent(data any) (ContinuityStatus, bool) {
	switch v := data.(type) {
	case ContinuityStatus:
		return v, true
	case *ContinuityStatus:
		if v == nil {
			return ContinuityStatus{}, false
		}
		return *v, true
	default:
		return ContinuityStatus{}, false
	}
}

func statusFingerprint(status ContinuityStatus) continuityStatusFingerprint {
	return continuityStatusFingerprint{
		ActiveProfile:     status.ActiveProfile,
		OperationState:    status.OperationState,
		ActiveWorker:      status.ActiveWorkerPubKey,
		RunID:             status.CurrentRunID,
		CurrentStepIndex:  status.CurrentStepIndex,
		CurrentStepCount:  status.CurrentStepCount,
		CurrentStepAction: status.CurrentStepAction,
	}
}

func recoveryFingerprint(status ContinuityStatus) continuityRecoveryFingerprint {
	return continuityRecoveryFingerprint{
		OperationState:    status.OperationState,
		CurrentStepIndex:  status.CurrentStepIndex,
		CurrentStepCount:  status.CurrentStepCount,
		CurrentStepAction: status.CurrentStepAction,
		ActiveProfile:     status.ActiveProfile,
		ActiveWorker:      status.ActiveWorkerPubKey,
		Reason:            status.Reason,
	}
}

func recoveryProgressKey(status ContinuityStatus) string {
	return status.ServiceKey + ":" + status.CurrentRunID
}

func continuityStatusDTag(serviceKey string) string {
	return "continuity-status:" + serviceKey
}

func recoveryProgressDTag(serviceKey, runID string) string {
	return "recovery-progress:" + serviceKey + ":" + runID
}

func continuityStatusTags(status ContinuityStatus) gonostr.Tags {
	tags := gonostr.Tags{
		{"d", continuityStatusDTag(status.ServiceKey)},
		{"service", status.ServiceKey},
		{"profile", string(status.ActiveProfile)},
		{"operation_state", status.OperationState},
		{"t", "bahia"},
		{"t", "continuity"},
		{"t", "continuity-status"},
	}
	return appendWorkerAndRunTags(tags, status)
}

func degradedActivationTags(previous ContinuityStatus, hadPrevious bool, status ContinuityStatus) gonostr.Tags {
	tags := gonostr.Tags{
		{"service", status.ServiceKey},
		{"profile", string(status.ActiveProfile)},
		{"operation_state", status.OperationState},
		{"t", "bahia"},
		{"t", "continuity"},
		{"t", "degraded-mode-activation"},
	}
	if hadPrevious {
		tags = append(tags, gonostr.Tag{"previous_profile", string(previous.ActiveProfile)})
	}
	return appendWorkerAndRunTags(tags, status)
}

func recoveryProgressTags(status ContinuityStatus) gonostr.Tags {
	tags := gonostr.Tags{
		{"d", recoveryProgressDTag(status.ServiceKey, status.CurrentRunID)},
		{"service", status.ServiceKey},
		{"run", status.CurrentRunID},
		{"profile", string(status.ActiveProfile)},
		{"operation_state", status.OperationState},
		{"step", strconv.Itoa(status.CurrentStepIndex)},
		{"step_count", strconv.Itoa(status.CurrentStepCount)},
		{"t", "bahia"},
		{"t", "continuity"},
		{"t", "recovery-progress"},
	}
	if status.CurrentStepAction != "" {
		tags = append(tags, gonostr.Tag{"action", status.CurrentStepAction})
	}
	return appendWorkerAndRunTags(tags, status)
}

func appendWorkerAndRunTags(tags gonostr.Tags, status ContinuityStatus) gonostr.Tags {
	if status.PrimaryWorkerPubKey != "" {
		tags = append(tags, gonostr.Tag{"primary_worker", status.PrimaryWorkerPubKey}, gonostr.Tag{"p", status.PrimaryWorkerPubKey})
	}
	if status.ActiveWorkerPubKey != "" {
		tags = append(tags, gonostr.Tag{"active_worker", status.ActiveWorkerPubKey}, gonostr.Tag{"p", status.ActiveWorkerPubKey})
	}
	if status.StandbyWorkerPubKey != "" {
		tags = append(tags, gonostr.Tag{"standby_worker", status.StandbyWorkerPubKey}, gonostr.Tag{"p", status.StandbyWorkerPubKey})
	}
	if status.CurrentRunID != "" && !hasTag(tags, "run") {
		tags = append(tags, gonostr.Tag{"run", status.CurrentRunID})
	}
	return tags
}

func hasTag(tags gonostr.Tags, name string) bool {
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			return true
		}
	}
	return false
}

func continuityStatusContent(status ContinuityStatus) map[string]any {
	return map[string]any{
		"service_key":           status.ServiceKey,
		"active_profile":        string(status.ActiveProfile),
		"operation_state":       status.OperationState,
		"primary_worker_pubkey": status.PrimaryWorkerPubKey,
		"active_worker_pubkey":  status.ActiveWorkerPubKey,
		"standby_worker_pubkey": status.StandbyWorkerPubKey,
		"reason":                status.Reason,
		"changed_at":            status.ChangedAt.UTC().Format(time.RFC3339Nano),
		"current_run_id":        status.CurrentRunID,
		"current_step_index":    status.CurrentStepIndex,
		"current_step_count":    status.CurrentStepCount,
		"current_step_action":   status.CurrentStepAction,
	}
}

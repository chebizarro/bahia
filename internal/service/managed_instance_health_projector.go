package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const (
	managedHealthStatusSchema = "bahia.status.managed-instance-health.v1"
	managedHealthStateSchema  = "bahia.state.managed-instance-health.v1"
	managedHealthAuditSchema  = "bahia.audit.managed-instance-health.v1"
)

// ManagedInstanceHealthProjector projects internal supervisor events to canonical durable Nostr observables.
type ManagedInstanceHealthProjector struct {
	publisher NostrEventPublisher
	logger    *zap.Logger
	mu        sync.Mutex
	published map[string]struct{}
}

func NewManagedInstanceHealthProjector(bus events.Publisher, publisher NostrEventPublisher, logger *zap.Logger) *ManagedInstanceHealthProjector {
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &ManagedInstanceHealthProjector{publisher: publisher, logger: logger.Named("managed-instance-health-projector"), published: map[string]struct{}{}}
	if bus != nil {
		for _, typ := range []events.EventType{events.EventRuntimeInstanceHealthChanged, events.EventRuntimeRecoveryRequested, events.EventRuntimeRecoveryCompleted, events.EventRuntimeRecoveryFailed, events.EventRuntimeRecoveryBudgetExhausted, events.EventRuntimeMaintenanceChanged} {
			if subscriber, ok := bus.(events.ErrorSubscriber); ok {
				subscriber.SubscribeWithError(typ, p.handle)
			} else {
				bus.Subscribe(typ, func(ctx context.Context, e events.Event) {
					if err := p.handle(ctx, e); err != nil {
						p.logger.Error("project managed instance event", zap.Error(err))
					}
				})
			}
		}
	}
	return p
}

func (p *ManagedInstanceHealthProjector) handle(ctx context.Context, e events.Event) error {
	switch v := e.Data.(type) {
	case ManagedInstanceHealthChanged:
		return p.projectHealth(ctx, v)
	case *ManagedInstanceHealthChanged:
		if v != nil {
			return p.projectHealth(ctx, *v)
		}
	case ManagedInstanceRecoveryEvent:
		return p.projectRecovery(ctx, e.Type, v)
	case *ManagedInstanceRecoveryEvent:
		if v != nil {
			return p.projectRecovery(ctx, e.Type, *v)
		}
	case ManagedInstanceMaintenanceEvent:
		return p.projectMaintenance(ctx, v)
	case *ManagedInstanceMaintenanceEvent:
		if v != nil {
			return p.projectMaintenance(ctx, *v)
		}
	}
	return fmt.Errorf("unsupported managed instance event payload %T", e.Data)
}

func (p *ManagedInstanceHealthProjector) projectHealth(ctx context.Context, payload ManagedInstanceHealthChanged) error {
	h := sanitizeProjectedHealth(payload.Health)
	base := managedInstanceTags(h, managedHealthStatusSchema)
	statusContent := map[string]any{"schema": managedHealthStatusSchema, "event_id": payload.EventID, "status": h.Status, "severity": payload.Severity, "alert": payload.Alert, "reason": domain.SanitizeEvidence(payload.Reason), "observed_at": h.LastObservedAt}
	stateContent := map[string]any{"schema": managedHealthStateSchema, "event_id": payload.EventID, "health": h}
	eventsToPublish := []gonostr.Event{
		newManagedEvent(kinds.NIP38Status, payload.OccurredAt.Unix(), append(base, gonostr.Tag{"status", string(h.Status)}), statusContent),
		newManagedEvent(kinds.CASControlState, payload.OccurredAt.Unix(), managedInstanceTags(h, managedHealthStateSchema), stateContent),
	}
	if payload.PreviousStatus != h.Status {
		audit := map[string]any{"schema": managedHealthAuditSchema, "event_id": payload.EventID, "type": "health_transition", "previous_status": payload.PreviousStatus, "status": h.Status, "reason": domain.SanitizeEvidence(payload.Reason), "observed_at": h.LastObservedAt}
		tags := append(managedInstanceResourceTags(h), gonostr.Tag{"domain", "runtime"}, gonostr.Tag{"schema", managedHealthAuditSchema}, gonostr.Tag{"type", "health_transition"})
		eventsToPublish = append(eventsToPublish, newManagedEvent(kinds.CASAudit, payload.OccurredAt.Unix(), tags, audit))
	}
	return p.publishAll(ctx, eventsToPublish)
}

func (p *ManagedInstanceHealthProjector) projectRecovery(ctx context.Context, typ events.EventType, payload ManagedInstanceRecoveryEvent) error {
	h := sanitizeProjectedHealth(payload.Health)
	action := strings.TrimPrefix(string(typ), "runtime.")
	content := map[string]any{"schema": managedHealthAuditSchema, "event_id": payload.EventID, "type": action, "decision": payload.Decision, "attempt": sanitizeAttempt(payload.Attempt), "status": h.Status, "occurred_at": payload.OccurredAt}
	tags := append(managedInstanceResourceTags(h), gonostr.Tag{"domain", "runtime"}, gonostr.Tag{"schema", managedHealthAuditSchema}, gonostr.Tag{"type", action}, gonostr.Tag{"correlation", payload.Attempt.CorrelationID})
	statusTags := append(managedInstanceTags(h, managedHealthStatusSchema), gonostr.Tag{"status", action})
	return p.publishAll(ctx, []gonostr.Event{newManagedEvent(kinds.NIP38Status, payload.OccurredAt.Unix(), statusTags, content), newManagedEvent(kinds.CASAudit, payload.OccurredAt.Unix(), tags, content)})
}

func (p *ManagedInstanceHealthProjector) projectMaintenance(ctx context.Context, payload ManagedInstanceMaintenanceEvent) error {
	var h domain.ManagedInstanceHealth
	if payload.Health != nil {
		h = sanitizeProjectedHealth(*payload.Health)
	} else {
		h.ManagedInstanceKey = payload.Override.ManagedInstanceKey
	}
	action := "maintenance_cleared"
	if payload.Active {
		action = "maintenance_enabled"
	}
	o := payload.Override
	o.Actor = domain.SanitizeEvidence(o.Actor)
	o.Reason = domain.SanitizeEvidence(o.Reason)
	content := map[string]any{"schema": managedHealthAuditSchema, "event_id": payload.EventID, "type": action, "active": payload.Active, "override": o, "occurred_at": payload.OccurredAt}
	status := newManagedEvent(kinds.NIP38Status, payload.OccurredAt.Unix(), append(managedInstanceTags(h, managedHealthStatusSchema), gonostr.Tag{"status", action}), content)
	state := newManagedEvent(kinds.CASControlState, payload.OccurredAt.Unix(), managedInstanceTags(h, managedHealthStateSchema), map[string]any{"schema": managedHealthStateSchema, "event_id": payload.EventID, "health": h, "maintenance_override": func() any {
		if payload.Active {
			return o
		}
		return nil
	}()})
	audit := newManagedEvent(kinds.CASAudit, payload.OccurredAt.Unix(), append(managedInstanceResourceTags(h), gonostr.Tag{"domain", "runtime"}, gonostr.Tag{"schema", managedHealthAuditSchema}, gonostr.Tag{"type", action}), content)
	return p.publishAll(ctx, []gonostr.Event{status, state, audit})
}

func (p *ManagedInstanceHealthProjector) publishAll(ctx context.Context, list []gonostr.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range list {
		fingerprint, err := managedEventFingerprint(list[i])
		if err != nil {
			return err
		}
		if _, ok := p.published[fingerprint]; ok {
			continue
		}
		if p.publisher != nil {
			if err := p.publisher.PublishSignedEvent(ctx, &list[i]); err != nil {
				return fmt.Errorf("publish managed instance kind %d: %w", list[i].Kind, err)
			}
		}
		p.published[fingerprint] = struct{}{}
	}
	return nil
}

func newManagedEvent(kind int, createdAt int64, tags gonostr.Tags, content any) gonostr.Event {
	encoded, _ := json.Marshal(content)
	return gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: gonostr.Timestamp(createdAt), Tags: tags, Content: string(encoded)}
}

func managedInstanceTags(h domain.ManagedInstanceHealth, schema string) gonostr.Tags {
	return append(gonostr.Tags{{"d", managedInstanceDTag(h.ManagedInstanceKey)}, {"domain", "runtime"}, {"schema", schema}, {"entity", "managed-instance-health"}}, managedInstanceResourceTags(h)...)
}
func managedInstanceResourceTags(h domain.ManagedInstanceHealth) gonostr.Tags {
	return gonostr.Tags{{"service", h.ServiceID.String()}, {"environment", h.EnvironmentID.String()}, {"deployment_unit", h.DeploymentUnitID.String()}, {"target", strings.TrimSpace(h.RuntimeTargetName)}, {"supervisor", string(h.SupervisorType)}}
}
func managedInstanceDTag(k domain.ManagedInstanceKey) string {
	target := sha256.Sum256([]byte(strings.TrimSpace(k.RuntimeTargetName)))
	return fmt.Sprintf("runtime:instance:%s:%s:%s:%s", k.ServiceID, k.EnvironmentID, k.DeploymentUnitID, hex.EncodeToString(target[:]))
}
func sanitizeProjectedHealth(h domain.ManagedInstanceHealth) domain.ManagedInstanceHealth {
	h.Host = domain.SanitizeEvidence(h.Host)
	h.FailureReason = domain.SanitizeEvidence(h.FailureReason)
	if h.LastRecoveryAttempt != nil {
		a := sanitizeAttempt(*h.LastRecoveryAttempt)
		h.LastRecoveryAttempt = &a
	}
	return h
}
func sanitizeAttempt(a domain.RecoveryAttempt) domain.RecoveryAttempt {
	a.Evidence = domain.SanitizeEvidence(a.Evidence)
	return a
}
func managedEventFingerprint(ev gonostr.Event) (string, error) {
	encoded, err := json.Marshal(struct {
		Kind      gonostr.Kind
		CreatedAt gonostr.Timestamp
		Tags      gonostr.Tags
		Content   string
	}{ev.Kind, ev.CreatedAt, ev.Tags, ev.Content})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

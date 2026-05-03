// Package notifications implements the notification dispatch system.
package notifications

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Sender is the interface for delivering a notification.
type Sender interface {
	Send(ctx context.Context, channel *domain.NotificationChannel, eventType string, payload map[string]any) error
}

// Dispatcher routes events to matching notification channels.
type Dispatcher struct {
	repo    repository.NotificationRepository
	senders map[domain.ChannelType]Sender
	logger  *zap.Logger
}

// NewDispatcher creates a new notification dispatcher.
func NewDispatcher(repo repository.NotificationRepository, logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		repo:    repo,
		senders: make(map[domain.ChannelType]Sender),
		logger:  logger,
	}
}

// RegisterSender adds a sender for a channel type.
func (d *Dispatcher) RegisterSender(channelType domain.ChannelType, sender Sender) {
	d.senders[channelType] = sender
}

// SetupSubscriptions subscribes the dispatcher to all events from the publisher.
func (d *Dispatcher) SetupSubscriptions(pub events.Publisher) {
	// Subscribe to all event types we care about.
	eventTypes := []events.EventType{
		events.EventDeploymentIntentCreated,
		events.EventDeploymentIntentApproved,
		events.EventDeploymentRunCompleted,
		events.EventBuildStatusChanged,
		events.EventDriftDetected,
		events.EventReconcileCompleted,
		events.EventToolProvisionApprovalRequired,
		events.EventToolProvisionCompleted,
		events.EventToolProvisionFailed,
	}

	for _, et := range eventTypes {
		et := et // capture
		pub.Subscribe(et, func(ctx context.Context, e events.Event) {
			d.dispatch(ctx, string(et), map[string]any{
				"event_type": string(et),
				"entity_id":  e.EntityID,
				"data":       e.Data,
				"timestamp":  time.Now().UTC().Format(time.RFC3339),
			})
		})
	}
}

// dispatch sends a notification to all matching enabled channels.
func (d *Dispatcher) dispatch(ctx context.Context, eventType string, payload map[string]any) {
	channels, err := d.repo.ListChannels(ctx, true) // enabled only
	if err != nil {
		d.logger.Error("failed to list notification channels", zap.Error(err))
		return
	}

	for _, ch := range channels {
		if !ch.MatchesEvent(eventType) {
			continue
		}

		_ = d.sendToChannel(ctx, &ch, eventType, payload)
	}
}

// Dispatch sends a notification to all matching channels (public API for manual triggers).
func (d *Dispatcher) Dispatch(ctx context.Context, eventType string, payload map[string]any) {
	d.dispatch(ctx, eventType, payload)
}

// DispatchToChannel sends a notification directly to one channel, bypassing event filters.
// It is intended for explicit test/send operations where the caller already selected the channel.
func (d *Dispatcher) DispatchToChannel(ctx context.Context, ch *domain.NotificationChannel, eventType string, payload map[string]any) error {
	if ch == nil {
		return fmt.Errorf("notification channel is required")
	}
	return d.sendToChannel(ctx, ch, eventType, payload)
}

func (d *Dispatcher) sendToChannel(ctx context.Context, ch *domain.NotificationChannel, eventType string, payload map[string]any) error {
	sender, ok := d.senders[ch.ChannelType]
	if !ok {
		err := fmt.Errorf("no sender registered for channel type %s", ch.ChannelType)
		d.logger.Warn("no sender registered for channel type",
			zap.String("type", string(ch.ChannelType)),
			zap.String("channel", ch.Name),
		)
		return err
	}

	// Create log entry.
	logEntry := &domain.NotificationLog{
		ID:        uuid.New(),
		ChannelID: ch.ID,
		EventType: eventType,
		Payload:   payload,
		Status:    domain.NotificationStatusPending,
		Attempts:  0,
	}

	if err := d.repo.CreateLog(ctx, logEntry); err != nil {
		d.logger.Error("failed to create notification log", zap.Error(err))
	}

	// Attempt delivery.
	logEntry.Attempts = 1
	if err := sender.Send(ctx, ch, eventType, payload); err != nil {
		logEntry.Status = domain.NotificationStatusRetrying
		logEntry.LastError = err.Error()
		d.logger.Warn("notification delivery failed",
			zap.String("channel", ch.Name),
			zap.String("event", eventType),
			zap.Error(err),
		)
		_ = d.repo.UpdateLog(ctx, logEntry)
		return err
	} else {
		logEntry.Status = domain.NotificationStatusSent
		d.logger.Debug("notification sent",
			zap.String("channel", ch.Name),
			zap.String("event", eventType),
		)
	}

	_ = d.repo.UpdateLog(ctx, logEntry)
	return nil
}

// RetryFailed retries failed/pending notifications up to maxAttempts.
func (d *Dispatcher) RetryFailed(ctx context.Context, maxAttempts int) (int, error) {
	logs, err := d.repo.ListRetryable(ctx, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("listing retryable: %w", err)
	}

	retried := 0
	for _, logEntry := range logs {
		ch, err := d.repo.GetChannelByID(ctx, logEntry.ChannelID)
		if err != nil || ch == nil || !ch.Enabled {
			continue
		}

		sender, ok := d.senders[ch.ChannelType]
		if !ok {
			continue
		}

		logEntry.Attempts++
		if err := sender.Send(ctx, ch, logEntry.EventType, logEntry.Payload); err != nil {
			logEntry.LastError = err.Error()
			if logEntry.Attempts >= maxAttempts {
				logEntry.Status = domain.NotificationStatusFailed
			}
		} else {
			logEntry.Status = domain.NotificationStatusSent
			logEntry.LastError = ""
			retried++
		}
		_ = d.repo.UpdateLog(ctx, &logEntry)
	}

	return retried, nil
}

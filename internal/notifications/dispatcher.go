// Package notifications implements the notification dispatch system.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
//
// the dispatcher maintains an in-memory channel cache that is
// updated event-driven by the intent handler's OnChannelChanged callback.
// The initial load from the DB happens lazily on first dispatch; after that
// the cache is authoritative and the DB is not polled.
type Dispatcher struct {
	repo    repository.NotificationRepository
	senders map[domain.ChannelType]Sender
	logger  *zap.Logger

	// Event-driven channel cache.
	channelMu     sync.RWMutex
	channelCache  map[uuid.UUID]*domain.NotificationChannel
	cacheHydrated bool
}

// NewDispatcher creates a new notification dispatcher.
func NewDispatcher(repo repository.NotificationRepository, logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		repo:         repo,
		senders:      make(map[domain.ChannelType]Sender),
		logger:       logger,
		channelCache: make(map[uuid.UUID]*domain.NotificationChannel),
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
		events.EventSecurityPolicyBreached,
		events.EventRuntimeInstanceHealthChanged,
		events.EventRuntimeRecoveryRequested,
		events.EventRuntimeRecoveryCompleted,
		events.EventRuntimeRecoveryFailed,
		events.EventRuntimeRecoveryBudgetExhausted,
		events.EventRuntimeMaintenanceChanged,
	}

	errorSubscriber, supportsErrors := pub.(events.ErrorSubscriber)
	for _, et := range eventTypes {
		et := et // capture
		handler := func(ctx context.Context, e events.Event) error {
			if alert, ok := e.Data.(interface{ ShouldNotify() bool }); ok && !alert.ShouldNotify() {
				return nil
			}
			return d.dispatch(ctx, string(et), map[string]any{
				"event_type": string(et),
				"entity_id":  e.EntityID,
				"data":       e.Data,
				"timestamp":  time.Now().UTC().Format(time.RFC3339),
			})
		}
		if supportsErrors {
			errorSubscriber.SubscribeWithError(et, handler)
			continue
		}
		pub.Subscribe(et, func(ctx context.Context, e events.Event) {
			if err := handler(ctx, e); err != nil {
				d.logger.Error("notification event dispatch failed", zap.Error(err))
			}
		})
	}
}

// dispatch sends a notification to all matching enabled channels.
//
// uses the in-memory channel cache instead of polling the DB.
// The cache is hydrated lazily on first call and updated event-driven by
// OnChannelChanged.
func (d *Dispatcher) dispatch(ctx context.Context, eventType string, payload map[string]any) error {
	channels := d.enabledChannels(ctx)

	var dispatchErrors []error
	for _, ch := range channels {
		if !ch.MatchesEvent(eventType) {
			continue
		}
		chCopy := ch // copy for pointer
		if err := d.sendToChannel(ctx, &chCopy, eventType, payload); err != nil {
			dispatchErrors = append(dispatchErrors, fmt.Errorf("channel %s: %w", ch.Name, err))
		}
	}
	return errors.Join(dispatchErrors...)
}

// enabledChannels returns the enabled channels from the in-memory cache.
// On first call, it hydrates the cache from the DB.
func (d *Dispatcher) enabledChannels(ctx context.Context) []domain.NotificationChannel {
	d.channelMu.RLock()
	if d.cacheHydrated {
		var result []domain.NotificationChannel
		for _, ch := range d.channelCache {
			if ch.Enabled {
				result = append(result, *ch)
			}
		}
		d.channelMu.RUnlock()
		return result
	}
	d.channelMu.RUnlock()

	// Lazy hydration.
	d.channelMu.Lock()
	defer d.channelMu.Unlock()
	if d.cacheHydrated {
		// Another goroutine hydrated while we waited.
		var result []domain.NotificationChannel
		for _, ch := range d.channelCache {
			if ch.Enabled {
				result = append(result, *ch)
			}
		}
		return result
	}

	channels, err := d.repo.ListChannels(ctx, false) // load all, filter locally
	if err != nil {
		// Do NOT mark hydrated — retry on the next dispatch (bounded by the
		// caller's dispatch rate). Returning nil means this notification is
		// silently dropped, which is safe because the cache will hydrate on
		// the next event and catch up.
		d.logger.Warn("failed to hydrate channel cache from DB, will retry next dispatch", zap.Error(err))
		return nil
	}
	for i := range channels {
		ch := channels[i]
		d.channelCache[ch.ID] = &ch
	}
	d.cacheHydrated = true
	d.logger.Info("notification channel cache hydrated from DB",
		zap.Int("channel_count", len(channels)))

	var result []domain.NotificationChannel
	for _, ch := range d.channelCache {
		if ch.Enabled {
			result = append(result, *ch)
		}
	}
	return result
}

// OnChannelChanged updates the in-memory channel cache when a channel is
// created, updated, or deleted via the intent handler. This is the
// event-driven path; the dispatcher does not poll the DB.
func (d *Dispatcher) OnChannelChanged(ch *domain.NotificationChannel, deleted bool) {
	d.channelMu.Lock()
	defer d.channelMu.Unlock()
	if deleted {
		delete(d.channelCache, ch.ID)
		d.logger.Debug("notification channel removed from cache",
			zap.String("channel_id", ch.ID.String()),
			zap.String("name", ch.Name))
	} else {
		cp := *ch
		d.channelCache[ch.ID] = &cp
		d.logger.Debug("notification channel updated in cache",
			zap.String("channel_id", ch.ID.String()),
			zap.String("name", ch.Name),
			zap.Bool("enabled", ch.Enabled))
	}
}

// Dispatch sends a notification to all matching channels (public API for manual triggers).
func (d *Dispatcher) Dispatch(ctx context.Context, eventType string, payload map[string]any) error {
	return d.dispatch(ctx, eventType, payload)
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
		Attempts:  1,
	}

	if err := d.repo.CreateLog(ctx, logEntry); err != nil {
		return fmt.Errorf("creating notification log: %w", err)
	}

	// Attempt delivery.
	if err := sender.Send(ctx, ch, eventType, payload); err != nil {
		logEntry.Status = domain.NotificationStatusRetrying
		logEntry.LastError = err.Error()
		d.logger.Warn("notification delivery failed",
			zap.String("channel", ch.Name),
			zap.String("event", eventType),
			zap.Error(err),
		)
		updateErr := d.repo.UpdateLog(ctx, logEntry)
		return errors.Join(err, wrapNotificationLogError("recording failed delivery", updateErr))
	} else {
		logEntry.Status = domain.NotificationStatusSent
		d.logger.Debug("notification sent",
			zap.String("channel", ch.Name),
			zap.String("event", eventType),
		)
	}

	if err := d.repo.UpdateLog(ctx, logEntry); err != nil {
		return fmt.Errorf("finalizing delivered notification log: %w", err)
	}
	return nil
}

// RetryFailed retries failed/pending notifications up to maxAttempts.
func wrapNotificationLogError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (d *Dispatcher) RetryFailed(ctx context.Context, maxAttempts int) (int, error) {
	logs, err := d.repo.ListRetryable(ctx, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("listing retryable: %w", err)
	}

	retried := 0
	var retryErrors []error
	for _, logEntry := range logs {
		ch, err := d.repo.GetChannelByID(ctx, logEntry.ChannelID)
		if err != nil {
			retryErrors = append(retryErrors, fmt.Errorf("loading channel %s: %w", logEntry.ChannelID, err))
			continue
		}
		if ch == nil || !ch.Enabled {
			retryErrors = append(retryErrors, fmt.Errorf("channel %s unavailable for retry", logEntry.ChannelID))
			continue
		}

		sender, ok := d.senders[ch.ChannelType]
		if !ok {
			retryErrors = append(retryErrors, fmt.Errorf("no sender registered for channel type %s", ch.ChannelType))
			continue
		}

		logEntry.Attempts++
		sendErr := sender.Send(ctx, ch, logEntry.EventType, logEntry.Payload)
		if sendErr != nil {
			logEntry.LastError = sendErr.Error()
			logEntry.Status = domain.NotificationStatusRetrying
			if logEntry.Attempts >= maxAttempts {
				logEntry.Status = domain.NotificationStatusFailed
			}
		} else {
			logEntry.Status = domain.NotificationStatusSent
			logEntry.LastError = ""
		}
		updateErr := d.repo.UpdateLog(ctx, &logEntry)
		if sendErr != nil || updateErr != nil {
			retryErrors = append(retryErrors, errors.Join(sendErr, wrapNotificationLogError("updating retry log", updateErr)))
			continue
		}
		retried++
	}

	return retried, errors.Join(retryErrors...)
}

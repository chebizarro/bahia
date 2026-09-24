package controlplane

import (
	"context"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

func waitContinuityReconnect(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (h *ContinuityRuntime) consumeDefinitions(ctx context.Context, merged *nostradapter.MergedSubscription, backoff *nostradapter.Backoff) error {
	if merged == nil {
		return fmt.Errorf("nil continuity subscription")
	}
	defer merged.Close()
	eose := merged.EndOfStoredEvents
	closed := merged.Closed
	relayEOSE := merged.RelayEOSE
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case info, ok := <-relayEOSE:
			if !ok {
				relayEOSE = nil
				continue
			}
			h.logger.Debug("continuity relay EOSE", zap.String("relay", info.RelayURL))
		case info, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			h.pool.RecordRelayClosed(info.RelayURL, info.Reason)
			if nostradapter.IsAuthRequiredReason(info.Reason) {
				if err := h.pool.AuthenticateRelay(ctx, info.RelayURL); err != nil {
					return fmt.Errorf("continuity relay AUTH: %w", err)
				}
			}
			return fmt.Errorf("continuity subscription CLOSED: %s", info.Reason)
		case <-eose:
			if !merged.AllRelaysReachedEOSE() {
				return fmt.Errorf("continuity subscription ended without EOSE")
			}
			// EVENT and EOSE use separate channels. Apply already-delivered history
			// synchronously before releasing commands; event-bus dispatch is asynchronous.
		drain:
			for {
				select {
				case event, ok := <-merged.Events:
					if !ok {
						return fmt.Errorf("continuity event stream closed during backfill")
					}
					h.observeDefinition(ctx, event)
				default:
					break drain
				}
			}
			h.readyOnce.Do(func() { close(h.ready) })
			backoff.Reset()
			eose = nil
		case event, ok := <-merged.Events:
			if !ok {
				return fmt.Errorf("continuity event stream closed")
			}
			h.observeDefinition(ctx, event)
		}
	}
}

func (h *ContinuityRuntime) observeDefinition(ctx context.Context, event *nostr.Event) {
	if err := h.handleDefinition(ctx, event); err != nil {
		h.logger.Warn("continuity definition rejected", zap.Error(err))
	}
}

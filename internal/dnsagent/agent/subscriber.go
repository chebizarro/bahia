package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/dnsagent/engine"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
)

// ZoneSyncEvent is the content structure of a zone-sync addressable event
// published by the daemon. The agent subscribes to these instead of receiving
// ContextVM RPC pushes.
type ZoneSyncEvent struct {
	Zone    domain.DNSZone     `json:"zone"`
	Records []domain.DNSRecord `json:"records"`
}

// EventStore is the read interface the zone subscriber needs.
type EventStore interface {
	// QueryByAuthor returns the latest events from a given pubkey matching the
	// filter. This is used to load zone-sync events on startup.
	QueryByAuthor(ctx context.Context, pubkey string, kinds []nostr.Kind, limit int) ([]nostr.Event, error)
}

// ZoneSubscriber watches for zone-sync events from the daemon and applies them
// to the local dnsmasq engine. It replaces the ContextVM SyncHandler path.
type ZoneSubscriber struct {
	agent        *Agent
	daemonPubkey string
	events       <-chan nostr.Event
	logger       *zap.Logger
}

// NewZoneSubscriber creates a subscriber that feeds zone sync events to the
// agent. The events channel must deliver events from the daemon's pubkey with
// topic tag t=dns-zone-sync and kind 30900 (addressable replaceable).
func NewZoneSubscriber(agent *Agent, daemonPubkey string, events <-chan nostr.Event, logger *zap.Logger) *ZoneSubscriber {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ZoneSubscriber{
		agent:        agent,
		daemonPubkey: daemonPubkey,
		events:       events,
		logger:       logger.Named("zone-subscriber"),
	}
}

// Run processes zone sync events until the context is cancelled.
func (s *ZoneSubscriber) Run(ctx context.Context) error {
	s.logger.Info("zone subscriber started", zap.String("daemon_pubkey", s.daemonPubkey))
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-s.events:
			if !ok {
				return nil
			}
			if err := s.handleEvent(ctx, ev); err != nil {
				s.logger.Warn("zone sync event handling failed",
					zap.String("event_id", ev.ID.Hex()),
					zap.Error(err))
			}
		}
	}
}

func (s *ZoneSubscriber) handleEvent(ctx context.Context, ev nostr.Event) error {
	// Verify the event is from the daemon.
	if ev.PubKey.Hex() != s.daemonPubkey {
		return nil // ignore events from other authors
	}

	// Check NIP-40 expiration.
	now := s.agent.now()
	if nostrutil.Expired(&ev, now) {
		s.logger.Debug("ignoring expired zone sync event", zap.String("event_id", ev.ID.Hex()))
		return nil
	}

	// Parse the zone sync payload.
	var sync ZoneSyncEvent
	if err := json.Unmarshal([]byte(ev.Content), &sync); err != nil {
		return fmt.Errorf("decode zone sync event %s: %w", ev.ID.Hex(), err)
	}

	zoneName, err := s.agent.requireAllowedZone(sync.Zone.Name)
	if err != nil {
		s.logger.Debug("ignoring zone sync for disallowed zone",
			zap.String("zone", sync.Zone.Name),
			zap.String("event_id", ev.ID.Hex()))
		return nil
	}
	sync.Zone.Name = zoneName

	// Use created_at as serial, event ID as tie-breaker ( monotonicity).
	serial := int64(ev.CreatedAt)
	eventID := ev.ID.Hex()

	return s.agent.ApplyZoneSync(ctx, sync.Zone, sync.Records, serial, eventID)
}

// ApplyZoneSync applies a zone sync from an event. It mirrors the SyncHandler
// logic but takes serial and event ID from the event envelope instead of RPC
// params. The serial monotonicity and "lowest event ID on ties" rules are
// preserved.
func (a *Agent) ApplyZoneSync(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord, serial int64, eventID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	zoneName := zone.Name
	lastSerial, previouslyApplied := a.state.ZoneSerials[zoneName]

	if previouslyApplied && serial < lastSerial {
		// Stale event — our state is already past this serial.
		return nil
	}
	if previouslyApplied && serial == lastSerial {
		// Equal serial: apply only if event ID is lower (NIP-01 tie-break).
		applied := nostrutil.Version{CreatedAt: nostr.Timestamp(lastSerial), ID: a.state.ZoneRequestIDs[zoneName]}
		candidate := nostrutil.Version{CreatedAt: nostr.Timestamp(serial), ID: eventID}
		if eventID == "" || applied.ID == "" || !candidate.Supersedes(applied) {
			return nil
		}
	}

	// Check if the zone content actually changed.
	changed := true
	if previouslyApplied {
		desiredData, renderErr := engine.RenderZone(zone, records)
		if renderErr != nil {
			return renderErr
		}
		currentData, readErr := os.ReadFile(a.zoneIncludePath(zoneName))
		switch {
		case readErr == nil:
			changed = !bytes.Equal(currentData, desiredData)
		case errors.Is(readErr, os.ErrNotExist):
			changed = true
		default:
			return fmt.Errorf("read zone file for %q: %w", zoneName, readErr)
		}
	}

	if changed {
		if err := a.engine.ApplyZone(ctx, zone, records); err != nil {
			return err
		}
	}

	next := cloneState(a.state)
	next.ZoneSerials[zoneName] = serial
	if eventID != "" {
		next.ZoneRequestIDs[zoneName] = eventID
	} else {
		delete(next.ZoneRequestIDs, zoneName)
	}
	next.LastApplySerial = serial
	next.LastApplyAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeStateAtomic(ctx, a.stateFilePath, next); err != nil {
		a.state = next
		return err
	}
	a.state = next
	return nil
}

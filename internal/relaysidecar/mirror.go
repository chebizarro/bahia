package relaysidecar

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"go.uber.org/zap"
)

// MirrorConnectionStatus is the transport's per-relay connectivity evidence.
type MirrorConnectionStatus struct {
	RelayURL  string
	Connected bool
	LastError string
}

// MirrorTransport is supplied by cmd/relay, where the daemon relay-pool
// adapter can be imported without creating a service/sidecar test cycle.
// PublishToRelaysWithResults must return per-relay NIP-01 OK outcomes and
// handle NIP-11 probing, NIP-42 AUTH and websocket reconnects.
type MirrorTransport interface {
	Connect(context.Context)
	PublishToRelaysWithResults(context.Context, nostr.Event, []string) ([]nostrout.Result, error)
	ConnectionStatus() []MirrorConnectionStatus
	Close()
}

// mirrorForwarder replicates the sidecar's accepted, durable events to each
// external service relay independently. The queue and per-relay cursors live
// beside the canonical events in bbolt; only a relay's OK (or duplicate OK)
// advances its cursor. A failed relay never holds up another relay.
type mirrorForwarder struct {
	store     *eventStore
	policy    *policy
	transport MirrorTransport
	guard     *relayguard.Guard
	logger    *zap.Logger
	targets   []*mirrorTarget
	publish   func(context.Context, nostr.Event, []string) ([]nostrout.Result, error)
	waitRetry func(context.Context, time.Duration) bool
}

type mirrorTarget struct {
	url     string
	wake    chan struct{}
	cursor  atomic.Uint64
	skipped atomic.Uint64
	mu      sync.RWMutex
	failure string
}

// MirrorRelayStatus is one destination's durable replication progress.
type MirrorRelayStatus struct {
	RelayURL  string `json:"relay_url"`
	Connected bool   `json:"connected"`
	Cursor    uint64 `json:"cursor"`
	Backlog   uint64 `json:"backlog"`
	Skipped   uint64 `json:"skipped"`
	Failure   string `json:"failure,omitempty"`
}

func newMirrorForwarder(ctx context.Context, cfg config.NostrConfig, signer nostr.Signer, store *eventStore, admission *policy, logger *zap.Logger, transport MirrorTransport) (*mirrorForwarder, error) {
	targets, err := cfg.MirrorExternalRelays()
	if err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, fmt.Errorf("sidecar mirror requires a Nostr relay transport")
	}
	service, err := signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("read mirror service pubkey: %w", err)
	}
	guard, err := relayguard.New(service, cfg.ControlledRelaySet())
	if err != nil {
		return nil, err
	}
	if err := store.ensureMirrorIndex(ctx); err != nil {
		return nil, fmt.Errorf("reconcile sidecar mirror cursor index: %w", err)
	}
	mirror := &mirrorForwarder{
		store: store, policy: admission, transport: transport, guard: guard, logger: logger,
		publish: transport.PublishToRelaysWithResults,
		waitRetry: func(ctx context.Context, delay time.Duration) bool {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-timer.C:
				return true
			}
		},
	}
	for _, url := range targets {
		cursor, err := store.mirrorCursor(url)
		if err != nil {
			transport.Close()
			return nil, err
		}
		target := &mirrorTarget{url: url, wake: make(chan struct{}, 1)}
		target.cursor.Store(cursor)
		mirror.targets = append(mirror.targets, target)
	}
	return mirror, nil
}

func (m *mirrorForwarder) Notify() {
	for _, target := range m.targets {
		select {
		case target.wake <- struct{}{}:
		default:
		}
	}
}

func (m *mirrorForwarder) Run(ctx context.Context) {
	defer m.transport.Close()
	m.transport.Connect(ctx) // NIP-11 and NIP-42 are handled by the shared relay pool.
	var wg sync.WaitGroup
	for _, target := range m.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.runTarget(ctx, target)
		}()
	}
	wg.Wait()
}

func (m *mirrorForwarder) runTarget(ctx context.Context, target *mirrorTarget) {
	retryDelay := time.Second
	for ctx.Err() == nil {
		seq, id, found, err := m.store.mirrorNext(target.cursor.Load())
		if err != nil {
			target.setFailure(err.Error())
			return
		}
		if !found {
			select {
			case <-ctx.Done():
				return
			case <-target.wake:
				continue
			}
		}
		event, stored := m.store.latest(nostr.Filter{IDs: []nostr.ID{id}})
		if !stored || expired(event, nostr.Now()) {
			if err := m.advance(target, seq); err != nil {
				target.setFailure(err.Error())
				return
			}
			target.skipped.Add(1)
			continue
		}
		if reject, reason := m.policy.acceptEvent(ctx, event); reject {
			m.logger.Warn("sidecar mirror skips event refused by current posting policy",
				zap.String("relay", target.url), zap.String("event_id", id.Hex()), zap.String("reason", reason))
			if err := m.advance(target, seq); err != nil {
				target.setFailure(err.Error())
				return
			}
			target.skipped.Add(1)
			continue
		}
		if err := m.guard.CheckControlPlanePool(target.url, event); err != nil {
			target.setFailure(err.Error())
			return
		}
		publishCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		results, publishErr := m.publish(publishCtx, event, []string{target.url})
		cancel()
		if len(results) == 1 && results[0].RelayURL == target.url && results[0].Succeeded() {
			if err := m.advance(target, seq); err != nil {
				target.setFailure(err.Error())
				return
			}
			target.setFailure("")
			retryDelay = time.Second
			continue
		}
		reason := fmt.Sprintf("relay did not accept event %s", id.Hex())
		if len(results) > 0 {
			if results[0].Error != nil {
				reason = results[0].Error.Error()
			} else if results[0].Reason != "" {
				reason = results[0].Reason
			}
		} else if publishErr != nil {
			reason = publishErr.Error()
		}
		target.setFailure(reason)
		m.logger.Warn("sidecar mirror relay remains behind",
			zap.String("relay", target.url), zap.String("event_id", id.Hex()), zap.String("reason", reason))
		// A policy/auth refusal is terminal until configuration or credentials
		// change. Only transport errors and retryable relay responses reconnect
		// with backoff; no timer scans for new events.
		retry := publishErr != nil && len(results) == 0
		if len(results) == 1 {
			retry = results[0].Error != nil || results[0].IsRateLimited() || strings.HasPrefix(results[0].Reason, "error:")
		}
		if !retry || !m.waitRetry(ctx, retryDelay) {
			return
		}
		retryDelay = min(retryDelay*2, 2*time.Minute)
	}
}

func (m *mirrorForwarder) advance(target *mirrorTarget, seq uint64) error {
	if err := m.store.advanceMirrorCursor(target.url, seq); err != nil {
		return fmt.Errorf("advance sidecar mirror cursor: %w", err)
	}
	target.cursor.Store(seq)
	return nil
}

func (t *mirrorTarget) setFailure(reason string) {
	t.mu.Lock()
	t.failure = reason
	t.mu.Unlock()
}

func (m *mirrorForwarder) Status() ([]MirrorRelayStatus, error) {
	last, err := m.store.mirrorLastSequence()
	if err != nil {
		return nil, err
	}
	status := make([]MirrorRelayStatus, 0, len(m.targets))
	connections := make(map[string]MirrorConnectionStatus, len(m.targets))
	if m.transport != nil {
		for _, relay := range m.transport.ConnectionStatus() {
			connections[relay.RelayURL] = relay
		}
	}
	for _, target := range m.targets {
		cursor := target.cursor.Load()
		target.mu.RLock()
		failure := target.failure
		target.mu.RUnlock()
		connection, known := connections[target.url]
		if known && !connection.Connected && failure == "" {
			failure = connection.LastError
			if failure == "" {
				failure = "external relay disconnected"
			}
		}
		status = append(status, MirrorRelayStatus{
			RelayURL: target.url, Connected: connection.Connected,
			Cursor: cursor, Backlog: last - min(last, cursor),
			Skipped: target.skipped.Load(), Failure: failure,
		})
	}
	return status, nil
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantSessionRecoveryConfig configures startup recovery of assistant sessions.
type AssistantSessionRecoveryConfig struct {
	// RecentLimit bounds the startup hydration query. It is a cache warm-up
	// bound, not a historical inventory or migration-completeness guarantee.
	RecentLimit   int
	ServicePubkey string
	Logger        *slog.Logger
	Engine        AssistantTurnEngine
	Store         AssistantCheckpointStore
	// Subscriber defaults to the orchestrator's relay subscriber.
	Subscriber AssistantRelaySubscriber
}

// AssistantSessionRecoveryRunner is the single recovery path for both
// workflows: validate source sessions, classify/convert v1 history through the
// pure compatibility classifier, checkpoint the conversion idempotently, then
// hand the newest valid execution checkpoint to the engine's Recover entry.
type AssistantSessionRecoveryRunner struct {
	engine        AssistantTurnEngine
	store         AssistantCheckpointStore
	subscriber    AssistantRelaySubscriber
	limit         int
	servicePubkey string
	logger        *slog.Logger
}

func NewAssistantSessionRecoveryRunner(orchestrator *AssistantOrchestrator, cfg AssistantSessionRecoveryConfig) *AssistantSessionRecoveryRunner {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	limit := cfg.RecentLimit
	if limit <= 0 {
		limit = 500
	}
	subscriber := cfg.Subscriber
	servicePubkey := strings.TrimSpace(cfg.ServicePubkey)
	if orchestrator != nil {
		if subscriber == nil {
			subscriber = orchestrator.subscriber
		}
		if servicePubkey == "" {
			servicePubkey = strings.TrimSpace(orchestrator.identity.Pubkey)
		}
	}
	return &AssistantSessionRecoveryRunner{engine: cfg.Engine, store: cfg.Store, subscriber: subscriber, limit: limit, servicePubkey: servicePubkey, logger: logger.With("component", "assistant_session_recovery")}
}

func (r *AssistantSessionRecoveryRunner) Name() string { return "assistant-session-recovery" }

// assistantRecoverySource is the NIP-01-selected latest session projection for
// one session coordinate, after signature, author and coordinate validation.
type assistantRecoverySource struct {
	event  *nostr.Event
	schema string
}

// Run performs one EOSE-aware startup pass. It does not poll.
func (r *AssistantSessionRecoveryRunner) Run(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if r.engine == nil || r.store == nil {
		r.logger.Warn("assistant recovery skipped: unified executor is not configured; sessions remain parked")
		return nil
	}
	if r.subscriber == nil || r.servicePubkey == "" {
		r.logger.Warn("assistant recovery skipped: relay subscriber or service pubkey not configured")
		return nil
	}
	sources, err := r.collectSources(ctx)
	if err != nil {
		r.logger.Warn("assistant recovery query failed; sessions remain parked", "error", err)
		return nil
	}
	recovered := 0
	for _, src := range sources {
		if ctx.Err() != nil {
			return nil
		}
		var recErr error
		if src.schema == domain.AssistantSessionSchemaV2 {
			recErr = r.recoverV2(ctx, src.event)
		} else {
			recErr = r.recoverV1(ctx, src.event)
		}
		if recErr != nil {
			r.logger.Error("assistant session parked during recovery", "event_id", src.event.ID.Hex(), "schema", src.schema, "error", recErr)
			continue
		}
		recovered++
	}
	r.logger.Info("assistant recovery pass completed", "sessions_seen", len(sources), "sessions_recovered", recovered, "limit", r.limit)
	return nil
}

func (r *AssistantSessionRecoveryRunner) collectSources(ctx context.Context) ([]assistantRecoverySource, error) {
	author, err := nostr.PubKeyFromHex(r.servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("decode service pubkey: %w", err)
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{domain.KindAssistantSessionState}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{domain.AssistantSessionTagSchema: []string{domain.AssistantSessionSchema, domain.AssistantSessionSchemaV2}}, Limit: r.limit}
	sub, err := r.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	latest := map[string]assistantRecoverySource{}
	order := []string{}
	selected := func() []assistantRecoverySource {
		out := make([]assistantRecoverySource, 0, len(order))
		for _, id := range order {
			out = append(out, latest[id])
		}
		return out
	}
	events := sub.EventChan()
	closed := sub.ClosedChan()
	eose := sub.EOSEChan()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			// Incomplete history cannot prove absence; park rather than guess.
			return nil, fmt.Errorf("assistant recovery subscription closed: %s %s", c.RelayURL, c.Reason)
		case ev, ok := <-events:
			if !ok {
				if !assistantEOSEReached(eose) {
					return nil, errors.New("assistant recovery subscription ended before EOSE")
				}
				return selected(), nil
			}
			if ev == nil || ev.PubKey != author || !ev.CheckID() || !ev.VerifySignature() {
				continue
			}
			schema := tagValue(ev.Tags, domain.AssistantSessionTagSchema)
			if schema != domain.AssistantSessionSchema && schema != domain.AssistantSessionSchemaV2 {
				continue
			}
			var header struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal([]byte(ev.Content), &header) != nil || header.SessionID == "" || tagValue(ev.Tags, "session") != header.SessionID || tagValue(ev.Tags, "d") != schema+":"+header.SessionID {
				continue
			}
			current, seen := latest[header.SessionID]
			if !seen {
				order = append(order, header.SessionID)
			}
			if !seen || assistantRecoverySourceNewer(schema, ev, current) {
				copyEvent := *ev
				latest[header.SessionID] = assistantRecoverySource{event: &copyEvent, schema: schema}
			}
		case <-eose:
			return selected(), nil
		}
	}
}

// assistantRecoverySourceNewer prefers a v2 projection over v1 history for
// the same session; within one coordinate it applies NIP-01 replaceable
// ordering (newest created_at, then lowest event ID).
func assistantRecoverySourceNewer(schema string, ev *nostr.Event, current assistantRecoverySource) bool {
	if schema != current.schema {
		return schema == domain.AssistantSessionSchemaV2
	}
	if ev.CreatedAt != current.event.CreatedAt {
		return ev.CreatedAt > current.event.CreatedAt
	}
	return ev.ID.Hex() < current.event.ID.Hex()
}

func (r *AssistantSessionRecoveryRunner) hydrate(p domain.AssistantSessionV2, publishedAt nostr.Timestamp) {
	if hydrator, ok := r.engine.(AssistantExecutionProjectionHydrator); ok {
		hydrator.HydrateProjection(p, publishedAt)
	}
}

func (r *AssistantSessionRecoveryRunner) recoverV2(ctx context.Context, ev *nostr.Event) error {
	var p domain.AssistantSessionV2
	if err := json.Unmarshal([]byte(ev.Content), &p); err != nil {
		return fmt.Errorf("decode v2 projection: %w", err)
	}
	if p.Schema != domain.AssistantSessionSchemaV2 || p.SessionID == "" || p.CurrentRunID == "" || p.CheckpointEventID == "" {
		return errors.New("v2 projection lacks run or checkpoint identity")
	}
	// The selected event is the NIP-01 latest for the v2 coordinate, so its
	// created_at seeds the engine's monotonic projection clock.
	r.hydrate(p, ev.CreatedAt)
	if p.Phase == domain.AssistantExecutionCompleted || p.Phase == domain.AssistantExecutionFailed {
		// Finished runs need no execution; identity hydration suffices.
		return nil
	}
	return r.engine.Recover(ctx, AssistantExecutionReference{SessionID: p.SessionID, RunID: p.CurrentRunID, CheckpointEventID: p.CheckpointEventID})
}

func (r *AssistantSessionRecoveryRunner) recoverV1(ctx context.Context, ev *nostr.Event) error {
	conversion := ClassifyAssistantLegacySession(AssistantLegacySessionSource{EventID: ev.ID.Hex(), Schema: domain.AssistantSessionSchema, JSON: []byte(ev.Content)})
	if conversion.Execution == nil {
		r.logger.Info("assistant v1 history not executable", "event_id", ev.ID.Hex(), "classification", conversion.Classification, "reason", conversion.Reason)
		return nil
	}
	var legacy domain.AssistantSession
	if err := json.Unmarshal([]byte(ev.Content), &legacy); err != nil {
		return fmt.Errorf("decode v1 session identity: %w", err)
	}
	x := *conversion.Execution
	r.hydrate(domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: x.SessionID, OperatorPubkey: legacy.OperatorPubkey, Participants: legacy.Participants, AssistantID: legacy.AssistantID, AssistantPubkey: legacy.AssistantPubkey, TranscriptSummary: legacy.TranscriptSummary, CurrentRunID: x.RunID, Workflow: x.Workflow}, 0)
	// The conversion run ID is derived from the source event, so this root is
	// idempotent: an existing chain (possibly advanced) is never re-rooted.
	if _, err := r.store.Load(ctx, x.SessionID, x.RunID); err != nil {
		if !errors.Is(err, ErrAssistantCheckpointNotFound) {
			return fmt.Errorf("load conversion checkpoint: %w", err)
		}
		if _, err = r.store.Append(ctx, x, ""); err != nil {
			return fmt.Errorf("publish conversion checkpoint: %w", err)
		}
	}
	return r.engine.Recover(ctx, AssistantExecutionReference{SessionID: x.SessionID, RunID: x.RunID, LegacySourceEventID: ev.ID.Hex()})
}

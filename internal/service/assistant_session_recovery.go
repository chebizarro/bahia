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
	"github.com/openagentsinc/bahia/internal/kinds"
)

// AssistantSessionRecoveryConfig configures startup recovery of assistant sessions.
type AssistantSessionRecoveryConfig struct {
	// PageLimit bounds one page of the relay inventory query. The inventory
	// is never truncated to it: pages are requested with `until` until one
	// comes back short.
	PageLimit     int
	ServicePubkey string
	Logger        *slog.Logger
	Engine        AssistantTurnEngine
	Store         AssistantCheckpointStore
	// Subscriber defaults to the orchestrator's relay subscriber. It is the
	// inventory source only when LocalStore is nil.
	Subscriber AssistantRelaySubscriber
	// LocalStore is the daemon's local event store. When set, the session
	// inventory is every assistant-session record it holds, read once
	// Readiness reports the first relay catch-up complete; the bootstrapper
	// pages the daemon's 30900 records to completion into it.
	LocalStore SupervisionEventStore
	// Readiness gates the local-store inventory on the first relay catch-up.
	// Optional: without it the local store is read as it is.
	Readiness SupervisionReadiness
}

// AssistantSessionRecoveryRunner is the single recovery path for both
// workflows: validate source sessions, classify/convert v1 history through the
// pure compatibility classifier, checkpoint the conversion idempotently, then
// hand the newest valid execution checkpoint to the engine's Recover entry.
// The inventory it recovers is complete: every session record in the local
// event store, or, without one, every record a relay holds, paged with a
// cursor to the last page. Recovery is idempotent: a run the engine already
// owns is adopted in place and a finished run is only hydrated.
type AssistantSessionRecoveryRunner struct {
	engine         AssistantTurnEngine
	store          AssistantCheckpointStore
	subscriber     AssistantRelaySubscriber
	local          SupervisionEventStore
	readiness      SupervisionReadiness
	pageLimit      int
	servicePubkey  string
	logger         *slog.Logger
	topicMigration *AssistantSessionTopicMigration
}

const defaultAssistantRecoveryPageLimit = 500

func NewAssistantSessionRecoveryRunner(orchestrator *AssistantOrchestrator, cfg AssistantSessionRecoveryConfig) *AssistantSessionRecoveryRunner {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	pageLimit := cfg.PageLimit
	if pageLimit <= 0 {
		pageLimit = defaultAssistantRecoveryPageLimit
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
	return &AssistantSessionRecoveryRunner{engine: cfg.Engine, store: cfg.Store, subscriber: subscriber, local: cfg.LocalStore, readiness: cfg.Readiness, pageLimit: pageLimit, servicePubkey: servicePubkey, logger: logger.With("component", "assistant_session_recovery")}
}

// SetTopicMigration attaches the startup migration that adds t=assistant-session
// tags to compatibility records. It runs synchronously before recovery queries the relay.
func (r *AssistantSessionRecoveryRunner) SetTopicMigration(m *AssistantSessionTopicMigration) {
	if r != nil {
		r.topicMigration = m
	}
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
	if (r.subscriber == nil && r.local == nil) || r.servicePubkey == "" {
		r.logger.Warn("assistant recovery skipped: no session inventory source or service pubkey configured")
		return nil
	}
	// re-tag compatibility assistant session events before recovery
	// queries the relay with #t. The migration reads from the local event
	// store, adds t=assistant-session to untagged records, and re-publishes
	// them so the relay indexes them under #t. Idempotent: a no-op once
	// all records carry the tag.
	if r.topicMigration != nil {
		if err := r.topicMigration.Run(ctx); err != nil {
			r.logger.Warn("assistant session topic migration failed; recovery proceeds without it", "error", err)
		}
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
	r.logger.Info("assistant recovery pass completed", "sessions_seen", len(sources), "sessions_recovered", recovered, "source", r.inventorySource())
	return nil
}

func (r *AssistantSessionRecoveryRunner) inventorySource() string {
	if r.local != nil {
		return "local-store"
	}
	return "relay-paged"
}

// assistantRecoveryInventory selects the latest valid projection per session
// out of the records it is offered, in first-seen order.
type assistantRecoveryInventory struct {
	author nostr.PubKey
	latest map[string]assistantRecoverySource
	order  []string
}

func newAssistantRecoveryInventory(author nostr.PubKey) *assistantRecoveryInventory {
	return &assistantRecoveryInventory{author: author, latest: map[string]assistantRecoverySource{}}
}

// offer validates ev as a session record of the service author and keeps it
// when it is the session's newest.
func (inv *assistantRecoveryInventory) offer(ev *nostr.Event) {
	if ev == nil || ev.PubKey != inv.author || !ev.CheckID() || !ev.VerifySignature() {
		return
	}
	schema := tagValue(ev.Tags, domain.AssistantSessionTagSchema)
	if schema != domain.AssistantSessionSchema && schema != domain.AssistantSessionSchemaV2 {
		return
	}
	var header struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(ev.Content), &header) != nil || header.SessionID == "" || tagValue(ev.Tags, "session") != header.SessionID || tagValue(ev.Tags, "d") != schema+":"+header.SessionID {
		return
	}
	current, seen := inv.latest[header.SessionID]
	if !seen {
		inv.order = append(inv.order, header.SessionID)
	}
	if !seen || assistantRecoverySourceNewer(schema, ev, current) {
		copyEvent := *ev
		inv.latest[header.SessionID] = assistantRecoverySource{event: &copyEvent, schema: schema}
	}
}

func (inv *assistantRecoveryInventory) selected() []assistantRecoverySource {
	out := make([]assistantRecoverySource, 0, len(inv.order))
	for _, id := range inv.order {
		out = append(out, inv.latest[id])
	}
	return out
}

func assistantRecoveryFilter(author nostr.PubKey) nostr.Filter {
	// scope on #t (single-letter) instead of #schema
	// (multi-letter, invisible to NIP-01 relays). Compatibility records published
	// before.43 are re-tagged by the topic migration that runs synchronously
	// before the inventory is read (SetTopicMigration).
	return nostr.Filter{Kinds: []nostr.Kind{domain.KindAssistantSessionState}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"t": []string{kinds.AssistantSessionTopic}}}
}

// collectSources enumerates the session inventory: every record in the local
// event store, or every record the relays hold when no local store is
// configured.
func (r *AssistantSessionRecoveryRunner) collectSources(ctx context.Context) ([]assistantRecoverySource, error) {
	author, err := nostr.PubKeyFromHex(r.servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("decode service pubkey: %w", err)
	}
	if r.local != nil {
		return r.collectLocalSources(ctx, author)
	}
	return r.collectRelaySources(ctx, author)
}

// collectLocalSources reads the inventory from the local event store after
// the first relay catch-up. The store collapses each coordinate to its
// latest version and the query has no limit, so the inventory is complete.
func (r *AssistantSessionRecoveryRunner) collectLocalSources(ctx context.Context, author nostr.PubKey) ([]assistantRecoverySource, error) {
	if err := waitForSupervisionReadiness(ctx, r.readiness); err != nil {
		return nil, err
	}
	inventory := newAssistantRecoveryInventory(author)
	for ev := range r.local.QueryEvents(assistantRecoveryFilter(author)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		inventory.offer(&ev)
	}
	return inventory.selected(), nil
}

// collectRelaySources pages the relay inventory to completion: each page is
// one EOSE-aware REQ of at most pageLimit events, and a full page is followed
// by an `until`-bounded page from its oldest created_at (inclusive, events
// already seen deduplicated by id) until a page comes back short.
func (r *AssistantSessionRecoveryRunner) collectRelaySources(ctx context.Context, author nostr.PubKey) ([]assistantRecoverySource, error) {
	inventory := newAssistantRecoveryInventory(author)
	seen := map[nostr.ID]struct{}{}
	var until nostr.Timestamp
	for page := 1; ; page++ {
		filter := assistantRecoveryFilter(author)
		filter.Limit = r.pageLimit
		filter.Until = until
		delivered, oldest, err := r.collectRelayPage(ctx, filter, seen, inventory)
		if err != nil {
			return nil, err
		}
		if delivered < r.pageLimit {
			return inventory.selected(), nil
		}
		if until != 0 && oldest >= until {
			// A full page at one created_at cannot be paged past with until.
			return nil, fmt.Errorf("assistant recovery page %d: %d events share created_at %d; inventory is incomplete", page, delivered, oldest)
		}
		until = oldest
	}
}

// collectRelayPage runs one page and returns how many distinct events the
// relays delivered for it and the oldest created_at among them.
func (r *AssistantSessionRecoveryRunner) collectRelayPage(ctx context.Context, filter nostr.Filter, seen map[nostr.ID]struct{}, inventory *assistantRecoveryInventory) (int, nostr.Timestamp, error) {
	sub, err := r.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return 0, 0, err
	}
	defer sub.Close()
	delivered := 0
	var oldest nostr.Timestamp
	page := map[nostr.ID]struct{}{}
	events := sub.EventChan()
	closed := sub.ClosedChan()
	eose := sub.EOSEChan()
	for {
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			// Incomplete history cannot prove absence; park rather than guess.
			return 0, 0, fmt.Errorf("assistant recovery subscription closed: %s %s", c.RelayURL, c.Reason)
		case ev, ok := <-events:
			if !ok {
				if !assistantEOSEReached(eose) {
					return 0, 0, errors.New("assistant recovery subscription ended before EOSE")
				}
				return delivered, oldest, nil
			}
			if ev == nil {
				continue
			}
			// Every delivered event counts against the relay's limit, even
			// one already seen on the previous page's inclusive boundary.
			if _, dup := page[ev.ID]; !dup {
				page[ev.ID] = struct{}{}
				delivered++
				if oldest == 0 || ev.CreatedAt < oldest {
					oldest = ev.CreatedAt
				}
			}
			if _, dup := seen[ev.ID]; dup {
				continue
			}
			seen[ev.ID] = struct{}{}
			inventory.offer(ev)
		case <-eose:
			return delivered, oldest, nil
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

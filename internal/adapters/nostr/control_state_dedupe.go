package nostr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// This file generalizes the emergency DNS-only fingerprint/backoff containment
// across EVERY projection the Projector signs. It is the P0 relay-storm fix on
// the projector side: unchanged replaceable coordinates emit no new event,
// burst triggers for the same coordinate coalesce into a single publish, and a
// publish the outbox could not queue opens one bounded, jittered backoff shared
// by the whole projector path. Relay-level failures are not the projector's
// concern: the outbox publisher keeps the signed event and retries each relay.
// Tombstones and real changes are never suppressed, and the append-only audit
// log is never deduplicated.

// ErrProjectorBackoff is returned when a publish is skipped because the shared
// projector backoff window is open after a publish that was not queued.
// Callers already log-and-continue; the periodic repair loop retries after the
// window closes.
var ErrProjectorBackoff = errors.New("projector publish suppressed: relay backoff window open")

// ErrProjectorHydrationBackoff means retained state is still unavailable.
// A later projection trigger retries the load after the bounded window.
var ErrProjectorHydrationBackoff = errors.New("projector publish suppressed: hydration backoff window open")

const (
	projectionBackoffMin          = 2 * time.Second
	projectionBackoffMax          = time.Minute
	projectionHydrationBackoffMin = 2 * time.Second
	projectionHydrationBackoffMax = time.Minute
	// projectionHydrateLimit bounds how many retained records are read per wire
	// kind when warming the dedupe cache after a restart. The history holds one
	// event per coordinate, all the daemon's own, so this is a bound on live
	// coordinates per wire kind, across every projection family.
	projectionHydrateLimit = 100000
	projectionFamilyAudit  = "audit"
)

// volatileContentKeys are publication/bookkeeping timestamps and rotating
// linkage identifiers that change on every reconcile pass without any change
// to the projected read model. They are excluded from the stable fingerprint,
// exactly as the DNS containment zeroed MaterializedAt. If only these keys
// differ, the coordinate is materially unchanged and no event is emitted.
var volatileContentKeys = map[string]struct{}{
	"updated_at":             {},
	"last_reconciled_at":     {},
	"observed_at":            {},
	"current_observation_id": {},
	"materialized_at":        {},
	"materializedAt":         {},
	"received_at":            {},
	"published_at":           {},
}

// projectionKey identifies one replaceable coordinate. legacyKind is included
// because many projection families collapse onto the same wire kind
// (KindCASControlState) and could otherwise collide on a shared d-tag.
type projectionKey struct {
	wireKind   int
	legacyKind string
	d          string
}

// pendingRetryArgs stores the arguments for a publish suppressed by the shared
// backoff window. When the backoff timer fires, pending retries are flushed.
// The map is keyed by projectionKey so only the latest state per coordinate
// is retained (bounded memory).
type pendingRetryArgs struct {
	kind       int
	tags       gonostr.Tags
	content    string
	entityType string
	entityID   *uuid.UUID
}

// ProjectionFamilyMetrics are per-family publish counters. They are exposed
// for telemetry and tests; the umbrella restart gate reads the same numbers.
// Queued counts publishes the outbox kept below the publish quorum (still
// being retried per relay); Rejected counts publishes that were not queued.
type ProjectionFamilyMetrics struct {
	Attempted int64 `json:"attempted"`
	Accepted  int64 `json:"accepted"`
	Queued    int64 `json:"queued"`
	Rejected  int64 `json:"rejected"`
	Deduped   int64 `json:"deduped"`
	Coalesced int64 `json:"coalesced"`
	Backoff   int64 `json:"backoff"`
}

// projectionState is the lazily-initialized dedupe/coalescing/backoff/metrics
// state. It lives behind a pointer so the Projector literal needs no changes.
type projectionState struct {
	mu        sync.Mutex
	published map[projectionKey]string
	// createdAt is the created_at of the newest event signed per coordinate.
	// Relays keep the newest event on an addressable coordinate and break
	// created_at ties by lowest id, so a follow-up (typically a tombstone)
	// signed in the same second as its predecessor could lose. Each publish
	// on a coordinate is therefore stamped strictly after the previous one.
	createdAt map[projectionKey]gonostr.Timestamp
	keyLocks  map[projectionKey]*sync.Mutex
	hydration map[int]*projectionHydrationState
	metrics   map[string]*ProjectionFamilyMetrics
	// auditFacts are the fact ids of audit events already signed (or retained
	// from before a restart), so a republished source fact is not signed twice.
	auditFacts auditFactSet

	backoffMu      sync.Mutex
	retryAfter     time.Time
	retryDelay     time.Duration
	retryTimer     *time.Timer
	pendingRetries map[projectionKey]pendingRetryArgs
	now            func() time.Time
	jitterSource   *rand.Rand
}

// A wire kind has one load in flight. Waiters cannot observe it as hydrated
// until its retained records have been applied to the fingerprint cache.
type projectionHydrationState struct {
	mu         sync.Mutex
	hydrated   bool
	retryAfter time.Time
	retryDelay time.Duration
}

func (p *Projector) projection() *projectionState {
	p.projInitOnce.Do(func() {
		p.proj = &projectionState{
			published:    map[projectionKey]string{},
			createdAt:    map[projectionKey]gonostr.Timestamp{},
			keyLocks:     map[projectionKey]*sync.Mutex{},
			hydration:    map[int]*projectionHydrationState{},
			metrics:      map[string]*ProjectionFamilyMetrics{},
			now:          time.Now,
			jitterSource: rand.New(rand.NewSource(time.Now().UnixNano())),
		}
	})
	return p.proj
}

// ProjectionMetrics returns a snapshot of per-family publish counters.
func (p *Projector) ProjectionMetrics() map[string]ProjectionFamilyMetrics {
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ProjectionFamilyMetrics, len(s.metrics))
	for family, m := range s.metrics {
		out[family] = *m
	}
	return out
}

func (s *projectionState) family(name string) *ProjectionFamilyMetrics {
	m, ok := s.metrics[name]
	if !ok {
		m = &ProjectionFamilyMetrics{}
		s.metrics[name] = m
	}
	return m
}

func (s *projectionState) count(name string, apply func(*ProjectionFamilyMetrics)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	apply(s.family(name))
}

// projectionFamily names the projection family for metrics: the legacy_kind
// tag (mapped through canonicalStateDomain) when present, else the wire kind's
// own canonical domain, else the audit log, else the numeric kind.
func projectionFamily(wireKind int, tags gonostr.Tags) string {
	if wireKind == KindCASAudit {
		return projectionFamilyAudit
	}
	if legacy := tagValue(tags, "legacy_kind"); legacy != "" {
		if n, err := strconv.Atoi(legacy); err == nil {
			if domainName, entity := canonicalStateDomain(n); domainName != "" {
				return domainName + "/" + entity
			}
		}
	}
	if domainName, entity := canonicalStateDomain(wireKind); domainName != "" {
		return domainName + "/" + entity
	}
	return "kind:" + strconv.Itoa(wireKind)
}

func projectionKeyOf(wireKind int, tags gonostr.Tags) projectionKey {
	return projectionKey{
		wireKind:   wireKind,
		legacyKind: tagValue(tags, "legacy_kind"),
		d:          tagValue(tags, "d"),
	}
}

func isTombstoneTags(tags gonostr.Tags) bool {
	return tagValue(tags, "deleted") == "true"
}

// projectionFingerprint is the stable content hash of a projection: wire kind,
// order-insensitive tags, and the content with volatile keys stripped. The
// same function is used at publish time and when hydrating from retained
// records, so a restart never disagrees with the live path.
func projectionFingerprint(wireKind int, tags gonostr.Tags, content string) string {
	tagLines := make([]string, 0, len(tags))
	for _, tag := range tags {
		tagLines = append(tagLines, strings.Join(tag, "\x1f"))
	}
	sort.Strings(tagLines)
	h := sha256.New()
	h.Write([]byte(strconv.Itoa(wireKind) + "\x00" + strings.Join(tagLines, "\x1e") + "\x00"))
	_, keepRevision := revisionTokenFamilies[tagValue(tags, "legacy_kind")]
	if stateHash := tagValue(tags, confidentialStateHashTag); stateHash != "" {
		// Confidential ciphertext is randomized. Compare the signed keyed
		// plaintext digest, plus the OCK version so key rotation still republishes.
		var envelope struct {
			KeyVersion string `json:"key_version"`
		}
		_ = json.Unmarshal([]byte(content), &envelope)
		h.Write([]byte(stateHash + "\x00" + envelope.KeyVersion))
	} else {
		h.Write([]byte(stableContent(content, keepRevision)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// revisionTokenFamilies are the cp-state families (by legacy_kind) whose
// updated_at is not bookkeeping but the entity revision: clients send it back
// as expected_updated_at for an optimistic-concurrency update. It changes
// only when the entity does, and a record whose revision lags the cache makes
// the next edit conflict, so for these families it is part of the stable
// fingerprint. The relay-first registry publishes before the cache stamps the
// new revision, and the projection that follows must replace that record.
var revisionTokenFamilies = map[string]struct{}{
	strconv.Itoa(KindServiceRegistry):     {},
	strconv.Itoa(KindEnvironmentRegistry): {},
}

// stableContent canonicalizes a JSON object by stripping volatile keys (all
// but updated_at when keepRevision) and re-marshalling (encoding/json emits
// map keys in sorted order). Non-object content is used verbatim.
func stableContent(content string, keepRevision bool) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		return content
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return content
	}
	for key := range volatileContentKeys {
		if keepRevision && key == "updated_at" {
			continue
		}
		delete(object, key)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return content
	}
	return string(canonical)
}

// lockProjectionKey serializes publishes for one coordinate so a burst of
// identical triggers coalesces: the first publishes, the rest re-check the
// cache under the lock and skip. contended reports whether another publish
// for this key was already in flight when we arrived.
func (p *Projector) lockProjectionKey(key projectionKey) (contended bool, unlock func()) {
	s := p.projection()
	s.mu.Lock()
	lock, ok := s.keyLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		s.keyLocks[key] = lock
	}
	s.mu.Unlock()
	if lock.TryLock() {
		return false, lock.Unlock
	}
	lock.Lock()
	return true, lock.Unlock
}

func (p *Projector) projectionUnchanged(key projectionKey, fingerprint string) bool {
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.published[key]
	return ok && previous == fingerprint
}

func (p *Projector) rememberProjection(key projectionKey, fingerprint string, createdAt gonostr.Timestamp) {
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published[key] = fingerprint
	if createdAt > s.createdAt[key] {
		s.createdAt[key] = createdAt
	}
}

// nextProjectionCreatedAt returns now, or one second past the last event signed
// on key when now would not be strictly newer. Callers hold key's lock.
func (p *Projector) nextProjectionCreatedAt(key projectionKey) gonostr.Timestamp {
	now := gonostr.Now()
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	if last := s.createdAt[key]; now <= last {
		return last + 1
	}
	return now
}

// hydrateProjectionCache warms the dedupe cache for one wire kind from the
// projection history so an unchanged coordinate is not re-signed merely
// because the process restarted. Newest record per coordinate wins; only this
// projector's own events are considered.
func (p *Projector) hydrateProjectionCache(ctx context.Context, wireKind int) error {
	if p.history == nil {
		return nil
	}
	s := p.projection()
	s.mu.Lock()
	hydration := s.hydration[wireKind]
	if hydration == nil {
		hydration = &projectionHydrationState{}
		s.hydration[wireKind] = hydration
	}
	s.mu.Unlock()
	hydration.mu.Lock()
	defer hydration.mu.Unlock()
	if hydration.hydrated {
		return nil
	}
	if p.projectionNow().Before(hydration.retryAfter) {
		return fmt.Errorf("%w: kind %d", ErrProjectorHydrationBackoff, wireKind)
	}

	limit := projectionHydrateLimit
	if wireKind == KindCASAudit {
		limit = auditFactCapacity
	}
	records, err := p.history.ListByKind(ctx, wireKind, limit)
	if err != nil {
		if hydration.retryDelay == 0 {
			hydration.retryDelay = projectionHydrationBackoffMin
		} else {
			hydration.retryDelay *= 2
			if hydration.retryDelay > projectionHydrationBackoffMax {
				hydration.retryDelay = projectionHydrationBackoffMax
			}
		}
		hydration.retryAfter = p.projectionNow().Add(hydration.retryDelay)
		p.logger.Warn("hydrate projection dedupe cache failed; suppressing publish", zap.Int("kind", wireKind), zap.Error(err))
		return fmt.Errorf("hydrate projection dedupe cache for kind %d: %w", wireKind, err)
	}
	servicePubkey := ""
	if p.privateKey != "" {
		if derived, deriveErr := publicKeyHexFromPrivateKeyHex(p.privateKey); deriveErr == nil {
			servicePubkey = derived
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	seen := map[projectionKey]struct{}{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		if servicePubkey != "" && record.PubKey != servicePubkey {
			continue
		}
		tags := recordTags(record)
		if record.PublishState == repository.NostrPublishStateFailed {
			// Abandoned by the outbox: never reached the quorum, so it must
			// not suppress the next publish of the same content. Its
			// created_at still floors the coordinate, so the replacement is
			// strictly newer and wins over it in every store.
			if record.Kind != KindCASAudit {
				key := projectionKeyOf(record.Kind, tags)
				if createdAt := gonostr.Timestamp(record.CreatedAt.Unix()); createdAt > s.createdAt[key] {
					s.createdAt[key] = createdAt
				}
			}
			continue
		}
		if record.Kind == KindCASAudit {
			s.auditFacts.add(tagValue(tags, kinds.CPAuditTagFact))
			continue
		}
		key := projectionKeyOf(record.Kind, tags)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		s.published[key] = projectionFingerprint(record.Kind, tags, record.Content)
		if createdAt := gonostr.Timestamp(record.CreatedAt.Unix()); createdAt > s.createdAt[key] {
			s.createdAt[key] = createdAt
		}
	}
	hydration.hydrated = true
	return nil
}

func recordTags(record repository.NostrEventRecord) gonostr.Tags {
	var tags gonostr.Tags
	_ = json.Unmarshal(record.Tags, &tags)
	return tags
}

// Shared backoff -------------------------------------------------------------

func (p *Projector) projectionNow() time.Time {
	s := p.projection()
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// projectionBackoffActive reports whether the shared backoff window is open.
func (p *Projector) projectionBackoffActive() bool {
	s := p.projection()
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	return !s.retryAfter.IsZero() && p.projectionNow().Before(s.retryAfter)
}

// noteProjectionRejection opens or extends the shared backoff window with
// bounded exponential growth and ±25% jitter so many projectors do not retry
// in lockstep against a rate-limited relay.
func (p *Projector) noteProjectionRejection() {
	s := p.projection()
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	if s.retryDelay <= 0 {
		s.retryDelay = projectionBackoffMin
	} else {
		s.retryDelay *= 2
		if s.retryDelay > projectionBackoffMax {
			s.retryDelay = projectionBackoffMax
		}
	}
	jitter := time.Duration(0)
	if s.jitterSource != nil {
		span := int64(s.retryDelay) / 4
		if span > 0 {
			jitter = time.Duration(s.jitterSource.Int63n(2*span+1) - span)
		}
	}
	delay := s.retryDelay + jitter
	s.retryAfter = p.projectionNow().Add(delay)
	// Schedule event-driven retry at the backoff window end (Phase 3 X1).
	// This replaces the 10-minute periodic RepublishSnapshot ticker.
	p.scheduleBackoffRetry(delay)
}

func (p *Projector) resetProjectionBackoff() {
	s := p.projection()
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	s.retryAfter = time.Time{}
	s.retryDelay = 0
	if s.retryTimer != nil {
		s.retryTimer.Stop()
		s.retryTimer = nil
	}
}

// savePendingRetry stores the publish arguments for a coordinate suppressed
// by the shared backoff window. The map is keyed by projectionKey so only the
// latest state per coordinate is retained (bounded memory, Phase 3 X1).
func (p *Projector) savePendingRetry(key projectionKey, kind int, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) {
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRetries == nil {
		s.pendingRetries = map[projectionKey]pendingRetryArgs{}
	}
	s.pendingRetries[key] = pendingRetryArgs{
		kind:       kind,
		tags:       tags,
		content:    content,
		entityType: entityType,
		entityID:   entityID,
	}
}

// scheduleBackoffRetry sets a one-shot timer that flushes pending retries
// when the backoff window closes. It replaces the 10-minute periodic ticker
// with an event-driven mechanism: the timer fires exactly when the backoff
// expires, not on a fixed schedule (Phase 3 X1). Must be called with
// backoffMu held.
func (p *Projector) scheduleBackoffRetry(delay time.Duration) {
	s := p.projection()
	if s.retryTimer != nil {
		s.retryTimer.Stop()
	}
	s.retryTimer = time.AfterFunc(delay, func() {
		p.flushPendingRetries()
	})
}

// flushPendingRetries drains the pending-retry map and attempts each
// suppressed publish. If a publish fails (relay still unavailable),
// noteProjectionRejection re-opens the backoff window and schedules a
// new retry timer; the remaining pending items are re-saved by
// publishSigned's backoff check.
func (p *Projector) flushPendingRetries() {
	s := p.projection()
	s.mu.Lock()
	pending := s.pendingRetries
	s.pendingRetries = nil
	s.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	ctx := context.Background()
	for _, args := range pending {
		_ = p.publishSigned(ctx, args.kind, args.tags, args.content, args.entityType, args.entityID)
	}
}

// publishSigned is the single choke point for every projection the Projector
// signs. It applies, in order: replaceable-coordinate dedupe (never for the
// audit log, never for tombstones), per-coordinate coalescing, the shared
// rejection backoff, and per-family metrics, then delegates the actual
// sign+publish+record to publishSignedDirect.
func (p *Projector) publishSigned(ctx context.Context, kind int, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	wireKind := int(canonicalKind(kind))
	family := projectionFamily(wireKind, tags)
	s := p.projection()

	dedupable := wireKind != KindCASAudit
	var key projectionKey
	var fingerprint string
	unlock := func() {}
	contended := false
	if dedupable {
		key = projectionKeyOf(wireKind, tags)
		fingerprint = projectionFingerprint(wireKind, tags, content)
		// Fail closed on unavailable retained state for dedupable projections:
		// a cold cache would re-sign every unchanged coordinate. Tombstones are
		// never deduped, so they do not depend on the cache and must not be
		// held back by a retained-state read failure.
		if err := p.hydrateProjectionCache(ctx, wireKind); err != nil && !isTombstoneTags(tags) {
			return err
		}
		contended, unlock = p.lockProjectionKey(key)
	}
	defer unlock()

	if dedupable && !isTombstoneTags(tags) && p.projectionUnchanged(key, fingerprint) {
		if contended {
			s.count(family, func(m *ProjectionFamilyMetrics) { m.Coalesced++ })
		} else {
			s.count(family, func(m *ProjectionFamilyMetrics) { m.Deduped++ })
		}
		return nil
	}

	s.count(family, func(m *ProjectionFamilyMetrics) { m.Attempted++ })
	if p.projectionBackoffActive() {
		s.count(family, func(m *ProjectionFamilyMetrics) { m.Backoff++ })
		if dedupable {
			p.savePendingRetry(key, kind, tags, content, entityType, entityID)
		}
		return ErrProjectorBackoff
	}

	createdAt := gonostr.Now()
	if dedupable {
		createdAt = p.nextProjectionCreatedAt(key)
	}
	queued, err := p.publishSignedDirect(ctx, kind, createdAt, tags, content, entityType, entityID)
	if err != nil {
		s.count(family, func(m *ProjectionFamilyMetrics) { m.Rejected++ })
		p.noteProjectionRejection()
		return err
	}
	// A queued event is kept by the outbox, which retries the relays that
	// have not accepted it; it is remembered exactly like an accepted one so
	// neither a bus event nor the periodic repair re-signs it.
	if queued {
		s.count(family, func(m *ProjectionFamilyMetrics) { m.Queued++ })
	} else {
		s.count(family, func(m *ProjectionFamilyMetrics) { m.Accepted++ })
	}
	p.resetProjectionBackoff()
	if dedupable {
		p.rememberProjection(key, fingerprint, createdAt)
	}
	return nil
}

// publishSignedRelayFirst signs one cp-state record for a writer that commits
// only after the publish quorum accepted it (RelayFirstStatePublisher). It
// shares publishSigned's per-coordinate lock, created_at floor and
// fingerprint memory, so the relay-first record and the projection of the
// same state are one signed event, and every later event on the coordinate is
// newer. It delivers with PublishBeforeCommit (one round first, outbox only
// at the quorum) and does not open or honour the projector's backoff window.
//
// A record whose stable content the coordinate already carries is not signed
// again. That event reached the quorum or is held by the outbox, which keeps
// delivering it.
func (p *Projector) publishSignedRelayFirst(ctx context.Context, wireKind int, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID, publisher PreCommitPublisher) error {
	key := projectionKeyOf(wireKind, tags)
	fingerprint := projectionFingerprint(wireKind, tags, content)
	tombstone := isTombstoneTags(tags)
	// Retained state only sharpens the dedupe and the created_at floor. A
	// local-store outage must not block a mutation, so a failed load is
	// logged by hydrateProjectionCache and otherwise ignored here.
	_ = p.hydrateProjectionCache(ctx, wireKind)
	_, unlock := p.lockProjectionKey(key)
	defer unlock()

	if !tombstone && p.projectionUnchanged(key, fingerprint) {
		p.logger.Debug("relay-first record unchanged on its coordinate; not re-signed", zap.Int("kind", wireKind), zap.String("d", key.d))
		return nil
	}
	createdAt := p.nextProjectionCreatedAt(key)
	ev := gonostr.Event{Kind: gonostr.Kind(wireKind), CreatedAt: createdAt, Tags: tags, Content: content}
	if err := signEventWithPrivateKeyHex(&ev, p.privateKey); err != nil {
		return fmt.Errorf("sign relay-first record: %w", err)
	}
	if err := publisher.PublishBeforeCommit(ctx, ev, entityType, entityID); err != nil {
		return fmt.Errorf("publish relay-first record: %w", err)
	}
	p.rememberProjection(key, fingerprint, createdAt)
	p.logger.Debug("relay-first record published", zap.Int("kind", wireKind), zap.String("event_id", eventIDHex(&ev)))
	return nil
}

// ForgetAbandonedProjection is the outbox publisher's abandon hook (see
// Publisher.OnDeliveryAbandoned). When the abandoned event is still the latest
// one signed on its coordinate, its dedupe entry is dropped so the next
// trigger or repair re-signs the content instead of assuming it was
// delivered. The per-coordinate created_at floor is kept, so the replacement
// is still strictly newer. Events that are not this projector's, or that a
// newer publish has superseded, are ignored.
func (p *Projector) ForgetAbandonedProjection(ev gonostr.Event) {
	wireKind := int(ev.Kind)
	if p == nil {
		return
	}
	if wireKind == KindCASAudit {
		// The fact never reached the quorum: a republish must sign it again.
		p.releaseAuditFact(tagValue(ev.Tags, kinds.CPAuditTagFact))
		return
	}
	key := projectionKeyOf(wireKind, ev.Tags)
	fingerprint := projectionFingerprint(wireKind, ev.Tags, ev.Content)
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createdAt[key] == ev.CreatedAt && s.published[key] == fingerprint {
		delete(s.published, key)
	}
}

// publishAuthoritative signs one cp-state record for a domain that publishes
// its canonical state directly from the code that mutates it (Phase 3 S2).
// It shares publishSigned's per-coordinate lock, created_at floor and
// fingerprint memory so the record is fingerprint-deduped, and every later
// event on the coordinate is strictly newer. Unlike publishSigned it does
// not honour the projector's backoff window (same as publishSignedRelayFirst),
// because the mutation has already committed and the record must reach the
// outbox. It delivers with PublishProjection (the normal outbox path), not
// PublishBeforeCommit.
func (p *Projector) publishAuthoritative(ctx context.Context, wireKind int, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	key := projectionKeyOf(wireKind, tags)
	fingerprint := projectionFingerprint(wireKind, tags, content)
	tombstone := isTombstoneTags(tags)
	// Hydrate retained state; a local-store outage must not block a mutation.
	_ = p.hydrateProjectionCache(ctx, wireKind)
	_, unlock := p.lockProjectionKey(key)
	defer unlock()

	if !tombstone && p.projectionUnchanged(key, fingerprint) {
		return nil
	}
	createdAt := p.nextProjectionCreatedAt(key)
	_, err := p.publishSignedDirect(ctx, wireKind, createdAt, tags, content, entityType, entityID)
	if err != nil {
		return err
	}
	p.rememberProjection(key, fingerprint, createdAt)
	return nil
}

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
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// This file generalizes the emergency DNS-only fingerprint/backoff containment
// across EVERY projection the Projector signs. It is the P0 relay-storm fix on
// the projector side: unchanged replaceable coordinates emit no new event,
// burst triggers for the same coordinate coalesce into a single publish, and a
// synchronous relay rejection opens one bounded, jittered backoff shared by the
// whole projector path. Tombstones and real changes are never suppressed, and
// the append-only audit log is never deduplicated.

// ErrProjectorBackoff is returned when a publish is skipped because the shared
// projector backoff window is open after a relay rejection. Callers already
// log-and-continue; the periodic repair loop retries after the window closes.
var ErrProjectorBackoff = errors.New("projector publish suppressed: relay backoff window open")

const (
	projectionBackoffMin = 2 * time.Second
	projectionBackoffMax = time.Minute
	// projectionHydrateLimit bounds how many retained records are read per wire
	// kind when warming the dedupe cache after a restart.
	projectionHydrateLimit = 10000
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

// ProjectionFamilyMetrics are per-family publish counters. They are exposed
// for telemetry and tests; the umbrella restart gate reads the same numbers.
type ProjectionFamilyMetrics struct {
	Attempted int64 `json:"attempted"`
	Accepted  int64 `json:"accepted"`
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
	keyLocks  map[projectionKey]*sync.Mutex
	hydrated  map[int]bool
	metrics   map[string]*ProjectionFamilyMetrics

	backoffMu    sync.Mutex
	retryAfter   time.Time
	retryDelay   time.Duration
	now          func() time.Time
	jitterSource *rand.Rand
}

func (p *Projector) projection() *projectionState {
	p.projInitOnce.Do(func() {
		p.proj = &projectionState{
			published:    map[projectionKey]string{},
			keyLocks:     map[projectionKey]*sync.Mutex{},
			hydrated:     map[int]bool{},
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
	fmt.Fprintf(h, "%d\x00%s\x00", wireKind, strings.Join(tagLines, "\x1e"))
	h.Write([]byte(stableContent(content)))
	return hex.EncodeToString(h.Sum(nil))
}

// stableContent canonicalizes a JSON object by stripping volatile keys and
// re-marshalling (encoding/json emits map keys in sorted order). Non-object
// content is used verbatim.
func stableContent(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		return content
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return content
	}
	for key := range volatileContentKeys {
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

func (p *Projector) rememberProjection(key projectionKey, fingerprint string) {
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published[key] = fingerprint
}

// hydrateProjectionCache warms the dedupe cache for one wire kind from the
// retained event store so an unchanged coordinate is not re-signed merely
// because the process restarted. Newest record per coordinate wins; only this
// projector's own events are considered.
func (p *Projector) hydrateProjectionCache(ctx context.Context, wireKind int) {
	s := p.projection()
	s.mu.Lock()
	if s.hydrated[wireKind] || p.eventRepo == nil {
		s.hydrated[wireKind] = true
		s.mu.Unlock()
		return
	}
	s.hydrated[wireKind] = true
	s.mu.Unlock()

	records, err := p.eventRepo.ListByKind(ctx, wireKind, projectionHydrateLimit)
	if err != nil {
		p.logger.Warn("hydrate projection dedupe cache failed; treating kind as cold", zap.Int("kind", wireKind), zap.Error(err))
		return
	}
	servicePubkey := ""
	if p.privateKey != "" {
		if derived, deriveErr := publicKeyHexFromPrivateKeyHex(p.privateKey); deriveErr == nil {
			servicePubkey = derived
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	seen := map[projectionKey]struct{}{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		if servicePubkey != "" && record.PubKey != servicePubkey {
			continue
		}
		tags := recordTags(record)
		key := projectionKeyOf(record.Kind, tags)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		s.published[key] = projectionFingerprint(record.Kind, tags, record.Content)
	}
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
	s.retryAfter = p.projectionNow().Add(s.retryDelay + jitter)
}

func (p *Projector) resetProjectionBackoff() {
	s := p.projection()
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	s.retryAfter = time.Time{}
	s.retryDelay = 0
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
		p.hydrateProjectionCache(ctx, wireKind)
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
		return ErrProjectorBackoff
	}

	if err := p.publishSignedDirect(ctx, kind, tags, content, entityType, entityID); err != nil {
		s.count(family, func(m *ProjectionFamilyMetrics) { m.Rejected++ })
		p.noteProjectionRejection()
		return err
	}
	s.count(family, func(m *ProjectionFamilyMetrics) { m.Accepted++ })
	p.resetProjectionBackoff()
	if dedupable {
		p.rememberProjection(key, fingerprint)
	}
	return nil
}

package nostr

import (
	"context"
	"sort"

	"go.uber.org/zap"
)

// ReadinessWaiter gates the warm-start on the intent subscriber's first
// EOSE catch-up. In production, *controlplane.ReadinessTracker satisfies
// this interface. The channel-based Ready() method avoids polling: the
// warm-start selects on Ready() and ctx.Done().
type ReadinessWaiter interface {
	// Ready returns a channel that is closed when all preconditions are met.
	// If no preconditions exist the channel is already closed.
	Ready() <-chan struct{}
}

// WithReadinessTracker configures the projector to wait for the given waiter
// before running warm-start comparison for migrated domains.
func WithReadinessTracker(r ReadinessWaiter) ProjectorOption {
	return func(p *Projector) { p.readiness = r }
}

// WithIntentDomains sets the domain families migrated to the intent pipeline.
// Migrated domains are warm-started from the daemon's own history instead of
// warm-started from the daemon's own history. The domain strings must
// match the cpStateFamilies domain names (e.g. "service", "environment").
func WithIntentDomains(domains []string) ProjectorOption {
	return func(p *Projector) { p.intentDomains = domains }
}

// warmStartMigratedDomains runs warm-start comparison for
// domains listed in intentDomains. It:
//  1. Waits for the intent subscriber's first catch-up (EOSE + NIP-77) via
//     the ReadinessWaiter channel — no polling, no timeout-as-completion.
//  2. Hydrates the fingerprint cache from the daemon's own published history
//     so that unchanged coordinates are recognised and not re-signed.
//  3. Compares history records against the cache: any record present in history
//     but absent from the cache (e.g. an abandoned outbox publish) is
//     re-published. Records already in the cache are skipped.
//
// If ctx is cancelled before readiness, warm-start is skipped entirely and
// a warning is logged — it never proceeds on a guess.
//
// all domains are now included in intentDomains, so
// every family is warm-started and no legacy snapshot path remains.
//
// See docs/architecture/intents-and-authority.md.
func (p *Projector) warmStartMigratedDomains(ctx context.Context) {
	if p.readiness == nil || len(p.intentDomains) == 0 {
		return
	}

	// Wait for readiness or context cancellation — event-driven, no polling.
	select {
	case <-p.readiness.Ready():
		p.logger.Info("warm-start: intent subscriber caught up")
	case <-ctx.Done():
		p.logger.Warn("warm-start: context cancelled before readiness, skipping")
		return
	}

	// Hydrate the fingerprint cache for the canonical state wire kind (30900).
	// This loads published records from the daemon's own history so that
	// unchanged coordinates are recognised and not re-signed. Failed (abandoned)
	// records are excluded from the cache, making them visible as stale.
	wireKind := KindCASControlState
	if err := p.hydrateProjectionCache(ctx, wireKind); err != nil {
		p.logger.Warn("warm-start: fingerprint cache hydration failed",
			zap.Int("wire_kind", wireKind), zap.Error(err))
	}

	if p.history == nil {
		p.logger.Info("warm-start: no history available, skipping comparison",
			zap.Strings("domains", p.intentDomains))
		return
	}

	servicePubkey := ""
	if p.privateKey != "" {
		if derived, err := publicKeyHexFromPrivateKeyHex(p.privateKey); err == nil {
			servicePubkey = derived
		}
	}

	totalPublished := 0
	for _, domain := range p.intentDomains {
		n := p.warmStartDomain(ctx, domain, wireKind, servicePubkey)
		totalPublished += n
	}
	p.logger.Info("warm-start completed",
		zap.Strings("domains", p.intentDomains),
		zap.Int("total_published", totalPublished))
}

// warmStartDomain compares the daemon's own records for one domain against
// the fingerprint cache and re-publishes any that are stale or missing on the
// relay. Returns the number of records published.
func (p *Projector) warmStartDomain(ctx context.Context, domain string, wireKind int, servicePubkey string) int {
	records, err := p.history.FindByTag(ctx, "domain", domain, []int{wireKind}, projectionHydrateLimit)
	if err != nil {
		p.logger.Warn("warm-start: failed to query history",
			zap.String("domain", domain), zap.Error(err))
		return 0
	}

	// Newest record per coordinate wins. Sort newest-first so the first
	// occurrence of each projectionKey is the authoritative version.
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})

	published := 0
	seen := make(map[projectionKey]struct{})
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

		fingerprint := projectionFingerprint(record.Kind, tags, record.Content)
		if p.projectionUnchanged(key, fingerprint) {
			continue // Cache matches — relay already has this version.
		}

		// Record is in the daemon's history but the fingerprint cache does not
		// recognise it (the publish was abandoned, or the cache is cold for this
		// coordinate). Re-sign and publish so the relay is up to date.
		if err := p.publishSigned(ctx, record.Kind, tags, record.Content, domain+".warm-start", nil); err != nil {
			p.logger.Warn("warm-start: re-publish stale record failed",
				zap.String("domain", domain),
				zap.String("d", key.d),
				zap.Error(err))
		} else {
			published++
		}
	}

	p.logger.Info("warm-start domain complete",
		zap.String("domain", domain),
		zap.Int("records", len(records)),
		zap.Int("published", published))
	return published
}

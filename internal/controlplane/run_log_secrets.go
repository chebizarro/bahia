package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// ErrRunLogsUnredactable: a run's logs may contain a secret value redaction
// cannot resolve, so they are withheld.
var ErrRunLogsUnredactable = errors.New("run logs cannot be safely redacted")

// ErrRunLogRedactionRateLimited: the requester resolved run-log secrets too
// often; cached runs stay readable.
var ErrRunLogRedactionRateLimited = errors.New("run log redaction is rate limited for this requester; retry shortly")

const (
	// A requester may cause runLogResolveBurst secret-store batches at
	// once and runLogResolvesPerMinute per minute; reads served from the
	// cache are free. Each batch is paced on the shared bulk lane.
	runLogResolvesPerMinute = 6
	runLogResolveBurst      = 3
	runLogLimiterCap        = 1024
	runLogCacheCap          = 32
)

// directRuntimeRunJobID marks a run the daemon applied itself
// (workflow.Coordinator): it injects the service's secrets.
const directRuntimeRunJobID = "runtime:direct"

// runLogText is the text redaction rewrites.
type runLogText interface {
	redact(values []string)
}

// runLogsText redacts both streams of stored run logs.
type runLogsText struct{ logs *adapterruntime.RunLogs }

func (t runLogsText) redact(values []string) {
	t.logs.Stdout = redactValues(t.logs.Stdout, values)
	t.logs.Stderr = redactValues(t.logs.Stderr, values)
}

// redactValues replaces every value, raw and JSON-escaped, longest first so a
// value that is a substring of another leaves nothing behind.
func redactValues(text string, values []string) string {
	sorted := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			sorted = append(sorted, value)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, value := range sorted {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
		if encoded, err := json.Marshal(value); err == nil && len(encoded) >= 2 {
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		}
	}
	return text
}

// runLogSecretCache holds, per run, the values redaction resolved, until the
// earliest store lease of the batch ends or the entry is evicted; either
// way the values are zeroized. A reader of the same run within the lease
// costs no store round trip.
type runLogSecretCache struct {
	mu      sync.Mutex
	entries map[string]*runLogCacheEntry
	order   []string
}

type runLogCacheEntry struct {
	resolved []secretstore.Resolved
	expires  time.Time
	timer    *time.Timer
}

func newRunLogSecretCache() *runLogSecretCache {
	return &runLogSecretCache{entries: map[string]*runLogCacheEntry{}}
}

func runLogCacheKey(runID uuid.UUID, accesses []domain.RunSecretAccess) string {
	var b strings.Builder
	b.WriteString(runID.String())
	for _, access := range accesses {
		fmt.Fprintf(&b, "|%s@%d", access.SecretID, access.RecordVersion)
	}
	return b.String()
}

// values returns copies of the cached values, or nil.
func (c *runLogSecretCache) values(key string, now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return nil
	}
	if !now.Before(entry.expires) {
		c.evictLocked(key)
		return nil
	}
	out := make([]string, 0, len(entry.resolved))
	for _, r := range entry.resolved {
		out = append(out, string(r.Value.Bytes()))
	}
	return out
}

// put takes ownership of resolved; it zeroizes them now when they cannot
// be cached.
func (c *runLogSecretCache) put(key string, resolved []secretstore.Resolved, now time.Time) {
	expires := time.Time{}
	for _, r := range resolved {
		if lease := r.Value.LeaseExpiresAt(); expires.IsZero() || lease.Before(expires) {
			expires = lease
		}
	}
	if !now.Before(expires) {
		secretstore.ZeroizeResolved(resolved)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(key)
	for len(c.order) >= runLogCacheCap {
		c.evictLocked(c.order[0])
	}
	entry := &runLogCacheEntry{resolved: resolved, expires: expires}
	entry.timer = time.AfterFunc(expires.Sub(now), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.entries[key] == entry {
			c.evictLocked(key)
		}
	})
	c.entries[key] = entry
	c.order = append(c.order, key)
}

func (c *runLogSecretCache) evictLocked(key string) {
	entry := c.entries[key]
	if entry == nil {
		return
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	secretstore.ZeroizeResolved(entry.resolved)
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// runLogResolveLimiter bounds the store batches each requester causes.
type runLogResolveLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func (l *runLogResolveLimiter) allow(requester string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limiters == nil || len(l.limiters) >= runLogLimiterCap {
		l.limiters = map[string]*rate.Limiter{}
	}
	limiter := l.limiters[requester]
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Limit(float64(runLogResolvesPerMinute)/60), runLogResolveBurst)
		l.limiters[requester] = limiter
	}
	return limiter.AllowN(now, 1)
}

// redactRunLogSecrets redacts the values of exactly the secrets the run
// injected (domain.RunSecretAccesses, written before each apply). It
// withholds the logs, never returning them unredacted, when it cannot know
// every such value:
//   - the run predates the record and the deployment references secrets (or
//     the run applied directly, which injects every effective secret);
//   - a secret has no canonical record, is a legacy value, or its record
//     revision differs from the one the run injected (it was re-pointed:
//     the store no longer releases the value the run used);
//   - the store cannot resolve it now.
func (h *EncryptedRouteHandlers) redactRunLogSecrets(ctx context.Context, requester string, run *domain.DeploymentRun, intent *domain.DeploymentIntent, logs runLogText) error {
	if run == nil {
		return fmt.Errorf("%w: unknown run", ErrRunLogsUnredactable)
	}
	accesses, recorded := domain.RunSecretAccesses(run.ApplyMetadata)
	if !recorded {
		referencesSecrets := intent != nil && intent.DesiredState != nil && len(intent.DesiredState.SecretRefs) > 0
		if referencesSecrets || strings.TrimSpace(run.LoomJobID) == directRuntimeRunJobID {
			return fmt.Errorf("%w: the run predates the record of the secrets it injected", ErrRunLogsUnredactable)
		}
		return nil
	}
	if len(accesses) == 0 {
		return nil
	}
	if h.secretRecords == nil || h.secretValues == nil {
		return fmt.Errorf("secret redaction dependencies are not configured")
	}
	ids := make([]uuid.UUID, 0, len(accesses))
	for _, access := range accesses {
		if access.SecretID == uuid.Nil {
			return fmt.Errorf("%w: the run's secret record is malformed", ErrRunLogsUnredactable)
		}
		current, err := h.secretRecords.CurrentSecretRef(ctx, access.SecretID)
		if err != nil {
			return fmt.Errorf("load referenced secret %s: %w", access.SecretID, err)
		}
		if current == nil {
			return fmt.Errorf("referenced secret %s: %w", access.SecretID, secretstore.ErrNoRecord)
		}
		if current.CredentialRef == nil {
			return domain.LegacySecretValueError(current.ID, current.Name)
		}
		if current.Version != access.RecordVersion {
			return fmt.Errorf("%w: secret %s (%s) was re-pointed after this run injected it; the store no longer releases the value the run used", ErrRunLogsUnredactable, current.ID, current.Name)
		}
		ids = append(ids, access.SecretID)
	}
	now := time.Now()
	key := runLogCacheKey(run.ID, accesses)
	values := h.runLogCache.values(key, now)
	if values == nil {
		if !h.runLogLimiter.allow(requester, now) {
			return ErrRunLogRedactionRateLimited
		}
		resolved, err := h.secretValues.ResolveAll(ctx, ids)
		if err != nil {
			return err
		}
		for i, r := range resolved {
			// Re-pointed between the check above and the resolve.
			if r.Secret.ID != accesses[i].SecretID || r.Secret.Version != accesses[i].RecordVersion {
				secretstore.ZeroizeResolved(resolved)
				return fmt.Errorf("%w: secret %s was re-pointed while the logs were prepared", ErrRunLogsUnredactable, accesses[i].SecretID)
			}
		}
		h.auditRunLogResolve(ctx, requester, run.ID, resolved)
		for _, r := range resolved {
			values = append(values, string(r.Value.Bytes()))
		}
		h.runLogCache.put(key, resolved, now)
	}
	logs.redact(values)
	return nil
}

// auditRunLogResolve records each resolve; an audit row that cannot be
// written (PostgreSQL absent or unreachable) is logged, never fatal.
func (h *EncryptedRouteHandlers) auditRunLogResolve(ctx context.Context, requester string, runID uuid.UUID, resolved []secretstore.Resolved) {
	if h.secretAudit == nil {
		return
	}
	now := time.Now().UTC()
	for _, r := range resolved {
		if err := h.secretAudit.RecordSecretAccessAudit(ctx, &domain.SecretAccessAudit{
			SecretID: r.Secret.ID, Version: r.Secret.CredentialRef.Version, ServiceID: r.Secret.ServiceID,
			EnvironmentID: r.Secret.EnvironmentID, Operation: domain.SecretAccessOperationResolve,
			Outcome: domain.SecretAccessOutcomeSuccess, Actor: requester, Reason: "deployment run log redaction",
			RequestID: runID.String(), AccessedAt: now,
		}); err != nil {
			h.logger.Warn("secret access audit not recorded", zap.String("secret_id", r.Secret.ID.String()), zap.Error(err))
		}
	}
}

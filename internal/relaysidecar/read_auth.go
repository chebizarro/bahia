package relaysidecar

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

// readAuthPolicy implements NIP-42 read-side authentication for the sidecar
// relay (C-21). Non-public kinds require the requester to have authenticated
// via NIP-42 and be in the allowed reader set. Public kinds are always
// readable without authentication.
//
// Allowed readers:
//   - admin allowlist pubkeys (NIP-86)
//   - intent authors (TrustSet-derived, from setintentauthors)
//   - the daemon's service pubkey
//   - configured ReadAuthAllowedPubkeys (fleet operators)
//
// The mode controls behaviour:
//   - "enforce": CLOSED auth-required for unauthenticated protected-kind REQs
//   - "warn":    log but allow (migration aid for existing deployments)
//   - "off":     no read-side auth (pre-C-21 behaviour)
type readAuthPolicy struct {
	mode          string // enforce | warn | off
	servicePubkey string
	admission     *policy // shares intent authors and admin policy
	logger        *zap.Logger

	// extraReaders are additional pubkeys allowed to read protected kinds,
	// beyond the admin and intent-author sets. Populated from config at init.
	extraReadersMu sync.RWMutex
	extraReaders   map[string]bool
}

func newReadAuthPolicy(cfg config.RelaySidecarConfig, admission *policy, logger *zap.Logger) *readAuthPolicy {
	extra := make(map[string]bool, len(cfg.ReadAuthAllowedPubkeys))
	for _, pk := range cfg.ReadAuthAllowedPubkeys {
		pk = strings.ToLower(strings.TrimSpace(pk))
		if pk != "" {
			extra[pk] = true
		}
	}
	return &readAuthPolicy{
		mode:          cfg.NormalizedReadAuthMode(),
		servicePubkey: admission.servicePubkey,
		admission:     admission,
		logger:        logger,
		extraReaders:  extra,
	}
}

// publicKinds are kinds that remain readable without authentication.
// These are standard Nostr discovery/profile kinds and open interop kinds.
var publicKinds = func() []nostr.Kind {
	return []nostr.Kind{
		nostr.KindProfileMetadata,        // 0: profiles
		nostr.KindFollowList,             // 3: follow lists
		nostr.KindDeletion,               // 5: deletion requests
		nostr.KindRelayListMetadata,      // 10002: NIP-65 relay lists
		nostr.KindRelayDiscovery,         // 30166: relay discovery
		nostr.KindRepositoryAnnouncement, // 30617: NIP-34 git repos
		nostr.KindRepositoryState,        // 30618: NIP-34 git state
		nostr.KindClientAuthentication,   // 22242: NIP-42 auth events
	}
}()

// publicKindRanges are ranges of kinds that are always public.
// NIP-34 collaboration kinds (1617-1633) are open interop.
var publicKindRanges = [][2]nostr.Kind{
	{1617, 1633}, // NIP-34: patches, PRs, issues, status
}

// isPublicKind reports whether the kind is always readable without auth.
func isPublicKind(kind nostr.Kind) bool {
	if slices.Contains(publicKinds, kind) {
		return true
	}
	for _, r := range publicKindRanges {
		if kind >= r[0] && kind <= r[1] {
			return true
		}
	}
	return false
}

// filterNeedsAuth reports whether a filter targets any non-public kind.
// A filter with no kinds specified is treated as potentially targeting
// protected kinds and requires auth. A filter that exclusively targets
// public kinds does not.
func filterNeedsAuth(filter nostr.Filter) bool {
	if len(filter.Kinds) == 0 {
		// No kind filter means "all kinds" which includes protected ones.
		return true
	}
	for _, kind := range filter.Kinds {
		if !isPublicKind(kind) {
			return true
		}
	}
	return false
}

// checkReadAuth enforces NIP-42 read-side auth for a REQ filter.
// Returns (reject, reason) like khatru's OnRequest hook.
func (r *readAuthPolicy) checkReadAuth(ctx context.Context, filter nostr.Filter) (bool, string) {
	if r.mode == config.ReadAuthModeOff {
		return false, ""
	}
	if !filterNeedsAuth(filter) {
		return false, ""
	}

	authedPubkey, isAuthed := khatru.GetAuthed(ctx)
	if isAuthed && r.isAllowedReader(authedPubkey.Hex()) {
		return false, ""
	}

	reason := "auth-required: this relay requires NIP-42 authentication to read fleet state"
	if isAuthed {
		// Authenticated but not in the allowed set.
		reason = fmt.Sprintf("restricted: pubkey %s is not authorized to read protected kinds", authedPubkey.Hex()[:16])
	}

	if r.mode == config.ReadAuthModeWarn {
		r.logger.Warn("read auth would reject REQ (warn mode)",
			zap.Bool("authenticated", isAuthed),
			zap.String("reason", reason))
		return false, ""
	}

	// enforce mode: request NIP-42 auth if the connection has a WebSocket.
	if !isAuthed && khatru.GetConnection(ctx) != nil {
		khatru.RequestAuth(ctx)
	}
	return true, reason
}

// isAllowedReader reports whether pubkey is permitted to read protected kinds.
// Unlike the write-side admin.admits() which is open when the allowlist is
// empty, read auth requires the pubkey to be explicitly listed in one of the
// allowed sets.
func (r *readAuthPolicy) isAllowedReader(pubkey string) bool {
	// Service pubkey is always allowed.
	if r.servicePubkey != "" && pubkey == r.servicePubkey {
		return true
	}
	// Admin/operator: administrators and explicitly allowed pubkeys.
	if r.admission.admin != nil && r.isAdminOrAllowed(pubkey) {
		return true
	}
	// Intent authors (TrustSet-derived).
	if r.admission.admitsIntentAuthor(pubkey) {
		return true
	}
	// Extra configured readers (fleet operators).
	r.extraReadersMu.RLock()
	defer r.extraReadersMu.RUnlock()
	return r.extraReaders[pubkey]
}

// isAdminOrAllowed checks if the pubkey is an administrator or in the explicit
// allowed pubkey list. Unlike admin.admits(), this does NOT open to all when
// the allowlist is empty — read auth is deny-by-default.
func (r *readAuthPolicy) isAdminOrAllowed(pubkey string) bool {
	r.admission.admin.mu.RLock()
	defer r.admission.admin.mu.RUnlock()
	for _, admin := range r.admission.admin.state.Administrators {
		if admin == pubkey {
			return true
		}
	}
	for _, entry := range r.admission.admin.state.AllowedPubkeys {
		if entry.Pubkey == pubkey {
			return true
		}
	}
	return false
}

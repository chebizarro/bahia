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
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// readAuthPolicy implements NIP-42 read-side authentication for the sidecar
// relay (C-21). Non-public kinds — and protected cp-state topics within kind
// 30900 — require the requester to have authenticated via NIP-42 and be in the
// allowed reader set.
//
// Kind 30900 (CASControlState) is shared by many families. The policy classifies
// 30900 reads by the single-letter "t" topic tag in the filter, not by kind
// alone. Filters that include a public #t topic are served anonymously; those
// targeting protected topics require auth. A 30900 filter with no #t is treated
// as protected (it could return any family).
//
// Allowed readers:
//   - admin allowlist pubkeys (NIP-86)
//   - intent authors (TrustSet-derived, from setintentauthors)
//   - the daemon's service pubkey
//   - configured ReadAuthAllowedPubkeys (fleet operators)
//
// The mode controls behaviour:
//   - "enforce": CLOSED auth-required for unauthenticated protected-kind REQs
//   - "warn":    log but allow (migration aid; default for this release)
//   - "off":     no read-side auth (pre-C-21 behaviour)
//
// The default is "warn". Set read_auth_mode to "enforce" only after verifying
// all readers (web, CLI, DNS agent, FIPS bridge, workers) authenticate or read
// only public topics. See the per-topic classification in publicCPStateTopics.
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

// publicKinds are non-30900 kinds that remain readable without authentication.
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

// publicCPStateTopics are cp-state 30900 topics readable without NIP-42 auth.
//
// Classification rationale — a topic is public when:
//   - an anonymous reader needs it (FIPS bridge, CLI NostrClient, web bootstrap), OR
//   - the content is always encrypted (OCK), so relay-level read auth is redundant, OR
//   - it is a supply-chain attestation consumed by external verifiers.
//
// A topic is protected when:
//   - it contains sensitive operational detail (security findings, secrets), OR
//   - it is operator-only config (relay-settings, config-status), OR
//   - it contains private conversation content (assistant transcripts).
//
// Per-topic decisions:
//
//	dns-endpoint, dns-zone, dns-policy, dns-backend — PUBLIC:
//	  FIPS bridge reads anonymously; pkg/discovery WithPrivateKey is optional;
//	  web pre-login bootstrap reads these for the DNS dashboard.
//
//	service-state, service-registry, environment-registry — PUBLIC:
//	  CLI NostrClient (pkg/client) reads anonymously (no WithPrivateKey in its pool);
//	  web pre-login bootstrap reads these for the services dashboard.
//
//	artifact-registry, build-registry, deployment-intent, deployment-run,
//	policy-registry, package-repository, package-artifact, package-promotion — PUBLIC:
//	  Web pre-login bootstrap reads all of these for fleet dashboards.
//
//	worker-state, worker-assignment, worker-drain, worker-eligibility,
//	worker-cleanup — PUBLIC:
//	  Web pre-login bootstrap reads worker state; loom worker adverts are open interop.
//
//	sbom-reference, sbom-availability — PUBLIC:
//	  Supply-chain attestations consumed by the security scanner and external verifiers.
//	  Kinds 30078/30004 are also public for the same reason.
//
//	security-scan-status, security-summary — PUBLIC:
//	  Observable security posture, no detailed vulnerability data.
//
//	assistant-status (30315) — PUBLIC:
//	  Worker health/adverts; web pre-login reads these.
//
//	continuity-heartbeat — PUBLIC:
//	  Monitoring observable, web pre-login bootstrap.
//
//	org-registry, org-member, org-invite, org-key-envelope — PUBLIC:
//	  Content is OCK-encrypted; the ciphertext envelope is not sensitive.
//	  Relay-level read auth is redundant for encrypted content.
//
//	secret-registry, notification-channel — PUBLIC:
//	  Content is OCK-encrypted; same rationale as org families.
//
//	llm-route, llm-state — PUBLIC:
//	  Web pre-login bootstrap reads LLM routing state.
//
//	backup-*, ml-* — PUBLIC:
//	  Web pre-login bootstrap reads all cp-state topics via controlplaneStateTopics().
//
//	security-findings, security-audit — PROTECTED:
//	  Detailed vulnerability data and audit logs; sensitive.
//
//	assistant-transcript — PROTECTED:
//	  Private conversation content.
//
//	relay-settings — PROTECTED:
//	  Operator relay policy, admin-only.
//
//	config-status — PROTECTED:
//	  Config-fabric operator state, admin-only.
//
//	assistant-session — PROTECTED:
//	  Session recovery data, private.
var publicCPStateTopics = map[string]bool{
	// DNS — anonymous readers (FIPS bridge, pkg/discovery).
	kinds.DNSZoneTopic:     true,
	kinds.DNSEndpointTopic: true,
	kinds.DNSPolicyTopic:   true,
	kinds.DNSBackendTopic:  true,

	// Core fleet state — CLI NostrClient, web pre-login bootstrap.
	kinds.CPStateTopicServiceState:        true,
	kinds.CPStateTopicServiceRegistry:     true,
	kinds.CPStateTopicEnvironmentRegistry: true,
	kinds.CPStateTopicLLMRoute:            true,
	kinds.CPStateTopicLLMState:            true,
	kinds.CPStateTopicArtifactRegistry:    true,
	kinds.CPStateTopicDeploymentIntent:    true,
	kinds.CPStateTopicDeploymentRun:       true,
	kinds.CPStateTopicBuildRegistry:       true,
	kinds.CPStateTopicPolicyRegistry:      true,
	kinds.CPStateTopicPackageRepository:   true,
	kinds.CPStateTopicPackageArtifact:     true,
	kinds.CPStateTopicPackagePromotion:    true,

	// Workers — web pre-login bootstrap, loom open interop.
	kinds.WorkerStateTopic:       true,
	kinds.WorkerAssignmentTopic:  true,
	kinds.WorkerDrainTopic:       true,
	kinds.WorkerEligibilityTopic: true,
	kinds.WorkerCleanupTopic:     true,

	// SBOM — supply-chain attestations, external verifiers.
	kinds.SBOMReferenceTopic:    true,
	kinds.SBOMAvailabilityTopic: true,

	// Security observable — posture summary, no detailed findings.
	kinds.SecurityScanStatusTopic: true,
	kinds.SecuritySummaryTopic:    true,

	// Monitoring — assistant health, continuity heartbeat.
	kinds.AssistantStatusTopic:     true,
	kinds.ContinuityHeartbeatTopic: true,

	// ML pipeline — web pre-login bootstrap.
	kinds.CPStateTopicMLModel:             true,
	kinds.CPStateTopicMLModelVersion:      true,
	kinds.CPStateTopicMLDataset:           true,
	kinds.CPStateTopicMLRecipe:            true,
	kinds.CPStateTopicMLRecipeRun:         true,
	kinds.CPStateTopicMLEndpoint:          true,
	kinds.CPStateTopicMLEndpointState:     true,
	kinds.CPStateTopicMLEvaluation:        true,
	kinds.CPStateTopicMLProvenance:        true,
	kinds.CPStateTopicMLRuntimeCapability: true,

	// Backup — web pre-login bootstrap.
	kinds.CPStateTopicBackupDefinition:         true,
	kinds.CPStateTopicBackupPolicy:             true,
	kinds.CPStateTopicBackupRepository:         true,
	kinds.CPStateTopicBackupRetention:          true,
	kinds.CPStateTopicBackupRecipe:             true,
	kinds.CPStateTopicBackupRun:                true,
	kinds.CPStateTopicBackupVerification:       true,
	kinds.CPStateTopicBackupRestore:            true,
	kinds.CPStateTopicBackupRuntimeObservation: true,

	// OCK-encrypted families — content is ciphertext; read auth is redundant.
	kinds.CPStateTopicOrgRegistry:                 true,
	kinds.CPStateTopicOrgMemberRegistry:           true,
	kinds.CPStateTopicOrgInviteRegistry:           true,
	kinds.CPStateTopicOrgKeyEnvelope:              true,
	kinds.CPStateTopicSecretRegistry:              true,
	kinds.CPStateTopicNotificationChannelRegistry: true,

	// B2 families (bahia-irsry.60): payment records and security findings/
	// schedules/finding-details are OCK-encrypted (fleet scope); ciphertext only.
	kinds.CPStateTopicPaymentRecord:         true,
	kinds.CPStateTopicSecurityFinding:       true,
	kinds.CPStateTopicSecuritySchedule:      true,
	kinds.CPStateTopicSecurityFindingDetail: true,
	// Runtime observables are sanitized before publication.
	kinds.CPStateTopicManagedInstanceHealth: true,
	kinds.CPStateTopicRouteCanary:           true,
	// Blossom records are OCK ciphertext; relay-level auth is redundant.
	kinds.CPStateTopicBlossomAdmin: true,
	kinds.CPStateTopicBlossomBlob:  true,

	// Protected topics (NOT in this map):
	//   security-findings, security-audit — detailed vulnerability data
	//   assistant-transcript — private conversation content
	//   assistant-session — session recovery data
	//   relay-settings — operator relay policy
	//   config-status — config-fabric state (admin-only)
}

// isPublicKind reports whether the kind is always readable without auth.
// Kind 30900 (CASControlState) is NOT public by kind alone; it is classified
// by the #t topic tags in the filter (see filterNeedsAuth).
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

// filterNeedsAuth reports whether a filter targets any non-public kind or
// any protected cp-state topic.
//
// Decision for mixed public/protected in a single filter: if any topic in the
// filter is protected (or no #t is specified for a 30900 filter), the entire
// filter requires auth. This is the safest stance — a filter that might return
// a protected record must be gated. Callers that want anonymous access to
// public 30900 families should use separate filters scoped by #t.
func filterNeedsAuth(filter nostr.Filter) bool {
	if len(filter.Kinds) == 0 {
		// No kind filter means "all kinds" which includes protected ones.
		return true
	}
	for _, kind := range filter.Kinds {
		if kind == nostr.Kind(kinds.CASControlState) {
			// Kind 30900: classify by #t topic tags, not by kind alone.
			topics := filter.Tags["t"]
			if len(topics) == 0 {
				// No topic scoping — could return any family including protected ones.
				return true
			}
			for _, topic := range topics {
				if !publicCPStateTopics[topic] {
					return true
				}
			}
			continue
		}
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

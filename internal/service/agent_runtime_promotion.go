package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

var (
	runtimePromotionCommit   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	runtimePromotionDigest   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	runtimePromotionPubkey   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	promotionIntentNamespace = uuid.MustParse("d8109c30-a1e8-5df4-88fa-5e5a36a0a1f8")
)

// RuntimePromotionSubscription is one Soul's declared expectation for a shared
// runtime release. A verified release may promote to the Soul only when the
// repository, branch, publisher, full commit, image digest, release channel,
// and CI gates all match this declaration. It is a promotion policy, not a
// second runtime-release model: the shared release itself is owned by
// domain.AgentRuntimeRelease.
type RuntimePromotionSubscription struct {
	AgentID        string
	ServiceID      uuid.UUID
	EnvironmentID  uuid.UUID
	ReleaseChannel string
	Repository     string
	Branch         string
	Publisher      string
	Commit         string
	Digest         string
	// Protected marks production targets whose promotion needs a narrowly
	// scoped approval; only the image digest changes automatically.
	Protected bool
}

// PromotionGateDecision is the all-or-nothing gate result for one subscription.
type PromotionGateDecision struct {
	Matched bool
	Reasons []string
}

// RuntimePromotionSubscriptionSource resolves the Souls subscribed to a shared
// runtime release channel for one tenant.
type RuntimePromotionSubscriptionSource interface {
	ListRuntimeReleaseSubscriptions(ctx context.Context, orgID uuid.UUID, channel string) ([]RuntimePromotionSubscription, error)
}

// PromotionIntentSink durably records a fully constructed, gate-matched
// deployment intent. Live kind-25910 publication remains a separately gated
// operator action and is not performed by promotion ingestion.
type PromotionIntentSink interface {
	SubmitPromotionIntent(ctx context.Context, intent *domain.DeploymentIntent) error
}

// RepositoryStateTrigger is the replay-safe result of a NIP-34 kind-30618
// repository state event. It is a secondary selection trigger only: it selects
// the (repository, commit) that a promotion evaluation may target, and never
// creates a deployment intent by itself.
type RepositoryStateTrigger struct {
	RepoAddress string
	Commit      string
	Fingerprint string
	Replay      bool
}

// RuntimePromotionRequest carries the shared release plus optional secondary
// trigger. A trigger that conflicts with the release's repository or commit is
// rejected so kind-30618 can never widen promotion authority.
type RuntimePromotionRequest struct {
	Source  *domain.AgentRuntimeSource
	Release domain.HiveCIAcceptedRelease
	Trigger *RepositoryStateTrigger
}

// RuntimePromotionOutcome reports one subscription's promotion decision.
type RuntimePromotionOutcome struct {
	Subscription RuntimePromotionSubscription
	Matched      bool
	Reasons      []string
	ReleaseID    uuid.UUID
	Intent       *domain.DeploymentIntent
}

// RuntimePromotionReport is the full guard-evaluated fan-out for one release.
type RuntimePromotionReport struct {
	Release  domain.AgentRuntimeRelease
	Promoted []RuntimePromotionOutcome
	Skipped  []RuntimePromotionOutcome
}

// AgentRuntimePromotionService registers ONE shared verified runtime release and
// fans it out to subscribed Souls only when every promotion gate matches. It
// composes the merged AgentRuntimeReleaseService (RegisterSource /
// RegisterVerifiedRelease / BindRelease) instead of introducing a new release
// model.
type AgentRuntimePromotionService struct {
	releases      *AgentRuntimeReleaseService
	subscriptions RuntimePromotionSubscriptionSource
	intents       PromotionIntentSink
	services      repository.ServiceRepository
	now           func() time.Time

	seenMu   sync.Mutex
	seenKeys map[string]struct{}
}

func NewAgentRuntimePromotionService(
	releases *AgentRuntimeReleaseService,
	subscriptions RuntimePromotionSubscriptionSource,
	intents PromotionIntentSink,
) *AgentRuntimePromotionService {
	return &AgentRuntimePromotionService{
		releases:      releases,
		subscriptions: subscriptions,
		intents:       intents,
		now:           func() time.Time { return time.Now().UTC() },
		seenKeys:      map[string]struct{}{},
	}
}

// RegisterSharedRuntimeRelease registers the runtime source and the one shared
// verified release for an accepted Hive-CI RELEASE result. Provenance is mapped
// from the signed release; the deterministic VerifiedAt makes replay idempotent.
func (s *AgentRuntimePromotionService) RegisterSharedRuntimeRelease(
	ctx context.Context,
	source *domain.AgentRuntimeSource,
	accepted domain.HiveCIAcceptedRelease,
) (*domain.AgentRuntimeRelease, error) {
	if s == nil || s.releases == nil {
		return nil, fmt.Errorf("agent runtime promotion service is not configured")
	}
	if source == nil || source.OrgID == uuid.Nil {
		return nil, fmt.Errorf("shared runtime release requires a tenant-scoped runtime source")
	}
	if err := validateAcceptedReleaseForPromotion(accepted); err != nil {
		return nil, err
	}
	source.Repository = strings.TrimSpace(source.Repository)
	source.Branch = strings.TrimSpace(source.Branch)
	source.ReleaseChannel = strings.TrimSpace(source.ReleaseChannel)
	if source.Repository == "" || source.Branch == "" || source.ReleaseChannel == "" {
		return nil, fmt.Errorf("runtime source requires repository, branch, and release channel")
	}
	if err := s.releases.RegisterSource(ctx, source); err != nil {
		return nil, fmt.Errorf("register runtime source: %w", err)
	}
	release := &domain.AgentRuntimeRelease{
		OrgID:       source.OrgID,
		SourceID:    source.ID,
		ImageRepo:   accepted.Result.Manifest.Repository,
		ImageDigest: accepted.Result.Manifest.Digest,
		VerifiedAt:  accepted.AcceptedAt.UTC(),
		Provenance: domain.RuntimeReleaseProvenance{
			Provider:           "hiveci-release",
			ReleaseEventID:     accepted.ResultEventID,
			WorkflowRunEventID: accepted.Result.Lineage.WorkflowRunEventID,
			ManifestDigest:     accepted.Result.Manifest.Digest,
			SBOMDigest:         accepted.Result.SBOM.Digest,
			ProvenanceDigest:   accepted.Result.Provenance.Digest,
			AttestorPubkey:     accepted.Attestor,
		},
	}
	if release.VerifiedAt.IsZero() {
		return nil, fmt.Errorf("accepted release is missing its durable acceptance timestamp")
	}
	if err := s.releases.RegisterVerifiedRelease(ctx, release); err != nil {
		return nil, fmt.Errorf("register shared runtime release: %w", err)
	}
	return release, nil
}

// EvaluatePromotionGate applies the repository/branch/publisher/commit/digest
// guards plus the CI gates. Every guard must match for a promotion.
func (s *AgentRuntimePromotionService) EvaluatePromotionGate(
	accepted domain.HiveCIAcceptedRelease,
	source domain.AgentRuntimeSource,
	sub RuntimePromotionSubscription,
) PromotionGateDecision {
	var reasons []string
	lineage := accepted.Result.Lineage

	if strings.TrimSpace(source.ReleaseChannel) != strings.TrimSpace(sub.ReleaseChannel) {
		reasons = append(reasons, "release channel does not match the subscription")
	}
	if !sameRepoCoordinate(lineage.RepoAddress, sub.Repository) {
		reasons = append(reasons, "repository does not match the subscription")
	}
	if strings.TrimSpace(accepted.Branch) != strings.TrimSpace(sub.Branch) {
		reasons = append(reasons, "branch does not match the subscription")
	}
	publisher, err := signedRunPublisher(accepted)
	if err != nil {
		reasons = append(reasons, "signed 5401 publisher is unavailable")
	} else if !strings.EqualFold(publisher, strings.TrimSpace(sub.Publisher)) {
		reasons = append(reasons, "publisher does not match the subscription")
	}
	commit := strings.ToLower(strings.TrimSpace(lineage.Commit))
	if !runtimePromotionCommit.MatchString(commit) || !strings.EqualFold(commit, strings.TrimSpace(sub.Commit)) {
		reasons = append(reasons, "commit does not match the subscription")
	}
	if !strings.EqualFold(accepted.Result.Manifest.Digest, strings.TrimSpace(sub.Digest)) {
		reasons = append(reasons, "image digest does not match the subscription")
	}
	reasons = append(reasons, promotionGateReasons(accepted)...)
	return PromotionGateDecision{Matched: len(reasons) == 0, Reasons: reasons}
}

// PromoteSubscribedSouls registers the shared release once and creates exactly
// one deployment intent per gated subscription, recording each promotion as an
// append-only AgentServiceReleaseBinding via BindRelease. Replays are safe: the
// binding source event id is deterministic per (release, agent, service).
func (s *AgentRuntimePromotionService) PromoteSubscribedSouls(
	ctx context.Context,
	req RuntimePromotionRequest,
) (RuntimePromotionReport, error) {
	var report RuntimePromotionReport
	if s == nil || s.releases == nil || s.subscriptions == nil || s.intents == nil {
		return report, fmt.Errorf("agent runtime promotion service is not configured")
	}
	if req.Source == nil {
		return report, fmt.Errorf("promotion requires a tenant-scoped runtime source")
	}
	if req.Trigger != nil {
		if err := s.validateSecondaryTrigger(req.Release, *req.Trigger); err != nil {
			return report, err
		}
	}
	release, err := s.RegisterSharedRuntimeRelease(ctx, req.Source, req.Release)
	if err != nil {
		return report, err
	}
	report.Release = *release
	subscriptions, err := s.subscriptions.ListRuntimeReleaseSubscriptions(ctx, req.Source.OrgID, req.Source.ReleaseChannel)
	if err != nil {
		return report, fmt.Errorf("list runtime release subscriptions: %w", err)
	}
	for _, sub := range subscriptions {
		decision := s.EvaluatePromotionGate(req.Release, *req.Source, sub)
		if !decision.Matched {
			report.Skipped = append(report.Skipped, RuntimePromotionOutcome{
				Subscription: sub, Matched: false, Reasons: decision.Reasons, ReleaseID: release.ID,
			})
			continue
		}
		binding := &domain.AgentServiceReleaseBinding{
			OrgID:          req.Source.OrgID,
			AgentID:        sub.AgentID,
			ServiceID:      sub.ServiceID,
			ReleaseID:      release.ID,
			ReleaseChannel: req.Source.ReleaseChannel,
			SourceEventID:  promotionBindingSourceEventID(req.Release, sub),
		}
		if err := s.releases.BindRelease(ctx, binding); err != nil {
			return report, fmt.Errorf("record promotion binding for agent %s: %w", sub.AgentID, err)
		}
		intent, err := s.buildPromotionIntent(req.Release, *req.Source, sub, *release, *binding)
		if err != nil {
			return report, err
		}
		if err := s.intents.SubmitPromotionIntent(ctx, intent); err != nil {
			return report, fmt.Errorf("submit promotion intent for agent %s: %w", sub.AgentID, err)
		}
		report.Promoted = append(report.Promoted, RuntimePromotionOutcome{
			Subscription: sub, Matched: true, ReleaseID: release.ID, Intent: intent,
		})
	}
	return report, nil
}

// HandleRepositoryStateTrigger parses a kind-30618 repository state event into a
// replay-safe secondary trigger. It has no deployment authority: it only names
// the repository and commit that a later gated promotion evaluation may target.
func (s *AgentRuntimePromotionService) HandleRepositoryStateTrigger(event *nostr.Event) (RepositoryStateTrigger, error) {
	if event == nil || int(event.Kind) != kinds.NIP34RepositoryState {
		return RepositoryStateTrigger{}, fmt.Errorf("repository state trigger requires kind-%d", kinds.NIP34RepositoryState)
	}
	repo := firstTagValue(event, "a")
	if repo == "" {
		repo = firstTagValue(event, "repo")
	}
	commit := strings.ToLower(strings.TrimSpace(firstTagValue(event, "commit")))
	if repo == "" || !runtimePromotionCommit.MatchString(commit) {
		return RepositoryStateTrigger{}, fmt.Errorf("repository state trigger requires a repository address and full commit")
	}
	sum := sha256.Sum256([]byte(repo + "|" + commit))
	trigger := RepositoryStateTrigger{
		RepoAddress: repo,
		Commit:      commit,
		Fingerprint: hex.EncodeToString(sum[:]),
	}
	s.seenMu.Lock()
	if _, ok := s.seenKeys[trigger.Fingerprint]; ok {
		trigger.Replay = true
	} else {
		if s.seenKeys == nil {
			s.seenKeys = map[string]struct{}{}
		}
		s.seenKeys[trigger.Fingerprint] = struct{}{}
	}
	s.seenMu.Unlock()
	return trigger, nil
}

func (s *AgentRuntimePromotionService) validateSecondaryTrigger(accepted domain.HiveCIAcceptedRelease, trigger RepositoryStateTrigger) error {
	if !sameRepoCoordinate(accepted.Result.Lineage.RepoAddress, trigger.RepoAddress) {
		return fmt.Errorf("secondary trigger repository does not match the release lineage")
	}
	if !strings.EqualFold(accepted.Result.Lineage.Commit, trigger.Commit) {
		return fmt.Errorf("secondary trigger commit does not match the release lineage")
	}
	return nil
}

func (s *AgentRuntimePromotionService) buildPromotionIntent(
	accepted domain.HiveCIAcceptedRelease,
	source domain.AgentRuntimeSource,
	sub RuntimePromotionSubscription,
	release domain.AgentRuntimeRelease,
	binding domain.AgentServiceReleaseBinding,
) (*domain.DeploymentIntent, error) {
	if sub.ServiceID == uuid.Nil || sub.EnvironmentID == uuid.Nil || strings.TrimSpace(sub.AgentID) == "" {
		return nil, fmt.Errorf("promotion subscription for agent %q requires service and environment", sub.AgentID)
	}
	if binding.ID == uuid.Nil || binding.ReleaseID != release.ID || binding.ServiceID != sub.ServiceID {
		return nil, fmt.Errorf("promotion intent requires its persisted release binding")
	}
	publisher, _ := signedRunPublisher(accepted)
	now := s.now().UTC()
	identity := release.ID.String() + "|" + sub.ServiceID.String() + "|" + binding.ID.String()
	intent := &domain.DeploymentIntent{
		ID:            uuid.NewSHA1(promotionIntentNamespace, []byte(identity)),
		ServiceID:     sub.ServiceID,
		EnvironmentID: sub.EnvironmentID,
		RequestedBy:   "soul-factory-promotion",
		SourceKind:    domain.SourceKindAutoPromote,
		Metadata: map[string]any{
			"promotion_source":           "hiveci_release",
			"runtime_release_id":         release.ID.String(),
			"runtime_release_binding_id": binding.ID.String(),
			"promotion_idempotency_key":  identity,
			"release_identity":           accepted.Result.ReleaseIdentity,
			"image_repo":                 release.ImageRepo,
			"image_digest":               release.ImageDigest,
			"repository":                 accepted.Result.Lineage.RepoAddress,
			"branch":                     accepted.Branch,
			"commit":                     strings.ToLower(accepted.Result.Lineage.Commit),
			"publisher":                  publisher,
			"agent_id":                   sub.AgentID,
			"release_channel":            source.ReleaseChannel,
			"release_result_event_id":    accepted.ResultEventID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if sub.Protected {
		intent.ApprovalStatus = domain.ApprovalStatusPending
		intent.Status = domain.IntentStatusPending
	} else {
		intent.ApprovalStatus = domain.ApprovalStatusNotRequired
		intent.Status = domain.IntentStatusApproved
	}
	if err := validatePromotionIntent(intent); err != nil {
		return nil, err
	}
	return intent, nil
}

func validatePromotionIntent(intent *domain.DeploymentIntent) error {
	if intent == nil || intent.ServiceID == uuid.Nil || intent.EnvironmentID == uuid.Nil {
		return fmt.Errorf("promotion intent requires a service and environment")
	}
	digest, _ := intent.Metadata["image_digest"].(string)
	if !runtimePromotionDigest.MatchString(digest) {
		return fmt.Errorf("promotion intent requires an immutable image digest")
	}
	if releaseID, _ := intent.Metadata["runtime_release_id"].(string); strings.TrimSpace(releaseID) == "" {
		return fmt.Errorf("promotion intent requires a shared runtime release reference")
	}
	return nil
}

func promotionGateReasons(accepted domain.HiveCIAcceptedRelease) []string {
	var reasons []string
	if accepted.Result.Status != "success" {
		reasons = append(reasons, "release result status is not successful")
	}
	if accepted.Policy.ID == uuid.Nil || !accepted.Policy.Enabled {
		reasons = append(reasons, "release policy gate is not enabled")
	}
	if accepted.Policy.RepoCoordinate != "" && accepted.Policy.RepoCoordinate != accepted.Result.Lineage.RepoAddress {
		reasons = append(reasons, "release policy repository does not match lineage")
	}
	if accepted.Policy.WorkflowPath != "" && accepted.Policy.WorkflowPath != accepted.Workflow {
		reasons = append(reasons, "release policy workflow does not match the accepted workflow")
	}
	exec := accepted.Result.Execution
	if !exec.Complete || exec.Status != "success" || exec.ExitCode != 0 {
		reasons = append(reasons, "release execution gate is not green")
	}
	tests := exec.Tests
	if tests.Status != "success" || tests.Failed != 0 || tests.Passed <= 0 {
		reasons = append(reasons, "release test gate is not green")
	}
	attestation := accepted.Result.ArtifactAttestation
	if attestation.Type == "" || !strings.EqualFold(attestation.SignerPubkey, accepted.Attestor) {
		reasons = append(reasons, "signet attestation does not bind the release attestor")
	}
	covered := false
	for _, subject := range attestation.Subjects {
		if strings.EqualFold(subject.Digest, accepted.Result.Manifest.Digest) &&
			subject.Repository == accepted.Result.Manifest.Repository {
			covered = true
			break
		}
	}
	if !covered {
		reasons = append(reasons, "signet attestation does not cover the manifest digest")
	}
	return reasons
}

func validateAcceptedReleaseForPromotion(accepted domain.HiveCIAcceptedRelease) error {
	if accepted.ResultEventID == "" {
		return fmt.Errorf("accepted release is missing its result event id")
	}
	if !runtimePromotionDigest.MatchString(accepted.Result.Manifest.Digest) ||
		!runtimePromotionDigest.MatchString(accepted.Result.SBOM.Digest) ||
		!runtimePromotionDigest.MatchString(accepted.Result.Provenance.Digest) {
		return fmt.Errorf("accepted release is missing immutable artifact digests")
	}
	if !runtimePromotionCommit.MatchString(strings.ToLower(strings.TrimSpace(accepted.Result.Lineage.Commit))) {
		return fmt.Errorf("accepted release is missing a full source commit")
	}
	if !runtimePromotionPubkey.MatchString(strings.ToLower(strings.TrimSpace(accepted.Attestor))) {
		return fmt.Errorf("accepted release is missing its signet attestor")
	}
	if accepted.Result.Lineage.WorkflowRunEventID == "" {
		return fmt.Errorf("accepted release is missing its workflow run lineage")
	}
	return nil
}

// signedRunPublisher extracts the delegated `publisher` tag from the stored
// signed kind-5401 workflow run event.
func signedRunPublisher(accepted domain.HiveCIAcceptedRelease) (string, error) {
	raw := strings.TrimSpace(accepted.WorkflowRunSignedEvent)
	if raw == "" {
		return "", fmt.Errorf("signed workflow run event is not available")
	}
	var decoded struct {
		Tags [][]string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return "", fmt.Errorf("decode signed workflow run event: %w", err)
	}
	for _, tag := range decoded.Tags {
		if len(tag) == 2 && tag[0] == "publisher" {
			return strings.ToLower(strings.TrimSpace(tag[1])), nil
		}
	}
	return "", fmt.Errorf("signed workflow run event is missing its publisher")
}

func promotionBindingSourceEventID(accepted domain.HiveCIAcceptedRelease, sub RuntimePromotionSubscription) string {
	return accepted.ResultEventID + ":" + strings.TrimSpace(sub.AgentID) + ":" + sub.ServiceID.String()
}

func sameRepoCoordinate(left, right string) bool {
	return normalizeRepoCoordinate(left) != "" && normalizeRepoCoordinate(left) == normalizeRepoCoordinate(right)
}

func normalizeRepoCoordinate(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".git")
}

func firstTagValue(event *nostr.Event, key string) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return strings.TrimSpace(tag[1])
		}
	}
	return ""
}

package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

var promotionDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var ErrPromotionReplayConflict = errors.New("promotion replay conflicts with accepted intent")

type ReleasePromotionDecision struct {
	ReleaseIdentity        string
	ArtifactDigest         string
	PreviousArtifactDigest string
	IdempotencyKey         string
	Requester              string
	RequestEventID         string
	Fingerprint            string
	Metadata               map[string]any
	ExistingIntent         *domain.DeploymentIntent
	Replay                 bool
}

type releasePromotionRegistry interface {
	GetArtifact(context.Context, uuid.UUID) (*domain.Artifact, error)
	GetEnvironmentServiceState(context.Context, uuid.UUID, uuid.UUID) (*domain.EnvironmentServiceState, error)
	GetDeploymentIntentByReleasePromotionKey(context.Context, uuid.UUID, uuid.UUID, string, string) (*domain.DeploymentIntent, error)
}

type ReleasePromotionAuditor interface {
	AuditPromotionDecision(context.Context, ReleasePromotionDecision, string, error) error
	PreparePromotionDecisionAudit(context.Context, ReleasePromotionDecision, string, error) (*repository.NostrEventRecord, error)
}

type ReleasePromotionAuthorizer struct {
	registry releasePromotionRegistry
	auditor  ReleasePromotionAuditor
}

func NewReleasePromotionAuthorizer(registry releasePromotionRegistry, auditor ReleasePromotionAuditor) *ReleasePromotionAuthorizer {
	return &ReleasePromotionAuthorizer{registry: registry, auditor: auditor}
}

func (a *ReleasePromotionAuthorizer) Audit(ctx context.Context, decision ReleasePromotionDecision, status string, decisionErr error) error {
	if a == nil || a.auditor == nil {
		return fmt.Errorf("promotion audit is not configured")
	}
	return a.auditor.AuditPromotionDecision(ctx, decision, status, decisionErr)
}

func (a *ReleasePromotionAuthorizer) PrepareAudit(
	ctx context.Context,
	decision ReleasePromotionDecision,
	status string,
	decisionErr error,
) (*repository.NostrEventRecord, error) {
	if a == nil || a.auditor == nil {
		return nil, fmt.Errorf("promotion audit is not configured")
	}
	return a.auditor.PreparePromotionDecisionAudit(ctx, decision, status, decisionErr)
}

func (a *ReleasePromotionAuthorizer) Authorize(
	ctx context.Context,
	event *nostr.Event,
	params map[string]any,
	serviceID, environmentID, artifactID uuid.UUID,
	strategy, idempotencyKey string,
	env *domain.Environment,
) (ReleasePromotionDecision, error) {
	decision := ReleasePromotionDecision{IdempotencyKey: strings.TrimSpace(idempotencyKey)}
	if a == nil || a.registry == nil {
		return decision, fmt.Errorf("release promotion authorization is not configured")
	}
	if event == nil || event.PubKey.Hex() == "" || event.ID.Hex() == "" {
		return decision, fmt.Errorf("signed promotion requester is required")
	}
	decision.Requester = event.PubKey.Hex()
	decision.RequestEventID = event.ID.Hex()
	if decision.IdempotencyKey == "" {
		return decision, fmt.Errorf("promotion idempotency_key is required")
	}
	if env == nil || env.ID != environmentID || env.DeployStrategy != domain.DeployStrategyCanary {
		return decision, fmt.Errorf("promotion environment must be the explicit canary target")
	}
	if strings.TrimSpace(strategy) != "canary" {
		return decision, fmt.Errorf("registered release promotion strategy must be canary")
	}
	artifact, err := a.registry.GetArtifact(ctx, artifactID)
	if err != nil {
		return decision, fmt.Errorf("load registered promotion artifact: %w", err)
	}
	if artifact == nil || artifact.ServiceID != serviceID || artifact.ImageTag != "" ||
		!promotionDigestPattern.MatchString(artifact.ImageDigest) ||
		artifact.Metadata["registration_mode"] != "hiveci_release_digest" ||
		artifact.Metadata["promotion_authority"] != "required" ||
		artifact.Metadata["ci_mutates_desired_state"] != false {
		return decision, fmt.Errorf("artifact is not an eligible digest-only Hive-CI release registration")
	}
	decision.ArtifactDigest = artifact.ImageDigest
	releaseIdentity, _ := artifact.Metadata["release_identity"].(string)
	decision.ReleaseIdentity = strings.TrimSpace(releaseIdentity)
	if decision.ReleaseIdentity == "" || stringParam(params, "release_identity") != decision.ReleaseIdentity ||
		stringParam(params, "artifact_digest") != artifact.ImageDigest {
		return decision, fmt.Errorf("promotion request does not bind the registered release identity and digest")
	}
	policy, err := decodeMetadata[domain.HiveCIPipelinePolicy](artifact.Metadata["policy"])
	if err != nil || policy.ID == uuid.Nil || !policy.Enabled || policy.ServiceID != serviceID ||
		policy.EnvironmentID != environmentID {
		return decision, fmt.Errorf("registered release policy does not bind the target service and environment")
	}
	if err := validateReleaseVerificationMetadata(artifact); err != nil {
		return decision, err
	}
	health, readiness, err := promotionContracts(artifact.Metadata["health_readiness_contracts"])
	if err != nil {
		return decision, err
	}
	intent, err := a.registry.GetDeploymentIntentByReleasePromotionKey(
		ctx, serviceID, environmentID, decision.Requester, decision.IdempotencyKey,
	)
	if err != nil {
		return decision, fmt.Errorf("load promotion replay state: %w", err)
	}
	if intent != nil {
		decision.PreviousArtifactDigest = strings.TrimSpace(fmt.Sprint(intent.Metadata["previous_artifact_digest"]))
		if decision.PreviousArtifactDigest == "" || stringParam(params, "previous_artifact_digest") != decision.PreviousArtifactDigest {
			return decision, fmt.Errorf("%w: %s", ErrPromotionReplayConflict, decision.IdempotencyKey)
		}
		decision.Fingerprint = promotionFingerprint(decision, serviceID, environmentID, artifactID)
		if intent.Metadata["promotion_fingerprint"] != decision.Fingerprint {
			return decision, fmt.Errorf("%w: %s", ErrPromotionReplayConflict, decision.IdempotencyKey)
		}
		if intent.Status == domain.IntentStatusApproved {
			state, stateErr := a.registry.GetEnvironmentServiceState(ctx, serviceID, environmentID)
			if stateErr != nil || state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID ||
				state.DesiredArtifactID == nil || *state.DesiredArtifactID != artifactID {
				return decision, fmt.Errorf("approved promotion replay has not durably advanced desired state")
			}
		}
		decision.Metadata = intent.Metadata
		decision.ExistingIntent = intent
		decision.Replay = true
		return decision, nil
	}
	state, err := a.registry.GetEnvironmentServiceState(ctx, serviceID, environmentID)
	if err != nil {
		return decision, fmt.Errorf("load previous desired artifact: %w", err)
	}
	if state == nil || state.DesiredArtifactID == nil {
		return decision, fmt.Errorf("promotion requires a previous desired artifact for rollback")
	}
	previous, err := a.registry.GetArtifact(ctx, *state.DesiredArtifactID)
	if err != nil || previous == nil || !promotionDigestPattern.MatchString(previous.ImageDigest) {
		return decision, fmt.Errorf("load previous rollback artifact: %w", err)
	}
	decision.PreviousArtifactDigest = previous.ImageDigest
	if stringParam(params, "previous_artifact_digest") != previous.ImageDigest {
		return decision, fmt.Errorf("promotion request previous_artifact_digest is stale or missing")
	}
	rollback, err := metadataMap(artifact.Metadata["rollback_compatibility"])
	if err != nil || !containsString(rollback["compatible_from_digests"], previous.ImageDigest) {
		return decision, fmt.Errorf("registered release is not rollback-compatible with the previous artifact")
	}
	decision.Metadata = map[string]any{
		"release_promotion":          true,
		"release_identity":           decision.ReleaseIdentity,
		"artifact_digest":            decision.ArtifactDigest,
		"previous_artifact_digest":   decision.PreviousArtifactDigest,
		"promotion_strategy":         "canary",
		"promotion_idempotency_key":  decision.IdempotencyKey,
		"promotion_requester":        decision.Requester,
		"promotion_request_event_id": decision.RequestEventID,
		"health_contract":            health,
		"readiness_contract":         readiness,
		"rollback_compatibility":     rollback,
	}
	decision.Fingerprint = promotionFingerprint(decision, serviceID, environmentID, artifactID)
	decision.Metadata["promotion_fingerprint"] = decision.Fingerprint
	return decision, nil
}

func promotionFingerprint(decision ReleasePromotionDecision, serviceID, environmentID, artifactID uuid.UUID) string {
	fingerprintInput := struct {
		Requester, Idempotency, Release, Digest, Previous string
		ServiceID, EnvironmentID, ArtifactID              uuid.UUID
		Strategy                                          string
	}{
		Requester: decision.Requester, Idempotency: decision.IdempotencyKey, Release: decision.ReleaseIdentity,
		Digest: decision.ArtifactDigest, Previous: decision.PreviousArtifactDigest,
		ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID, Strategy: "canary",
	}
	encoded, _ := json.Marshal(fingerprintInput)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateReleaseVerificationMetadata(artifact *domain.Artifact) error {
	verification, err := metadataMap(artifact.Metadata["verification"])
	if err != nil || verification["state"] != "verified" || verification["source"] != "oci_digest" {
		return fmt.Errorf("registered release verification evidence is incomplete")
	}
	manifest, err := decodeMetadata[domain.HiveCIReleaseArtifact](verification["manifest"])
	if err != nil || manifest.Repository != artifact.ImageRepo || manifest.Digest != artifact.ImageDigest ||
		!promotionDigestPattern.MatchString(manifest.Digest) {
		return fmt.Errorf("registered manifest evidence conflicts with artifact identity")
	}
	for _, key := range []string{"sbom", "provenance"} {
		descriptor, descriptorErr := decodeMetadata[domain.HiveCIReleaseArtifact](verification[key])
		if descriptorErr != nil || descriptor.Repository != artifact.ImageRepo ||
			!promotionDigestPattern.MatchString(descriptor.Digest) || descriptor.Size <= 0 ||
			strings.TrimSpace(descriptor.MediaType) == "" {
			return fmt.Errorf("registered %s evidence is incomplete", key)
		}
	}
	signedRelease, releaseOK := artifact.Metadata["signed_release_event"].(string)
	signedWorkflowRun, workflowOK := artifact.Metadata["signed_workflow_run_event"].(string)
	if !releaseOK || !workflowOK || strings.TrimSpace(signedRelease) == "" || strings.TrimSpace(signedWorkflowRun) == "" {
		return fmt.Errorf("registered signed release lineage is incomplete")
	}
	return nil
}

func promotionContracts(value any) (map[string]any, map[string]any, error) {
	contracts, err := metadataMap(value)
	if err != nil {
		return nil, nil, fmt.Errorf("health/readiness contracts are missing")
	}
	health, healthErr := metadataMap(contracts["health"])
	readiness, readinessErr := metadataMap(contracts["readiness"])
	if healthErr != nil || readinessErr != nil || !concreteContract(health) || !concreteContract(readiness) {
		return nil, nil, fmt.Errorf("concrete health and readiness contracts are required")
	}
	return health, readiness, nil
}

func concreteContract(contract map[string]any) bool {
	kind := strings.TrimSpace(fmt.Sprint(contract["type"]))
	timeout, ok := numericValue(contract["timeout_seconds"])
	return kind != "" && ok && timeout > 0
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func metadataMap(value any) (map[string]any, error) {
	if value == nil {
		return nil, fmt.Errorf("metadata is missing")
	}
	if mapped, ok := value.(map[string]any); ok {
		return mapped, nil
	}
	return decodeMetadata[map[string]any](value)
}

func decodeMetadata[T any](value any) (T, error) {
	var decoded T
	encoded, err := json.Marshal(value)
	if err != nil {
		return decoded, err
	}
	err = json.Unmarshal(encoded, &decoded)
	return decoded, err
}

func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

func containsString(value any, want string) bool {
	switch values := value.(type) {
	case []string:
		for _, candidate := range values {
			if candidate == want {
				return true
			}
		}
	case []any:
		for _, candidate := range values {
			if text, ok := candidate.(string); ok && text == want {
				return true
			}
		}
	}
	return false
}

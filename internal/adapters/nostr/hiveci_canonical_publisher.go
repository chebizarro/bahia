package nostr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// Hive-CI cp-state families (audit C-48). They are 30900-only families with no
// catalog kind.
const (
	KindHiveCIPolicyRecord     = int(kinds.CPStateFamilyHiveCIPolicy)
	KindHiveCIResultRecord     = int(kinds.CPStateFamilyHiveCIResult)
	KindHiveCIInitiationRecord = int(kinds.CPStateFamilyHiveCIInitiation)
	KindHiveCIReleaseRecord    = int(kinds.CPStateFamilyHiveCIRelease)
)

// Accepted-release ledger record statuses (public "status" tag).
const (
	hiveCIReleaseStatusAccepted = "accepted"
	hiveCIReleaseStatusConflict = "conflict"
)

// HiveCICanonicalPublisher is the canonical store of the daemon's own Hive-CI
// state (audit C-48): the pipeline policies release admission is checked
// against, the processing state of each signed workflow result, the build
// initiation journal and the accepted-release ledger (bahia-xjdo9). Records
// are fleet-OCK encrypted 30900 cp-state, published through the outbox before
// any SQL index is written, and read back from the daemon's retained records
// in the local event store. The signed 5401/5402/4903 evidence itself is not
// republished here: it stays the producers' events, held in the same store.
type HiveCICanonicalPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	logger    *zap.Logger
}

// NewHiveCICanonicalPublisher creates a publisher backed by projector.
// encryptor is required; publishes fail closed without it.
func NewHiveCICanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, logger *zap.Logger) *HiveCICanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &HiveCICanonicalPublisher{projector: projector, encryptor: encryptor, logger: logger.Named("hiveci-canonical")}
}

func (p *HiveCICanonicalPublisher) available() error {
	if p == nil || p.projector == nil || !p.projector.Enabled() || p.encryptor == nil {
		return fmt.Errorf("Hive-CI canonical publisher is unavailable")
	}
	return nil
}

// HiveCIPolicyKey identifies one pipeline policy: a repository workflow and
// branch pattern bound to a service and environment.
func HiveCIPolicyKey(policy domain.HiveCIPipelinePolicy) string {
	sum := sha256.Sum256([]byte(policy.RepoCoordinate + "\x00" + policy.WorkflowPath + "\x00" + policy.BranchPattern +
		"\x00" + policy.ServiceID.String() + "\x00" + policy.EnvironmentID.String()))
	return hex.EncodeToString(sum[:])
}

// PublishPipelinePolicy publishes one pipeline policy on
// "hiveci:policy:<key>". The repository coordinate and workflow are only in
// the encrypted content.
func (p *HiveCICanonicalPublisher) PublishPipelinePolicy(ctx context.Context, policy domain.HiveCIPipelinePolicy) error {
	if err := p.available(); err != nil {
		return err
	}
	content, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	key := HiveCIPolicyKey(policy)
	tags := gonostr.Tags{{"policy", policy.ID.String()}, {"enabled", strconv.FormatBool(policy.Enabled)}}
	return p.publishConfidential(ctx, KindHiveCIPolicyRecord, "hiveci:policy:"+key, tags, string(content), "hiveci_policy.projection", &policy.ID)
}

// ListPipelinePolicies returns every retained pipeline policy.
func (p *HiveCICanonicalPublisher) ListPipelinePolicies(ctx context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	raw, err := p.listState(ctx, KindHiveCIPolicyRecord, nil)
	if err != nil {
		return nil, err
	}
	out := make([]domain.HiveCIPipelinePolicy, 0, len(raw))
	for _, b := range raw {
		var policy domain.HiveCIPipelinePolicy
		if err := json.Unmarshal(b, &policy); err != nil {
			return nil, fmt.Errorf("decode Hive-CI pipeline policy: %w", err)
		}
		out = append(out, policy)
	}
	return out, nil
}

// hiveCIResultStateTags are the public tags of a result state record. The
// result and run are public signed events already, so naming them (and the
// state) lets the daemon select records without decrypting each one.
func hiveCIResultStateTags(state domain.HiveCIResultState) gonostr.Tags {
	return gonostr.Tags{{"result", state.ResultEventID}, {"run", state.RunEventID}, {"status", string(state.ProcessingState)}}
}

// PublishResultState publishes the processing state of one workflow result on
// "hiveci:result:<result-event-id>".
func (p *HiveCICanonicalPublisher) PublishResultState(ctx context.Context, state domain.HiveCIResultState) error {
	if err := p.available(); err != nil {
		return err
	}
	if state.ResultEventID == "" {
		return fmt.Errorf("Hive-CI result state requires a result event id")
	}
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return p.publishConfidential(ctx, KindHiveCIResultRecord, "hiveci:result:"+state.ResultEventID, hiveCIResultStateTags(state), string(content), "hiveci_result.projection", nil)
}

// ListResultStates returns the retained result states; resultEventID and
// runEventID, when set, narrow the result.
func (p *HiveCICanonicalPublisher) ListResultStates(ctx context.Context, resultEventID, runEventID string) ([]domain.HiveCIResultState, error) {
	raw, err := p.listState(ctx, KindHiveCIResultRecord, func(tags gonostr.Tags) bool {
		return (resultEventID == "" || tagValue(tags, "result") == resultEventID) &&
			(runEventID == "" || tagValue(tags, "run") == runEventID)
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.HiveCIResultState, 0, len(raw))
	for _, b := range raw {
		var state domain.HiveCIResultState
		if err := json.Unmarshal(b, &state); err != nil {
			return nil, fmt.Errorf("decode Hive-CI result state: %w", err)
		}
		out = append(out, state)
	}
	return out, nil
}

// HiveCIResultStateRef reads the public tags of a signed event and reports
// the result it is the processing state of. ok is false for any other event.
// It never decrypts, so a publish-delivery hook can call it.
func HiveCIResultStateRef(ev gonostr.Event) (resultEventID string, state domain.HiveCIProcessingState, ok bool) {
	if int(ev.Kind) != KindCASControlState || tagValue(ev.Tags, "legacy_kind") != strconv.Itoa(KindHiveCIResultRecord) || isTombstoneTags(ev.Tags) {
		return "", "", false
	}
	resultEventID = tagValue(ev.Tags, "result")
	return resultEventID, domain.HiveCIProcessingState(tagValue(ev.Tags, "status")), resultEventID != ""
}

// HiveCIInitiationEntry is one retained initiation journal record: the
// fleet-visible document and the service-only secret material that was
// stored beside it, decrypted.
type HiveCIInitiationEntry struct {
	SourceEventID string
	Stage         string
	BuildID       string
	Document      []byte
	ServiceOnly   []byte
}

func hiveCIInitiationDTag(sourceEventID string) string { return "hiveci:initiation:" + sourceEventID }

// PublishInitiation journals one build initiation on
// "hiveci:initiation:<source-event-id>" (audit C-49). document is fleet-OCK
// encrypted; serviceOnly, when non-empty, is additionally NIP-44 encrypted to
// the service pubkey as the envelope's service_inner, so key material never
// reaches the relay in plaintext or under the fleet key.
func (p *HiveCICanonicalPublisher) PublishInitiation(ctx context.Context, sourceEventID, stage, buildID string, document, serviceOnly []byte) error {
	if err := p.available(); err != nil {
		return err
	}
	if sourceEventID == "" || stage == "" {
		return fmt.Errorf("Hive-CI initiation journal requires a source event id and stage")
	}
	tags := gonostr.Tags{{"source", sourceEventID}, {"stage", stage}, {"build", buildID}}
	return p.publishConfidentialWithSecret(ctx, KindHiveCIInitiationRecord, hiveCIInitiationDTag(sourceEventID), tags, string(document), serviceOnly, "hiveci_initiation.projection", nil)
}

// ReadInitiation returns the retained journal record for the source event,
// or nil.
func (p *HiveCICanonicalPublisher) ReadInitiation(ctx context.Context, sourceEventID string) (*HiveCIInitiationEntry, error) {
	entries, err := p.listInitiations(ctx, func(tags gonostr.Tags) bool { return tagValue(tags, "source") == sourceEventID })
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return &entries[0], nil
}

// ListInitiations returns every retained journal record.
func (p *HiveCICanonicalPublisher) ListInitiations(ctx context.Context) ([]HiveCIInitiationEntry, error) {
	return p.listInitiations(ctx, nil)
}

func (p *HiveCICanonicalPublisher) listInitiations(ctx context.Context, match func(gonostr.Tags) bool) ([]HiveCIInitiationEntry, error) {
	if p == nil || p.projector == nil || p.projector.history == nil || p.encryptor == nil {
		return nil, fmt.Errorf("Hive-CI canonical local view is unavailable")
	}
	family := cpStateFamilies[KindHiveCIInitiationRecord]
	records, err := p.projector.history.FindByTag(ctx, "t", family.topic, []int{KindCASControlState}, canonicalViewLimit)
	if err != nil {
		return nil, err
	}
	if len(records) >= canonicalViewLimit {
		return nil, fmt.Errorf("Hive-CI initiation view reached history limit")
	}
	var out []HiveCIInitiationEntry
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(KindHiveCIInitiationRecord) || isTombstoneTags(tags) {
			continue
		}
		if match != nil && !match(tags) {
			continue
		}
		d := tagValue(tags, "d")
		document, err := p.encryptor.DecryptConfidential(ctx, record.Content, KindHiveCIInitiationRecord, d, family.topic)
		if err != nil {
			return nil, fmt.Errorf("decrypt Hive-CI initiation %s: %w", record.ID, err)
		}
		serviceOnly, err := p.encryptor.DecryptServiceInner(ctx, record.Content)
		if err != nil {
			return nil, fmt.Errorf("decrypt Hive-CI initiation %s service layer: %w", record.ID, err)
		}
		out = append(out, HiveCIInitiationEntry{
			SourceEventID: tagValue(tags, "source"), Stage: tagValue(tags, "stage"), BuildID: tagValue(tags, "build"),
			Document: document, ServiceOnly: serviceOnly,
		})
	}
	return out, nil
}

// Accepted releases ---------------------------------------------------------

func hiveCIReleaseDTag(releaseIdentity string) string { return "hiveci:release:" + releaseIdentity }

func hiveCIReleaseConflictDTag(releaseIdentity, conflictingDigest string) string {
	return "hiveci:release-conflict:" + releaseIdentity + ":" + conflictingDigest
}

// PublishAcceptedRelease publishes the daemon's admission of one release on
// "hiveci:release:<release-identity>" (bahia-xjdo9). The identity, the
// attestation's content digest and the signed events it was accepted from
// are public tags so a replay is recognised without decrypting; the release
// itself (its policy snapshot, worker admission and signed evidence) is in
// the fleet-OCK encrypted content.
func (p *HiveCICanonicalPublisher) PublishAcceptedRelease(ctx context.Context, release domain.HiveCIAcceptedRelease) error {
	if err := p.available(); err != nil {
		return err
	}
	if err := release.Validate(); err != nil {
		return err
	}
	content, err := json.Marshal(release)
	if err != nil {
		return err
	}
	tags := gonostr.Tags{
		{"release", release.Result.ReleaseIdentity}, {"digest", release.ContentDigest},
		{"result", release.ResultEventID}, {"run", release.Result.Lineage.WorkflowRunEventID},
		{"status", hiveCIReleaseStatusAccepted},
	}
	return p.publishConfidential(ctx, KindHiveCIReleaseRecord, hiveCIReleaseDTag(release.Result.ReleaseIdentity), tags, string(content), "hiveci_release.projection", nil)
}

// ListAcceptedReleases returns the retained accepted releases; releaseIdentity,
// when set, narrows the result to that identity's record.
func (p *HiveCICanonicalPublisher) ListAcceptedReleases(ctx context.Context, releaseIdentity string) ([]domain.HiveCIAcceptedRelease, error) {
	raw, err := p.listState(ctx, KindHiveCIReleaseRecord, func(tags gonostr.Tags) bool {
		return tagValue(tags, "status") == hiveCIReleaseStatusAccepted &&
			(releaseIdentity == "" || tagValue(tags, "release") == releaseIdentity)
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.HiveCIAcceptedRelease, 0, len(raw))
	for _, b := range raw {
		var release domain.HiveCIAcceptedRelease
		if err := json.Unmarshal(b, &release); err != nil {
			return nil, fmt.Errorf("decode Hive-CI accepted release: %w", err)
		}
		out = append(out, release)
	}
	return out, nil
}

// PublishReleaseConflict quarantines an attestation that names an accepted
// release identity with different content, on
// "hiveci:release-conflict:<release-identity>:<conflicting-digest>". The
// accepted record is untouched.
func (p *HiveCICanonicalPublisher) PublishReleaseConflict(ctx context.Context, conflict domain.HiveCIReleaseConflict) error {
	if err := p.available(); err != nil {
		return err
	}
	if conflict.ReleaseIdentity == "" || conflict.ConflictingContentDigest == "" || conflict.AcceptedContentDigest == "" {
		return fmt.Errorf("Hive-CI release conflict requires the release identity and both content digests")
	}
	content, err := json.Marshal(conflict)
	if err != nil {
		return err
	}
	tags := gonostr.Tags{
		{"release", conflict.ReleaseIdentity}, {"digest", conflict.ConflictingContentDigest},
		{"accepted", conflict.AcceptedContentDigest}, {"result", conflict.ResultEventID},
		{"status", hiveCIReleaseStatusConflict},
	}
	return p.publishConfidential(ctx, KindHiveCIReleaseRecord, hiveCIReleaseConflictDTag(conflict.ReleaseIdentity, conflict.ConflictingContentDigest), tags, string(content), "hiveci_release_conflict.projection", nil)
}

func (p *HiveCICanonicalPublisher) publishConfidential(ctx context.Context, legacyKind int, dTag string, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	return p.publishConfidentialWithSecret(ctx, legacyKind, dTag, tags, content, nil, entityType, entityID)
}

func (p *HiveCICanonicalPublisher) publishConfidentialWithSecret(ctx context.Context, legacyKind int, dTag string, tags gonostr.Tags, content string, serviceOnly []byte, entityType string, entityID *uuid.UUID) error {
	family := cpStateFamilies[legacyKind]
	// Hive-CI state is fleet-scoped: it names private repositories and the
	// operator's release constraints.
	encrypted, err := p.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, []byte(content), legacyKind, dTag, family.topic, serviceOnly)
	if err != nil {
		return fmt.Errorf("encrypt Hive-CI %s state: %w", family.entity, err)
	}
	return p.projector.publishCanonicalFirst(ctx, legacyKind, dTag, false, tags, content, encrypted, entityType, entityID)
}

// listState returns the decrypted content of the daemon's retained records of
// one Hive-CI family. match, when non-nil, selects records by their public
// tags before anything is decrypted. A record that cannot be decrypted fails
// the read: admission and retry must not act on a partial view.
func (p *HiveCICanonicalPublisher) listState(ctx context.Context, kind int, match func(gonostr.Tags) bool) ([][]byte, error) {
	if p == nil || p.projector == nil || p.projector.history == nil || p.encryptor == nil {
		return nil, fmt.Errorf("Hive-CI canonical local view is unavailable")
	}
	family := cpStateFamilies[kind]
	records, err := p.projector.history.FindByTag(ctx, "t", family.topic, []int{KindCASControlState}, canonicalViewLimit)
	if err != nil {
		return nil, err
	}
	if len(records) >= canonicalViewLimit {
		return nil, fmt.Errorf("Hive-CI %s view reached history limit", family.entity)
	}
	out := make([][]byte, 0, len(records))
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(kind) || isTombstoneTags(tags) {
			continue
		}
		if match != nil && !match(tags) {
			continue
		}
		d := tagValue(tags, "d")
		if d == "" {
			return nil, fmt.Errorf("Hive-CI %s record %s lacks coordinate", family.entity, record.ID)
		}
		plaintext, err := p.encryptor.DecryptConfidential(ctx, record.Content, kind, d, family.topic)
		if err != nil {
			return nil, fmt.Errorf("decrypt Hive-CI %s/%s: %w", family.entity, record.ID, err)
		}
		out = append(out, plaintext)
	}
	return out, nil
}

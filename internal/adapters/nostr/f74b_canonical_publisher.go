package nostr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	// One replaceable notification record per channel retains only the newest
	// 50 delivery attempts. Payloads are capped independently, then the oldest
	// entries are dropped if the NIP-44 plaintext budget would be exceeded.
	NotificationLogWindowLimit = 50
	notificationPayloadLimit   = 512
	notificationErrorLimit     = 256
	canonicalPlaintextLimit    = 60 * 1024
)

// F74bCanonicalPublisher routes fleet-private state through the shared 30900
// envelope/signing/outbox path. Package request payloads can contain signed
// source URLs; tool policy and notification payloads expose operator details.
// All five families therefore use the fleet OCK, never plaintext relay content.
type F74bCanonicalPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
}

func NewF74bCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor) *F74bCanonicalPublisher {
	return &F74bCanonicalPublisher{projector: projector, encryptor: encryptor}
}

// SetEncryptor is used during app assembly: package and tool repositories are
// built before OCK bootstrap, while no ingress is started until assembly ends.
func (p *F74bCanonicalPublisher) SetEncryptor(encryptor ConfidentialStateEncryptor) {
	p.encryptor = encryptor
}

func (p *F74bCanonicalPublisher) publish(ctx context.Context, kind int, dTag string, deleted bool, value any, entityType string, entityID *uuid.UUID) error {
	if p == nil || p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if p.encryptor == nil {
		return fmt.Errorf("fleet confidential encryptor not configured for %s", entityType)
	}
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", entityType, err)
	}
	if len(body) > canonicalPlaintextLimit {
		return fmt.Errorf("%s exceeds %d-byte canonical plaintext bound", entityType, canonicalPlaintextLimit)
	}
	family := cpStateFamilies[kind]
	ciphertext, err := p.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, body, kind, dTag, family.topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt %s: %w", entityType, err)
	}
	return p.projector.publishControlState(ctx, kind, dTag, deleted, nil, ciphertext, entityType, entityID)
}

func packageIntentDTag(requestEventID string) string { return "package:intent:" + requestEventID }
func packageClaimDTag(requestEventID string) string  { return "package:claim:" + requestEventID }
func packageApprovalDTag(id uuid.UUID) string        { return "package:approval:" + id.String() }
func toolIntentDTag(id uuid.UUID) string             { return "tool:intent:" + id.String() }
func toolProfileDTag(serviceID, envID uuid.UUID) string {
	return "tool:profile:" + serviceID.String() + ":" + envID.String()
}
func toolDenylistDTag(packageName, manager string) string {
	sum := sha256.Sum256([]byte(manager + "\x00" + packageName))
	return "tool:denylist:" + hex.EncodeToString(sum[:])
}
func notificationLogDTag(channelID uuid.UUID) string {
	return "notification:log:" + channelID.String()
}

func (p *F74bCanonicalPublisher) PublishPackageIntent(ctx context.Context, intent *domain.PackageIntent) error {
	if intent == nil || intent.RequestEventID == "" {
		return fmt.Errorf("package intent requires request event ID")
	}
	return p.publish(ctx, KindPackageIntentState, packageIntentDTag(intent.RequestEventID), false,
		struct {
			RecordType string `json:"record_type"`
			*domain.PackageIntent
		}{"intent", intent}, "package_intent.projection", &intent.ID)
}

// PublishSignedPackageIntent records one terminal state after the signed
// package handler reconciles. A hashed coordinate bounds arbitrary intent IDs.
func (p *F74bCanonicalPublisher) PublishSignedPackageIntent(ctx context.Context, state *domain.PackageIntentState) error {
	if state == nil || state.ID == "" {
		return fmt.Errorf("signed package intent requires ID")
	}
	sum := sha256.Sum256([]byte(state.ID))
	dTag := "package:signed-intent:" + hex.EncodeToString(sum[:])
	return p.publish(ctx, KindPackageIntentState, dTag, false, state, "package_signed_intent.projection", nil)
}

func (p *F74bCanonicalPublisher) publishPackageIntentDeleted(ctx context.Context, requestEventID string) error {
	return p.publish(ctx, KindPackageIntentState, packageIntentDTag(requestEventID), true,
		map[string]any{"request_event_id": requestEventID, "deleted": true}, "package_intent.projection", nil)
}

func (p *F74bCanonicalPublisher) PublishPackageClaim(ctx context.Context, claim repository.PackageRequestClaim) error {
	status := "claimed"
	if claim.Completed {
		status = "completed"
	}
	// The authoritative claim store retains requester/token/fingerprint. The
	// completion mutation receives only the event ID, so publish the same
	// minimal shape on both transitions rather than replacing full fields with
	// empty values.
	return p.publish(ctx, KindPackageIntentState, packageClaimDTag(claim.EventID), false,
		map[string]any{"record_type": "claim", "request_event_id": claim.EventID, "status": status},
		"package_claim.projection", nil)
}

func (p *F74bCanonicalPublisher) PublishPackageApproval(ctx context.Context, approval repository.PackageApproval, status string) error {
	// Expiry and source event are held by the single-use local approval store;
	// ConsumePackageApproval returns only these stable fields plus status.
	return p.publish(ctx, KindPackageIntentState, packageApprovalDTag(approval.ID), false,
		map[string]any{"record_type": "approval", "approval_id": approval.ID.String(), "requester": approval.Requester,
			"approver": approval.Approver, "method": approval.Method, "plan_hash": approval.PlanHash, "status": status},
		"package_approval.projection", &approval.ID)
}

func (p *F74bCanonicalPublisher) PublishToolIntent(ctx context.Context, intent *domain.ToolProvisionIntent) error {
	if intent == nil || intent.ID == uuid.Nil {
		return fmt.Errorf("tool intent requires ID")
	}
	return p.publish(ctx, KindToolProvisionIntentState, toolIntentDTag(intent.ID), false, intent, "tool_intent.projection", &intent.ID)
}

func (p *F74bCanonicalPublisher) publishToolIntentDeleted(ctx context.Context, id uuid.UUID) error {
	return p.publish(ctx, KindToolProvisionIntentState, toolIntentDTag(id), true,
		map[string]any{"id": id.String(), "deleted": true}, "tool_intent.projection", &id)
}

func (p *F74bCanonicalPublisher) PublishToolDenylist(ctx context.Context, entry *domain.ToolDenylistEntry, deleted bool) error {
	if entry == nil {
		return fmt.Errorf("tool denylist entry is nil")
	}
	value := any(entry)
	if deleted {
		value = map[string]any{"package_name": entry.PackageName, "manager": entry.Manager, "deleted": true}
	}
	return p.publish(ctx, KindToolDenylistState, toolDenylistDTag(entry.PackageName, entry.Manager), deleted,
		value, "tool_denylist.projection", nil)
}

func (p *F74bCanonicalPublisher) PublishToolProfile(ctx context.Context, profile *domain.ToolProfileState, deleted bool) error {
	if profile == nil {
		return fmt.Errorf("tool profile is nil")
	}
	value := any(profile)
	if deleted {
		value = map[string]any{"service_id": profile.ServiceID.String(), "environment_id": profile.EnvironmentID.String(), "deleted": true}
	}
	return p.publish(ctx, KindToolProfileState, toolProfileDTag(profile.ServiceID, profile.EnvironmentID), deleted,
		value, "tool_profile.projection", nil)
}

// PublishNotificationWindow replaces one channel's bounded index. The channel
// coordinate is tombstoned when the channel is removed. Historical log rows
// beyond this window remain in the optional DB cache, never on the relay.
func (p *F74bCanonicalPublisher) PublishNotificationWindow(ctx context.Context, channelID uuid.UUID, logs []domain.NotificationLog, deleted bool) error {
	if deleted {
		return p.publish(ctx, KindNotificationLogState, notificationLogDTag(channelID), true,
			map[string]any{"channel_id": channelID.String(), "deleted": true}, "notification_log.projection", &channelID)
	}
	bounded := make([]domain.NotificationLog, 0, min(len(logs), NotificationLogWindowLimit))
	for _, log := range logs {
		if len(bounded) >= NotificationLogWindowLimit {
			break
		}
		if body, err := json.Marshal(log.Payload); err == nil && len(body) > notificationPayloadLimit {
			log.Payload = map[string]any{"truncated": true, "original_bytes": len(body)}
		}
		if chars := []rune(log.LastError); len(chars) > notificationErrorLimit {
			log.LastError = string(chars[:notificationErrorLimit])
		}
		bounded = append(bounded, log)
	}
	for {
		value := map[string]any{"channel_id": channelID.String(), "logs": bounded}
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if len(body) <= canonicalPlaintextLimit || len(bounded) == 0 {
			return p.publish(ctx, KindNotificationLogState, notificationLogDTag(channelID), false,
				value, "notification_log.projection", &channelID)
		}
		bounded = bounded[:len(bounded)-1]
	}
}

// CanonicalPackageRepository publishes once after each successful persisted
// intent/claim/approval mutation. Non-mutating methods remain delegated.
type CanonicalPackageRepository struct {
	repository.PackageControlPlaneRepository
	repository.PackageAuthorizationStore
	publisher *F74bCanonicalPublisher
}

func NewCanonicalPackageRepository(inner repository.PackageControlPlaneRepository, auth repository.PackageAuthorizationStore, publisher *F74bCanonicalPublisher) *CanonicalPackageRepository {
	return &CanonicalPackageRepository{PackageControlPlaneRepository: inner, PackageAuthorizationStore: auth, publisher: publisher}
}

func (r *CanonicalPackageRepository) UpsertIntent(ctx context.Context, intent *domain.PackageIntent) error {
	if err := r.PackageControlPlaneRepository.UpsertIntent(ctx, intent); err != nil {
		return err
	}
	// PostgreSQL may retain a terminal row when a stale transition races it.
	// Publish the persisted winner, not the caller's attempted transition.
	stored, err := r.PackageControlPlaneRepository.GetIntentByRequestEventID(ctx, intent.RequestEventID)
	if err != nil {
		return err
	}
	if stored == nil {
		return fmt.Errorf("persisted package intent %s not found", intent.RequestEventID)
	}
	return r.publisher.PublishPackageIntent(ctx, stored)
}
func (r *CanonicalPackageRepository) ClaimPackageRequest(ctx context.Context, claim repository.PackageRequestClaim) (*repository.PackageRequestClaim, bool, error) {
	stored, fresh, err := r.PackageAuthorizationStore.ClaimPackageRequest(ctx, claim)
	if err != nil || !fresh {
		return stored, fresh, err
	}
	return stored, fresh, r.publisher.PublishPackageClaim(ctx, *stored)
}
func (r *CanonicalPackageRepository) CompletePackageRequest(ctx context.Context, eventID string) error {
	if err := r.PackageAuthorizationStore.CompletePackageRequest(ctx, eventID); err != nil {
		return err
	}
	return r.publisher.PublishPackageClaim(ctx, repository.PackageRequestClaim{EventID: eventID, Completed: true})
}
func (r *CanonicalPackageRepository) CreatePackageApproval(ctx context.Context, approval repository.PackageApproval) error {
	if err := r.PackageAuthorizationStore.CreatePackageApproval(ctx, approval); err != nil {
		return err
	}
	return r.publisher.PublishPackageApproval(ctx, approval, "pending")
}
func (r *CanonicalPackageRepository) ConsumePackageApproval(ctx context.Context, id uuid.UUID, requester, method, hash string, approvers []string) (string, error) {
	approvedBy, err := r.PackageAuthorizationStore.ConsumePackageApproval(ctx, id, requester, method, hash, approvers)
	if err != nil {
		return "", err
	}
	approval := repository.PackageApproval{ID: id, Requester: requester, Approver: approvedBy, Method: method, PlanHash: hash}
	return approvedBy, r.publisher.PublishPackageApproval(ctx, approval, "consumed")
}

// CanonicalToolRepository covers MCP, reactor, and coordinator mutation sites.
type CanonicalToolRepository struct {
	repository.ToolProvisioningRepository
	publisher *F74bCanonicalPublisher
}

func NewCanonicalToolRepository(inner repository.ToolProvisioningRepository, publisher *F74bCanonicalPublisher) *CanonicalToolRepository {
	return &CanonicalToolRepository{ToolProvisioningRepository: inner, publisher: publisher}
}
func (r *CanonicalToolRepository) CreateIntent(ctx context.Context, intent *domain.ToolProvisionIntent) error {
	if err := r.ToolProvisioningRepository.CreateIntent(ctx, intent); err != nil {
		return err
	}
	return r.publisher.PublishToolIntent(ctx, intent)
}
func (r *CanonicalToolRepository) UpdateIntent(ctx context.Context, intent *domain.ToolProvisionIntent) error {
	if err := r.ToolProvisioningRepository.UpdateIntent(ctx, intent); err != nil {
		return err
	}
	return r.publisher.PublishToolIntent(ctx, intent)
}
func (r *CanonicalToolRepository) UpsertProfileState(ctx context.Context, profile *domain.ToolProfileState) error {
	if err := r.ToolProvisioningRepository.UpsertProfileState(ctx, profile); err != nil {
		return err
	}
	return r.publisher.PublishToolProfile(ctx, profile, false)
}
func (r *CanonicalToolRepository) AddToDenylist(ctx context.Context, entry *domain.ToolDenylistEntry) error {
	if err := r.ToolProvisioningRepository.AddToDenylist(ctx, entry); err != nil {
		return err
	}
	return r.publisher.PublishToolDenylist(ctx, entry, false)
}
func (r *CanonicalToolRepository) RemoveFromDenylist(ctx context.Context, packageName, manager string) error {
	if err := r.ToolProvisioningRepository.RemoveFromDenylist(ctx, packageName, manager); err != nil {
		return err
	}
	return r.publisher.PublishToolDenylist(ctx, &domain.ToolDenylistEntry{PackageName: packageName, Manager: manager}, true)
}

// CanonicalNotificationRepository serializes the DB write and window query so
// concurrent deliveries cannot publish an older snapshot after a newer one.
type CanonicalNotificationRepository struct {
	repository.NotificationRepository
	publisher *F74bCanonicalPublisher
	mu        sync.Mutex
}

func NewCanonicalNotificationRepository(inner repository.NotificationRepository, publisher *F74bCanonicalPublisher) *CanonicalNotificationRepository {
	return &CanonicalNotificationRepository{NotificationRepository: inner, publisher: publisher}
}
func (r *CanonicalNotificationRepository) publishWindow(ctx context.Context, channelID uuid.UUID) error {
	logs, err := r.NotificationRepository.ListLogsByChannel(ctx, channelID, NotificationLogWindowLimit)
	if err != nil {
		return err
	}
	return r.publisher.PublishNotificationWindow(ctx, channelID, logs, false)
}
func (r *CanonicalNotificationRepository) CreateLog(ctx context.Context, log *domain.NotificationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.NotificationRepository.CreateLog(ctx, log); err != nil {
		return err
	}
	return r.publishWindow(ctx, log.ChannelID)
}
func (r *CanonicalNotificationRepository) UpdateLog(ctx context.Context, log *domain.NotificationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.NotificationRepository.UpdateLog(ctx, log); err != nil {
		return err
	}
	return r.publishWindow(ctx, log.ChannelID)
}
func (r *CanonicalNotificationRepository) DeleteChannel(ctx context.Context, channelID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.NotificationRepository.DeleteChannel(ctx, channelID); err != nil {
		return err
	}
	return r.publisher.PublishNotificationWindow(ctx, channelID, nil, true)
}

// Tenant-scoped notification methods preserve the encrypted ContextVM handler
// contract while sharing the same mutation-bound log publisher.
type tenantNotificationStore interface {
	GetChannelByIDForOrg(context.Context, uuid.UUID, uuid.UUID) (*domain.NotificationChannel, error)
	ListChannelsByOrg(context.Context, uuid.UUID, bool) ([]domain.NotificationChannel, error)
	UpdateChannelForOrg(context.Context, *domain.NotificationChannel, uuid.UUID) error
	DeleteChannelForOrg(context.Context, uuid.UUID, uuid.UUID) error
	ListRecentLogsByOrg(context.Context, uuid.UUID, int) ([]domain.NotificationLog, error)
}

func (r *CanonicalNotificationRepository) tenant() tenantNotificationStore {
	return r.NotificationRepository.(tenantNotificationStore)
}
func (r *CanonicalNotificationRepository) GetChannelByIDForOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.NotificationChannel, error) {
	return r.tenant().GetChannelByIDForOrg(ctx, id, orgID)
}
func (r *CanonicalNotificationRepository) ListChannelsByOrg(ctx context.Context, orgID uuid.UUID, enabledOnly bool) ([]domain.NotificationChannel, error) {
	return r.tenant().ListChannelsByOrg(ctx, orgID, enabledOnly)
}
func (r *CanonicalNotificationRepository) UpdateChannelForOrg(ctx context.Context, channel *domain.NotificationChannel, orgID uuid.UUID) error {
	return r.tenant().UpdateChannelForOrg(ctx, channel, orgID)
}
func (r *CanonicalNotificationRepository) DeleteChannelForOrg(ctx context.Context, id, orgID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.tenant().DeleteChannelForOrg(ctx, id, orgID); err != nil {
		return err
	}
	return r.publisher.PublishNotificationWindow(ctx, id, nil, true)
}
func (r *CanonicalNotificationRepository) ListRecentLogsByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	return r.tenant().ListRecentLogsByOrg(ctx, orgID, limit)
}

package nostr

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"go.uber.org/zap"
)

type f74bToolRepo struct {
	repository.ToolProvisioningRepository
	intents int
	denied  map[string]domain.ToolDenylistEntry
	profile *domain.ToolProfileState
}

func (r *f74bToolRepo) CreateIntent(_ context.Context, _ *domain.ToolProvisionIntent) error {
	r.intents++
	return nil
}
func (r *f74bToolRepo) UpdateIntent(_ context.Context, _ *domain.ToolProvisionIntent) error {
	r.intents++
	return nil
}
func (r *f74bToolRepo) UpsertProfileState(_ context.Context, state *domain.ToolProfileState) error {
	r.profile = state
	return nil
}
func (r *f74bToolRepo) AddToDenylist(_ context.Context, entry *domain.ToolDenylistEntry) error {
	r.denied[entry.PackageName] = *entry
	return nil
}
func (r *f74bToolRepo) RemoveFromDenylist(_ context.Context, name, _ string) error {
	delete(r.denied, name)
	return nil
}

func f74bTestPublisher(t *testing.T) (*F74bCanonicalPublisher, *captureProjectionPublisher, *mockConfidentialEncryptor) {
	t.Helper()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	encryptor := &mockConfidentialEncryptor{}
	return NewF74bCanonicalPublisher(projector, encryptor), sink, encryptor
}

func TestF74bToolMutationPublishingAndTombstone(t *testing.T) {
	ctx := context.Background()
	pub, sink, enc := f74bTestPublisher(t)
	inner := &f74bToolRepo{denied: map[string]domain.ToolDenylistEntry{}}
	repo := NewCanonicalToolRepository(inner, pub)
	intent := &domain.ToolProvisionIntent{ID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(), Status: domain.ToolProvisionStatusPending}
	if err := repo.CreateIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intent.Status = domain.ToolProvisionStatusValidating
	if err := repo.UpdateIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	profile := &domain.ToolProfileState{ServiceID: intent.ServiceID, EnvironmentID: intent.EnvironmentID, CurrentToolsetHash: "hash"}
	if err := repo.UpsertProfileState(ctx, profile); err != nil {
		t.Fatal(err)
	}
	entry := &domain.ToolDenylistEntry{PackageName: "private-package", Manager: "npm", Reason: "operator policy", BlockedAt: time.Now().UTC()}
	if err := repo.AddToDenylist(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveFromDenylist(ctx, entry.PackageName, entry.Manager); err != nil {
		t.Fatal(err)
	}
	if inner.intents != 2 || inner.profile == nil || len(inner.denied) != 0 {
		t.Fatal("mutations did not reach the repository exactly once")
	}
	events := sink.byKind(KindCASControlState)
	if len(events) != 5 {
		t.Fatalf("got %d canonical events, want one per mutation", len(events))
	}
	wantKinds := []int{KindToolProvisionIntentState, KindToolProvisionIntentState, KindToolProfileState, KindToolDenylistState, KindToolDenylistState}
	wantTopics := []string{kinds.CPStateTopicToolProvisionIntent, kinds.CPStateTopicToolProvisionIntent, kinds.CPStateTopicToolProfile, kinds.CPStateTopicToolDenylist, kinds.CPStateTopicToolDenylist}
	for i, ev := range events {
		if !hasTag(ev.Tags, "legacy_kind", strconv.Itoa(wantKinds[i])) {
			t.Fatalf("event %d wrong kind: %v", i, ev.Tags)
		}
		if !hasTag(ev.Tags, "t", wantTopics[i]) {
			t.Fatalf("event %d wrong topic: %v", i, ev.Tags)
		}
	}
	if !hasTag(events[4].Tags, "deleted", "true") || !hasTag(events[3].Tags, "d", toolDenylistDTag(entry.PackageName, entry.Manager)) || !hasTag(events[4].Tags, "d", toolDenylistDTag(entry.PackageName, entry.Manager)) {
		t.Fatal("denylist tombstone did not replace live coordinate")
	}
	if enc.lastOrgID != kinds.FleetOCKScope {
		t.Fatalf("OCK scope %q", enc.lastOrgID)
	}
}

func TestF74bNotificationWindowIsBoundedAndTombstoned(t *testing.T) {
	ctx := context.Background()
	pub, sink, enc := f74bTestPublisher(t)
	channelID := uuid.New()
	logs := make([]domain.NotificationLog, 80)
	for i := range logs {
		logs[i] = domain.NotificationLog{ID: uuid.New(), ChannelID: channelID, Payload: map[string]any{"recipient": strings.Repeat("secret", 200)}, LastError: strings.Repeat("x", 600)}
	}
	if err := pub.PublishNotificationWindow(ctx, channelID, logs, false); err != nil {
		t.Fatal(err)
	}
	events := sink.byKind(KindCASControlState)
	if len(events) != 1 {
		t.Fatalf("got %d events, want one window", len(events))
	}
	var envelope struct {
		OrgVisible json.RawMessage `json:"_org_visible"`
	}
	if err := json.Unmarshal([]byte(events[0].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	var window struct {
		Logs []domain.NotificationLog `json:"logs"`
	}
	if err := json.Unmarshal(envelope.OrgVisible, &window); err != nil {
		t.Fatal(err)
	}
	if len(window.Logs) > NotificationLogWindowLimit || len(envelope.OrgVisible) > canonicalPlaintextLimit {
		t.Fatalf("unbounded window: count=%d bytes=%d", len(window.Logs), len(envelope.OrgVisible))
	}
	if window.Logs[0].Payload["truncated"] != true {
		t.Fatal("oversized payload not truncated")
	}
	if enc.lastOrgID != kinds.FleetOCKScope {
		t.Fatal("notification log not fleet encrypted")
	}
	if err := pub.PublishNotificationWindow(ctx, channelID, nil, true); err != nil {
		t.Fatal(err)
	}
	events = sink.byKind(KindCASControlState)
	if len(events) != 2 || !hasTag(events[1].Tags, "deleted", "true") || !hasTag(events[1].Tags, "d", notificationLogDTag(channelID)) {
		t.Fatal("notification window tombstone coordinate mismatch")
	}
}

func TestF74bConfidentialPublisherFailsClosed(t *testing.T) {
	pub, sink, _ := f74bTestPublisher(t)
	pub.SetEncryptor(nil)
	if err := pub.PublishToolIntent(context.Background(), &domain.ToolProvisionIntent{ID: uuid.New()}); err == nil {
		t.Fatal("missing OCK encryptor accepted")
	}
	if got := len(sink.byKind(KindCASControlState)); got != 0 {
		t.Fatalf("published %d plaintext events", got)
	}
}

type f74bPackageRepo struct {
	repository.PackageControlPlaneRepository
	repository.PackageAuthorizationStore
	upserts   int
	claims    int
	approvals int
	intent    *domain.PackageIntent
}

func (r *f74bPackageRepo) UpsertIntent(_ context.Context, intent *domain.PackageIntent) error {
	r.upserts++
	copy := *intent
	r.intent = &copy
	return nil
}
func (r *f74bPackageRepo) GetIntentByRequestEventID(_ context.Context, _ string) (*domain.PackageIntent, error) {
	return r.intent, nil
}
func (r *f74bPackageRepo) ClaimPackageRequest(_ context.Context, claim repository.PackageRequestClaim) (*repository.PackageRequestClaim, bool, error) {
	r.claims++
	return &claim, true, nil
}
func (r *f74bPackageRepo) CompletePackageRequest(context.Context, string) error {
	r.claims++
	return nil
}
func (r *f74bPackageRepo) CreatePackageApproval(context.Context, repository.PackageApproval) error {
	r.approvals++
	return nil
}
func (r *f74bPackageRepo) ConsumePackageApproval(context.Context, uuid.UUID, string, string, string, []string) (string, error) {
	r.approvals++
	return "operator", nil
}

func TestF74bPackageIntentAndApprovalMutationPublishing(t *testing.T) {
	ctx := context.Background()
	pub, sink, _ := f74bTestPublisher(t)
	inner := &f74bPackageRepo{}
	repo := NewCanonicalPackageRepository(inner, inner, pub)
	intent := &domain.PackageIntent{ID: uuid.New(), RequestEventID: "request-1", Status: domain.PackageIntentStatusAccepted}
	if err := repo.UpsertIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	claim := repository.PackageRequestClaim{EventID: "request-2", Requester: "requester", Method: "package/publish", Token: "token", Fingerprint: "hash"}
	if _, fresh, err := repo.ClaimPackageRequest(ctx, claim); err != nil || !fresh {
		t.Fatalf("claim fresh=%v err=%v", fresh, err)
	}
	if err := repo.CompletePackageRequest(ctx, claim.EventID); err != nil {
		t.Fatal(err)
	}
	approval := repository.PackageApproval{ID: uuid.New(), Requester: "requester", Approver: "operator", Method: "package/publish", PlanHash: "hash", EventID: "request-2"}
	if err := repo.CreatePackageApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ConsumePackageApproval(ctx, approval.ID, approval.Requester, approval.Method, approval.PlanHash, []string{"operator"}); err != nil {
		t.Fatal(err)
	}
	if inner.upserts != 1 || inner.claims != 2 || inner.approvals != 2 {
		t.Fatal("package mutation did not reach repository exactly once")
	}
	if got := len(sink.byKind(KindCASControlState)); got != 5 {
		t.Fatalf("canonical publications=%d, want 5", got)
	}
	if err := pub.publishPackageIntentDeleted(ctx, intent.RequestEventID); err != nil {
		t.Fatal(err)
	}
	events := sink.byKind(KindCASControlState)
	if !hasTag(events[5].Tags, "deleted", "true") || !hasTag(events[5].Tags, "d", packageIntentDTag(intent.RequestEventID)) {
		t.Fatal("package tombstone coordinate mismatch")
	}
}

type f74bNotificationRepo struct {
	repository.NotificationRepository
	logs    []domain.NotificationLog
	deletes int
}

func (r *f74bNotificationRepo) CreateLog(_ context.Context, log *domain.NotificationLog) error {
	r.logs = append(r.logs, *log)
	return nil
}
func (r *f74bNotificationRepo) UpdateLog(_ context.Context, log *domain.NotificationLog) error {
	for i := range r.logs {
		if r.logs[i].ID == log.ID {
			r.logs[i] = *log
			return nil
		}
	}
	return nil
}
func (r *f74bNotificationRepo) ListLogsByChannel(_ context.Context, id uuid.UUID, _ int) ([]domain.NotificationLog, error) {
	out := []domain.NotificationLog{}
	for _, log := range r.logs {
		if log.ChannelID == id {
			out = append(out, log)
		}
	}
	return out, nil
}
func (r *f74bNotificationRepo) DeleteChannel(context.Context, uuid.UUID) error {
	r.deletes++
	return nil
}

func TestF74bNotificationRepositoryPublishesOnCreateUpdateDelete(t *testing.T) {
	ctx := context.Background()
	pub, sink, _ := f74bTestPublisher(t)
	inner := &f74bNotificationRepo{}
	repo := NewCanonicalNotificationRepository(inner, pub)
	channelID := uuid.New()
	log := &domain.NotificationLog{ID: uuid.New(), ChannelID: channelID, Status: domain.NotificationStatusPending}
	if err := repo.CreateLog(ctx, log); err != nil {
		t.Fatal(err)
	}
	log.Status = domain.NotificationStatusSent
	if err := repo.UpdateLog(ctx, log); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteChannel(ctx, channelID); err != nil {
		t.Fatal(err)
	}
	if len(inner.logs) != 1 || inner.logs[0].Status != domain.NotificationStatusSent || inner.deletes != 1 {
		t.Fatal("notification mutations did not reach repository")
	}
	events := sink.byKind(KindCASControlState)
	if len(events) != 3 || !hasTag(events[2].Tags, "deleted", "true") {
		t.Fatalf("expected create/update/tombstone, got %d", len(events))
	}
	for _, ev := range events {
		if !hasTag(ev.Tags, "d", notificationLogDTag(channelID)) {
			t.Fatal("notification coordinate drifted")
		}
	}
}

func TestF74bToolIntentAndProfileTombstones(t *testing.T) {
	ctx := context.Background()
	pub, sink, _ := f74bTestPublisher(t)
	intent := &domain.ToolProvisionIntent{ID: uuid.New()}
	profile := &domain.ToolProfileState{ServiceID: uuid.New(), EnvironmentID: uuid.New()}
	if err := pub.PublishToolIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := pub.publishToolIntentDeleted(ctx, intent.ID); err != nil {
		t.Fatal(err)
	}
	if err := pub.PublishToolProfile(ctx, profile, false); err != nil {
		t.Fatal(err)
	}
	if err := pub.PublishToolProfile(ctx, profile, true); err != nil {
		t.Fatal(err)
	}
	events := sink.byKind(KindCASControlState)
	if len(events) != 4 {
		t.Fatalf("got %d events", len(events))
	}
	if !hasTag(events[1].Tags, "deleted", "true") || !hasTag(events[1].Tags, "d", toolIntentDTag(intent.ID)) {
		t.Fatal("tool intent tombstone mismatched")
	}
	if !hasTag(events[3].Tags, "deleted", "true") || !hasTag(events[3].Tags, "d", toolProfileDTag(profile.ServiceID, profile.EnvironmentID)) {
		t.Fatal("tool profile tombstone mismatched")
	}
}

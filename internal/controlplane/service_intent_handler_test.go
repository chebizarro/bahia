package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// --- test doubles ---

// memServiceRepo is an in-memory ServiceRepository for tests.
type memServiceRepo struct {
	mu       sync.RWMutex
	services map[uuid.UUID]*domain.Service
}

func newMemServiceRepo() *memServiceRepo {
	return &memServiceRepo{services: make(map[uuid.UUID]*domain.Service)}
}

func (r *memServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, ok := r.services[id]
	if !ok {
		return nil, nil
	}
	cp := *svc
	return &cp, nil
}

func (r *memServiceRepo) put(svc *domain.Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *svc
	r.services[svc.ID] = &cp
}

func (r *memServiceRepo) del(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, id)
}

// serviceIntentMutationBackend satisfies RegistryMutationBackend for tests
// using the in-memory repo.
type serviceIntentMutationBackend struct {
	repo *memServiceRepo
}

func (b *serviceIntentMutationBackend) CreateService(_ context.Context, svc *domain.Service) error {
	if svc.ID == uuid.Nil {
		svc.ID = domain.NewEntityID()
	}
	if svc.RuntimeType == "" {
		svc.RuntimeType = domain.RuntimeTypeDocker
	}
	if svc.DefaultBranch == "" {
		svc.DefaultBranch = "main"
	}
	now := time.Now().UTC()
	if svc.CreatedAt.IsZero() {
		svc.CreatedAt = now
	}
	if svc.UpdatedAt.IsZero() {
		svc.UpdatedAt = now
	}
	b.repo.put(svc)
	return nil
}

func (b *serviceIntentMutationBackend) UpdateService(_ context.Context, svc *domain.Service) error {
	svc.UpdatedAt = time.Now().UTC()
	b.repo.put(svc)
	return nil
}

func (b *serviceIntentMutationBackend) UpdateServiceWithExpectedRevision(ctx context.Context, svc *domain.Service, expectedUpdatedAt time.Time) error {
	existing, _ := b.repo.GetByID(ctx, svc.ID)
	if existing == nil {
		return fmt.Errorf("service %s not found", svc.ID)
	}
	if !domain.SameRevision(existing.UpdatedAt, expectedUpdatedAt) {
		return fmt.Errorf("service %s revision conflict (expected %s, actual %s)",
			svc.ID, expectedUpdatedAt.UTC().Format(time.RFC3339Nano),
			existing.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	svc.UpdatedAt = time.Now().UTC()
	b.repo.put(svc)
	return nil
}

func (b *serviceIntentMutationBackend) DeleteService(_ context.Context, id uuid.UUID, _ bool) error {
	b.repo.del(id)
	return nil
}

func (b *serviceIntentMutationBackend) ImportObservedArtifact(_ context.Context, _ service.ImportObservedArtifactInput) (*service.ImportObservedArtifactResult, error) {
	return nil, nil
}

func (b *serviceIntentMutationBackend) CreateEnvironment(_ context.Context, _ *domain.Environment) error {
	return nil
}

func (b *serviceIntentMutationBackend) CreateEnvironmentWithDeploymentUnits(_ context.Context, _ *domain.Environment, _ []*domain.DeploymentUnit) error {
	return nil
}

func (b *serviceIntentMutationBackend) GetEnvironment(_ context.Context, _ uuid.UUID) (*domain.Environment, error) {
	return nil, nil
}

func (b *serviceIntentMutationBackend) UpdateEnvironment(_ context.Context, _ *domain.Environment) error {
	return nil
}

func (b *serviceIntentMutationBackend) UpdateEnvironmentWithDeploymentUnits(_ context.Context, _ *domain.Environment, _ []*domain.DeploymentUnit, _ time.Time) error {
	return nil
}

func (b *serviceIntentMutationBackend) DeleteEnvironment(_ context.Context, _ uuid.UUID, _ bool) error {
	return nil
}

func (b *serviceIntentMutationBackend) RegisterArtifact(_ context.Context, _ *domain.Artifact) error {
	return nil
}

// --- test helpers ---

type svcIntentFixture struct {
	processor *IntentProcessor
	handler   *ServiceIntentHandler
	repo      *memServiceRepo
	backend   *serviceIntentMutationBackend
	trustSet  *TrustSet
	ownerPub  string
	logger    *zap.Logger
}

func newSvcIntentFixture(t *testing.T) *svcIntentFixture {
	t.Helper()
	logger := zap.NewNop()

	// Generate a real keypair for the owner.
	_, ownerPub := testNostrKeypair()

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	repo := newMemServiceRepo()
	backend := &serviceIntentMutationBackend{repo: repo}

	trustSet := NewTrustSet(nil, logger,
		WithBootstrapOwners(map[string]string{
			orgID.String(): ownerPub,
		}),
	)

	processor := NewIntentProcessor(
		trustSet, nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"service": true}},
		logger,
	)

	handler := NewServiceIntentHandler(ServiceIntentHandlerConfig{
		Registry: backend,
		Reader:   repo,
		Logger:   logger,
	})
	processor.RegisterHandler("service", handler)

	return &svcIntentFixture{
		processor: processor,
		handler:   handler,
		repo:      repo,
		backend:   backend,
		trustSet:  trustSet,
		ownerPub:  ownerPub,
		logger:    logger,
	}
}


func makeTestServiceIntent(t *testing.T, op string, svc *domain.Service, intentID, pubkeyHex string) *nostr.Event {
	t.Helper()

	content := map[string]interface{}{
		"id":             svc.ID.String(),
		"name":           svc.Name,
		"repo_url":       svc.RepoURL,
		"artifact_repo":  svc.ArtifactRepo,
		"default_branch": svc.DefaultBranch,
		"runtime_type":   string(svc.RuntimeType),
		"org_id":         svc.OrgID.String(),
	}
	contentJSON, _ := json.Marshal(content)

	ev := &nostr.Event{
		Kind:      30900,
		PubKey:    testNostrPubKeyFromHex(t, pubkeyHex),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", svc.ID.String()},
			{"domain", "service"},
			{"schema", "bahia.intent.service.v1"},
			{"t", "bahia-intent"},
			{"t", "service-registry"},
			{"op", op},
			{"org", svc.OrgID.String()},
			{"intent_id", intentID},
		},
		Content: string(contentJSON),
	}
	ev.ID = ev.GetID()
	return ev
}

// --- tests ---

func TestServiceIntentHandler_CreateViaRelayIntent(t *testing.T) {
	f := newSvcIntentFixture(t)
	ctx := context.Background()

	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		RepoURL:      "https://github.com/example/api",
		ArtifactRepo: "registry.example/api",
		RuntimeType:  domain.RuntimeTypeDocker,
	}
	ev := makeTestServiceIntent(t, "create", svc, uuid.New().String(), f.ownerPub)

	err := f.processor.ProcessRelayIntent(ctx, ev)
	if err != nil {
		t.Fatalf("ProcessRelayIntent failed: %v", err)
	}

	stored, err := f.repo.GetByID(ctx, svc.ID)
	if err != nil || stored == nil {
		t.Fatal("service not created after relay intent")
	}
	if stored.Name != "api" {
		t.Errorf("expected name 'api', got %q", stored.Name)
	}
	if stored.ArtifactRepo != "registry.example/api" {
		t.Errorf("expected artifact_repo 'registry.example/api', got %q", stored.ArtifactRepo)
	}
}

func TestServiceIntentHandler_UpdateViaContextVM_ProducesIdenticalState(t *testing.T) {
	f := newSvcIntentFixture(t)
	ctx := context.Background()

	// Create via relay intent.
	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		RepoURL:      "https://github.com/example/api",
		ArtifactRepo: "registry.example/api",
		RuntimeType:  domain.RuntimeTypeDocker,
	}
	ev := makeTestServiceIntent(t, "create", svc, uuid.New().String(), f.ownerPub)

	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create via relay failed: %v", err)
	}

	// Update via in-process dispatch (simulating ContextVM dual dispatch).
	intent := &Intent{
		Domain:     "service",
		Op:         "update",
		OrgID:      testOrgID(),
		IntentID:   uuid.New().String(),
		Coordinate: svc.ID.String(),
		Actor:      f.ownerPub,
		Content: map[string]interface{}{
			"id":             svc.ID.String(),
			"name":           "api-v2",
			"repo_url":       "https://github.com/example/api",
			"artifact_repo":  "registry.example/api",
			"default_branch": "main",
			"runtime_type":   "docker",
			"org_id":         testOrgID().String(),
		},
	}

	if err := f.processor.ProcessInProcess(ctx, intent); err != nil {
		t.Fatalf("update via in-process failed: %v", err)
	}

	stored, err := f.repo.GetByID(ctx, svc.ID)
	if err != nil || stored == nil {
		t.Fatal("service not found after update")
	}
	if stored.Name != "api-v2" {
		t.Errorf("expected name 'api-v2' after ContextVM update, got %q", stored.Name)
	}
}

func TestServiceIntentHandler_LevelTriggered_ColdDaemonCreateFromUpdate(t *testing.T) {
	// A cold daemon sees only an "update" intent for an entity it has never seen.
	// Level-triggered: it should create the entity.
	f := newSvcIntentFixture(t)
	ctx := context.Background()

	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "worker",
		RepoURL:      "https://github.com/example/worker",
		ArtifactRepo: "registry.example/worker",
		RuntimeType:  domain.RuntimeTypeDocker,
	}
	ev := makeTestServiceIntent(t, "update", svc, uuid.New().String(), f.ownerPub)

	err := f.processor.ProcessRelayIntent(ctx, ev)
	if err != nil {
		t.Fatalf("ProcessRelayIntent for update on cold daemon failed: %v", err)
	}

	stored, err := f.repo.GetByID(ctx, svc.ID)
	if err != nil || stored == nil {
		t.Fatal("service not created by level-triggered update on cold daemon")
	}
	if stored.Name != "worker" {
		t.Errorf("expected name 'worker', got %q", stored.Name)
	}
}

func TestServiceIntentHandler_StaleRevision_Conflict(t *testing.T) {
	f := newSvcIntentFixture(t)
	ctx := context.Background()

	// Create a service first.
	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		ArtifactRepo: "registry.example/api",
		RuntimeType:  domain.RuntimeTypeDocker,
	}
	if err := f.backend.CreateService(ctx, svc); err != nil {
		t.Fatalf("create service: %v", err)
	}

	// Stale timestamp (1 hour before the entity's actual UpdatedAt).
	staleTime := svc.UpdatedAt.Add(-1 * time.Hour)
	staleEpoch := staleTime.UnixNano()

	intent := &Intent{
		Domain:            "service",
		Op:                "update",
		OrgID:             testOrgID(),
		IntentID:          uuid.New().String(),
		Coordinate:        svc.ID.String(),
		Actor:             f.ownerPub,
		ExpectedUpdatedAt: &staleEpoch,
		Content: map[string]interface{}{
			"id":                  svc.ID.String(),
			"name":                "api-updated",
			"artifact_repo":       "registry.example/api",
			"runtime_type":        "docker",
			"org_id":              testOrgID().String(),
			"expected_updated_at": staleTime.Format(time.RFC3339Nano),
		},
	}

	err := f.processor.ProcessInProcess(ctx, intent)
	if err == nil {
		t.Fatal("expected revision conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "revision conflict") {
		t.Errorf("expected 'revision conflict' in error, got: %v", err)
	}

	// Verify the service was not updated.
	stored, _ := f.repo.GetByID(ctx, svc.ID)
	if stored.Name != "api" {
		t.Errorf("expected name to remain 'api', got %q", stored.Name)
	}
}

func TestServiceIntentHandler_UnauthorizedKnownPrincipal_Rejection(t *testing.T) {
	logger := zap.NewNop()
	repo := newMemServiceRepo()
	backend := &serviceIntentMutationBackend{repo: repo}

	// Generate a known principal with viewer role (no write permission).
	_, viewerPub := testNostrKeypair()

	trustSet := NewTrustSet(nil, logger)
	trustSet.SetRelayMembers(testOrgID().String(), map[string]domain.Role{
		viewerPub: domain.RoleViewer,
	})

	processor := NewIntentProcessor(
		trustSet, nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"service": true}},
		logger,
	)
	handler := NewServiceIntentHandler(ServiceIntentHandlerConfig{
		Registry: backend,
		Reader:   repo,
		Logger:   logger,
	})
	processor.RegisterHandler("service", handler)

	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		ArtifactRepo: "registry.example/api",
	}
	ev := makeTestServiceIntent(t, "create", svc, uuid.New().String(), viewerPub)

	err := processor.ProcessRelayIntent(context.Background(), ev)
	if err == nil {
		t.Fatal("expected rejection error for known principal with insufficient permission")
	}
	if !strings.Contains(err.Error(), "insufficient permission") {
		t.Errorf("expected 'insufficient permission' in error, got: %v", err)
	}

	// Verify service was not created.
	stored, _ := repo.GetByID(context.Background(), svc.ID)
	if stored != nil {
		t.Error("service should not have been created for unauthorized known principal")
	}
}

func TestServiceIntentHandler_UntrustedAuthor_DroppedSilently(t *testing.T) {
	logger := zap.NewNop()
	repo := newMemServiceRepo()
	backend := &serviceIntentMutationBackend{repo: repo}

	_, ownerPub := testNostrKeypair()
	_, untrustedPub := testNostrKeypair()

	// TrustSet with only the owner. The untrusted key is not in it.
	trustSet := NewTrustSet(nil, logger,
		WithBootstrapOwners(map[string]string{
			testOrgID().String(): ownerPub,
		}),
	)

	processor := NewIntentProcessor(
		trustSet, nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"service": true}},
		logger,
	)
	handler := NewServiceIntentHandler(ServiceIntentHandlerConfig{
		Registry: backend,
		Reader:   repo,
		Logger:   logger,
	})
	processor.RegisterHandler("service", handler)

	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		ArtifactRepo: "registry.example/api",
	}
	ev := makeTestServiceIntent(t, "create", svc, uuid.New().String(), untrustedPub)

	// Untrusted authors are dropped silently — no error.
	err := processor.ProcessRelayIntent(context.Background(), ev)
	if err != nil {
		t.Fatalf("expected silent drop for untrusted author, got error: %v", err)
	}

	// Verify service was not created.
	stored, _ := repo.GetByID(context.Background(), svc.ID)
	if stored != nil {
		t.Error("service should not have been created for untrusted author")
	}
}

func TestServiceIntentHandler_NoDB_StateSurvives(t *testing.T) {
	// The in-memory repo simulates no Postgres — no txExecutor, no DB.
	// Intent processing should work end-to-end using only the memory backend.
	f := newSvcIntentFixture(t)
	ctx := context.Background()

	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		RepoURL:      "https://github.com/example/api",
		ArtifactRepo: "registry.example/api",
		RuntimeType:  domain.RuntimeTypeDocker,
	}
	ev := makeTestServiceIntent(t, "create", svc, uuid.New().String(), f.ownerPub)

	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Service exists in the in-memory store (no DB).
	stored, err := f.repo.GetByID(ctx, svc.ID)
	if err != nil || stored == nil {
		t.Fatal("service not found in memory store — state did not survive without DB")
	}

	// Update it, verify state persists.
	updateIntent := &Intent{
		Domain:     "service",
		Op:         "update",
		OrgID:      testOrgID(),
		IntentID:   uuid.New().String(),
		Coordinate: svc.ID.String(),
		Actor:      f.ownerPub,
		Content: map[string]interface{}{
			"id":            svc.ID.String(),
			"name":          "api-updated",
			"artifact_repo": "registry.example/api",
			"runtime_type":  "docker",
			"org_id":        testOrgID().String(),
		},
	}

	if err := f.processor.ProcessInProcess(ctx, updateIntent); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	stored, _ = f.repo.GetByID(ctx, svc.ID)
	if stored == nil || stored.Name != "api-updated" {
		t.Fatal("service update did not survive without DB")
	}
}

func TestServiceIntentHandler_DeleteViaIntent(t *testing.T) {
	f := newSvcIntentFixture(t)
	ctx := context.Background()

	// Create a service first.
	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		ArtifactRepo: "registry.example/api",
	}
	if err := f.backend.CreateService(ctx, svc); err != nil {
		t.Fatalf("create service: %v", err)
	}

	// Delete via relay intent.
	deleteContent := map[string]interface{}{
		"id":      svc.ID.String(),
		"deleted": true,
	}
	contentJSON, _ := json.Marshal(deleteContent)
	ev := &nostr.Event{
		Kind:      30900,
		PubKey:    testNostrPubKeyFromHex(t, f.ownerPub),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", svc.ID.String()},
			{"domain", "service"},
			{"schema", "bahia.intent.service.v1"},
			{"t", "bahia-intent"},
			{"op", "delete"},
			{"org", testOrgID().String()},
			{"intent_id", uuid.New().String()},
		},
		Content: string(contentJSON),
	}
	ev.ID = ev.GetID()

	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("delete via relay intent failed: %v", err)
	}

	stored, _ := f.repo.GetByID(ctx, svc.ID)
	if stored != nil {
		t.Error("service should have been deleted")
	}
}

func TestServiceIntentHandler_DualDispatch_LevelTriggeredReconciliation(t *testing.T) {
	// A create via in-process, then the same intent arrives from the relay.
	// Without a local store for dedup, the handler uses level-triggered
	// reconciliation: it sees the entity exists and updates (harmless merge).
	f := newSvcIntentFixture(t)
	ctx := context.Background()

	svc := &domain.Service{
		ID:           domain.NewEntityID(),
		OrgID:        testOrgID(),
		Name:         "api",
		ArtifactRepo: "registry.example/api",
		RuntimeType:  domain.RuntimeTypeDocker,
	}

	// Create via in-process (ContextVM path).
	intent := &Intent{
		Domain:     "service",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   uuid.New().String(),
		Coordinate: svc.ID.String(),
		Actor:      f.ownerPub,
		Content: map[string]interface{}{
			"id":            svc.ID.String(),
			"name":          "api",
			"artifact_repo": "registry.example/api",
			"runtime_type":  "docker",
			"org_id":        testOrgID().String(),
		},
	}
	if err := f.processor.ProcessInProcess(ctx, intent); err != nil {
		t.Fatalf("ProcessInProcess failed: %v", err)
	}

	stored, _ := f.repo.GetByID(ctx, svc.ID)
	if stored == nil {
		t.Fatal("service not created via in-process")
	}

	// Same create arrives from relay — should reconcile gracefully.
	ev := makeTestServiceIntent(t, "create", svc, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("ProcessRelayIntent after ProcessInProcess failed: %v", err)
	}

	// State should be unchanged.
	stored, _ = f.repo.GetByID(ctx, svc.ID)
	if stored == nil || stored.Name != "api" {
		t.Error("service state changed unexpectedly after dual dispatch")
	}
}

package controlplane

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestHandleBackupRunRequestCreatesDurableRunAndInvokesExecutor(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, _ := nostr.GetPublicKey(requestKey)
	registry, recipe := newBackupRequestRegistryFixture()
	executor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 1)}
	responder := &recordingBackupRunResponder{}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop())
	reactor.backupRegistry = registry
	reactor.backupExecutor = executor
	reactor.backupResponder = responder
	request := signedLLMRequest(t, requestKey, KindBackupRunRequest, `{"recipe":"recipe:daily:v1","metadata":{"site":"dc1"}}`, nostr.Tags{{"d", "backup:daily:prod"}, {"recipe", "recipe:daily:v1"}, {"site", "dc1"}})

	reactor.handleBackupRunRequest(ctx, request)

	select {
	case runID := <-executor.calls:
		run := registry.runs[runID]
		if run == nil {
			t.Fatalf("executor got run %s but registry has no run", runID)
		}
		if run.RecipeID != recipe.ID || run.RequestEventID != request.ID || run.RequestDTag != "backup:daily:prod" {
			t.Fatalf("unexpected run: %#v", run)
		}
		if run.Metadata["nostr_request_pubkey"] != requestPubkey || run.Metadata["nostr_recipe_coord"] != "recipe:daily:v1" {
			t.Fatalf("missing Nostr metadata: %#v", run.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("backup executor was not invoked")
	}
	if len(registry.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(registry.runs))
	}
	if got := responder.statusSteps; len(got) != 1 || got[0] != "queued" {
		t.Fatalf("status steps = %#v, want queued", got)
	}
}

func TestHandleBackupRunRequestIsIdempotentByRequesterKindAndDTag(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, _ := nostr.GetPublicKey(requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	executor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 2)}
	responder := &recordingBackupRunResponder{}
	signer, _ := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop())
	reactor.backupRegistry = registry
	reactor.backupExecutor = executor
	reactor.backupResponder = responder
	first := signedLLMRequest(t, requestKey, KindBackupRunRequest, `{"recipe":"recipe:daily:v1"}`, nostr.Tags{{"d", "backup:daily:prod"}, {"recipe", "recipe:daily:v1"}})
	second := signedLLMRequest(t, requestKey, KindBackupRunRequest, `{"recipe":"recipe:daily:v1"}`, nostr.Tags{{"d", "backup:daily:prod"}, {"recipe", "recipe:daily:v1"}})

	reactor.handleBackupRunRequest(ctx, first)
	select {
	case <-executor.calls:
	case <-time.After(time.Second):
		t.Fatal("first backup executor call missing")
	}
	reactor.handleBackupRunRequest(ctx, second)

	select {
	case runID := <-executor.calls:
		t.Fatalf("duplicate request invoked executor for run %s", runID)
	default:
	}
	if len(registry.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(registry.runs))
	}
	if got := responder.statusSteps; len(got) != 2 || got[1] != "duplicate" {
		t.Fatalf("status steps = %#v, want queued then duplicate", got)
	}
}

func TestBackupRequestSubscriptionIncludesBackupRunKind(t *testing.T) {
	since := nostr.Now()
	reactor := &Reactor{config: Config{AuthorizedPubkeys: []string{"operator"}}}
	filters := reactor.buildRequestSubscriptionFilters(since)
	if len(filters) == 0 {
		t.Fatal("expected subscription filters")
	}
	found := false
	for _, kind := range filters[0].Kinds {
		if kind == KindBackupRunRequest {
			found = true
		}
	}
	if !found {
		t.Fatalf("default control-plane filter missing backup run request kind: %#v", filters[0].Kinds)
	}
	if len(filters[0].Authors) != 1 || filters[0].Authors[0] != "operator" {
		t.Fatalf("backup request filter should retain default author scope: %#v", filters[0].Authors)
	}
}

func TestBackupRunResponderPublishesResultAttestationAndPersistsPublishSummaries(t *testing.T) {
	ctx := context.Background()
	publisher := &captureBackupResultPublisher{results: []nostrpool.PublishResult{{RelayURL: "wss://relay.accepted.example", Accepted: true}, {RelayURL: "wss://relay.blocked.example", Reason: "blocked: policy denied"}}}
	registry := &recordingBackupPublishRegistry{}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	responder := NewBackupRunResponder(publisher, signer, registry, nil, zap.NewNop())
	policyID := uuid.New()
	run := &domain.BackupRun{ID: uuid.New(), RecipeID: uuid.New(), RepositoryID: uuid.New(), PolicyID: &policyID, RequestedBy: "requester", RequestEventID: "request-event", RequestKind: KindBackupRunRequest, RequestDTag: "backup:daily", Status: domain.RunStatusFailed, Backend: domain.BackupBackendKopia, TargetRef: "fs:/srv/data", SnapshotCreated: true, SnapshotID: "snap-1", VerificationStatus: domain.BackupVerificationFailed, Error: "checksum mismatch", Metadata: map[string]any{"nostr_recipe_coord": "recipe:daily:v1", "nostr_repository_name": "primary"}}
	verification := &domain.BackupVerificationRecord{ID: uuid.New(), BackupRunID: run.ID, Mode: domain.BackupVerificationKopiaSnapshotVerify, Status: domain.BackupVerificationFailed, Verified: false, Error: "checksum mismatch"}

	if err := responder.PublishBackupRunResult(ctx, run, verification, "backup run failed verification"); err != nil {
		t.Fatalf("publish result: %v", err)
	}
	if len(publisher.events) != 3 {
		t.Fatalf("published events = %d, want result + run attestation + verification attestation", len(publisher.events))
	}
	result := publisher.events[0]
	if result.Kind != KindBackupRunResult {
		t.Fatalf("result kind = %d", result.Kind)
	}
	assertReactorTag(t, result.Tags, "d", "result:"+run.RequestEventID)
	assertReactorTag(t, result.Tags, "e", run.RequestEventID)
	assertReactorTag(t, result.Tags, "p", run.RequestedBy)
	assertReactorTag(t, result.Tags, "run", run.ID.String())
	assertReactorTag(t, result.Tags, "verification", string(domain.BackupVerificationFailed))
	assertReactorTag(t, result.Tags, "repository", "primary")
	if ok, err := result.CheckSignature(); err != nil || !ok {
		t.Fatalf("result signature invalid: ok=%v err=%v", ok, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decode result content: %v", err)
	}
	if payload["restore_eligible"] != false {
		t.Fatalf("restore_eligible = %#v, want false", payload["restore_eligible"])
	}
	if len(registry.runs) == 0 || registry.runs[len(registry.runs)-1].PublishSummary["backup_run_result"] == nil || registry.runs[len(registry.runs)-1].PublishSummary["backup_run_attestation"] == nil {
		t.Fatalf("run publish summaries were not persisted: %#v", registry.runs)
	}
	if len(registry.verifications) == 0 || registry.verifications[len(registry.verifications)-1].PublishSummary["backup_verification_attestation"] == nil {
		t.Fatalf("verification publish summary was not persisted: %#v", registry.verifications)
	}
}

type recordingBackupExecutor struct{ calls chan uuid.UUID }

func (e *recordingBackupExecutor) ProcessBackupRun(_ context.Context, runID uuid.UUID) error {
	e.calls <- runID
	return nil
}

type recordingBackupRunResponder struct{ statusSteps []string }

func (r *recordingBackupRunResponder) PublishBackupRunStatus(_ context.Context, _ *domain.BackupRun, step, _ string) error {
	r.statusSteps = append(r.statusSteps, step)
	return nil
}
func (r *recordingBackupRunResponder) PublishBackupRunResult(context.Context, *domain.BackupRun, *domain.BackupVerificationRecord, string) error {
	return nil
}

type backupRequestRegistry struct {
	recipes       map[uuid.UUID]*domain.BackupRecipe
	repositories  map[uuid.UUID]*domain.BackupRepository
	policies      map[uuid.UUID]*domain.BackupPolicy
	runs          map[uuid.UUID]*domain.BackupRun
	coordinates   map[string]uuid.UUID
	verifications map[uuid.UUID]*domain.BackupVerificationRecord
}

func newBackupRequestRegistryFixture() (*backupRequestRegistry, *domain.BackupRecipe) {
	repoID := uuid.New()
	policyID := uuid.New()
	recipeID := uuid.New()
	registry := &backupRequestRegistry{recipes: map[uuid.UUID]*domain.BackupRecipe{}, repositories: map[uuid.UUID]*domain.BackupRepository{}, policies: map[uuid.UUID]*domain.BackupPolicy{}, runs: map[uuid.UUID]*domain.BackupRun{}, coordinates: map[string]uuid.UUID{}, verifications: map[uuid.UUID]*domain.BackupVerificationRecord{}}
	registry.repositories[repoID] = &domain.BackupRepository{ID: repoID, Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary"}
	registry.policies[policyID] = &domain.BackupPolicy{ID: policyID, Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
	registry.recipes[recipeID] = &domain.BackupRecipe{ID: recipeID, Name: "daily", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repoID, PolicyID: &policyID, TargetRef: "fs:/srv/data", VerificationMode: domain.BackupVerificationNone}
	return registry, registry.recipes[recipeID]
}

func (r *backupRequestRegistry) GetRecipe(_ context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	return r.recipes[id], nil
}
func (r *backupRequestRegistry) GetRecipeByNameVersion(_ context.Context, name, version string) (*domain.BackupRecipe, error) {
	for _, recipe := range r.recipes {
		if recipe.Name == name && recipe.Version == version {
			return recipe, nil
		}
	}
	return nil, nil
}
func (r *backupRequestRegistry) GetRepository(_ context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	return r.repositories[id], nil
}
func (r *backupRequestRegistry) GetPolicy(_ context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	return r.policies[id], nil
}
func (r *backupRequestRegistry) CreateBackupRunIfAbsent(_ context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error) {
	key := backupCoordinate(run.RequestedBy, run.RequestKind, run.RequestDTag)
	if existingID, ok := r.coordinates[key]; ok {
		return r.runs[existingID], false, nil
	}
	cp := *run
	r.runs[cp.ID] = &cp
	r.coordinates[key] = cp.ID
	return &cp, true, nil
}
func (r *backupRequestRegistry) GetBackupVerificationByRunID(_ context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error) {
	return r.verifications[runID], nil
}

func backupCoordinate(pubkey string, kind int, dTag string) string {
	return pubkey + "|" + strconv.Itoa(kind) + "|" + dTag
}

type captureBackupResultPublisher struct {
	events  []nostr.Event
	results []nostrpool.PublishResult
	err     error
}

func (p *captureBackupResultPublisher) PublishWithResults(_ context.Context, ev nostr.Event) ([]nostrpool.PublishResult, error) {
	p.events = append(p.events, ev)
	return p.results, p.err
}

type recordingBackupPublishRegistry struct {
	runs          []*domain.BackupRun
	verifications []*domain.BackupVerificationRecord
}

func (r *recordingBackupPublishRegistry) CreateOrUpdateBackupRun(_ context.Context, run *domain.BackupRun) error {
	cp := *run
	r.runs = append(r.runs, &cp)
	return nil
}
func (r *recordingBackupPublishRegistry) RecordBackupVerification(_ context.Context, record *domain.BackupVerificationRecord) error {
	cp := *record
	r.verifications = append(r.verifications, &cp)
	return nil
}

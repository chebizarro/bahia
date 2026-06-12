package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestHandleBackupRunRequestCreatesDurableRunAndInvokesExecutor(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, recipe := newBackupRequestRegistryFixture()
	executor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 1)}
	responder := &recordingBackupRunResponder{}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
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
		if run.RecipeID != recipe.ID || run.RequestEventID != request.ID.Hex() || run.RequestDTag != "backup:daily:prod" {
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
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	executor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 2)}
	responder := &recordingBackupRunResponder{}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
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

func TestHandleBackupRestoreRequiresApprovalBeforeExecutor(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	sourceRun := registry.addRestoreEligibleRun()
	executor := &recordingBackupRestoreExecutor{calls: make(chan uuid.UUID, 1)}
	responder := &recordingBackupRestoreResponder{}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop())
	reactor.backupRegistry = registry
	reactor.backupRestoreExecutor = executor
	reactor.backupRestoreResponder = responder
	request := signedLLMRequest(t, requestKey, KindBackupRestoreRequest, fmt.Sprintf(`{"backup_run_id":"%s","restore_target_ref":"fs:/restore"}`, sourceRun.ID), nostr.Tags{{"d", "restore:daily:prod"}, {"backup_run_id", sourceRun.ID.String()}, {"target", "fs:/restore"}})

	reactor.handleBackupRestoreRequest(ctx, request)

	select {
	case runID := <-executor.calls:
		t.Fatalf("restore executor invoked before approval for %s", runID)
	default:
	}
	if got := responder.statusSteps; len(got) != 1 || got[0] != "pending_approval" {
		t.Fatalf("restore status steps = %#v, want pending_approval", got)
	}
	var restoreID uuid.UUID
	for id := range registry.restores {
		restoreID = id
	}
	approval := signedLLMRequest(t, requestKey, KindBackupRestoreApproval, fmt.Sprintf(`{"restore_id":"%s","approved":true,"message":"operator-approved"}`, restoreID), nostr.Tags{{"d", "approve:restore:daily:prod"}, {"restore_id", restoreID.String()}, {"decision", "approved"}})

	reactor.handleBackupRestoreApproval(ctx, approval)

	select {
	case got := <-executor.calls:
		if got != restoreID {
			t.Fatalf("executor restore id = %s, want %s", got, restoreID)
		}
	case <-time.After(time.Second):
		t.Fatal("restore executor was not invoked after approval")
	}
	if len(responder.approvals) != 1 || !responder.approvals[0] {
		t.Fatalf("approval results = %#v, want approved", responder.approvals)
	}
}

func TestHandleBackupRepositoryRegisterRequestAppliesRegistryRecordAndPublishesResult(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	capture := &captureNostrPublisher{published: 1}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
	reactor.backupRegistry = registry
	request := signedLLMRequest(t, requestKey, KindBackupRepositoryRegister, `{"name":"archive","backend":"kopia","repository_uri":"kopia://archive","metadata":{"site":"west"}}`, nostr.Tags{{"d", "repository:archive"}, {"repository", "archive"}, {"backend", "kopia"}})

	reactor.handleBackupRepositoryRegisterRequest(ctx, request)

	repo := registry.repositoryByName("archive")
	if repo == nil || repo.RepositoryURI != "kopia://archive" || repo.Metadata["nostr_request_pubkey"] != requestPubkey {
		t.Fatalf("repository was not applied with Nostr metadata: %#v", repo)
	}
	if len(capture.events) != 1 || capture.events[0].Kind != KindBackupRepositoryRegisterResult {
		t.Fatalf("result events = %#v", capture.events)
	}
	assertReactorTag(t, capture.events[0].Tags, "status", "success")
	assertReactorTag(t, capture.events[0].Tags, "repository_id", repo.ID.String())
	assertSignedEvent(t, capture.events[0])
}

func TestHandleBackupPolicyApplyRequestAppliesRegistryRecordAndPublishesResult(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	capture := &captureNostrPublisher{published: 1}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
	reactor.backupRegistry = registry
	request := signedLLMRequest(t, requestKey, KindBackupPolicyApply, `{"name":"monthly-verified","require_verification":true,"verification_mode":"kopia_snapshot_verify"}`, nostr.Tags{{"d", "policy:monthly-verified"}, {"policy", "monthly-verified"}, {"verification", "kopia_snapshot_verify"}})

	reactor.handleBackupPolicyApplyRequest(ctx, request)

	policy := registry.policyByName("monthly-verified")
	if policy == nil || !policy.RequireVerification || policy.VerificationMode != domain.BackupVerificationKopiaSnapshotVerify {
		t.Fatalf("policy was not applied: %#v", policy)
	}
	if len(capture.events) != 1 || capture.events[0].Kind != KindBackupPolicyApplyResult {
		t.Fatalf("result events = %#v", capture.events)
	}
	assertReactorTag(t, capture.events[0].Tags, "policy_id", policy.ID.String())
	assertSignedEvent(t, capture.events[0])
}

func TestHandleBackupRecipeApplyRequestAppliesRegistryRecordAndPublishesResult(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	capture := &captureNostrPublisher{published: 1}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
	reactor.backupRegistry = registry
	repoID := registry.firstRepositoryID()
	policyID := registry.firstPolicyID()
	request := signedLLMRequest(t, requestKey, KindBackupRecipeApply, fmt.Sprintf(`{"name":"weekly","version":"v2","backend":"kopia","repository_id":"%s","policy_id":"%s","target_ref":"fs:/srv/weekly","verification_mode":"kopia_snapshot_verify"}`, repoID, policyID), nostr.Tags{{"d", "recipe:weekly:v2"}, {"repository_id", repoID.String()}, {"policy_id", policyID.String()}})

	reactor.handleBackupRecipeApplyRequest(ctx, request)

	recipe, err := registry.GetRecipeByNameVersion(ctx, "weekly", "v2")
	if err != nil || recipe == nil || recipe.RepositoryID != repoID || recipe.PolicyID == nil || *recipe.PolicyID != policyID {
		t.Fatalf("recipe was not applied: recipe=%#v err=%v", recipe, err)
	}
	if len(capture.events) != 1 || capture.events[0].Kind != KindBackupRecipeApplyResult {
		t.Fatalf("result events = %#v", capture.events)
	}
	assertReactorTag(t, capture.events[0].Tags, "recipe_id", recipe.ID.String())
	assertSignedEvent(t, capture.events[0])
}

func TestHandleBackupDefinitionApplyRequestAppliesRegistryRecordAndPublishesResult(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, recipe := newBackupRequestRegistryFixture()
	capture := &captureNostrPublisher{published: 1}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithBackupDefinitionRegistry(registry))
	reactor.backupRegistry = registry
	policyID := *recipe.PolicyID
	request := signedLLMRequest(t, requestKey, KindBackupDefinitionApply, fmt.Sprintf(`{"name":"daily-prod","repository_id":"%s","policy_id":"%s","recipe_id":"%s","schedule_enabled":true,"schedule_expression":"0 2 * * *","requires_approval":true,"approval_policy":"operator"}`, recipe.RepositoryID, policyID, recipe.ID), nostr.Tags{{"d", "definition:daily-prod"}, {"definition", "daily-prod"}, {"repository_id", recipe.RepositoryID.String()}, {"policy_id", policyID.String()}, {"recipe_id", recipe.ID.String()}})

	reactor.handleBackupDefinitionApplyRequest(ctx, request)

	definition := registry.definitionByName("daily-prod")
	if definition == nil || definition.RepositoryName != "primary" || definition.PolicyName != "verified" || definition.RecipeName != recipe.Name || definition.CreatedBy != requestPubkey {
		t.Fatalf("definition was not applied with resolved references: %#v", definition)
	}
	if len(capture.events) != 1 || capture.events[0].Kind != KindBackupDefinitionApplyResult {
		t.Fatalf("result events = %#v", capture.events)
	}
	assertReactorTag(t, capture.events[0].Tags, "definition_id", definition.ID.String())
	assertSignedEvent(t, capture.events[0])
}

func TestHandleBackupDefinitionApplyRequestReusesExistingDefinitionIDAndCanonicalReferenceNames(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, recipe := newBackupRequestRegistryFixture()
	policyID := *recipe.PolicyID
	existingID := uuid.New()
	registry.definitions[existingID] = &domain.BackupDefinition{ID: existingID, Name: "daily-prod", RepositoryID: recipe.RepositoryID, RepositoryName: "primary", PolicyID: policyID, PolicyName: "verified", RecipeID: recipe.ID, RecipeName: recipe.Name, RecipeVersion: recipe.Version, CreatedBy: requestPubkey}
	capture := &captureNostrPublisher{published: 1}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithBackupDefinitionRegistry(registry))
	reactor.backupRegistry = registry
	request := signedLLMRequest(t, requestKey, KindBackupDefinitionApply, fmt.Sprintf(`{"name":"daily-prod","repository_id":"%s","repository_name":"wrong","policy_id":"%s","policy_name":"wrong","recipe_id":"%s","recipe_name":"wrong","recipe_version":"wrong"}`, recipe.RepositoryID, policyID, recipe.ID), nostr.Tags{{"d", "definition:daily-prod"}})

	reactor.handleBackupDefinitionApplyRequest(ctx, request)

	definition := registry.definitions[existingID]
	if definition == nil || definition.ID != existingID || definition.RepositoryName != "primary" || definition.PolicyName != "verified" || definition.RecipeName != recipe.Name || definition.RecipeVersion != recipe.Version {
		t.Fatalf("definition was not idempotently canonicalized: %#v", definition)
	}
	if len(registry.definitions) != 1 {
		t.Fatalf("definitions = %d, want 1", len(registry.definitions))
	}
}

func TestHandleBackupVerificationRequestDoesNotReinvokeExecutorForExistingVerification(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	run := registry.addRestoreEligibleRun()
	run.VerificationMode = domain.BackupVerificationKopiaSnapshotVerify
	existing := &domain.BackupVerificationRecord{ID: uuid.New(), BackupRunID: run.ID, Mode: domain.BackupVerificationKopiaSnapshotVerify, Status: domain.BackupVerificationSucceeded, Verified: true}
	registry.verifications[run.ID] = existing
	capture := &captureNostrPublisher{published: 1}
	executor := &recordingBackupVerificationExecutor{calls: make(chan uuid.UUID, 1)}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithBackupVerificationExecutor(executor))
	reactor.backupRegistry = registry
	request := signedLLMRequest(t, requestKey, KindBackupVerificationRequest, fmt.Sprintf(`{"backup_run_id":"%s","mode":"kopia_snapshot_verify"}`, run.ID), nostr.Tags{{"d", "verify:daily"}, {"backup_run_id", run.ID.String()}})

	reactor.handleBackupVerificationRequest(ctx, request)

	select {
	case got := <-executor.calls:
		t.Fatalf("duplicate verification invoked executor for %s", got)
	default:
	}
	if len(capture.events) != 1 || capture.events[0].Kind != KindBackupVerificationResult {
		t.Fatalf("result events = %#v", capture.events)
	}
	assertReactorTag(t, capture.events[0].Tags, "status", "duplicate")
	assertReactorTag(t, capture.events[0].Tags, "verification_id", existing.ID.String())
}

func TestHandleBackupRepositoryProbeRequestPublishesQueuedResultAndInvokesExecutor(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	capture := &captureNostrPublisher{published: 1}
	executor := &recordingBackupRepositoryProbeExecutor{calls: make(chan uuid.UUID, 1)}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithBackupRepositoryProbeExecutor(executor))
	reactor.backupRegistry = registry
	repoID := registry.firstRepositoryID()
	request := signedLLMRequest(t, requestKey, KindBackupRepositoryProbe, fmt.Sprintf(`{"repository_id":"%s"}`, repoID), nostr.Tags{{"d", "probe:primary"}, {"repository_id", repoID.String()}})

	reactor.handleBackupRepositoryProbeRequest(ctx, request)

	select {
	case got := <-executor.calls:
		if got != repoID {
			t.Fatalf("probe executor repository = %s, want %s", got, repoID)
		}
	case <-time.After(time.Second):
		t.Fatal("repository probe executor was not invoked")
	}
	if len(capture.events) != 1 || capture.events[0].Kind != KindBackupRepositoryProbeResult {
		t.Fatalf("result events = %#v", capture.events)
	}
	assertReactorTag(t, capture.events[0].Tags, "status", "queued")
	assertSignedEvent(t, capture.events[0])
}

func TestHandleBackupVerificationRequestRecordsPendingVerificationAndInvokesExecutor(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	run := registry.addRestoreEligibleRun()
	run.VerificationMode = domain.BackupVerificationKopiaSnapshotVerify
	capture := &captureNostrPublisher{published: 1}
	executor := &recordingBackupVerificationExecutor{calls: make(chan uuid.UUID, 1)}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithBackupVerificationExecutor(executor))
	reactor.backupRegistry = registry
	request := signedLLMRequest(t, requestKey, KindBackupVerificationRequest, fmt.Sprintf(`{"backup_run_id":"%s","mode":"kopia_snapshot_verify"}`, run.ID), nostr.Tags{{"d", "verify:daily"}, {"backup_run_id", run.ID.String()}, {"verification_mode", "kopia_snapshot_verify"}})

	reactor.handleBackupVerificationRequest(ctx, request)

	var verificationID uuid.UUID
	select {
	case verificationID = <-executor.calls:
	case <-time.After(time.Second):
		t.Fatal("backup verification executor was not invoked")
	}
	verification := registry.verificationByID(verificationID)
	if verification == nil || verification.BackupRunID != run.ID || verification.Status != domain.BackupVerificationPending || verification.Evidence["nostr_request_pubkey"] != requestPubkey {
		t.Fatalf("verification was not recorded from request: %#v", verification)
	}
	if len(capture.events) != 1 || capture.events[0].Kind != KindBackupVerificationResult {
		t.Fatalf("result events = %#v", capture.events)
	}
	assertReactorTag(t, capture.events[0].Tags, "verification_id", verificationID.String())
	assertSignedEvent(t, capture.events[0])
}

func TestHandleBackupRetentionRequestCreatesDurableRunAndInvokesExecutor(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	registry, _ := newBackupRequestRegistryFixture()
	executor := &recordingBackupRetentionExecutor{calls: make(chan uuid.UUID, 1)}
	responder := &recordingBackupRetentionResponder{}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop())
	reactor.backupRegistry = registry
	reactor.backupRetentionExecutor = executor
	reactor.backupRetentionResponder = responder
	policyID := registry.firstPolicyID()
	repoID := registry.firstRepositoryID()
	request := signedLLMRequest(t, requestKey, KindBackupRetentionEnforce, fmt.Sprintf(`{"repository_id":"%s","policy_id":"%s","dry_run":true}`, repoID, policyID), nostr.Tags{{"d", "retention:primary:dry-run"}, {"repository_id", repoID.String()}, {"policy_id", policyID.String()}})

	reactor.handleBackupRetentionRequest(ctx, request)

	select {
	case runID := <-executor.calls:
		run := registry.retentionRuns[runID]
		if run == nil || !run.DryRun || run.RepositoryID != repoID || *run.PolicyID != policyID {
			t.Fatalf("unexpected retention run: %#v", run)
		}
	case <-time.After(time.Second):
		t.Fatal("retention executor was not invoked")
	}
	if got := responder.statusSteps; len(got) != 1 || got[0] != "queued" {
		t.Fatalf("retention status steps = %#v, want queued", got)
	}
}

func TestBackupRequestKindsAreOmittedFromRuntimeSubscription(t *testing.T) {
	since := nostr.Now()
	operatorPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	reactor := &Reactor{config: Config{AuthorizedPubkeys: []string{operatorPubkey}}}
	filters := reactor.buildRequestSubscriptionFilters(since)
	if len(filters) != 1 {
		t.Fatalf("expected one canonical runtime filter, got %d", len(filters))
	}
	for _, legacy := range backupRequestKinds() {
		for _, kind := range filters[0].Kinds {
			if kind == nostr.Kind(legacy) {
				t.Fatalf("canonical runtime filter still includes legacy backup request kind %d: %#v", legacy, filters[0].Kinds)
			}
		}
	}
	if len(filters[0].Authors) != 1 || filters[0].Authors[0].Hex() != operatorPubkey {
		t.Fatalf("canonical runtime filter should scope ContextVM reads to authorized operators: %#v", filters[0].Authors)
	}
}

func TestBackupRunResponderPublishesResultAttestationAndPersistsPublishSummaries(t *testing.T) {
	ctx := context.Background()
	publisher := &captureBackupResultPublisher{results: []nostrpool.PublishResult{{RelayURL: "wss://relay.accepted.example", Accepted: true}, {RelayURL: "wss://relay.blocked.example", Reason: "blocked: policy denied"}}}
	registry := &recordingBackupPublishRegistry{}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
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
	if !result.VerifySignature() {
		t.Fatalf("result signature invalid")
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

type recordingBackupRestoreExecutor struct{ calls chan uuid.UUID }

func (e *recordingBackupRestoreExecutor) ProcessBackupRestore(_ context.Context, restoreID uuid.UUID) error {
	e.calls <- restoreID
	return nil
}

type recordingBackupRetentionExecutor struct{ calls chan uuid.UUID }

func (e *recordingBackupRetentionExecutor) ProcessBackupRetentionRun(_ context.Context, runID uuid.UUID) error {
	e.calls <- runID
	return nil
}

type recordingBackupVerificationExecutor struct{ calls chan uuid.UUID }

func (e *recordingBackupVerificationExecutor) ProcessBackupVerification(_ context.Context, verificationID uuid.UUID) error {
	e.calls <- verificationID
	return nil
}

type recordingBackupRepositoryProbeExecutor struct{ calls chan uuid.UUID }

func (e *recordingBackupRepositoryProbeExecutor) ProcessBackupRepositoryProbe(_ context.Context, repositoryID uuid.UUID, _ string) error {
	e.calls <- repositoryID
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

type recordingBackupRestoreResponder struct {
	statusSteps []string
	approvals   []bool
}

func (r *recordingBackupRestoreResponder) PublishBackupRestoreStatus(_ context.Context, _ *domain.BackupRestoreRun, step, _ string) error {
	r.statusSteps = append(r.statusSteps, step)
	return nil
}
func (r *recordingBackupRestoreResponder) PublishBackupRestoreResult(context.Context, *domain.BackupRestoreRun, string) error {
	return nil
}
func (r *recordingBackupRestoreResponder) PublishBackupRestoreApprovalResult(_ context.Context, _ *domain.BackupRestoreRun, approved bool, _ bool, _ string) error {
	r.approvals = append(r.approvals, approved)
	return nil
}

type recordingBackupRetentionResponder struct{ statusSteps []string }

func (r *recordingBackupRetentionResponder) PublishBackupRetentionStatus(_ context.Context, _ *domain.BackupRetentionRun, step, _ string) error {
	r.statusSteps = append(r.statusSteps, step)
	return nil
}
func (r *recordingBackupRetentionResponder) PublishBackupRetentionResult(context.Context, *domain.BackupRetentionRun, string) error {
	return nil
}

type backupRequestRegistry struct {
	recipes         map[uuid.UUID]*domain.BackupRecipe
	repositories    map[uuid.UUID]*domain.BackupRepository
	policies        map[uuid.UUID]*domain.BackupPolicy
	runs            map[uuid.UUID]*domain.BackupRun
	coordinates     map[string]uuid.UUID
	verifications   map[uuid.UUID]*domain.BackupVerificationRecord
	definitions     map[uuid.UUID]*domain.BackupDefinition
	restores        map[uuid.UUID]*domain.BackupRestoreRun
	restoreCoords   map[string]uuid.UUID
	retentionRuns   map[uuid.UUID]*domain.BackupRetentionRun
	retentionCoords map[string]uuid.UUID
}

func newBackupRequestRegistryFixture() (*backupRequestRegistry, *domain.BackupRecipe) {
	repoID := uuid.New()
	policyID := uuid.New()
	recipeID := uuid.New()
	registry := &backupRequestRegistry{recipes: map[uuid.UUID]*domain.BackupRecipe{}, repositories: map[uuid.UUID]*domain.BackupRepository{}, policies: map[uuid.UUID]*domain.BackupPolicy{}, runs: map[uuid.UUID]*domain.BackupRun{}, coordinates: map[string]uuid.UUID{}, verifications: map[uuid.UUID]*domain.BackupVerificationRecord{}, definitions: map[uuid.UUID]*domain.BackupDefinition{}, restores: map[uuid.UUID]*domain.BackupRestoreRun{}, restoreCoords: map[string]uuid.UUID{}, retentionRuns: map[uuid.UUID]*domain.BackupRetentionRun{}, retentionCoords: map[string]uuid.UUID{}}
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
func (r *backupRequestRegistry) GetRepositoryByName(_ context.Context, name string) (*domain.BackupRepository, error) {
	return r.repositoryByName(name), nil
}
func (r *backupRequestRegistry) CreateOrUpdateRepository(_ context.Context, repo *domain.BackupRepository) error {
	if err := domain.ValidateBackupRepository(repo); err != nil {
		return err
	}
	if repo.ID == uuid.Nil {
		repo.ID = uuid.New()
	}
	cp := *repo
	r.repositories[cp.ID] = &cp
	return nil
}
func (r *backupRequestRegistry) GetPolicy(_ context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	return r.policies[id], nil
}
func (r *backupRequestRegistry) GetPolicyByName(_ context.Context, name string) (*domain.BackupPolicy, error) {
	return r.policyByName(name), nil
}
func (r *backupRequestRegistry) CreateOrUpdatePolicy(_ context.Context, policy *domain.BackupPolicy) error {
	if err := domain.ValidateBackupPolicy(policy); err != nil {
		return err
	}
	if policy.ID == uuid.Nil {
		policy.ID = uuid.New()
	}
	cp := *policy
	r.policies[cp.ID] = &cp
	return nil
}
func (r *backupRequestRegistry) CreateOrUpdateRecipe(_ context.Context, recipe *domain.BackupRecipe) error {
	if err := domain.ValidateBackupRecipe(recipe); err != nil {
		return err
	}
	if r.repositories[recipe.RepositoryID] == nil {
		return fmt.Errorf("repository missing")
	}
	if recipe.PolicyID != nil && r.policies[*recipe.PolicyID] == nil {
		return fmt.Errorf("policy missing")
	}
	if recipe.ID == uuid.Nil {
		recipe.ID = uuid.New()
	}
	cp := *recipe
	r.recipes[cp.ID] = &cp
	return nil
}
func (r *backupRequestRegistry) UpsertBackupDefinition(_ context.Context, definition *domain.BackupDefinition) error {
	if err := domain.ValidateBackupDefinition(definition); err != nil {
		return err
	}
	if definition.ID == uuid.Nil {
		definition.ID = uuid.New()
	}
	cp := *definition
	r.definitions[cp.ID] = &cp
	return nil
}
func (r *backupRequestRegistry) GetBackupDefinitionByName(_ context.Context, name string) (*domain.BackupDefinition, error) {
	return r.definitionByName(name), nil
}
func (r *backupRequestRegistry) GetBackupRun(_ context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	return r.runs[id], nil
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
func (r *backupRequestRegistry) RecordBackupVerification(_ context.Context, record *domain.BackupVerificationRecord) error {
	if err := domain.ValidateBackupVerificationRecord(record); err != nil {
		return err
	}
	cp := *record
	r.verifications[cp.BackupRunID] = &cp
	if run := r.runs[cp.BackupRunID]; run != nil {
		run.VerificationStatus = cp.Status
	}
	return nil
}

func (r *backupRequestRegistry) CreateBackupRestoreIfAbsent(_ context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error) {
	key := backupCoordinate(restore.RequestedBy, restore.RequestKind, restore.RequestDTag)
	if existingID, ok := r.restoreCoords[key]; ok {
		return r.restores[existingID], false, nil
	}
	source := r.runs[restore.BackupRunID]
	if source == nil {
		return nil, false, fmt.Errorf("source backup run missing")
	}
	cp := *restore
	cp.RecipeID = source.RecipeID
	cp.RepositoryID = source.RepositoryID
	cp.PolicyID = source.PolicyID
	cp.SnapshotID = source.SnapshotID
	cp.Backend = source.Backend
	cp.ApprovalStatus = domain.BackupApprovalPending
	cp.VerificationStatus = domain.BackupVerificationPending
	r.restores[cp.ID] = &cp
	r.restoreCoords[key] = cp.ID
	return &cp, true, nil
}

func (r *backupRequestRegistry) ApplyBackupRestoreApproval(_ context.Context, restoreID uuid.UUID, approved bool, approvalEventID, approvedBy, message string, reasonParts ...any) (*domain.BackupRestoreRun, bool, error) {
	restore := r.restores[restoreID]
	if restore == nil {
		return nil, false, fmt.Errorf("restore missing")
	}
	if restore.ApprovalStatus != domain.BackupApprovalPending || backupRestoreTerminal(restore) {
		return restore, false, nil
	}
	restore.ApprovalEventID = approvalEventID
	restore.ApprovedBy = approvedBy
	restore.ApprovalMessage = message
	if approved {
		restore.ApprovalStatus = domain.BackupApprovalApproved
	} else {
		restore.ApprovalStatus = domain.BackupApprovalRejected
		restore.Status = domain.RunStatusFailed
	}
	return restore, true, nil
}

func (r *backupRequestRegistry) CreateBackupRetentionRunIfAbsent(_ context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error) {
	key := backupCoordinate(run.RequestedBy, run.RequestKind, run.RequestDTag)
	if existingID, ok := r.retentionCoords[key]; ok {
		return r.retentionRuns[existingID], false, nil
	}
	cp := *run
	repo := r.repositories[cp.RepositoryID]
	if repo != nil {
		cp.Backend = repo.Backend
	}
	r.retentionRuns[cp.ID] = &cp
	r.retentionCoords[key] = cp.ID
	return &cp, true, nil
}

func (r *backupRequestRegistry) addRestoreEligibleRun() *domain.BackupRun {
	recipe := r.firstRecipe()
	run := &domain.BackupRun{ID: uuid.New(), RecipeID: recipe.ID, RepositoryID: recipe.RepositoryID, PolicyID: recipe.PolicyID, RequestedBy: "requester", RequestEventID: "backup-event", RequestKind: KindBackupRunRequest, RequestDTag: "backup:daily", Status: domain.RunStatusSucceeded, Backend: recipe.Backend, TargetRef: recipe.TargetRef, SnapshotCreated: true, SnapshotID: "snap-1", VerificationStatus: domain.BackupVerificationSucceeded}
	r.runs[run.ID] = run
	return run
}

func (r *backupRequestRegistry) firstRecipe() *domain.BackupRecipe {
	for _, recipe := range r.recipes {
		return recipe
	}
	return nil
}

func (r *backupRequestRegistry) repositoryByName(name string) *domain.BackupRepository {
	for _, repo := range r.repositories {
		if repo.Name == name {
			return repo
		}
	}
	return nil
}

func (r *backupRequestRegistry) policyByName(name string) *domain.BackupPolicy {
	for _, policy := range r.policies {
		if policy.Name == name {
			return policy
		}
	}
	return nil
}

func (r *backupRequestRegistry) definitionByName(name string) *domain.BackupDefinition {
	for _, definition := range r.definitions {
		if definition.Name == name {
			return definition
		}
	}
	return nil
}

func (r *backupRequestRegistry) verificationByID(id uuid.UUID) *domain.BackupVerificationRecord {
	for _, verification := range r.verifications {
		if verification.ID == id {
			return verification
		}
	}
	return nil
}

func (r *backupRequestRegistry) firstPolicyID() uuid.UUID {
	for id := range r.policies {
		return id
	}
	return uuid.Nil
}

func (r *backupRequestRegistry) firstRepositoryID() uuid.UUID {
	for id := range r.repositories {
		return id
	}
	return uuid.Nil
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

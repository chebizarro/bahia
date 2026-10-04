package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type d80Scanner struct {
	SecurityScannerControlPlane
	requests []service.SecurityScanRequest
}

func (s *d80Scanner) SubmitScan(_ context.Context, req service.SecurityScanRequest) (*service.SecurityScanAccepted, error) {
	s.requests = append(s.requests, req)
	return &service.SecurityScanAccepted{Status: "accepted", RunID: uuid.New(), TargetKeyHash: "hash", TargetType: domain.SecurityTargetPackage}, nil
}

type d80SBOMRunner struct {
	requests []service.SBOMGenerateRequest
	imports  []service.SBOMImportRequest
}

func (r *d80SBOMRunner) EnqueueGenerate(_ context.Context, req service.SBOMGenerateRequest) (service.SBOMAcceptedAck, error) {
	r.requests = append(r.requests, req)
	return service.NewSBOMAcceptedAck(req.IDempotencyKey)
}
func (r *d80SBOMRunner) EnqueueImport(_ context.Context, req service.SBOMImportRequest) (service.SBOMAcceptedAck, error) {
	r.imports = append(r.imports, req)
	return service.NewSBOMAcceptedAck(req.IDempotencyKey)
}

type d80NotificationDispatcher struct{ calls int }

func (d *d80NotificationDispatcher) DispatchToChannel(context.Context, *domain.NotificationChannel, string, map[string]any) error {
	d.calls++
	return nil
}

type d80BuildRegistrar struct {
	artifact *domain.Artifact
	calls    int
}

func (r *d80BuildRegistrar) RegisterBuildResult(context.Context, uuid.UUID) (*domain.Artifact, error) {
	r.calls++
	return r.artifact, nil
}

func TestD80SecurityScanRequestAcceptedReplayRejected(t *testing.T) {
	scanner := &d80Scanner{}
	p, statuses := d70Processor(t, "security", testPubkey, NewSecurityScanIntentHandler(scanner))
	id := uuid.NewString()
	intent := d70Intent("security", "scan-run", "security-scan:"+id, testPubkey,
		map[string]interface{}{"target": map[string]any{"type": "package", "package": map[string]any{"ecosystem": "npm", "name": "left-pad", "version": "1.3.0"}}})
	intent.IntentID = id
	assertD70AcceptedReplayAndUnauthorized(t, p, statuses, intent, func() int { return len(scanner.requests) })
	require.Less(t, len(statuses.events[0].Content), 16*1024)
	require.Equal(t, domain.SecurityTriggerManual, scanner.requests[0].Trigger)
	require.Equal(t, testPubkey, scanner.requests[0].RequestedBy)
	require.Equal(t, "hash", intent.StatusData["target_key_hash"])
	bad := d70Intent("security", "scan-run", "security-scan:"+uuid.NewString(), testPubkey, map[string]interface{}{"target": map[string]any{"type": "package"}})
	bad.Coordinate = "security-scan:" + bad.IntentID
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), bad), "package ecosystem")
	require.Len(t, scanner.requests, 1)
}

func TestD80SBOMGenerateRequestAcceptedReplayRejected(t *testing.T) {
	runner := &d80SBOMRunner{}
	p, statuses := d70Processor(t, "sbom", testPubkey, NewSBOMIntentHandler(runner))
	id := uuid.NewString()
	intent := d70Intent("sbom", "generate", "sbom-generate:"+id, testPubkey, map[string]interface{}{
		"idempotencyKey": id,
		"subject":        map[string]any{"type": "artifact", "id": uuid.NewString(), "digest": "sha256:abc"},
		"source":         map[string]any{"kind": "oci-image", "locator": "registry.example/app@sha256:abc"},
		"formats":        []any{"spdx"}, "generator": "syft", "storage": "blossom",
	})
	intent.IntentID = id
	assertD70AcceptedReplayAndUnauthorized(t, p, statuses, intent, func() int { return len(runner.requests) })
	require.Less(t, len(statuses.events[0].Content), 16*1024)
	require.Equal(t, id, runner.requests[0].IDempotencyKey)
	require.Equal(t, id, intent.StatusData["run_id"])
	bad := d70Intent("sbom", "generate", "sbom-generate:"+uuid.NewString(), testPubkey,
		map[string]interface{}{"idempotencyKey": "different"})
	bad.Coordinate = "sbom-generate:" + bad.IntentID
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), bad), "must match intent_id")
	require.Len(t, runner.requests, 1)
}

func TestD80SBOMImportRequestAcceptedReplayRejected(t *testing.T) {
	runner := &d80SBOMRunner{}
	p, statuses := d70Processor(t, "sbom", testPubkey, NewSBOMIntentHandler(runner))
	id := uuid.NewString()
	intent := d70Intent("sbom", "import", "sbom-import:"+id, testPubkey, map[string]interface{}{
		"idempotencyKey": id,
		"subject":        map[string]any{"type": "artifact", "id": uuid.NewString(), "digest": "sha256:abc"},
		"format":         "spdx", "location": map[string]any{"type": "blossom", "uri": "https://blossom.example/sha256"},
		"storage": "blossom", "generator": map[string]any{"id": "import"},
	})
	intent.IntentID = id
	assertD70AcceptedReplayAndUnauthorized(t, p, statuses, intent, func() int { return len(runner.imports) })
	require.Equal(t, id, runner.imports[0].IDempotencyKey)
	require.Less(t, len(statuses.events[0].Content), 16*1024)
	bad := d70Intent("sbom", "import", "", testPubkey, map[string]interface{}{"payloadBase64": "%%%"})
	bad.Coordinate = "sbom-import:" + bad.IntentID
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), bad), "payloadBase64")
	require.Len(t, runner.imports, 1)
}

func TestD80SignatureVerifyRequestAcceptedReplayRejected(t *testing.T) {
	artifactID, serviceID := uuid.New(), uuid.New()
	sigRepo := &fakeEncryptedSignatureRepo{}
	routes := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		Artifacts:    &fakeEncryptedArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID}},
		Signatures:   sigRepo,
		SignVerifier: fakeEncryptedSignatureVerifier{sigs: []domain.ArtifactSignature{{ID: uuid.New(), ArtifactID: artifactID, SignatureType: domain.SignatureCosign, SignatureRef: "ref", VerificationStatus: domain.SignatureStatusVerified}}},
		Services:     &testServiceRepo{service: &domain.Service{ID: serviceID, OrgID: testOrgID()}}, Logger: zap.NewNop(),
	})
	handler := NewArtifactIntentHandler(&fakeEncryptedRegistryMutations{}, routes.services)
	handler.ConfigureSignatureVerification(routes)
	p, statuses := d76Processor(t, "artifact", testPubkey, handler)
	intent := d70Intent("artifact", "signature-verify", "artifact:"+artifactID.String(), testPubkey,
		map[string]interface{}{"artifact_id": artifactID.String()})
	ctx := context.Background()
	require.NoError(t, p.ProcessInProcess(ctx, intent))
	require.Equal(t, 1, len(sigRepo.records))
	require.Equal(t, 1, intent.StatusData["verified"])
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	require.Less(t, len(statuses.events[0].Content), 16*1024)
	require.NoError(t, p.ProcessInProcess(ctx, intent))
	require.Len(t, sigRepo.records, 1)
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-denied"
	require.ErrorContains(t, p.ProcessInProcess(ctx, &denied), "insufficient permission")
	bad := d70Intent("artifact", "signature-verify", "artifact:"+uuid.NewString(), testPubkey, intent.Content)
	require.ErrorContains(t, p.ProcessInProcess(ctx, bad), "matching artifact_id")
}

func TestD80RegisterBuildResultAcceptedReplayRejected(t *testing.T) {
	buildID, serviceID, artifactID := uuid.New(), uuid.New(), uuid.New()
	registrar := &d80BuildRegistrar{artifact: &domain.Artifact{ID: artifactID, BuildID: buildID, ServiceID: serviceID, ImageDigest: "sha256:abc"}}
	handler := NewArtifactIntentHandler(&fakeEncryptedRegistryMutations{}, &testServiceRepo{service: &domain.Service{ID: serviceID, OrgID: testOrgID()}})
	handler.ConfigureBuildResultRegistration(buildResultTestLoader{build: &domain.Build{ID: buildID, ServiceID: serviceID, Status: domain.BuildStatusSucceeded}}, registrar)
	p, statuses := d76Processor(t, "artifact", testPubkey, handler)
	intent := d70Intent("artifact", "register-build-result", "build-result:"+buildID.String(), testPubkey,
		map[string]interface{}{"build_id": buildID.String()})
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, registrar.calls)
	require.Equal(t, artifactID.String(), intent.StatusData["artifact_id"])
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, registrar.calls)
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-denied"
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &denied), "insufficient permission")
	bad := *intent
	bad.IntentID = uuid.NewString()
	bad.Coordinate = "build-result:" + uuid.NewString()
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &bad), "matching build_id")
}

func TestD80RelayPolicyDesiredStateAcceptedReplayConflictRejected(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	projection := testProjection(t, servicePubkey, "existing-head", time.Unix(3_000, 0), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://old.example"}})
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: &config.Config{}, ProjectionStore: &memoryRelayPolicyProjectionStore{projection: projection}, ServicePubkey: servicePubkey, Logger: zap.NewNop()})
	publisher := &mockEncryptedPublisher{}
	signer, err := NewPrivateKeySigner(testServiceKey)
	require.NoError(t, err)
	h.SetPublisher(publisher, signer)
	p, statuses := d70Processor(t, "relay", actor, NewRelayPolicyIntentHandler(h))
	content := map[string]interface{}{"browser_relays": []any{"wss://new.example"}, "expected_projection": map[string]any{"event_id": projection.EventID}}
	intent := d70Intent("relay", "policy-set", RelaySettingsDTag, actor, content)
	intent.Event = &nostr.Event{ID: testNostrID("relay-intent"), PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.NotEmpty(t, publisher.events)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	count := len(publisher.events)
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Len(t, publisher.events, count)
	conflict := *intent
	conflict.IntentID = uuid.NewString()
	conflict.Content = map[string]interface{}{"browser_relays": []any{"wss://new.example"}, "expected_projection": map[string]any{"event_id": "wrong"}}
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &conflict), "projection changed")
	require.Equal(t, "conflict", tagValueNostr(statuses.events[1].Tags, "status"))
	require.Len(t, publisher.events, count)
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-non-fleet-principal"
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &denied), "insufficient permission")
}

func TestD80NotificationChannelTestAcceptedReplayRejected(t *testing.T) {
	id := uuid.New()
	repo := newMemNotificationRepo()
	require.NoError(t, repo.CreateChannel(t.Context(), &domain.NotificationChannel{ID: id, OrgID: testOrgID()}))
	dispatcher := &d80NotificationDispatcher{}
	h := NewNotificationIntentHandler(NotificationIntentHandlerConfig{Registry: repo, TestDispatcher: dispatcher, Logger: zap.NewNop()})
	p, statuses := d76Processor(t, "notification", testPubkey, h)
	intent := d70Intent("notification", "channel-test", id.String(), testPubkey, map[string]interface{}{"id": id.String()})
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, dispatcher.calls)
	require.Equal(t, "test sent", intent.StatusData["status"])
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, dispatcher.calls)
	bad := *intent
	bad.IntentID = uuid.NewString()
	bad.Content = map[string]interface{}{"id": uuid.NewString()}
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &bad), "matching id")
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-denied"
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &denied), "insufficient permission")
}

func TestD80EnvironmentWorkerPolicyDesiredStateAcceptedReplayConflict(t *testing.T) {
	id := uuid.New()
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	workers := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey})
	stamp := domain.NormalizeRevisionTime(time.Now().UTC())
	reg := &stubEnvironmentRegistry{getByID: map[uuid.UUID]*domain.Environment{
		id: {ID: id, OrgID: testOrgID(), Name: "prod", UpdatedAt: stamp, RuntimeConfig: map[string]any{"other": "kept"}},
	}}
	p, statuses := d70Processor(t, "environment", testPubkey, NewEnvironmentIntentHandler(reg, nil, zap.NewNop(), workers))
	intent := d70Intent("environment", "worker-policy-apply", id.String(), testPubkey,
		map[string]interface{}{"environment_id": id.String(), "policy": map[string]any{"pinned_worker": workerPubkey}, "expected_updated_at": stamp.Format(time.RFC3339Nano)})
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Len(t, reg.updated, 1)
	require.Equal(t, "kept", reg.updated[0].env.RuntimeConfig["other"])
	require.Equal(t, workerPubkey, reg.updated[0].env.RuntimeConfig["worker_policy"].(map[string]any)["pinned_worker"])
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Len(t, reg.updated, 1)
	conflict := *intent
	conflict.IntentID = uuid.NewString()
	conflict.Content = map[string]interface{}{"environment_id": id.String(), "policy": map[string]any{}, "expected_updated_at": stamp.Add(-time.Minute).Format(time.RFC3339Nano)}
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &conflict), "revision conflict")
	require.Equal(t, "conflict", tagValueNostr(statuses.events[1].Tags, "status"))
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-non-fleet-principal"
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &denied), "insufficient permission")
}

func TestD80MLPinDesiredStateAcceptedReplayConflict(t *testing.T) {
	id, envID := uuid.New(), uuid.New()
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	workers := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey})
	stamp := domain.NormalizeRevisionTime(time.Now().UTC())
	repo, canonical := newD70MLRepo(), &d70MLCanonical{}
	repo.endpoints[id] = &domain.MLInferenceEndpoint{ID: id, Name: "inference", EnvironmentID: envID, UpdatedAt: stamp}
	registry := service.NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
	registry.SetMLCPStatePublisher(canonical)
	p, statuses := d70Processor(t, "ml", testPubkey, NewMLIntentHandler(registry, workers))
	intent := d70Intent("ml", "pin", "endpoint:"+id.String(), testPubkey,
		map[string]interface{}{"workload_id": id.String(), "workload_kind": "ml_inference", "environment_id": envID.String(), "worker_pubkey": workerPubkey, "expected_updated_at": stamp.Format(time.RFC3339Nano)})
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, canonical.endpoints)
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, canonical.endpoints)
	conflict := *intent
	conflict.IntentID = uuid.NewString()
	conflict.Content = map[string]interface{}{"workload_id": id.String(), "workload_kind": "ml_inference", "worker_pubkey": workerPubkey, "expected_updated_at": stamp.Add(-time.Minute).Format(time.RFC3339Nano)}
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &conflict), "revision conflict")
	require.Equal(t, "conflict", tagValueNostr(statuses.events[1].Tags, "status"))
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-non-fleet-principal"
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &denied), "insufficient permission")
}

func TestD80SecurityAndSBOMContextVMDualDispatchAndLegacy(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	event := &nostr.Event{ID: testNostrID("d80-request"), PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}
	scanner := &d80Scanner{}
	securityProcessor, _ := d70Processor(t, "security", actor, NewSecurityScanIntentHandler(scanner))
	scanParams := json.RawMessage(`{"target":{"type":"package","package":{"ecosystem":"npm","name":"left-pad"}}}`)
	scanRequest := ContextVMRequest{Event: event, RPC: ContextVMJSONRPCRequest{Params: scanParams}}
	security := securityContextVMHandler{scanner: scanner, processor: securityProcessor}
	_, err := security.scan(t.Context(), scanRequest)
	require.NoError(t, err)
	_, err = security.scan(t.Context(), scanRequest)
	require.NoError(t, err)
	require.Len(t, scanner.requests, 1)
	security.processor = nil
	_, err = security.scan(t.Context(), scanRequest)
	require.NoError(t, err)
	require.Len(t, scanner.requests, 2, "legacy path must still call the scanner directly")

	runner := &d80SBOMRunner{}
	sbomProcessor, _ := d70Processor(t, "sbom", actor, NewSBOMIntentHandler(runner))
	key := uuid.NewString()
	sbomParams, err := json.Marshal(map[string]any{"idempotencyKey": key, "subject": map[string]any{"type": "artifact", "id": uuid.NewString(), "digest": "sha256:abc"}, "source": map[string]any{"kind": "oci-image", "locator": "registry.example/a@sha256:abc"}, "formats": []any{"spdx"}, "generator": "syft", "storage": "blossom"})
	require.NoError(t, err)
	sbomRequest := ContextVMRequest{Event: event, RPC: ContextVMJSONRPCRequest{Params: sbomParams}}
	sbom := sbomContextVMHandler{runner: runner, processor: sbomProcessor}
	_, err = sbom.generate(t.Context(), sbomRequest)
	require.NoError(t, err)
	_, err = sbom.generate(t.Context(), sbomRequest)
	require.NoError(t, err)
	require.Len(t, runner.requests, 1)
	sbom.processor = nil
	_, err = sbom.generate(t.Context(), sbomRequest)
	require.NoError(t, err)
	require.Len(t, runner.requests, 2, "legacy path must still enqueue exactly once")
	importParams, err := json.Marshal(map[string]any{"idempotencyKey": uuid.NewString(), "subject": map[string]any{"type": "artifact", "id": uuid.NewString(), "digest": "sha256:abc"}, "format": "spdx", "location": map[string]any{"type": "blossom", "uri": "https://blossom.example/sha256"}, "storage": "blossom", "generator": map[string]any{"id": "import"}})
	require.NoError(t, err)
	sbomRequest.RPC.Params = importParams
	sbom.processor = sbomProcessor
	_, err = sbom.importSBOM(t.Context(), sbomRequest)
	require.NoError(t, err)
	_, err = sbom.importSBOM(t.Context(), sbomRequest)
	require.NoError(t, err)
	require.Len(t, runner.imports, 1)
	sbom.processor = nil
	_, err = sbom.importSBOM(t.Context(), sbomRequest)
	require.NoError(t, err)
	require.Len(t, runner.imports, 2)
}

func TestD80WorkerPlacementContextVMDualDispatch(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	event := &nostr.Event{ID: testNostrID("d80-worker-placement"), PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	workers := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey})
	envID, endpointID := uuid.New(), uuid.New()
	reg := &stubEnvironmentRegistry{getByID: map[uuid.UUID]*domain.Environment{envID: {ID: envID, OrgID: testOrgID(), Name: "prod"}}}
	envHandler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop(), workers)
	envProcessor, _ := d70Processor(t, "environment", actor, envHandler)
	workerContext := workerContextVMHandlers{intentProcessor: envProcessor}
	policyParams, err := json.Marshal(WorkerPolicyApplyCommand{EnvironmentID: envID.String(), Policy: map[string]any{"pinned_worker": workerPubkey}, IdempotencyKey: uuid.NewString()})
	require.NoError(t, err)
	request := ContextVMRequest{Event: event, RPC: ContextVMJSONRPCRequest{Params: policyParams}}
	_, err = workerContext.policyApply(t.Context(), request)
	require.NoError(t, err)
	_, err = workerContext.policyApply(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, reg.updated, 1)

	repo := newD70MLRepo()
	repo.endpoints[endpointID] = &domain.MLInferenceEndpoint{ID: endpointID, Name: "inference", EnvironmentID: envID}
	registry := service.NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
	registry.SetMLCPStatePublisher(&d70MLCanonical{})
	mlProcessor, _ := d70Processor(t, "ml", actor, NewMLIntentHandler(registry, workers))
	workerContext.intentProcessor = mlProcessor
	pinParams, err := json.Marshal(WorkloadPinCommand{WorkloadID: endpointID.String(), WorkloadKind: "ml_inference", EnvironmentID: envID.String(), WorkerPubKey: workerPubkey, IdempotencyKey: uuid.NewString()})
	require.NoError(t, err)
	request.RPC.Params = pinParams
	_, err = workerContext.workloadPin(t.Context(), request)
	require.NoError(t, err)
	_, err = workerContext.workloadPin(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, 1, repo.writes)
}

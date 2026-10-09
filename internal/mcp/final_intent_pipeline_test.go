package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type finalIntentFixture struct {
	server           *Server
	canonical        canonicalMCPFixture
	actor, stranger  string
	orgID            uuid.UUID
	ctx, strangerCtx context.Context
}

func newFinalIntentFixture(t *testing.T, domainName, mode string) *finalIntentFixture {
	t.Helper()
	f := &finalIntentFixture{actor: nostr.Generate().Public().Hex(), stranger: nostr.Generate().Public().Hex(), orgID: uuid.New()}
	f.ctx = auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: f.actor, PubKey: f.actor, Method: auth.MethodNIP98})
	f.strangerCtx = auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: f.stranger, PubKey: f.stranger, Method: auth.MethodNIP98})
	f.server = newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{f.actor, f.stranger}})
	f.canonical = attachCanonicalMCPFixture(t, f.server)
	signer, err := controlplane.NewPrivateKeySigner(f.canonical.privateKey)
	require.NoError(t, err)
	status := controlplane.NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		if mode == "pending" {
			return nil
		}
		_, err := f.canonical.store.SaveEvent(ev)
		return err
	}, signer, zap.NewNop())
	trust := controlplane.NewTrustSet([]string{f.actor}, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{f.orgID.String(): f.actor, uuid.NewString(): f.stranger}))
	f.server.intentProc = controlplane.NewIntentProcessor(trust, f.canonical.store, status, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{domainName: true}}, zap.NewNop())
	return f
}

func (f *finalIntentFixture) publishState(t *testing.T, family int, entity any, id string) {
	t.Helper()
	var topic string
	for _, item := range nostrpool.CPStateFamilyTopics() {
		if item.LegacyKind == family {
			topic = item.Topic
			break
		}
	}
	require.NotEmpty(t, topic)
	raw, err := json.Marshal(entity)
	require.NoError(t, err)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"d", id}, {"domain", "test"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", fmt.Sprint(family)}, {"deleted", "false"}, {"t", topic}}, Content: string(raw)}
	signer, err := controlplane.NewPrivateKeySigner(f.canonical.privateKey)
	require.NoError(t, err)
	require.NoError(t, controlplane.SignGoNostrEvent(t.Context(), signer, &ev))
	_, err = f.canonical.store.SaveEvent(ev)
	require.NoError(t, err)
}

func (f *finalIntentFixture) exercise(t *testing.T, mode, name string, args map[string]any, effects func() int) {
	t.Helper()
	args["idempotency_key"] = "final-" + name
	ctx := f.ctx
	if mode == "rejected" {
		ctx = f.strangerCtx
	}
	first, err := f.server.CallTool(ctx, name, args)
	require.NoError(t, err)
	body := mcpIntentResult(t, first)
	if mode == "rejected" {
		require.True(t, first.IsError, "%v", body)
		require.Equal(t, "rejected", body["status"])
		require.Zero(t, effects())
		return
	}
	require.False(t, first.IsError, "%v", body)
	require.NotEmpty(t, body["intent_id"])
	require.NotEmpty(t, body["event_id"])
	require.Equal(t, 1, effects(), "handler must execute exactly once")
	if mode == "pending" {
		require.Equal(t, "pending", body["status"])
		require.NotContains(t, body, "result")
		return
	}
	require.Equal(t, "accepted", body["status"])
	if mode == "replay" {
		again, err := f.server.CallTool(ctx, name, args)
		require.NoError(t, err)
		require.False(t, again.IsError, "%v", mcpIntentResult(t, again))
		replay := mcpIntentResult(t, again)
		require.Equal(t, "accepted", replay["status"])
		require.Equal(t, body["intent_id"], replay["intent_id"])
		require.Equal(t, body["event_id"], replay["event_id"])
		require.Equal(t, 1, effects(), "replay repeated the handler")
	}
}

type finalMLRepo struct {
	controlplane.MLIntentRegistry
	model        *domain.MLModel
	version      *domain.MLModelVersion
	recipe       *domain.MLRecipe
	endpoint     *domain.MLInferenceEndpoint
	deployment   *domain.MLDeploymentIntent
	writes       int
	publishModel func(*domain.MLModel)
}

func (r *finalMLRepo) GetModel(context.Context, uuid.UUID) (*domain.MLModel, error) {
	return r.model, nil
}
func (r *finalMLRepo) GetModelBySlug(_ context.Context, slug string) (*domain.MLModel, error) {
	if r.model != nil && r.model.Slug == slug {
		return r.model, nil
	}
	return nil, nil
}
func (r *finalMLRepo) CreateOrUpdateModel(_ context.Context, m *domain.MLModel) error {
	r.model = m
	r.writes++
	if r.publishModel != nil {
		r.publishModel(m)
	}
	return nil
}
func (r *finalMLRepo) GetModelVersion(context.Context, uuid.UUID) (*domain.MLModelVersion, error) {
	return r.version, nil
}
func (r *finalMLRepo) GetModelVersionByModelVersion(_ context.Context, modelID uuid.UUID, version string) (*domain.MLModelVersion, error) {
	if r.version != nil && r.version.ModelID == modelID && r.version.Version == version {
		return r.version, nil
	}
	return nil, nil
}
func (r *finalMLRepo) CreateOrUpdateModelVersion(_ context.Context, v *domain.MLModelVersion) error {
	r.version = v
	r.writes++
	return nil
}
func (r *finalMLRepo) GetRecipe(_ context.Context, id uuid.UUID) (*domain.MLRecipe, error) {
	if r.recipe != nil && r.recipe.ID == id {
		return r.recipe, nil
	}
	return nil, nil
}
func (r *finalMLRepo) GetRecipeByNameVersion(_ context.Context, name, version string) (*domain.MLRecipe, error) {
	if r.recipe != nil && r.recipe.Name == name && r.recipe.Version == version {
		return r.recipe, nil
	}
	return nil, nil
}
func (r *finalMLRepo) CreateOrUpdateRecipe(_ context.Context, recipe *domain.MLRecipe) error {
	r.recipe = recipe
	r.writes++
	return nil
}
func (r *finalMLRepo) CreateOrUpdateRecipeRun(context.Context, *domain.MLRecipeRun) error {
	r.writes++
	return nil
}
func (r *finalMLRepo) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	if r.endpoint != nil && r.endpoint.ID == id {
		return r.endpoint, nil
	}
	return nil, nil
}
func (r *finalMLRepo) GetInferenceEndpointByNameEnv(_ context.Context, name string, env uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	if r.endpoint != nil && r.endpoint.Name == name && r.endpoint.EnvironmentID == env {
		return r.endpoint, nil
	}
	return nil, nil
}
func (r *finalMLRepo) CreateDeploymentIntent(_ context.Context, d *domain.MLDeploymentIntent) error {
	r.deployment = d
	r.writes++
	return nil
}
func (r *finalMLRepo) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	if r.deployment != nil && r.deployment.ID == id {
		return r.deployment, nil
	}
	return nil, nil
}
func (r *finalMLRepo) ApproveDeploymentIntent(context.Context, uuid.UUID) error {
	r.writes++
	return nil
}
func (r *finalMLRepo) RejectDeploymentIntent(context.Context, uuid.UUID) error {
	r.writes++
	return nil
}
func (r *finalMLRepo) RollbackWithMetadata(_ context.Context, endpoint, env uuid.UUID, actor string, metadata map[string]any) (*domain.MLDeploymentIntent, error) {
	r.writes++
	return &domain.MLDeploymentIntent{ID: uuid.New(), EndpointID: endpoint, EnvironmentID: env, ModelVersionID: uuid.New(), RequestedBy: actor}, nil
}

func TestFinalMCPMLRealHandlerPipeline(t *testing.T) {
	tools := []string{"bahia_ml_import_model", "bahia_ml_run_recipe", "bahia_ml_deploy", "bahia_ml_rollback", "bahia_assistant_ml_deploy", "bahia_assistant_ml_approve_deployment", "bahia_assistant_ml_rollback"}
	for _, tool := range tools {
		for _, mode := range []string{"accepted", "rejected", "replay", "pending"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				f := newFinalIntentFixture(t, "ml", mode)
				envID, endpointID, versionID, recipeID, deploymentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
				repo := &finalMLRepo{recipe: &domain.MLRecipe{ID: recipeID, Name: "train", Version: "1"}, endpoint: &domain.MLInferenceEndpoint{ID: endpointID, Name: "predict", EnvironmentID: envID}, version: &domain.MLModelVersion{ID: versionID}, deployment: &domain.MLDeploymentIntent{ID: deploymentID, EndpointID: endpointID, EnvironmentID: envID}}
				if mode != "pending" {
					repo.publishModel = func(m *domain.MLModel) { f.publishState(t, nostrpool.KindMLModelRegistry, m, "model:"+m.Slug) }
				}
				f.server.intentProc.RegisterHandler("ml", controlplane.NewMLIntentHandler(repo))
				f.publishState(t, nostrpool.KindEnvironmentRegistry, &domain.Environment{ID: envID, OrgID: f.orgID, Name: "prod"}, envID.String())
				f.publishState(t, nostrpool.KindMLInferenceEndpointRegistry, repo.endpoint, "endpoint:"+endpointID.String())
				args := map[string]any{}
				switch tool {
				case "bahia_ml_import_model":
					args["model"], args["source"], args["source_uri"], args["revision"] = "model:weather", "huggingface", "hf://weather/model", "v1"
				case "bahia_ml_run_recipe":
					args["recipe_id"], args["inputs"] = recipeID.String(), map[string]any{"dataset": "weather"}
				case "bahia_ml_deploy", "bahia_assistant_ml_deploy":
					args["endpoint_id"], args["model_version_id"] = endpointID.String(), versionID.String()
				case "bahia_ml_rollback", "bahia_assistant_ml_rollback":
					args["endpoint_id"] = endpointID.String()
				case "bahia_assistant_ml_approve_deployment":
					args["intent_id"], args["decision"] = deploymentID.String(), "approve"
				}
				if tool == "bahia_ml_import_model" && mode != "rejected" {
					// model and model version are two real registry mutations.
					f.exercise(t, mode, tool, args, func() int {
						if repo.writes == 2 {
							return 1
						}
						return repo.writes
					})
				} else {
					f.exercise(t, mode, tool, args, func() int { return repo.writes })
				}
			})
		}
	}
}

type finalToolApprovalRepo struct {
	repository.ToolProvisioningRepository
	intent *domain.ToolProvisionIntent
	writes int
}

func (r *finalToolApprovalRepo) ApplyToolApprovalDecision(_ context.Context, id uuid.UUID, status domain.ToolProvisionStatus, actor string, when time.Time) (*domain.ToolProvisionIntent, error) {
	if r.intent == nil || r.intent.ID != id {
		return nil, repository.ErrNotFound
	}
	if r.intent.Status != domain.ToolProvisionStatusAwaitingApproval {
		return nil, repository.ErrConflict
	}
	r.intent.Status = status
	r.writes++
	return r.intent, nil
}
func (*finalToolApprovalRepo) LogApproval(context.Context, uuid.UUID, string, string, string) error {
	return nil
}

func TestFinalMCPToolApprovalRealHandlerPipeline(t *testing.T) {
	for _, tool := range []string{"bahia_tool_provision_approve", "bahia_tool_provision_reject"} {
		for _, mode := range []string{"accepted", "rejected", "replay", "pending"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				f := newFinalIntentFixture(t, "tool", mode)
				serviceID, id := uuid.New(), uuid.New()
				f.publishState(t, nostrpool.KindServiceRegistry, &domain.Service{ID: serviceID, OrgID: f.orgID, Name: "api"}, serviceID.String())
				repo := &finalToolApprovalRepo{intent: &domain.ToolProvisionIntent{ID: id, ServiceID: serviceID, Status: domain.ToolProvisionStatusAwaitingApproval}}
				f.publishState(t, nostrpool.KindToolProvisionIntentState, repo.intent, "tool-intent:"+id.String())
				reactor := controlplane.NewReactor(controlplane.Config{AuthorizedPubkeys: []string{f.actor}}, nil, nil, nil, zap.NewNop(), controlplane.WithToolProvisioningRepository(repo))
				f.server.intentProc.RegisterHandler("tool", controlplane.NewToolIntentHandler(reactor))
				args := map[string]any{"intent_id": id.String(), "reason": "reviewed", "idempotency_key": "paused-" + tool}
				ctx := f.ctx
				if mode == "rejected" {
					ctx = f.strangerCtx
				}
				result, err := f.server.CallTool(ctx, tool, args)
				require.NoError(t, err)
				body := mcpIntentResult(t, result)
				require.True(t, result.IsError, "%v", body)
				require.Equal(t, "rejected", body["status"])
				if mode != "rejected" {
					require.Contains(t, body["reason"], "tool approval paused")
				}
				require.Zero(t, repo.writes, "SQL-only approval row must remain pending")
				require.Equal(t, domain.ToolProvisionStatusAwaitingApproval, repo.intent.Status)
				if mode == "replay" {
					again, err := f.server.CallTool(ctx, tool, args)
					require.NoError(t, err)
					require.True(t, again.IsError)
					require.Equal(t, "rejected", mcpIntentResult(t, again)["status"])
					require.Zero(t, repo.writes)
				}
			})
		}
	}
}

type finalSBOMRunner struct{ imports []service.SBOMImportRequest }

func (r *finalSBOMRunner) EnqueueGenerate(_ context.Context, req service.SBOMGenerateRequest) (service.SBOMAcceptedAck, error) {
	return service.NewSBOMAcceptedAck(req.IDempotencyKey)
}
func (r *finalSBOMRunner) EnqueueImport(_ context.Context, req service.SBOMImportRequest) (service.SBOMAcceptedAck, error) {
	r.imports = append(r.imports, req)
	return service.NewSBOMAcceptedAck(req.IDempotencyKey)
}

type finalNotificationDispatcher struct{ calls int }

func (d *finalNotificationDispatcher) DispatchToChannel(context.Context, *domain.NotificationChannel, string, map[string]any) error {
	d.calls++
	return nil
}

func TestFinalMCPD80RealHandlerPipeline(t *testing.T) {
	for _, tool := range []string{"bahia_verify_signatures", "bahia_ingest_sbom", "bahia_test_notification_channel"} {
		for _, mode := range []string{"accepted", "rejected", "replay", "pending"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				domainName := "artifact"
				if tool == "bahia_ingest_sbom" {
					domainName = "sbom"
				}
				if tool == "bahia_test_notification_channel" {
					domainName = "notification"
				}
				f := newFinalIntentFixture(t, domainName, mode)
				args := map[string]any{}
				var effects func() int
				switch tool {
				case "bahia_verify_signatures":
					serviceID, artifactID := uuid.New(), uuid.New()
					services, artifacts, signatures := newTestServiceRepo(), newTestArtifactRepo(), newTestMCPSignatureRepo()
					svc := &domain.Service{ID: serviceID, OrgID: f.orgID, Name: "api"}
					require.NoError(t, services.Create(t.Context(), svc))
					f.publishState(t, nostrpool.KindServiceRegistry, svc, serviceID.String())
					artifact := &domain.Artifact{ID: artifactID, ServiceID: serviceID, BuildID: uuid.New(), ImageDigest: "sha256:abc"}
					require.NoError(t, artifacts.Create(t.Context(), artifact))
					f.publishState(t, nostrpool.KindArtifactRegistry, artifact, "artifact:"+artifactID.String())
					registry := service.NewRegistryService(services, nil, nil, artifacts, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
					verifier := &testMCPSignatureVerifier{signatures: []domain.ArtifactSignature{{ID: uuid.New(), ArtifactID: artifactID, SignatureType: domain.SignatureCosign, SignatureRef: "ref", VerificationStatus: domain.SignatureStatusVerified}}}
					routes := controlplane.NewEncryptedRouteHandlers(controlplane.EncryptedRouteHandlersConfig{Artifacts: artifacts, Signatures: signatures, SignVerifier: verifier, Services: services, Logger: zap.NewNop()})
					handler := controlplane.NewArtifactIntentHandler(registry, services)
					handler.ConfigureSignatureVerification(routes)
					f.server.intentProc.RegisterHandler("artifact", handler)
					args["artifact_id"] = artifactID.String()
					effects = func() int { return len(signatures.signatures) }
				case "bahia_ingest_sbom":
					serviceID, artifactID := uuid.New(), uuid.New()
					f.publishState(t, nostrpool.KindServiceRegistry, &domain.Service{ID: serviceID, OrgID: f.orgID}, serviceID.String())
					artifact := &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
					f.publishState(t, nostrpool.KindArtifactRegistry, artifact, "artifact:"+artifactID.String())
					runner := &finalSBOMRunner{}
					f.server.intentProc.RegisterHandler("sbom", controlplane.NewSBOMIntentHandler(runner))
					args["artifact_id"], args["sbom_data"] = artifactID.String(), `{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"components":[]}`
					effects = func() int { return len(runner.imports) }
				case "bahia_test_notification_channel":
					id := uuid.New()
					channel := &domain.NotificationChannel{ID: id, OrgID: f.orgID, Name: "alerts", Enabled: true}
					repo := newTestNotificationRepo()
					require.NoError(t, repo.CreateChannel(t.Context(), channel))
					f.publishState(t, nostrpool.KindNotificationChannelRegistry, channel, id.String())
					dispatcher := &finalNotificationDispatcher{}
					f.server.intentProc.RegisterHandler("notification", controlplane.NewNotificationIntentHandler(controlplane.NotificationIntentHandlerConfig{Registry: repo, TestDispatcher: dispatcher, Logger: zap.NewNop()}))
					args["channel_id"] = id.String()
					effects = func() int { return dispatcher.calls }
				}
				f.exercise(t, mode, tool, args, effects)
			})
		}
	}
}

func TestFinalMCPAssistantMLExecutorIntentEvidence(t *testing.T) {
	for _, tool := range []string{"bahia_assistant_ml_deploy", "bahia_assistant_ml_approve_deployment", "bahia_assistant_ml_rollback"} {
		for _, mode := range []string{"accepted", "rejected", "replay", "pending"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				f := newFinalIntentFixture(t, "ml", mode)
				envID, endpointID, versionID, deploymentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
				repo := &finalMLRepo{endpoint: &domain.MLInferenceEndpoint{ID: endpointID, Name: "predict", EnvironmentID: envID}, version: &domain.MLModelVersion{ID: versionID}, deployment: &domain.MLDeploymentIntent{ID: deploymentID, EndpointID: endpointID, EnvironmentID: envID}}
				f.server.intentProc.RegisterHandler("ml", controlplane.NewMLIntentHandler(repo))
				f.publishState(t, nostrpool.KindEnvironmentRegistry, &domain.Environment{ID: envID, OrgID: f.orgID, Name: "prod"}, envID.String())
				f.publishState(t, nostrpool.KindMLInferenceEndpointRegistry, repo.endpoint, "endpoint:"+endpointID.String())
				args := map[string]any{"idempotency_key": "assistant-final", "endpoint_id": endpointID.String()}
				if tool == "bahia_assistant_ml_deploy" {
					args["model_version_id"] = versionID.String()
				}
				if tool == "bahia_assistant_ml_approve_deployment" {
					args["intent_id"], args["decision"] = deploymentID.String(), "approve"
				}
				ctx := f.ctx
				if mode == "rejected" {
					ctx = f.strangerCtx
				}
				receipt, err := f.server.InvokeAssistantAsyncTool(ctx, tool, args)
				if mode == "rejected" {
					require.Error(t, err)
					require.Zero(t, repo.writes)
					return
				}
				require.NoError(t, err)
				require.Equal(t, 30900, receipt.RequestKind)
				require.NotEmpty(t, receipt.RequestEventID)
				require.Equal(t, 1, repo.writes)
				proven, err := f.server.ResolveAssistantIntentReceipt(tool, f.actor, "assistant-final", receipt.RequestEventID)
				require.NoError(t, err)
				require.Equal(t, receipt.RequestEventID, proven.RequestEventID)
				if mode == "replay" {
					again, err := f.server.InvokeAssistantAsyncTool(ctx, tool, args)
					require.NoError(t, err)
					require.Equal(t, receipt.RequestEventID, again.RequestEventID)
					require.Equal(t, 1, repo.writes)
				}
			})
		}
	}
}

func TestFinalMCPD79D80FixtureMappings(t *testing.T) {
	type fixtureIntent struct {
		Domain, Op, Coordinate string
		Content                map[string]any
	}
	read := func(name string) []fixtureIntent {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", name))
		require.NoError(t, err)
		var doc struct{ Intents []fixtureIntent }
		require.NoError(t, json.Unmarshal(raw, &doc))
		return doc.Intents
	}
	d79, d80 := read("d79-intent-content.json"), read("d80-intent-content.json")
	f := newFinalIntentFixture(t, "ml", "pending")
	serviceID := uuid.New()
	f.publishState(t, nostrpool.KindServiceRegistry, &domain.Service{ID: serviceID, OrgID: f.orgID}, serviceID.String())
	endpoint := &domain.MLInferenceEndpoint{ID: uuid.MustParse(d79[3].Content["endpoint_id"].(string)), Name: "predict", EnvironmentID: uuid.New()}
	f.publishState(t, nostrpool.KindEnvironmentRegistry, &domain.Environment{ID: endpoint.EnvironmentID, OrgID: f.orgID, Name: "prod"}, endpoint.EnvironmentID.String())
	f.publishState(t, nostrpool.KindMLInferenceEndpointRegistry, endpoint, "endpoint:"+endpoint.ID.String())
	modelID := uuid.New()
	f.publishState(t, nostrpool.KindMLModelRegistry, &domain.MLModel{ID: modelID, Slug: "weather", Name: "weather"}, "model:weather")
	version := &domain.MLModelVersion{ID: uuid.MustParse(d79[3].Content["model_version_id"].(string)), ModelID: modelID, Version: "v1"}
	f.publishState(t, nostrpool.KindMLModelVersionRegistry, version, "model-version:"+version.ID.String())
	recipe := &domain.MLRecipe{ID: uuid.MustParse(d79[2].Content["recipe_id"].(string)), Name: "train", Version: "1"}
	f.publishState(t, nostrpool.KindMLRecipeRegistry, recipe, "recipe:train:1")
	provisionID := uuid.MustParse(d79[7].Content["intent_id"].(string))
	f.publishState(t, nostrpool.KindToolProvisionIntentState, &domain.ToolProvisionIntent{ID: provisionID, ServiceID: serviceID}, "tool-intent:"+provisionID.String())
	artifactID := uuid.MustParse(d80[3].Content["artifact_id"].(string))
	f.publishState(t, nostrpool.KindArtifactRegistry, &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:abc"}, "artifact:"+artifactID.String())
	channelID := uuid.MustParse(d80[6].Content["id"].(string))
	f.publishState(t, nostrpool.KindNotificationChannelRegistry, &domain.NotificationChannel{ID: channelID, OrgID: f.orgID, Name: "alerts"}, channelID.String())
	cases := []struct {
		fixture fixtureIntent
		tool    string
		args    map[string]any
	}{
		{d79[0], "bahia_ml_import_model", map[string]any{"model": "model:weather", "source": "huggingface", "source_uri": "hf://weather/model", "revision": "v1", "org_id": f.orgID.String()}},
		{d79[2], "bahia_ml_run_recipe", map[string]any{"recipe": "recipe:train:1", "inputs": map[string]any{"dataset": "weather"}, "org_id": f.orgID.String()}},
		{d79[3], "bahia_ml_deploy", map[string]any{"endpoint": "endpoint:predict:prod", "model_version": "model-version:weather:v1"}},
		{d79[4], "bahia_assistant_ml_approve_deployment", map[string]any{"intent_id": d79[4].Content["intent_id"], "decision": "approve", "org_id": f.orgID.String()}},
		{d79[6], "bahia_ml_rollback", map[string]any{"endpoint": "endpoint:predict:prod"}},
		{d79[7], "bahia_tool_provision_approve", map[string]any{"intent_id": provisionID.String(), "reason": "operator reviewed"}},
		{d79[8], "bahia_tool_provision_reject", map[string]any{"intent_id": provisionID.String(), "reason": "operator reviewed"}},
		{d80[3], "bahia_verify_signatures", map[string]any{"artifact_id": artifactID.String()}},
		{d80[6], "bahia_test_notification_channel", map[string]any{"channel_id": channelID.String()}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			w, err := f.server.intentWriteForTool(t.Context(), tc.tool, tc.args, uuid.NewString())
			require.NoError(t, err)
			require.Equal(t, tc.fixture.Domain, w.domain)
			require.Equal(t, tc.fixture.Op, w.op)
			require.Equal(t, tc.fixture.Coordinate, w.coordinate)
			for key, expected := range tc.fixture.Content {
				if key == "intent_id" && tc.fixture.Domain != "ml" && tc.fixture.Domain != "tool" {
					continue // callIntentWrite supplies the caller's generated intent ID.
				}
				require.Equal(t, expected, w.content[key], "content.%s", key)
			}
		})
	}
	sbomFixture := d80[2]
	require.Equal(t, "sbom", sbomFixture.Domain)
	require.Equal(t, "import", sbomFixture.Op)
	sbom, err := f.server.intentWriteForTool(t.Context(), "bahia_ingest_sbom", map[string]any{"artifact_id": artifactID.String(), "sbom_data": `{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"components":[]}`}, uuid.NewString())
	require.NoError(t, err)
	require.Equal(t, sbomFixture.Domain, sbom.domain)
	require.Equal(t, sbomFixture.Op, sbom.op)
	require.Equal(t, "artifact", sbom.content["subject"].(map[string]any)["type"])
	require.NotEmpty(t, sbom.content["payloadBase64"])
}

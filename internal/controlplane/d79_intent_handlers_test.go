package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type d79MLRegistry struct {
	MLIntentRegistry
	model      *domain.MLModel
	version    *domain.MLModelVersion
	recipe     *domain.MLRecipe
	endpoint   *domain.MLInferenceEndpoint
	deployment *domain.MLDeploymentIntent
	writes     int
}

type d79MLRepo struct{ *d70MLRepo }

func (r *d79MLRepo) GetModelBySlug(_ context.Context, slug string) (*domain.MLModel, error) {
	for _, model := range r.models {
		if model.Slug == slug {
			return model, nil
		}
	}
	return nil, nil
}
func (r *d79MLRepo) GetModelVersionByModelVersion(_ context.Context, modelID uuid.UUID, versionName string) (*domain.MLModelVersion, error) {
	for _, version := range r.versions {
		if version.ModelID == modelID && version.Version == versionName {
			return version, nil
		}
	}
	return nil, nil
}

func (r *d79MLRegistry) GetModelBySlug(_ context.Context, slug string) (*domain.MLModel, error) {
	if r.model != nil && r.model.Slug == slug {
		return r.model, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) CreateOrUpdateModel(_ context.Context, model *domain.MLModel) error {
	cp := *model
	cp.UpdatedAt = time.Now().UTC()
	r.model = &cp
	r.writes++
	return nil
}
func (r *d79MLRegistry) GetModelVersionByModelVersion(_ context.Context, modelID uuid.UUID, version string) (*domain.MLModelVersion, error) {
	if r.version != nil && r.version.ModelID == modelID && r.version.Version == version {
		return r.version, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) CreateOrUpdateModelVersion(_ context.Context, version *domain.MLModelVersion) error {
	cp := *version
	r.version = &cp
	r.writes++
	return nil
}
func (r *d79MLRegistry) GetModelVersion(_ context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	if r.version != nil && r.version.ID == id {
		return r.version, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) GetRecipe(_ context.Context, id uuid.UUID) (*domain.MLRecipe, error) {
	if r.recipe != nil && r.recipe.ID == id {
		return r.recipe, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) GetRecipeByNameVersion(_ context.Context, name, version string) (*domain.MLRecipe, error) {
	if r.recipe != nil && r.recipe.Name == name && r.recipe.Version == version {
		return r.recipe, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) CreateOrUpdateRecipe(_ context.Context, recipe *domain.MLRecipe) error {
	cp := *recipe
	cp.UpdatedAt = time.Now().UTC()
	r.recipe = &cp
	r.writes++
	return nil
}
func (r *d79MLRegistry) CreateOrUpdateRecipeRun(_ context.Context, _ *domain.MLRecipeRun) error {
	r.writes++
	return nil
}
func (r *d79MLRegistry) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	if r.endpoint != nil && r.endpoint.ID == id {
		return r.endpoint, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) GetInferenceEndpointByNameEnv(_ context.Context, name string, envID uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	if r.endpoint != nil && r.endpoint.Name == name && r.endpoint.EnvironmentID == envID {
		return r.endpoint, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) CreateDeploymentIntent(_ context.Context, deployment *domain.MLDeploymentIntent) error {
	cp := *deployment
	cp.UpdatedAt = time.Now().UTC()
	r.deployment = &cp
	r.writes++
	return nil
}
func (r *d79MLRegistry) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	if r.deployment != nil && r.deployment.ID == id {
		return r.deployment, nil
	}
	return nil, nil
}
func (r *d79MLRegistry) ApproveDeploymentIntent(_ context.Context, id uuid.UUID) error {
	r.deployment.ApprovalStatus = domain.ApprovalStatusApproved
	r.writes++
	return nil
}
func (r *d79MLRegistry) RejectDeploymentIntent(_ context.Context, id uuid.UUID) error {
	r.deployment.ApprovalStatus = domain.ApprovalStatusRejected
	r.writes++
	return nil
}
func (r *d79MLRegistry) RollbackWithMetadata(_ context.Context, endpointID, envID uuid.UUID, actor string, metadata map[string]any) (*domain.MLDeploymentIntent, error) {
	d := &domain.MLDeploymentIntent{ID: uuid.New(), EndpointID: endpointID, EnvironmentID: envID, RequestedBy: actor, Metadata: metadata}
	r.deployment = d
	r.writes++
	return d, nil
}

func TestD79MLOperationsAcceptedRejectedReplayConflict(t *testing.T) {
	modelID, recipeID, endpointID, versionID, deploymentID, envID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name, op, coordinate string
		content              map[string]any
		seed                 func(*d79MLRegistry)
	}{
		{"model import", "model-import", "model:weather", map[string]any{"model": "model:weather", "source": "huggingface", "source_uri": "hf://weather/model", "revision": "v1"}, nil},
		{"recipe definition", "recipe-apply", "recipe:train:1", map[string]any{"name": "train", "version": "1", "yaml": "name: train\nversion: '1'\ninputs: {}\nsteps:\n  - action: fetch_source\n    outputs:\n      source: artifact_ref\noutputs: {}\n"}, nil},
		{"recipe run", "recipe-run", "recipe-run:" + recipeID.String(), map[string]any{"recipe_id": recipeID.String(), "inputs": map[string]any{"dataset": "weather"}}, func(r *d79MLRegistry) { r.recipe = &domain.MLRecipe{ID: recipeID, Name: "train", Version: "1"} }},
		{"inference deploy", "inference-deploy", "inference-deploy:" + endpointID.String(), map[string]any{"endpoint_id": endpointID.String(), "model_version_id": versionID.String()}, func(r *d79MLRegistry) {
			r.endpoint = &domain.MLInferenceEndpoint{ID: endpointID, EnvironmentID: envID}
			r.version = &domain.MLModelVersion{ID: versionID, ModelID: modelID}
		}},
		{"inference approve", "inference-approval", "inference-approval:" + deploymentID.String(), map[string]any{"intent_id": deploymentID.String(), "decision": "approve"}, func(r *d79MLRegistry) {
			r.deployment = &domain.MLDeploymentIntent{ID: deploymentID, UpdatedAt: time.Now().UTC()}
		}},
		{"inference reject", "inference-approval", "inference-approval:" + deploymentID.String(), map[string]any{"intent_id": deploymentID.String(), "decision": "reject"}, func(r *d79MLRegistry) {
			r.deployment = &domain.MLDeploymentIntent{ID: deploymentID, UpdatedAt: time.Now().UTC()}
		}},
		{"inference rollback", "inference-rollback", "inference-rollback:" + endpointID.String(), map[string]any{"endpoint_id": endpointID.String()}, func(r *d79MLRegistry) { r.endpoint = &domain.MLInferenceEndpoint{ID: endpointID, EnvironmentID: envID} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &d79MLRegistry{}
			if tc.seed != nil {
				tc.seed(registry)
			}
			p, statuses := d70Processor(t, "ml", testPubkey, NewMLIntentHandler(registry))
			intent := d70Intent("ml", tc.op, tc.coordinate, testPubkey, tc.content)
			require.NoError(t, p.ProcessInProcess(t.Context(), intent))
			require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
			writes := registry.writes
			require.Positive(t, writes)
			require.NoError(t, p.ProcessInProcess(t.Context(), intent))
			require.Equal(t, writes, registry.writes)
			conflict := *intent
			conflict.Content = map[string]any{}
			for key, value := range intent.Content {
				conflict.Content[key] = value
			}
			conflict.Content["idempotency_conflict"] = true
			require.Error(t, p.ProcessInProcess(t.Context(), &conflict))
			require.Equal(t, "conflict", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
			denied := *intent
			denied.IntentID = uuid.NewString()
			denied.Actor = "known-non-fleet-principal"
			require.ErrorContains(t, p.ProcessInProcess(t.Context(), &denied), "insufficient permission")
			require.Equal(t, "rejected", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
			require.Equal(t, writes, registry.writes)
		})
	}
}

func TestD79ModelImportPublishesCanonicalOnce(t *testing.T) {
	repo := &d79MLRepo{d70MLRepo: newD70MLRepo()}
	registry := service.NewMLRegistryService(repo, nil, zap.NewNop())
	canonical := &d70MLCanonical{}
	registry.SetMLCPStatePublisher(canonical)
	p, _ := d70Processor(t, "ml", testPubkey, NewMLIntentHandler(registry))
	intent := d70Intent("ml", "model-import", "model:weather", testPubkey, map[string]any{"model": "model:weather", "source": "huggingface", "source_uri": "hf://weather/model", "revision": "v1"})
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, canonical.models)
	require.Equal(t, 1, canonical.versions)
	require.Equal(t, 2, repo.writes)
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, canonical.models)
	require.Equal(t, 1, canonical.versions)
}

func TestD79StatusPublishFailureReplaysWithoutSecondMutation(t *testing.T) {
	registry := &d79MLRegistry{}
	statuses := &statusCollector{}
	attempts := 0
	publisher := NewIntentStatusPublisher(func(ctx context.Context, event nostr.Event) error {
		attempts++
		if attempts == 1 {
			return errors.New("relay rejected status")
		}
		return statuses.publish(ctx, event)
	}, &testSigner{}, zap.NewNop())
	trust := NewTrustSet([]string{testPubkey}, zap.NewNop())
	p := NewIntentProcessor(trust, openTestStore(t), publisher, IntentProcessorConfig{EnabledDomains: map[string]bool{"ml": true}}, zap.NewNop())
	p.RegisterHandler("ml", NewMLIntentHandler(registry))
	intent := d70Intent("ml", "model-import", "model:weather", testPubkey, map[string]any{"model": "model:weather", "source": "huggingface", "source_uri": "hf://weather/model"})
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), intent), "relay rejected status")
	require.Equal(t, 1, registry.writes)
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.Equal(t, 1, registry.writes)
	require.Len(t, statuses.events, 1)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
}

func TestD79AdoptionScanStatusBoundedAndReplay(t *testing.T) {
	adoption := &stubAdoptionOperatorService{scanResp: []service.AdoptionPreview{{Target: service.AdoptionTarget{Name: "prod", EndpointRef: "docker-prod"}, Containers: []service.AdoptionPreviewContainer{{Discovered: runtime.DiscoveredContainer{ContainerID: "container-1", ContainerName: "api", Environment: map[string]string{"SECRET": "hidden"}}, SafeEnvironment: map[string]string{"APP": "prod"}, RedactedEnvironmentKeys: []string{"SECRET"}, Adoptable: true}}}}}
	p, statuses := d76Processor(t, "adoption", testPubkey, NewAdoptionIntentHandler(adoption, []string{testPubkey}))
	intent := d70Intent("adoption", "scan", "adoption:"+testOrgID().String(), testPubkey, map[string]any{"targets": []any{map[string]any{"name": "prod", "endpoint_ref": "docker-prod"}}, "limit": 1})
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.True(t, adoption.scanCalled)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	require.NotContains(t, statuses.events[0].Content, "hidden")
	require.Less(t, len(statuses.events[0].Content), 16*1024)
	adoption.scanCalled = false
	require.NoError(t, p.ProcessInProcess(t.Context(), intent))
	require.False(t, adoption.scanCalled)
	conflict := *intent
	conflict.Content = map[string]any{"limit": 2}
	require.Error(t, p.ProcessInProcess(t.Context(), &conflict))
	require.Equal(t, "conflict", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-denied"
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), &denied), "authorized adoption list")
	require.Equal(t, "rejected", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
}

func TestD79AdoptionScanOversizedFindingAdvancesBoundedPage(t *testing.T) {
	adoption := &stubAdoptionOperatorService{scanResp: []service.AdoptionPreview{{
		Target: service.AdoptionTarget{Name: "prod", EndpointRef: "docker-prod"},
		Containers: []service.AdoptionPreviewContainer{
			{Discovered: runtime.DiscoveredContainer{ContainerID: "large", ContainerName: strings.Repeat("x", 20000), Command: []string{"--password=must-not-publish"}}, SafeEnvironment: map[string]string{"PUBLIC": strings.Repeat("y", 20000)}},
			{Discovered: runtime.DiscoveredContainer{ContainerID: "small"}},
		},
	}}}
	p, statuses := d76Processor(t, "adoption", testPubkey, NewAdoptionIntentHandler(adoption, []string{testPubkey}))
	content := map[string]any{"targets": []any{map[string]any{"name": "prod", "endpoint_ref": "docker-prod"}}, "offset": 0, "limit": 1}
	first := d70Intent("adoption", "scan", "adoption:"+testOrgID().String(), testPubkey, content)
	require.NoError(t, p.ProcessInProcess(t.Context(), first))
	require.Less(t, len(statuses.events[0].Content), 16*1024)
	require.NotContains(t, statuses.events[0].Content, "must-not-publish")
	require.NotContains(t, statuses.events[0].Content, strings.Repeat("y", 100))
	findings := first.Result["findings"].([]any)
	require.Len(t, findings, 1)
	require.Equal(t, true, findings[0].(map[string]any)["item_truncated"])
	require.Equal(t, 1, first.Result["next_offset"])
	require.Equal(t, true, first.Result["truncated"])
	second := d70Intent("adoption", "scan", "adoption:"+testOrgID().String(), testPubkey, map[string]any{"targets": content["targets"], "offset": 1, "limit": 1})
	require.NoError(t, p.ProcessInProcess(t.Context(), second))
	require.Equal(t, 2, second.Result["next_offset"])
	require.Equal(t, false, second.Result["truncated"])
}

func TestD79ToolApprovalIntentPausedWithoutSQLMutation(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for _, source := range []string{"sql-only", "signed-request"} {
		t.Run(source, func(t *testing.T) {
			id := uuid.New()
			repo := newAtomicToolApprovalRepo(id, domain.ToolProvisionStatusAwaitingApproval)
			repo.intent.ResolvedTools = []domain.ResolvedTool{{Name: "malicious-package", Version: "1", Manager: "apt", Source: "sql-overridden-source"}}
			if source == "signed-request" {
				repo.intent.NostrEventID = strings.Repeat("a", 64)
			}
			reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(repo))
			p, statuses := d70Processor(t, "tool", actor, NewToolIntentHandler(reactor))
			event := toolApprovalEvent(t, testRequesterKey, "approve-d79-"+source, id, "approve")
			intent := d70Intent("tool", "approval-response", "tool-approval:"+id.String(), actor, map[string]any{"intent_id": id.String(), "action": "approve"})
			intent.Event = event
			require.ErrorContains(t, p.ProcessInProcess(t.Context(), intent), "tool approval paused")
			require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
			calls, applied, logs, status := repo.counts()
			require.Zero(t, calls)
			require.Zero(t, applied)
			require.Zero(t, logs)
			require.Equal(t, domain.ToolProvisionStatusAwaitingApproval, status)
		})
	}
}

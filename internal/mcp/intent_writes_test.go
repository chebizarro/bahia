package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mcpIntentHandler struct {
	calls   int
	failure error
	apply   func(*controlplane.Intent) error
}

func (h *mcpIntentHandler) PermissionFor(string) domain.Permission { return domain.PermWriteServices }
func (h *mcpIntentHandler) HandleIntent(_ context.Context, intent *controlplane.Intent) error {
	h.calls++
	if h.failure != nil {
		return h.failure
	}
	if h.apply != nil {
		return h.apply(intent)
	}
	return nil
}

func mcpIntentResult(t *testing.T, result *ToolResult) map[string]any {
	t.Helper()
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &content))
	return content
}

func TestMCPServiceIntentPipeline(t *testing.T) {
	operator := nostr.Generate().Public().Hex()
	orgID := uuid.New()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	canonical := attachCanonicalMCPFixture(t, server)
	handler := &mcpIntentHandler{}
	handler.apply = func(intent *controlplane.Intent) error {
		id, err := uuid.Parse(intent.Content["id"].(string))
		if err != nil {
			return err
		}
		canonical.publishService(t, &domain.Service{ID: id, OrgID: intent.OrgID, Name: intent.Content["name"].(string), ArtifactRepo: intent.Content["artifact_repo"].(string), DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker})
		return nil
	}
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): operator})), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"service": true}}, zap.NewNop())
	processor.RegisterHandler("service", handler)
	server.intentProc = processor
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: operator, Method: auth.MethodNIP98})
	args := map[string]any{"org_id": orgID.String(), "name": "api", "artifact_repo": "registry.example/api", "_meta": map[string]any{"progressToken": "call-7"}}

	first, err := server.CallTool(ctx, "bahia_create_service", args)
	require.NoError(t, err)
	require.False(t, first.IsError)
	firstContent := mcpIntentResult(t, first)
	require.Equal(t, "accepted", firstContent["status"])
	require.NotEmpty(t, firstContent["event_id"])
	require.Equal(t, "api", firstContent["state"].(map[string]any)["name"])
	require.Equal(t, 1, handler.calls)

	replay, err := server.CallTool(ctx, "bahia_create_service", args)
	require.NoError(t, err)
	require.False(t, replay.IsError)
	replayContent := mcpIntentResult(t, replay)
	require.Equal(t, firstContent["intent_id"], replayContent["intent_id"])
	require.Equal(t, firstContent["event_id"], replayContent["event_id"])
	require.Equal(t, firstContent["state"].(map[string]any)["id"], replayContent["state"].(map[string]any)["id"])
	require.Equal(t, 1, handler.calls, "same progressToken must not apply twice")

	wrongOrg, err := server.CallTool(ctx, "bahia_update_service", map[string]any{
		"service_id": firstContent["state"].(map[string]any)["id"], "org_id": uuid.New().String(), "name": "stolen",
	})
	require.NoError(t, err)
	require.True(t, wrongOrg.IsError)
	require.Equal(t, "rejected", mcpIntentResult(t, wrongOrg)["status"])
	require.Equal(t, 1, handler.calls, "cross-org update must not reach handler")

	unknown := nostr.Generate().Public().Hex()
	unknownCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "other", PubKey: unknown, Method: auth.MethodNIP98})
	denied, err := server.CallTool(unknownCtx, "bahia_create_service", map[string]any{
		"org_id": orgID.String(), "name": "other", "artifact_repo": "registry.example/other", "idempotency_key": "other-create",
	})
	require.NoError(t, err)
	require.True(t, denied.IsError)
	require.Equal(t, "rejected", mcpIntentResult(t, denied)["status"])
	require.Equal(t, 1, handler.calls, "TrustSet rejection must precede handler")
}

func TestMCPServiceIntentRejectionAndPending(t *testing.T) {
	operator := nostr.Generate().Public().Hex()
	orgID := uuid.New()
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: operator, Method: auth.MethodNIP98})
	for _, tc := range []struct {
		name    string
		failure error
		status  string
		isError bool
	}{
		{name: "rejected", failure: errors.New("validation refused"), status: "rejected", isError: true},
		{name: "pending", status: "pending", isError: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{operator}})
			store := testStateStore(t)
			server.stateStore = store
			handler := &mcpIntentHandler{failure: tc.failure}
			processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): operator})), store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"service": true}}, zap.NewNop())
			processor.RegisterHandler("service", handler)
			server.intentProc = processor
			result, err := server.CallTool(ctx, "bahia_create_service", map[string]any{"org_id": orgID.String(), "name": "api", "artifact_repo": "registry.example/api", "idempotency_key": "same-call"})
			require.NoError(t, err)
			require.Equal(t, tc.isError, result.IsError)
			content := mcpIntentResult(t, result)
			require.Equal(t, tc.status, content["status"])
			require.NotEmpty(t, content["intent_id"])
			require.NotEmpty(t, content["event_id"])
			require.Equal(t, 1, handler.calls)
		})
	}
}

func TestMCPDeleteServiceRequiresCanonicalTombstone(t *testing.T) {
	operator := nostr.Generate().Public().Hex()
	orgID, serviceID := uuid.New(), uuid.New()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{operator}})
	canonical := attachCanonicalMCPFixture(t, server)
	service := &domain.Service{ID: serviceID, OrgID: orgID, Name: "api", ArtifactRepo: "registry.example/api", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker}
	canonical.publishService(t, service)
	handler := &mcpIntentHandler{apply: func(*controlplane.Intent) error {
		return nostrpool.NewRelayFirstStatePublisher(canonical.projector, canonical.sink).PublishServiceRegistry(context.Background(), service, true)
	}}
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): operator})), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"service": true}}, zap.NewNop())
	processor.RegisterHandler("service", handler)
	server.intentProc = processor
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: operator, Method: auth.MethodNIP98})
	args := map[string]any{"service_id": serviceID.String(), "idempotency_key": "delete-service-1"}
	result, err := server.CallTool(ctx, "bahia_delete_service", args)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, "accepted", mcpIntentResult(t, result)["status"])
	require.Equal(t, 1, handler.calls)
	replay, err := server.CallTool(ctx, "bahia_delete_service", args)
	require.NoError(t, err)
	require.Equal(t, "accepted", mcpIntentResult(t, replay)["status"])
	require.Equal(t, 1, handler.calls)
}

type mcpFleetIntentHandler struct{ *mcpIntentHandler }

func (*mcpFleetIntentHandler) IsFleetScoped() bool { return true }

func TestMCPWorkerIntentPreservesCanonicalLabels(t *testing.T) {
	operator := nostr.Generate().Public().Hex()
	workerPubkey := nostr.Generate().Public().Hex()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{operator}})
	canonical := attachCanonicalMCPFixture(t, server)
	worker := &domain.Worker{PubKey: workerPubkey, Name: "worker", SchedulingState: domain.WorkerSchedulingActive, Labels: map[string]string{"region": "west"}}
	signer, err := controlplane.NewPrivateKeySigner(canonical.privateKey)
	require.NoError(t, err)
	publisher := controlplane.NewWorkerStatePublisher(canonical.sink, signer)
	require.NoError(t, publisher.Publish(context.Background(), worker))
	inner := &mcpIntentHandler{}
	inner.apply = func(intent *controlplane.Intent) error {
		worker.SchedulingState = domain.WorkerSchedulingState(intent.Content["scheduling_state"].(string))
		return publisher.Publish(context.Background(), worker)
	}
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet([]string{operator}, zap.NewNop()), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"worker": true}}, zap.NewNop())
	processor.RegisterHandler("worker", &mcpFleetIntentHandler{inner})
	server.intentProc = processor
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: operator, Method: auth.MethodNIP98})
	args := map[string]any{"worker_pubkey": workerPubkey, "idempotency_key": "cordon-1"}
	result, err := server.CallTool(ctx, "bahia_worker_cordon", args)
	require.NoError(t, err)
	require.False(t, result.IsError)
	payload := mcpIntentResult(t, result)
	require.Equal(t, "accepted", payload["status"])
	require.Equal(t, "cordoned", payload["state"].(map[string]any)["scheduling_state"])
	require.Equal(t, map[string]any{"region": "west"}, payload["state"].(map[string]any)["labels"])
	_, err = server.CallTool(ctx, "bahia_worker_cordon", args)
	require.NoError(t, err)
	require.Equal(t, 1, inner.calls)
}

func TestMCPIntentKeysAreScopedToActorAndTool(t *testing.T) {
	actor := nostr.Generate().Public().Hex()
	otherActor := nostr.Generate().Public().Hex()
	key := uuid.Must(uuid.NewV7()).String()
	args := map[string]any{"idempotency_key": key}
	first, err := mcpIntentID("bahia_create_service", actor, args)
	require.NoError(t, err)
	again, err := mcpIntentID("bahia_create_service", actor, args)
	require.NoError(t, err)
	require.Equal(t, first, again)
	otherTool, err := mcpIntentID("bahia_delete_service", actor, args)
	require.NoError(t, err)
	require.NotEqual(t, first, otherTool)
	otherPrincipal, err := mcpIntentID("bahia_create_service", otherActor, args)
	require.NoError(t, err)
	require.NotEqual(t, first, otherPrincipal)
}

func TestMCPNotificationIntentRedactsCanonicalCredentials(t *testing.T) {
	operator := nostr.Generate().Public().Hex()
	orgID := uuid.New()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{operator}})
	canonical := attachCanonicalMCPFixture(t, server)
	publisher := canonical.notificationPublisher(t, server)
	handler := &mcpIntentHandler{}
	handler.apply = func(intent *controlplane.Intent) error {
		id, err := uuid.Parse(intent.Content["id"].(string))
		if err != nil {
			return err
		}
		return publisher.PublishChannel(context.Background(), &domain.NotificationChannel{
			ID: id, OrgID: intent.OrgID, Name: intent.Content["name"].(string),
			ChannelType: domain.ChannelTypeWebhook,
			Config:      map[string]any{"url": "https://example.com/secret-webhook", "token": "private-token"},
			Enabled:     true,
		})
	}
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): operator})), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"notification": true}}, zap.NewNop())
	processor.RegisterHandler("notification", handler)
	server.intentProc = processor
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: operator, Method: auth.MethodNIP98})
	result, err := server.CallTool(ctx, "bahia_create_notification_channel", map[string]any{
		"org_id": orgID.String(), "name": "deployments", "channel_type": "webhook",
		"config":          map[string]any{"url": "https://example.com/secret-webhook", "token": "private-token"},
		"idempotency_key": "create-notification",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	payload := mcpIntentResult(t, result)
	require.Equal(t, "accepted", payload["status"])
	state := payload["state"].(map[string]any)
	require.Equal(t, true, state["config_redacted"])
	require.Equal(t, "[redacted]", state["config"].(map[string]any)["url"])
	require.Equal(t, "[redacted]", state["config"].(map[string]any)["token"])
	require.NotContains(t, result.Content[0].Text, "private-token")
}

func TestMCPPackageIntentReturnsCanonicalRepositoryAndArtifact(t *testing.T) {
	operator := nostr.Generate().Public().Hex()
	orgID := uuid.New()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{operator}})
	canonical := attachCanonicalMCPFixture(t, server)
	publisher := nostrpool.NewRelayFirstStatePublisher(canonical.projector, canonical.sink)
	handler := &mcpFleetIntentHandler{&mcpIntentHandler{}}
	handler.apply = func(intent *controlplane.Intent) error {
		switch intent.Op {
		case "repository-apply":
			return publisher.PublishPackageRepositoryRegistry(context.Background(), &domain.PackageRepository{
				ID: uuid.MustParse(intent.Content["id"].(string)), Name: intent.Content["name"].(string),
				Format:     domain.PackageRepositoryFormat(intent.Content["format"].(string)),
				BackendRef: intent.Content["backend_ref"].(string),
			}, false)
		case "publish":
			return publisher.PublishPackageArtifactRegistry(context.Background(), &domain.PackageArtifact{
				ID: uuid.New(), RepositoryID: uuid.MustParse(intent.Content["repository_id"].(string)),
				Namespace: intent.Content["namespace"].(string), PackageName: intent.Content["package_name"].(string),
				Version: intent.Content["version"].(string), Filename: intent.Content["filename"].(string),
			}, false)
		default:
			return errors.New("unexpected package operation")
		}
	}
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet([]string{operator}, zap.NewNop()), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"package": true}}, zap.NewNop())
	processor.RegisterHandler("package", handler)
	server.intentProc = processor
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: operator, Method: auth.MethodNIP98})
	applyArgs := map[string]any{"org_id": orgID.String(), "name": "npm-main", "format": "npm", "backend_ref": "nexus-main", "idempotency_key": "package-repo-1"}
	result, err := server.CallTool(ctx, "bahia_package_repository_apply", applyArgs)
	require.NoError(t, err)
	require.False(t, result.IsError)
	payload := mcpIntentResult(t, result)
	require.Equal(t, "accepted", payload["status"])
	repoID := payload["state"].(map[string]any)["id"].(string)
	_, err = server.CallTool(ctx, "bahia_package_repository_apply", applyArgs)
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls)

	uploadArgs := map[string]any{
		"org_id": orgID.String(), "repository_id": repoID, "namespace": "acme", "package_name": "api",
		"version": "v1", "filename": "api.tgz", "source_url": "https://example.com/api.tgz",
		"sha256": "sha256:abc", "size_bytes": 123, "idempotency_key": "package-upload-1",
	}
	result, err = server.CallTool(ctx, "bahia_package_upload", uploadArgs)
	require.NoError(t, err)
	require.False(t, result.IsError)
	payload = mcpIntentResult(t, result)
	require.Equal(t, "accepted", payload["status"])
	require.Equal(t, "api", payload["state"].(map[string]any)["package_name"])
	_, err = server.CallTool(ctx, "bahia_package_upload", uploadArgs)
	require.NoError(t, err)
	require.Equal(t, 2, handler.calls)
}

func TestAssistantServiceUsesInProcessIntents(t *testing.T) {
	for _, tc := range []struct {
		name, domain string
		args         func(orgID, serviceID, environmentID uuid.UUID) map[string]any
	}{
		{"bahia_assistant_service_deploy", "deployment", func(_, svc, env uuid.UUID) map[string]any {
			return map[string]any{"service_id": svc.String(), "environment_id": env.String(), "artifact_id": uuid.NewString()}
		}},
		{"bahia_assistant_service_rollback", "deployment", func(_, svc, env uuid.UUID) map[string]any {
			return map[string]any{"service_id": svc.String(), "environment_id": env.String(), "supersedes_intent_id": uuid.NewString(), "target_artifact_id": uuid.NewString()}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := nostr.Generate().Public().Hex()
			orgID, serviceID, environmentID := uuid.New(), uuid.New(), uuid.New()
			server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{actor}})
			canonical := attachCanonicalMCPFixture(t, server)
			canonical.publishService(t, &domain.Service{ID: serviceID, OrgID: orgID, Name: "api", ArtifactRepo: "registry.example/api", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker})
			canonical.publishEnvironment(t, &domain.Environment{ID: environmentID, OrgID: orgID, Name: "prod"})
			handler := &mcpIntentHandler{}
			proc := controlplane.NewIntentProcessor(controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): actor})), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{tc.domain: true}}, zap.NewNop())
			proc.RegisterHandler(tc.domain, handler)
			server.intentProc = proc
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: actor, PubKey: actor, Method: auth.MethodNIP98})
			args := tc.args(orgID, serviceID, environmentID)
			args["idempotency_key"] = "assistant-work-key"
			first, err := server.InvokeAssistantAsyncTool(ctx, tc.name, args)
			require.NoError(t, err)
			require.Equal(t, 30900, first.RequestKind)
			require.Equal(t, []int{30315}, first.ResultKinds)
			require.NotEmpty(t, first.RequestEventID)
			require.Equal(t, first.DTag, first.ResourceTags["intent_id"])
			require.Equal(t, 1, handler.calls)
			again, err := server.InvokeAssistantAsyncTool(ctx, tc.name, args)
			require.NoError(t, err)
			require.Equal(t, first.RequestEventID, again.RequestEventID)
			require.Equal(t, 1, handler.calls, "same assistant work key must not reapply")
			proven, err := server.ResolveAssistantIntentReceipt(tc.name, actor, "assistant-work-key", first.RequestEventID)
			require.NoError(t, err)
			require.Equal(t, first.RequestEventID, proven.RequestEventID)
			_, err = server.ResolveAssistantIntentReceipt(tc.name, actor, "assistant-work-key", strings.Repeat("0", 64))
			require.Error(t, err)
		})
	}
}

func TestLLMLifecycleMCPRefusesUnsignedTransportIntent(t *testing.T) {
	actor := nostr.Generate().Public().Hex()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{actor}})
	canonical := attachCanonicalMCPFixture(t, server)
	handler := &mcpIntentHandler{}
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet([]string{actor}, zap.NewNop()), canonical.store, nil,
		controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"llm": true}}, zap.NewNop())
	processor.RegisterHandler("llm", handler)
	server.intentProc = processor
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: actor, PubKey: actor, Method: auth.MethodNIP98})
	args := map[string]any{"route_id": uuid.NewString(), "environment_id": uuid.NewString(), "release_id": uuid.NewString(),
		"intent_id": uuid.NewString(), "org_id": uuid.NewString(), "decision": "approve", "idempotency_key": "llm-pre-submission-refusal"}
	for _, name := range []string{"bahia_llm_register_release", "bahia_llm_deploy", "bahia_llm_rollback", "bahia_llm_approve_deployment", "bahia_llm_reject_deployment"} {
		t.Run(name, func(t *testing.T) {
			result, err := server.CallTool(ctx, name, args)
			require.NoError(t, err)
			require.True(t, result.IsError)
			payload := mcpIntentResult(t, result)
			require.Equal(t, "rejected", payload["status"])
			require.Empty(t, payload["event_id"])
			require.Contains(t, payload["reason"], "operator-signed relay intent")
			require.Zero(t, handler.calls)
		})
	}
	for _, name := range []string{"bahia_assistant_llm_deploy", "bahia_assistant_llm_rollback", "bahia_assistant_llm_approve_deployment"} {
		t.Run(name, func(t *testing.T) {
			_, err := server.InvokeAssistantAsyncTool(ctx, name, args)
			require.ErrorIs(t, err, ErrToolCallUnauthorized)
			require.ErrorContains(t, err, "operator-signed relay intent")
			require.Zero(t, handler.calls)

			intentID, err := mcpIntentID(name, actor, args)
			require.NoError(t, err)
			oldEventID := strings.Repeat("a", 64)
			encoded, err := json.Marshal(controlplane.ProcessedIntentRecord{
				Actor: actor, Domain: "llm", Op: "deploy", Coordinate: "old-sql-route", EventID: oldEventID,
			})
			require.NoError(t, err)
			marker := nostr.Event{Kind: 30078, CreatedAt: nostr.Now(), Tags: nostr.Tags{
				{"d", "intent-processed:" + intentID}, {"intent_id", intentID},
			}, Content: string(encoded)}
			marker.ID = marker.GetID()
			_, err = canonical.store.SaveEvent(marker)
			require.NoError(t, err)
			_, err = server.ResolveAssistantIntentReceipt(name, actor, "llm-pre-submission-refusal", oldEventID)
			require.ErrorContains(t, err, "receipt recovery is unavailable")
		})
	}
}

func TestAssistantIntentHandlerFailureIsNotPreSubmissionRefusal(t *testing.T) {
	actor := nostr.Generate().Public().Hex()
	orgID, serviceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{actor}})
	canonical := attachCanonicalMCPFixture(t, server)
	canonical.publishService(t, &domain.Service{ID: serviceID, OrgID: orgID, Name: "api", ArtifactRepo: "registry.example/api", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker})
	canonical.publishEnvironment(t, &domain.Environment{ID: environmentID, OrgID: orgID, Name: "prod"})
	handler := &mcpIntentHandler{failure: errors.New("handler failed after dispatch")}
	proc := controlplane.NewIntentProcessor(controlplane.NewTrustSet(nil, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{orgID.String(): actor})), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"deployment": true}}, zap.NewNop())
	proc.RegisterHandler("deployment", handler)
	server.intentProc = proc
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: actor, PubKey: actor, Method: auth.MethodNIP98})
	_, err := server.InvokeAssistantAsyncTool(ctx, "bahia_assistant_service_deploy", map[string]any{
		"service_id": serviceID.String(), "environment_id": environmentID.String(), "artifact_id": uuid.NewString(), "idempotency_key": "handler-failure",
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrToolCallUnauthorized)
	require.Equal(t, 1, handler.calls)
}

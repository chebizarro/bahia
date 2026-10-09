package controlplane

import (
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func signedLLMLifecycleIntent(t *testing.T, key nostr.SecretKey, op, coordinate string, content map[string]any, createdAt time.Time) *Intent {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	return signedLLMLifecycleIntentWithID(t, key, id, op, coordinate, content, createdAt)
}

func signedLLMLifecycleIntentWithID(t *testing.T, key nostr.SecretKey, id uuid.UUID, op, coordinate string, content map[string]any, createdAt time.Time) *Intent {
	t.Helper()
	payload := make(map[string]any, len(content)+1)
	for name, value := range content {
		payload[name] = value
	}
	payload["intent_id"] = id.String()
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	event := &nostr.Event{Kind: 30900, CreatedAt: nostr.Timestamp(createdAt.Unix()), Content: string(encoded), Tags: nostr.Tags{
		{"d", coordinate}, {"t", "bahia-intent"}, {"domain", "llm"}, {"op", op},
		{"schema", "bahia.intent.llm.v1"}, {"intent_id", id.String()}, {"org", testOrgID().String()},
	}}
	require.NoError(t, event.Sign(key))
	intent, err := ParseIntent(event)
	require.NoError(t, err)
	intent.Actor = key.Public().Hex()
	return intent
}

func TestLLMLifecycleSignedAdmissionRejectsFabricatedAndDivergentRequests(t *testing.T) {
	key := nostr.Generate()
	route, env, release := uuid.New(), uuid.New(), uuid.New()
	content := map[string]any{"route_id": route.String(), "environment_id": env.String(), "release_id": release.String()}
	request := signedLLMLifecycleIntent(t, key, "deploy", route.String()+":"+env.String(), content, time.Now().Add(-time.Minute))
	store := openTestStore(t)
	statuses := &statusCollector{}
	registry := &llmDeploymentIntentRegistryTest{}
	processor := NewIntentProcessor(NewTrustSet([]string{key.Public().Hex()}, zap.NewNop()), store,
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()), IntentProcessorConfig{EnabledDomains: map[string]bool{"llm": true}}, zap.NewNop())
	processor.RegisterHandler("llm", NewLLMRouteIntentHandler(LLMRouteIntentHandlerConfig{Routes: registry, DeploymentUnavailableReason: "canonical executor unavailable"}))

	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), request), "not observed")
	require.Empty(t, statuses.events)
	_, err := store.SaveEvent(*request.Event)
	require.NoError(t, err)

	for name, change := range map[string]func(*Intent){
		"unsigned":             func(i *Intent) { i.Event = nil },
		"different actor":      func(i *Intent) { i.Actor = nostr.Generate().Public().Hex() },
		"different release":    func(i *Intent) { i.Content["release_id"] = uuid.NewString() },
		"different coordinate": func(i *Intent) { i.Coordinate = uuid.NewString() },
		"different revision":   func(i *Intent) { revision := time.Now(); i.ExpectedUpdatedAt = &revision },
		"tampered event":       func(i *Intent) { altered := *i.Event; altered.Content += " "; i.Event = &altered },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *request
			changed.Content = make(map[string]any, len(request.Content))
			for key, value := range request.Content {
				changed.Content[key] = value
			}
			change(&changed)
			require.Error(t, processor.ProcessInProcess(t.Context(), &changed))
			require.Empty(t, statuses.events, "invalid LLM identity must not induce a daemon-signed outcome")
			require.Zero(t, registry.creates)
		})
	}
	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), request), "LLM deployment paused")
	require.Len(t, statuses.events, 1)
	require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
	require.Zero(t, registry.creates)

	relayRequest := signedLLMLifecycleIntent(t, key, "deploy", route.String()+":"+env.String(), content, time.Now().Add(-30*time.Second))
	require.ErrorContains(t, processor.ProcessRelayIntent(t.Context(), relayRequest.Event), "not observed")
	require.Len(t, statuses.events, 1)
	_, err = store.SaveEvent(*relayRequest.Event)
	require.NoError(t, err)
	require.ErrorContains(t, processor.ProcessRelayIntent(t.Context(), relayRequest.Event), "LLM deployment paused")
	require.Len(t, statuses.events, 2)
	require.Zero(t, registry.creates)
}

func TestLLMLifecycleSignedAdmissionBindsImmutableSelection(t *testing.T) {
	key := nostr.Generate()
	route, env, release := uuid.New(), uuid.New(), uuid.New()
	base := map[string]any{"route_id": route.String(), "environment_id": env.String(), "release_id": release.String()}
	for _, tc := range []struct {
		name, op, coordinate, want string
		content                    map[string]any
	}{
		{"deploy", "deploy", route.String() + ":" + env.String(), "", base},
		{"wrong route coordinate", "deploy", uuid.NewString() + ":" + env.String(), "coordinate", base},
		{"missing release", "deploy", route.String() + ":" + env.String(), "release id", map[string]any{"route_id": route.String(), "environment_id": env.String()}},
		{"injected execution status", "deploy", route.String() + ":" + env.String(), "non-request field", map[string]any{"route_id": route.String(), "environment_id": env.String(), "release_id": release.String(), "status": "approved"}},
		{"rollback", "rollback", route.String() + ":" + env.String(), "", map[string]any{"route_id": route.String(), "environment_id": env.String()}},
		{"approve", "approve", release.String(), "", map[string]any{"deployment_intent_id": release.String()}},
		{"reject", "reject", release.String(), "", map[string]any{"deployment_intent_id": release.String()}},
		{"wrong decision coordinate", "approve", uuid.NewString(), "coordinate", map[string]any{"deployment_intent_id": release.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := signedLLMLifecycleIntent(t, key, tc.op, tc.coordinate, tc.content, time.Now().Add(-time.Minute))
			err := validateLLMLifecycleSignedRequest(request)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func TestLLMLifecycleCrossDomainProcessedMarkerCannotBypassPausedRefusal(t *testing.T) {
	key := nostr.Generate()
	store := openTestStore(t)
	statuses := &statusCollector{}
	registry := &llmDeploymentIntentRegistryTest{}
	processor := NewIntentProcessor(NewTrustSet([]string{key.Public().Hex()}, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): key.Public().Hex()})), store,
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true, "llm": true}}, zap.NewNop())
	processor.RegisterHandler("test", &testDomainHandler{})
	processor.RegisterHandler("llm", NewLLMRouteIntentHandler(LLMRouteIntentHandlerConfig{
		Routes: registry, DeploymentUnavailableReason: "canonical executor unavailable", Logger: zap.NewNop(),
	}))
	id, err := uuid.NewV7()
	require.NoError(t, err)
	prior := &Intent{Domain: "test", Op: "create", OrgID: testOrgID(), Actor: key.Public().Hex(),
		IntentID: id.String(), Coordinate: "other-family", Content: map[string]any{"id": "other-family"}}
	require.NoError(t, processor.ProcessInProcess(t.Context(), prior))
	require.NotNil(t, processor.ProcessedIntent(id.String()))
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))

	route, env := uuid.New(), uuid.New()
	request := signedLLMLifecycleIntentWithID(t, key, id, "deploy", route.String()+":"+env.String(),
		map[string]any{"route_id": route.String(), "environment_id": env.String(), "release_id": uuid.NewString()}, time.Now().Add(-time.Minute))
	_, err = store.SaveEvent(*request.Event)
	require.NoError(t, err)
	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), request), "LLM deployment paused")
	require.Len(t, statuses.events, 2)
	require.Equal(t, "rejected", tagValueNostr(statuses.events[1].Tags, "status"))
	require.Zero(t, registry.creates)
	require.Equal(t, "test", processor.ProcessedIntent(id.String()).Domain, "paused LLM request must not replace a prior receipt")
}

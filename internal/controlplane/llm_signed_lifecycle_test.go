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
		{"d", coordinate}, {"domain", "llm"}, {"schema", "bahia.intent.llm.v1"},
		{"t", "bahia-intent"}, {"t", "llm"}, {"op", op},
		{"org", testOrgID().String()}, {"intent_id", id.String()},
	}}
	require.NoError(t, event.Sign(key))
	intent, err := ParseIntent(event)
	require.NoError(t, err)
	intent.Actor = key.Public().Hex()
	return intent
}

func TestLLMPublisherTopicsBindExactIntentAndDomain(t *testing.T) {
	key := nostr.Generate()
	route, env := uuid.New(), uuid.New()
	base := signedLLMLifecycleIntent(t, key, "deploy", route.String()+":"+env.String(),
		map[string]any{"route_id": route.String(), "environment_id": env.String(), "release_id": uuid.NewString()}, time.Now().Add(-time.Minute))
	require.NoError(t, validateLLMLifecycleSignedRequest(base), "web and Go publisher emit both required t tags")
	for _, tc := range []struct {
		name   string
		change func(nostr.Tags) nostr.Tags
	}{
		{"duplicate intent topic", func(tags nostr.Tags) nostr.Tags { return append(tags, nostr.Tag{"t", "bahia-intent"}) }},
		{"duplicate domain topic", func(tags nostr.Tags) nostr.Tags { return append(tags, nostr.Tag{"t", "llm"}) }},
		{"unknown topic", func(tags nostr.Tags) nostr.Tags { return append(tags, nostr.Tag{"t", "other"}) }},
		{"missing domain topic", func(tags nostr.Tags) nostr.Tags {
			out := make(nostr.Tags, 0, len(tags))
			for _, tag := range tags {
				if len(tag) > 1 && tag[0] == "t" && tag[1] == "llm" {
					continue
				}
				out = append(out, tag)
			}
			return out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := *base.Event
			event.Tags = tc.change(append(nostr.Tags(nil), base.Event.Tags...))
			require.NoError(t, event.Sign(key))
			parsed, err := ParseIntent(&event)
			require.NoError(t, err)
			parsed.Actor = key.Public().Hex()
			require.ErrorContains(t, validateLLMLifecycleSignedRequest(parsed), "envelope differs")
		})
	}
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

func TestLLMReleaseRegistrationRequiresSignedObservationAndNeverWritesSQL(t *testing.T) {
	key := nostr.Generate()
	store := openTestStore(t)
	statuses := &statusCollector{}
	registry := &llmDeploymentIntentRegistryTest{}
	processor := NewIntentProcessor(NewTrustSet([]string{key.Public().Hex()}, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): key.Public().Hex()})), store,
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true, "llm": true}}, zap.NewNop())
	processor.RegisterHandler("test", &testDomainHandler{})
	processor.RegisterHandler("llm", NewLLMRouteIntentHandler(LLMRouteIntentHandlerConfig{Routes: registry}))
	id, err := uuid.NewV7()
	require.NoError(t, err)
	prior := &Intent{Domain: "test", Op: "create", OrgID: testOrgID(), Actor: key.Public().Hex(),
		IntentID: id.String(), Coordinate: "other-family", Content: map[string]any{"id": "other-family"}}
	require.NoError(t, processor.ProcessInProcess(t.Context(), prior))
	previousStatuses := len(statuses.events)
	releaseID, routeID := uuid.New(), uuid.New()
	request := signedLLMLifecycleIntentWithID(t, key, id, "release-register", "llm-release:"+releaseID.String(),
		map[string]any{"id": releaseID.String(), "route_id": routeID.String(), "version": "v1", "model_ref": "hf://example/chat",
			"model_source": "huggingface", "backend_preferences": []string{"external_api"},
			"external_backend": map[string]any{"base_url": "https://llm.example"}}, time.Now().Add(-time.Minute))
	require.NoError(t, validateLLMLifecycleSignedRequest(request))
	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), request), "not observed")
	require.Len(t, statuses.events, previousStatuses, "unobserved request must not induce a signed outcome")
	require.Zero(t, registry.releases)
	_, err = store.SaveEvent(*request.Event)
	require.NoError(t, err)
	for range 2 {
		require.ErrorContains(t, processor.ProcessInProcess(t.Context(), request), "LLM release registration paused")
		require.Equal(t, "rejected", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
		require.Zero(t, registry.releases, "a replay marker must not authorize SQL release creation")
	}
	require.ErrorContains(t, processor.ProcessRelayIntent(t.Context(), request.Event), "LLM release registration paused")
	require.Equal(t, "rejected", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	require.Zero(t, registry.releases)
	require.Equal(t, "test", processor.ProcessedIntent(id.String()).Domain)
}

func TestLLMReleaseRegistrationRejectsUnboundSignedContent(t *testing.T) {
	key := nostr.Generate()
	releaseID, routeID := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name, coordinate string
		content          map[string]any
		want             string
	}{
		{"release id differs", "llm-release:" + uuid.NewString(), map[string]any{"id": releaseID.String(), "route_id": routeID.String()}, "coordinate"},
		{"missing route", "llm-release:" + releaseID.String(), map[string]any{"id": releaseID.String()}, "coordinate"},
		{"injected outcome", "llm-release:" + releaseID.String(), map[string]any{"id": releaseID.String(), "route_id": routeID.String(), "status": "registered"}, "non-request field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := signedLLMLifecycleIntent(t, key, "release-register", tc.coordinate, tc.content, time.Now().Add(-time.Minute))
			require.ErrorContains(t, validateLLMLifecycleSignedRequest(request), tc.want)
		})
	}
}

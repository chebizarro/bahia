package reconcile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

type llmSealer struct{}

func (llmSealer) EncryptConfidential(_ context.Context, _ string, plain []byte, _ int, _, _ string, _ []byte) (string, error) {
	return "sealed:" + base64.StdEncoding.EncodeToString(plain), nil
}
func (llmSealer) DecryptConfidential(_ context.Context, ciphertext string, _ int, _, _ string) ([]byte, error) {
	if !strings.HasPrefix(ciphertext, "sealed:") {
		return nil, fmt.Errorf("not sealed")
	}
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "sealed:"))
}

type llmGatewayStub struct {
	obs       *llmadapter.GatewayRouteObservation
	calls     int
	blackhole bool
}

func (g *llmGatewayStub) UpsertRoute(_ context.Context, _ string, spec llmadapter.GatewayRouteSpec) (*llmadapter.GatewayRouteObservation, error) {
	g.calls++
	if g.blackhole {
		return nil, nil
	}
	g.obs = &llmadapter.GatewayRouteObservation{RouteName: spec.RouteName, Status: domain.GatewayRouteStatusSynced,
		GatewayConfigHash: spec.ManagedConfigHash()}
	return g.obs, nil
}
func (g *llmGatewayStub) GetRoute(context.Context, string, string) (*llmadapter.GatewayRouteObservation, error) {
	return g.obs, nil
}
func (g *llmGatewayStub) DeleteRoute(context.Context, string, string) error {
	return fmt.Errorf("unexpected delete")
}

type llmFixture struct {
	controller *LLMCanonicalController
	store      *localstore.Store
	outbox     *localstore.Outbox
	serviceKey nostr.SecretKey
	actorKey   nostr.SecretKey
	orgID      uuid.UUID
	routeID    uuid.UUID
	envID      uuid.UUID
	releaseID  uuid.UUID
	secretID   uuid.UUID
	request    *controlplane.Intent
	release    *controlplane.Intent
	gateway    *llmGatewayStub
	secret     *rotatedAutoSecretStore
	protected  bool
}

func llmSignedIntent(t *testing.T, actor nostr.SecretKey, orgID uuid.UUID, op, coordinate string, body map[string]any) *controlplane.Intent {
	return llmSignedIntentAt(t, actor, orgID, op, coordinate, body, nostr.Now())
}

func llmSignedIntentAt(t *testing.T, actor nostr.SecretKey, orgID uuid.UUID, op, coordinate string, body map[string]any, at nostr.Timestamp) *controlplane.Intent {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	body["intent_id"] = id.String()
	content, err := json.Marshal(body)
	require.NoError(t, err)
	event := nostr.Event{Kind: 30900, CreatedAt: at, Tags: nostr.Tags{
		{"d", coordinate}, {"domain", "llm"}, {"schema", "bahia.intent.llm.v1"},
		{"t", "bahia-intent"}, {"t", "llm"}, {"op", op},
		{"org", orgID.String()}, {"intent_id", id.String()},
	}, Content: string(content)}
	require.NoError(t, event.Sign(actor))
	parsed, err := controlplane.ParseIntent(&event)
	require.NoError(t, err)
	parsed.Actor = actor.Public().Hex()
	require.NoError(t, controlplane.ValidateLLMLifecycleSignedRequest(parsed))
	return parsed
}

func llmQuorumProof(t *testing.T, outbox *localstore.Outbox, event nostr.Event) {
	t.Helper()
	_, err := outbox.EnqueuePublisherDelivery(localstore.OutboxEntry{Event: event, Target: "default", Delivered: true,
		Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://relay"}, Required: 1},
		Relays: map[string]localstore.RelayDelivery{"wss://relay": {Accepted: true}}})
	require.NoError(t, err)
	_, err = outbox.CommitPublisherRound(event.ID, localstore.OutboxRound{Target: "default", Delivered: true,
		State:  localstore.OutboxPublished,
		Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://relay"}, Required: 1},
		Relays: map[string]localstore.RelayDelivery{"wss://relay": {Accepted: true}}})
	require.NoError(t, err)
	proof, ok, err := outbox.GetDeliveryProof(event.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, proof.ValidFor(event, "default"))
}

func (f *llmFixture) accept(t *testing.T, intent *controlplane.Intent) {
	t.Helper()
	status := nostr.Event{Kind: 30315, CreatedAt: intent.Event.CreatedAt + 1, Tags: nostr.Tags{
		{"d", "intent-status:" + intent.Actor + ":" + intent.Coordinate}, {"domain", "intent"},
		{"t", "intent-status"}, {"workflow", "llm"}, {"status", "accepted"},
		{"p", intent.Actor}, {"e", intent.Event.ID.Hex()}, {"intent_id", intent.IntentID},
	}, Content: `{"result":"applied"}`}
	require.NoError(t, status.Sign(f.serviceKey))
	_, err := f.store.SaveEvent(status)
	require.NoError(t, err)
	llmQuorumProof(t, f.outbox, status)
}

func newLLMFixture(t *testing.T, protected, withSecret bool) *llmFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	outboxPath := filepath.Join(dir, "outbox.bolt")
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	lock, err := service.NewRuntimeApplyLock(outboxPath)
	require.NoError(t, err)
	serviceKey, actorKey := nostr.Generate(), nostr.Generate()
	orgID, routeID, envID, releaseID, secretID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	route := domain.LLMRoute{ID: routeID, Name: "model", UpdatedAt: time.Unix(100, 0).UTC(),
		GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "model"}}
	if withSecret {
		route.GatewayConfig.HeaderSecretRefs = map[string]string{"Authorization": secretID.String()}
	}
	env := canonicalEnvironment{Environment: domain.Environment{ID: envID, OrgID: orgID, Name: "prod", Protected: protected}}
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.LLMRouteRegistry, routeID.String(), route, 100)
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.EnvironmentRegistry, envID.String(), env, 100)
	runtime, err := NewCanonicalRuntimeSource(store, serviceKey.Public().Hex())
	require.NoError(t, err)
	gateway := &llmGatewayStub{}
	controller := &LLMCanonicalController{Runtime: runtime, Outbox: outbox, DeliveryTarget: "default",
		Signer: keyer.NewPlainKeySigner(serviceKey), Encryptor: llmSealer{}, Gateway: gateway,
		GatewayRef: "gateway", GatewayAdminRevision: "gateway-admin-v1", Lock: lock,
		AllowedAuthor: func(author string) bool { return author == actorKey.Public().Hex() },
		PublishRelease: func(_ context.Context, event nostr.Event) error {
			_, err := store.SaveEvent(event)
			if err == nil {
				llmQuorumProof(t, outbox, event)
			}
			return err
		},
		Enqueue: func(_ context.Context, event nostr.Event) error {
			_, err := outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: "default"})
			return err
		},
	}
	f := &llmFixture{controller: controller, store: store, outbox: outbox,
		serviceKey: serviceKey, actorKey: actorKey, orgID: orgID, routeID: routeID,
		envID: envID, releaseID: releaseID, secretID: secretID, gateway: gateway, protected: protected}
	if withSecret {
		pin := domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "llm-credential", Version: 7}
		records := autoSecretRecords{orgID: orgID, ref: domain.SecretRef{ID: secretID, ServiceID: routeID,
			Name: "gateway auth", CredentialRef: &pin}}
		f.secret = &rotatedAutoSecretStore{activeVersion: 7}
		resolver := secretstore.NewResolver(f.secret)
		resolver.UseRecords(records)
		controller.Records, controller.Secrets = records, resolver
	}
	release := domain.LLMRelease{ID: releaseID, RouteID: routeID, Version: "v1",
		ModelRef: "external-model", ModelSource: domain.ModelSourceHuggingFace,
		BackendPreferences: []domain.LLMBackendKind{domain.LLMBackendKindExternalAPI},
		ExternalBackend:    &domain.LLMExternalBackendConfig{BaseURL: "https://api.example/v1"}}
	encoded, err := json.Marshal(release)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(encoded, &body))
	delete(body, "created_at")
	f.release = llmSignedIntent(t, actorKey, orgID, "release-register", "llm-release:"+releaseID.String(), body)
	_, err = store.SaveEvent(*f.release.Event)
	require.NoError(t, err)
	require.NoError(t, controller.HandleLLMIntent(t.Context(), f.release))
	return f
}

func (f *llmFixture) deploy(t *testing.T) {
	t.Helper()
	f.request = llmSignedIntent(t, f.actorKey, f.orgID, "deploy", f.routeID.String()+":"+f.envID.String(),
		map[string]any{"route_id": f.routeID.String(), "environment_id": f.envID.String(), "release_id": f.releaseID.String()})
	_, err := f.store.SaveEvent(*f.request.Event)
	require.NoError(t, err)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), f.request))
	f.accept(t, f.request)
}

func (f *llmFixture) decide(t *testing.T, action string) *controlplane.Intent {
	t.Helper()
	decision := llmSignedIntent(t, f.actorKey, f.orgID, action, f.request.IntentID,
		map[string]any{"deployment_intent_id": f.request.IntentID})
	_, err := f.store.SaveEvent(*decision.Event)
	require.NoError(t, err)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), decision))
	f.accept(t, decision)
	return decision
}

func TestCanonicalLLMReleaseQuorumBeforeAcceptanceAndReplay(t *testing.T) {
	f := newLLMFixture(t, false, false)
	admission, err := f.outbox.GetControlRecord(llmReleaseAdmissionFamily, f.release.Content["id"].(string))
	require.NoError(t, err)
	var record llmReleaseAdmission
	require.NoError(t, json.Unmarshal(admission, &record))
	require.NotContains(t, record.ReleaseEvent.Content, "external-model")
	proof, ok, err := f.outbox.GetDeliveryProof(record.ReleaseEvent.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, proof.ValidFor(record.ReleaseEvent, "default"))
	// A route revision moving after release publication does not change the
	// immutable release event or make a replay publish another release.
	saveCanonicalRuntimeJSON(t, f.store, f.serviceKey, kinds.LLMRouteRegistry, f.routeID.String(),
		domain.LLMRoute{ID: f.routeID, Name: "model-new"}, 101)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), f.release))
	again, err := f.outbox.GetControlRecord(llmReleaseAdmissionFamily, f.releaseID.String())
	require.NoError(t, err)
	require.Equal(t, admission, again)
}

func TestCanonicalLLMReleaseDoesNotSucceedWithoutQuorum(t *testing.T) {
	f := newLLMFixture(t, false, false)
	newID := uuid.New()
	request := llmSignedIntent(t, f.actorKey, f.orgID, "release-register", "llm-release:"+newID.String(),
		map[string]any{"id": newID.String(), "route_id": f.routeID.String(), "version": "v2",
			"model_ref": "external-model-2", "model_source": string(domain.ModelSourceHuggingFace),
			"backend_preferences": []domain.LLMBackendKind{domain.LLMBackendKindExternalAPI},
			"external_backend":    domain.LLMExternalBackendConfig{BaseURL: "https://api.example/v2"}})
	_, err := f.store.SaveEvent(*request.Event)
	require.NoError(t, err)
	publications := 0
	f.controller.PublishRelease = func(_ context.Context, event nostr.Event) error {
		publications++
		_, err := f.store.SaveEvent(event)
		return err
	}
	require.ErrorContains(t, f.controller.HandleLLMIntent(t.Context(), request), "lacks durable relay quorum receipt")
	raw, err := f.outbox.GetControlRecord(llmReleaseAdmissionFamily, newID.String())
	require.NoError(t, err)
	var admission llmReleaseAdmission
	require.NoError(t, json.Unmarshal(raw, &admission))
	require.Equal(t, 1, publications)
	llmQuorumProof(t, f.outbox, admission.ReleaseEvent)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), request))
	require.Equal(t, 1, publications, "replay uses the admitted immutable release event")
}

func TestCanonicalLLMRegistrationRejectsUnsupportedOrLiteralCredentialRelease(t *testing.T) {
	f := newLLMFixture(t, false, false)
	for _, backend := range []struct {
		name    string
		content map[string]any
	}{
		{"literal health header", map[string]any{"backend_preferences": []domain.LLMBackendKind{domain.LLMBackendKindExternalAPI},
			"external_backend": domain.LLMExternalBackendConfig{BaseURL: "https://api.example", HealthHeaders: map[string]string{"Authorization": "secret-value"}}}},
		{"runtime model launch", map[string]any{"backend_preferences": []domain.LLMBackendKind{domain.LLMBackendKindVLLM},
			"runtime_backend": domain.LLMRuntimeManagedBackendConfig{Image: "vllm", ContainerPort: 8000, HostPort: 18000,
				HealthPath: "/health", Environment: map[string]string{"TOKEN": "secret-value"}}}},
	} {
		t.Run(backend.name, func(t *testing.T) {
			id := uuid.New()
			body := map[string]any{"id": id.String(), "route_id": f.routeID.String(), "version": "v2",
				"model_ref": "model", "model_source": string(domain.ModelSourceHuggingFace)}
			for key, value := range backend.content {
				body[key] = value
			}
			request := llmSignedIntent(t, f.actorKey, f.orgID, "release-register", "llm-release:"+id.String(), body)
			_, err := f.store.SaveEvent(*request.Event)
			require.NoError(t, err)
			require.ErrorIs(t, f.controller.HandleLLMIntent(t.Context(), request), ErrFreshDecisionRequired)
			admission, err := f.outbox.GetControlRecord(llmReleaseAdmissionFamily, id.String())
			require.NoError(t, err)
			require.Nil(t, admission)
		})
	}
}

func TestCanonicalLLMDeployApprovalReplayAndDenialWithoutDatabase(t *testing.T) {
	for _, action := range []string{"direct", "approve", "reject"} {
		t.Run(action, func(t *testing.T) {
			f := newLLMFixture(t, action != "direct", false)
			f.deploy(t)
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			var decision *controlplane.Intent
			if action != "direct" {
				require.Zero(t, f.gateway.calls, "protected deployment waits for approval")
				decision = f.decide(t, action)
			}
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			receipt, err := f.controller.executor(nil, nil).readReceipt(uuid.MustParse(f.request.IntentID))
			require.NoError(t, err)
			require.NotNil(t, receipt)
			if action == "reject" {
				require.Equal(t, "refused", receipt.Phase)
				require.Zero(t, f.gateway.calls)
			} else {
				require.Equal(t, "terminal", receipt.Phase)
				require.Equal(t, 1, f.gateway.calls)
			}
			require.True(t, receipt.Checkpoint.CheckID())
			require.True(t, receipt.Checkpoint.VerifySignature())
			require.NoError(t, f.controller.HandleLLMIntent(t.Context(), f.request))
			if decision != nil {
				require.NoError(t, f.controller.HandleLLMIntent(t.Context(), decision))
			}
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			if action == "reject" {
				require.Zero(t, f.gateway.calls)
			} else {
				require.Equal(t, 1, f.gateway.calls)
			}
		})
	}
}

func TestCanonicalLLMApprovalWaitsForExactRequestQuorumProof(t *testing.T) {
	f := newLLMFixture(t, true, false)
	f.request = llmSignedIntent(t, f.actorKey, f.orgID, "deploy", f.routeID.String()+":"+f.envID.String(),
		map[string]any{"route_id": f.routeID.String(), "environment_id": f.envID.String(), "release_id": f.releaseID.String()})
	_, err := f.store.SaveEvent(*f.request.Event)
	require.NoError(t, err)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), f.request))
	approval := llmSignedIntent(t, f.actorKey, f.orgID, "approve", f.request.IntentID,
		map[string]any{"deployment_intent_id": f.request.IntentID})
	_, err = f.store.SaveEvent(*approval.Event)
	require.NoError(t, err)
	require.ErrorIs(t, f.controller.HandleLLMIntent(t.Context(), approval), ErrFreshDecisionRequired)
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Zero(t, f.gateway.calls)
	f.accept(t, f.request)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), approval))
	f.accept(t, approval)
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Equal(t, 1, f.gateway.calls)
}

func TestCanonicalLLMRefusesMovedWinnersAndPinnedSignetVersion(t *testing.T) {
	for _, change := range []string{"route", "environment", "release", "secret", "gateway-admin"} {
		t.Run(change, func(t *testing.T) {
			f := newLLMFixture(t, false, change == "secret")
			f.deploy(t)
			switch change {
			case "route":
				saveCanonicalRuntimeJSON(t, f.store, f.serviceKey, kinds.LLMRouteRegistry, f.routeID.String(),
					domain.LLMRoute{ID: f.routeID, Name: "moved"}, 101)
			case "environment":
				saveCanonicalRuntimeJSON(t, f.store, f.serviceKey, kinds.EnvironmentRegistry, f.envID.String(),
					canonicalEnvironment{Environment: domain.Environment{ID: f.envID, OrgID: f.orgID, Name: "moved"}}, 101)
			case "release":
				raw, err := f.outbox.GetControlRecord(llmReleaseAdmissionFamily, f.releaseID.String())
				require.NoError(t, err)
				var registered llmReleaseAdmission
				require.NoError(t, json.Unmarshal(raw, &registered))
				event := registered.ReleaseEvent
				event.CreatedAt++
				require.NoError(t, event.Sign(f.serviceKey))
				_, err = f.store.SaveEvent(event)
				require.NoError(t, err)
				llmQuorumProof(t, f.outbox, event)
			case "secret":
				f.secret.activeVersion = 8
			case "gateway-admin":
				f.controller.GatewayAdminRevision = "gateway-admin-v2"
			}
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			require.Zero(t, f.gateway.calls)
			receipt, err := f.controller.executor(nil, nil).readReceipt(uuid.MustParse(f.request.IntentID))
			require.NoError(t, err)
			require.NotNil(t, receipt)
			require.Equal(t, "refused", receipt.Phase)
		})
	}
}

func TestCanonicalLLMBindsSignetVersionWithoutPersistingValue(t *testing.T) {
	f := newLLMFixture(t, false, true)
	f.deploy(t)
	staged, err := f.outbox.GetControlRecord(llmPendingFamily, f.request.IntentID)
	require.NoError(t, err)
	var admission llmPending
	require.NoError(t, json.Unmarshal(staged, &admission))
	require.Equal(t, f.request.Event.ID.Hex(), admission.Snapshot.LLM.RequestEventID)
	require.Len(t, admission.Snapshot.SecretPins, 1)
	require.Equal(t, f.secretID, admission.Snapshot.SecretPins[0].SecretID)
	require.Equal(t, "llm-credential", admission.Snapshot.SecretPins[0].CredentialID)
	require.Equal(t, 7, admission.Snapshot.SecretPins[0].Version)
	require.NotContains(t, string(staged), "private-value")
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Equal(t, 1, f.gateway.calls)
	stored, err := f.outbox.GetControlRecord(llmRouteReceiptFamily, f.request.IntentID)
	require.NoError(t, err)
	require.NotContains(t, string(stored), "private-value")
}

func TestCanonicalLLMRefusesObservedRequestOrApprovalConflict(t *testing.T) {
	for _, which := range []string{"request", "approval"} {
		t.Run(which, func(t *testing.T) {
			f := newLLMFixture(t, which == "approval", false)
			f.deploy(t)
			original := f.request.Event
			if which == "approval" {
				decision := f.decide(t, "approve")
				original = decision.Event
			}
			moved := *original
			moved.CreatedAt++
			require.NoError(t, moved.Sign(f.actorKey))
			_, err := f.store.SaveEvent(moved)
			require.NoError(t, err)
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			require.Zero(t, f.gateway.calls)
			receipt, err := f.controller.executor(nil, nil).readReceipt(uuid.MustParse(f.request.IntentID))
			require.NoError(t, err)
			require.Equal(t, "refused", receipt.Phase)
		})
	}
}

func TestCanonicalLLMRollbackUsesPreviousTerminalRelease(t *testing.T) {
	f := newLLMFixture(t, false, false)
	f.deploy(t)
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	first := f.releaseID
	second := uuid.New()
	secondRelease := llmSignedIntentAt(t, f.actorKey, f.orgID, "release-register", "llm-release:"+second.String(),
		map[string]any{"id": second.String(), "route_id": f.routeID.String(), "version": "v2",
			"model_ref": "external-model-2", "model_source": string(domain.ModelSourceHuggingFace),
			"backend_preferences": []domain.LLMBackendKind{domain.LLMBackendKindExternalAPI},
			"external_backend":    domain.LLMExternalBackendConfig{BaseURL: "https://api.example/v2"}},
		f.request.Event.CreatedAt+2)
	_, err := f.store.SaveEvent(*secondRelease.Event)
	require.NoError(t, err)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), secondRelease))
	secondRequest := llmSignedIntentAt(t, f.actorKey, f.orgID, "deploy", f.routeID.String()+":"+f.envID.String(),
		map[string]any{"route_id": f.routeID.String(), "environment_id": f.envID.String(), "release_id": second.String()},
		f.request.Event.CreatedAt+2)
	_, err = f.store.SaveEvent(*secondRequest.Event)
	require.NoError(t, err)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), secondRequest))
	f.accept(t, secondRequest)
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Equal(t, 2, f.gateway.calls)
	rollback := llmSignedIntentAt(t, f.actorKey, f.orgID, "rollback", f.routeID.String()+":"+f.envID.String(),
		map[string]any{"route_id": f.routeID.String(), "environment_id": f.envID.String()},
		secondRequest.Event.CreatedAt+2)
	_, err = f.store.SaveEvent(*rollback.Event)
	require.NoError(t, err)
	require.NoError(t, f.controller.HandleLLMIntent(t.Context(), rollback))
	f.accept(t, rollback)
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Equal(t, 3, f.gateway.calls)
	receipt, err := f.controller.executor(nil, nil).readReceipt(uuid.MustParse(rollback.IntentID))
	require.NoError(t, err)
	require.Equal(t, "terminal", receipt.Phase)
	require.Equal(t, first, receipt.Snapshot.LLM.Release.ID)
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Equal(t, 3, f.gateway.calls)
}

func TestCanonicalLLMAmbiguousPriorEffectBlocksLaterRouteDecision(t *testing.T) {
	f := newLLMFixture(t, false, false)
	f.gateway.blackhole = true
	f.deploy(t)
	require.ErrorIs(t, f.controller.ReconcilePending(t.Context()), ErrAutoApplyRecoveryRequired)
	first, err := f.controller.executor(nil, nil).readReceipt(uuid.MustParse(f.request.IntentID))
	require.NoError(t, err)
	require.Equal(t, "recovery_required", first.Phase)
	require.Equal(t, 1, f.gateway.calls)
	// A later request for the same route/environment cannot overtake a PUT
	// whose outcome is unknown, even after the process lock is released.
	newRequest := llmSignedIntentAt(t, f.actorKey, f.orgID, "deploy", f.routeID.String()+":"+f.envID.String(),
		map[string]any{"route_id": f.routeID.String(), "environment_id": f.envID.String(), "release_id": f.releaseID.String()},
		f.request.Event.CreatedAt+2)
	_, err = f.store.SaveEvent(*newRequest.Event)
	require.NoError(t, err)
	require.ErrorIs(t, f.controller.HandleLLMIntent(t.Context(), newRequest), ErrAutoApplyRecoveryRequired)
	require.Equal(t, 1, f.gateway.calls)
}

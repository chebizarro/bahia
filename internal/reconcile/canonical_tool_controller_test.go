package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/build"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

type toolBuildStub struct {
	exists bool
	calls  int
}

func (b *toolBuildStub) CheckImageExists(context.Context, string) (string, bool, error) {
	return "sha256:tool-image", b.exists, nil
}
func (b *toolBuildStub) BuildImage(_ context.Context, _ build.BuildRequest) (*build.BuildResult, error) {
	b.calls++
	b.exists = true
	return &build.BuildResult{ImageID: "sha256:tool-image"}, nil
}

type toolDeployStub struct {
	exists bool
	calls  int
}

func (d *toolDeployStub) Observe(context.Context, ApplySnapshot) (bool, *domain.RuntimeObservation, error) {
	if !d.exists {
		return false, nil, nil
	}
	return true, &domain.RuntimeObservation{ID: uuid.MustParse("00000000-0000-0000-0000-000000000778")}, nil
}
func (d *toolDeployStub) Apply(context.Context, ApplySnapshot, map[string]string) error {
	d.calls++
	d.exists = true
	return nil
}

type toolDecryptStub struct{}

func (toolDecryptStub) DecryptConfidential(context.Context, string, int, string, string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected encrypted denylist")
}

type toolFixture struct {
	controller *ToolCanonicalController
	store      *localstore.Store
	outbox     *localstore.Outbox
	serviceKey nostr.SecretKey
	actorKey   nostr.SecretKey
	serviceID  uuid.UUID
	envID      uuid.UUID
	request    *controlplane.Intent
	approval   *controlplane.Intent
	build      *toolBuildStub
	deploy     *toolDeployStub
	secret     *rotatedAutoSecretStore
	eventsPath string
	outboxPath string
}

func newToolFixture(t *testing.T, action string, withSecret ...bool) *toolFixture {
	t.Helper()
	dir := t.TempDir()
	eventsPath, outboxPath := filepath.Join(dir, "events.bolt"), filepath.Join(dir, "outbox.bolt")
	store, err := localstore.Open(eventsPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	serviceKey, actorKey := nostr.Generate(), nostr.Generate()
	orgID, serviceID, envID, unitID, artifactID, intentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	digest := "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	spec := domain.DesiredServiceSpec{SchemaVersion: domain.DesiredStateSchemaVersion,
		ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID,
		DeploymentUnitKey: "api", UnitRuntimeType: domain.RuntimeTypeDocker,
		ArtifactID: artifactID, StableServiceKey: "api", ImageRef: "registry.example/api@" + digest}
	var pin domain.SecretCredentialRef
	secretID := uuid.New()
	if len(withSecret) > 0 && withSecret[0] {
		pin = domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred-tool", Version: 7}
		spec.SecretRefs = []domain.DesiredSecretRef{{SecretID: secretID, Name: "TOKEN", EnvVar: "TOKEN", CredentialRef: &pin,
			RedactedValue: domain.RedactedPlaceholder("TOKEN")}}
	}
	spec.ComputeDesiredHash()
	deployment := nostr.Event{Kind: 30900, CreatedAt: 100, Tags: nostr.Tags{
		{"t", "bahia-intent"}, {"d", "deployment:" + serviceID.String() + ":" + envID.String()},
		{"domain", "deployment"}, {"op", "create"}, {"org", orgID.String()}, {"intent_id", uuid.NewString()},
	}, Content: fmt.Sprintf(`{"service_id":%q,"environment_id":%q,"artifact_id":%q}`, serviceID, envID, artifactID)}
	require.NoError(t, deployment.Sign(actorKey))
	_, err = store.SaveEvent(deployment)
	require.NoError(t, err)
	status := nostr.Event{Kind: 30315, CreatedAt: 101, Tags: nostr.Tags{
		{"t", "intent-status"}, {"domain", "intent"}, {"status", "accepted"},
		{"d", "intent-status:" + actorKey.Public().Hex() + ":deployment:" + serviceID.String() + ":" + envID.String()},
		{"p", actorKey.Public().Hex()}, {"e", deployment.ID.Hex()},
	}, Content: `{"result":"applied"}`}
	require.NoError(t, status.Sign(serviceKey))
	_, err = store.SaveEvent(status)
	require.NoError(t, err)
	svc := domain.Service{ID: serviceID, OrgID: orgID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}
	env := canonicalEnvironment{Environment: domain.Environment{ID: envID, OrgID: orgID, Name: "prod"},
		DeploymentUnits: []domain.DeploymentUnit{{ID: unitID, EnvironmentID: envID, Key: "api",
			RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeAutoApply,
			OwnershipMode: domain.OwnershipModeBahiaManaged}}}
	artifact := domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example/api", ImageDigest: digest}
	intent := domain.DeploymentIntent{ID: intentID, ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, RequestedBy: actorKey.Public().Hex(), ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status: domain.IntentStatusApproved, DesiredState: &spec, DesiredHash: spec.DesiredHash,
		Metadata: map[string]any{"nostr_event_id": deployment.ID.Hex()}}
	state := domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID,
		DesiredArtifactID: &artifactID, DesiredIntentID: &intentID, DesiredRuntimeState: &spec, DesiredHash: spec.DesiredHash}
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.ServiceRegistry, serviceID.String(), svc, 101)
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.EnvironmentRegistry, envID.String(), env, 101)
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.ArtifactRegistry, artifactID.String(), artifact, 101)
	saveCanonicalRuntimeJSON(t, store, serviceKey, kinds.DeploymentIntentRegistry, intentID.String(), intent, 101)
	tags, content := nostradapter.RuntimeStateRecord(&state, nil)
	saveCanonicalRuntimeEvent(t, store, serviceKey, kinds.ServiceState,
		nostradapter.ServiceStateDTag(serviceID, envID), false, content, 101, tags)
	requestID := uuid.Must(uuid.NewV7())
	request := signedToolIntent(t, actorKey, "request", "tool-request:"+requestID.String(),
		map[string]any{"id": requestID, "service_id": serviceID, "environment_id": envID,
			"operation": "install", "tools": []domain.ToolRequest{{Name: "jq", Manager: "apt", Version: "1.7"}}, "reason": "operator wants jq"})
	approval := signedToolIntent(t, actorKey, "approval-response", "tool-approval:"+requestID.String(),
		map[string]any{"intent_id": requestID, "action": action, "reason": "reviewed",
			"request_event_id": request.Event.ID.Hex()})
	for _, item := range []*controlplane.Intent{request, approval} {
		_, err = store.SaveEvent(*item.Event)
		require.NoError(t, err)
	}
	toolStatusProof(t, store, outbox, serviceKey, request)
	runtimeSource, err := NewCanonicalRuntimeSource(store, serviceKey.Public().Hex())
	require.NoError(t, err)
	base := &CanonicalAutoApplySource{Runtime: runtimeSource}
	var secret *rotatedAutoSecretStore
	if len(withSecret) > 0 && withSecret[0] {
		records := autoSecretRecords{orgID: orgID, ref: domain.SecretRef{ID: secretID, ServiceID: serviceID, Name: "TOKEN", CredentialRef: &pin}}
		secret = &rotatedAutoSecretStore{activeVersion: 7}
		resolver := secretstore.NewResolver(secret)
		resolver.UseRecords(records)
		base.Records, base.Secrets = records, resolver
	}
	lock, err := service.NewRuntimeApplyLock(outboxPath)
	require.NoError(t, err)
	builder, deploy := &toolBuildStub{}, &toolDeployStub{}
	controller := &ToolCanonicalController{Base: base,
		Outbox: outbox, DeliveryTarget: "default", AllowedAuthors: map[string]bool{actorKey.Public().Hex(): true},
		ImageRepo: "bahia-tools", InstallerVersion: "v1", Decryptor: toolDecryptStub{},
		Builder: builder, DeployEffect: deploy, Lock: lock, Signer: keyer.NewPlainKeySigner(serviceKey),
		Enqueue: func(_ context.Context, event nostr.Event) error {
			_, err := outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: "default"})
			return err
		}}
	return &toolFixture{controller: controller, store: store, outbox: outbox, serviceKey: serviceKey,
		actorKey: actorKey, serviceID: serviceID, envID: envID, request: request, approval: approval,
		build: builder, deploy: deploy, secret: secret, eventsPath: eventsPath, outboxPath: outboxPath}
}

func signedToolIntent(t *testing.T, key nostr.SecretKey, op, coordinate string, body map[string]any) *controlplane.Intent {
	t.Helper()
	content, err := json.Marshal(body)
	require.NoError(t, err)
	event := nostr.Event{Kind: 30900, CreatedAt: 200, Tags: nostr.Tags{
		{"t", "bahia-intent"}, {"d", coordinate}, {"domain", "tool"}, {"op", op},
		{"schema", "bahia.intent.tool.v1"}, {"intent_id", uuid.Must(uuid.NewV7()).String()},
	}, Content: string(content)}
	require.NoError(t, event.Sign(key))
	parsed, err := controlplane.ParseIntent(&event)
	require.NoError(t, err)
	parsed.Actor = event.PubKey.Hex()
	return parsed
}

func toolStatusProof(t *testing.T, store *localstore.Store, outbox *localstore.Outbox,
	key nostr.SecretKey, intent *controlplane.Intent) {
	t.Helper()
	event := nostr.Event{Kind: 30315, CreatedAt: 201, Tags: nostr.Tags{
		{"d", "intent-status:" + intent.Actor + ":" + intent.Coordinate}, {"domain", "intent"},
		{"t", "intent-status"}, {"p", intent.Actor}, {"e", intent.Event.ID.Hex()},
		{"intent_id", intent.IntentID}, {"status", "accepted"},
	}, Content: `{"result":"applied"}`}
	require.NoError(t, event.Sign(key))
	_, err := store.SaveEvent(event)
	require.NoError(t, err)
	_, err = outbox.EnqueuePublisherDelivery(localstore.OutboxEntry{Event: event, Target: "default", Delivered: true,
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

func TestCanonicalToolApprovalReplayAndDeniedWithoutDatabase(t *testing.T) {
	for _, action := range []string{"approve", "reject"} {
		t.Run(action, func(t *testing.T) {
			f := newToolFixture(t, action)
			require.NoError(t, f.controller.Decide(t.Context(), f.approval))
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			require.Zero(t, f.build.calls+f.deploy.calls, "unacknowledged approval cannot cause an effect")
			toolStatusProof(t, f.store, f.outbox, f.serviceKey, f.approval)
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			if action == "approve" {
				require.Equal(t, 1, f.build.calls)
				require.Equal(t, 1, f.deploy.calls)
				build, err := f.controller.executor(nil, nil, toolBuildReceiptFamily, "tool-build").readReceipt(f.controllerID())
				require.NoError(t, err)
				require.Equal(t, "terminal", build.Phase)
				deploy, err := f.controller.executor(nil, nil, toolDeployReceiptFamily, "tool-deploy").readReceipt(f.controllerID())
				require.NoError(t, err)
				require.Equal(t, "terminal", deploy.Phase)
			} else {
				require.Zero(t, f.build.calls+f.deploy.calls)
				denial, err := f.controller.executor(nil, nil, toolDecisionFamily, "tool-decision").readReceipt(f.controllerID())
				require.NoError(t, err)
				require.Equal(t, "refused", denial.Phase)
			}
		})
	}
}

func (f *toolFixture) controllerID() uuid.UUID {
	var body toolRequestWire
	_ = json.Unmarshal([]byte(f.request.Event.Content), &body)
	return body.ID
}

func TestCanonicalToolMovedEnvironmentRefusesBeforeEffect(t *testing.T) {
	f := newToolFixture(t, "approve")
	require.NoError(t, f.controller.Decide(t.Context(), f.approval))
	toolStatusProof(t, f.store, f.outbox, f.serviceKey, f.approval)
	env := canonicalEnvironment{Environment: domain.Environment{ID: f.envID, OrgID: uuid.New(), Name: "different"}}
	saveCanonicalRuntimeJSON(t, f.store, f.serviceKey, kinds.EnvironmentRegistry, f.envID.String(), env, 301)
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Zero(t, f.build.calls+f.deploy.calls)
	denial, err := f.controller.executor(nil, nil, toolDecisionFamily, "tool-decision").readReceipt(f.controllerID())
	require.NoError(t, err)
	require.Equal(t, "refused", denial.Phase)
}

func TestCanonicalToolEachBoundRevisionMovedRefusesBeforeEffect(t *testing.T) {
	for _, field := range []string{"service", "state", "intent", "environment", "artifact"} {
		t.Run(field, func(t *testing.T) {
			f := newToolFixture(t, "approve")
			require.NoError(t, f.controller.Decide(t.Context(), f.approval))
			toolStatusProof(t, f.store, f.outbox, f.serviceKey, f.approval)
			raw, err := f.outbox.GetControlRecord(toolPendingFamily, f.controllerID().String())
			require.NoError(t, err)
			var stage toolPending
			require.NoError(t, json.Unmarshal(raw, &stage))
			idText := map[string]string{
				"service": stage.Decision.ServiceEventID, "state": stage.Decision.StateEventID,
				"intent": stage.Decision.IntentEventID, "environment": stage.Decision.EnvironmentEventID,
				"artifact": stage.Decision.ArtifactEventID,
			}[field]
			id, err := nostr.IDFromHex(idText)
			require.NoError(t, err)
			found := false
			for prior := range f.store.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}) {
				moved := prior
				moved.CreatedAt = 301
				require.NoError(t, moved.Sign(f.serviceKey))
				_, err = f.store.SaveEvent(moved)
				require.NoError(t, err)
				found = true
			}
			require.True(t, found)
			require.NoError(t, f.controller.ReconcilePending(t.Context()))
			require.Zero(t, f.build.calls+f.deploy.calls)
			denial, err := f.controller.executor(nil, nil, toolDecisionFamily, "tool-decision").readReceipt(f.controllerID())
			require.NoError(t, err)
			require.Equal(t, "refused", denial.Phase)
		})
	}
}

func TestCanonicalToolPinnedSignetVersionMovedRefusesBeforeEffect(t *testing.T) {
	f := newToolFixture(t, "approve", true)
	require.NoError(t, f.controller.Decide(t.Context(), f.approval))
	toolStatusProof(t, f.store, f.outbox, f.serviceKey, f.approval)
	f.secret.activeVersion = 8
	require.NoError(t, f.controller.ReconcilePending(t.Context()))
	require.Equal(t, 1, f.secret.deliveries, "Signet expected_version is checked at execution")
	require.Zero(t, f.build.calls+f.deploy.calls)
	denial, err := f.controller.executor(nil, nil, toolDecisionFamily, "tool-decision").readReceipt(f.controllerID())
	require.NoError(t, err)
	require.Equal(t, "refused", denial.Phase)
}

func TestCanonicalToolAdmissionSurvivesRestartBeforeApprovalReceipt(t *testing.T) {
	f := newToolFixture(t, "approve")
	require.NoError(t, f.controller.Decide(t.Context(), f.approval))
	require.NoError(t, f.outbox.Close())
	require.NoError(t, f.store.Close())
	store, err := localstore.Open(f.eventsPath)
	require.NoError(t, err)
	defer store.Close()
	outbox, err := localstore.OpenOutbox(f.outboxPath)
	require.NoError(t, err)
	defer outbox.Close()
	toolStatusProof(t, store, outbox, f.serviceKey, f.approval)
	runtimeSource, err := NewCanonicalRuntimeSource(store, f.serviceKey.Public().Hex())
	require.NoError(t, err)
	restarted := &ToolCanonicalController{
		Base: &CanonicalAutoApplySource{Runtime: runtimeSource}, Outbox: outbox,
		DeliveryTarget: f.controller.DeliveryTarget, AllowedAuthors: f.controller.AllowedAuthors,
		ImageRepo: f.controller.ImageRepo, InstallerVersion: f.controller.InstallerVersion,
		Decryptor: f.controller.Decryptor, Builder: f.build, DeployEffect: f.deploy,
		Lock: f.controller.Lock, Signer: f.controller.Signer,
	}
	restarted.Enqueue = func(_ context.Context, event nostr.Event) error {
		_, err := outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: "default"})
		return err
	}
	require.NoError(t, restarted.ReconcilePending(t.Context()))
	require.Equal(t, 1, f.build.calls)
	require.Equal(t, 1, f.deploy.calls)
	require.NoError(t, restarted.ReconcilePending(t.Context()))
	require.Equal(t, 1, f.build.calls+0)
}

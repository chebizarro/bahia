package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type virtualizationTestProvider struct {
	observer *domain.VMObservation
	effects  int
	before   func(domain.VMProviderOperation)
}

func (p *virtualizationTestProvider) Inspect(context.Context, domain.VMResourceIdentity) (*domain.VMObservation, error) {
	return p.observer, nil
}
func (*virtualizationTestProvider) Inventory(context.Context, domain.VirtualizationHost) ([]domain.VMInventoryEntry, error) {
	return nil, nil
}
func (*virtualizationTestProvider) PlanChange(context.Context, domain.VMChangeRequest) (*domain.VMChangePlan, error) {
	return nil, nil
}
func (p *virtualizationTestProvider) Execute(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
	if p.before != nil {
		p.before(q)
	}
	p.effects++
	state := domain.VMRuntimeStopped
	if q.Operation.Kind == domain.VMOperationStart || q.Operation.Kind == domain.VMOperationReboot {
		state = domain.VMRuntimeRunning
	}
	p.observer = &domain.VMObservation{Identity: q.Deployment.Identity, Availability: domain.VMObservationAvailable, Ownership: domain.VMOwned, Drift: domain.VMDriftInSync, RuntimeState: &state, Marker: &domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: q.Deployment.Identity, AppliedGeneration: q.Deployment.Generation, OperationID: q.Operation.ID, ImageDigest: q.Image.ManifestDigest, ConfigDigest: q.Deployment.ConfigDigest}}
	return &domain.VMProviderResult{OperationID: q.Operation.ID, Confirmed: true, Observation: p.observer}, nil
}

type virtualizationHarness struct {
	t              *testing.T
	actor          nostr.SecretKey
	org            uuid.UUID
	service        nostr.SecretKey
	store          *localstore.Store
	outbox         *localstore.Outbox
	storePath      string
	outboxPath     string
	provider       *virtualizationTestProvider
	handler        *VirtualizationIntentHandler
	processor      *IntentProcessor
	publishCount   int
	publishBlocked bool
	statuses       []nostr.Event
}

func newVirtualizationHarness(t *testing.T) *virtualizationHarness {
	t.Helper()
	dir := t.TempDir()
	storePath, outboxPath := filepath.Join(dir, "events.bolt"), filepath.Join(dir, "outbox.bolt")
	store, err := localstore.Open(storePath)
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	h := &virtualizationHarness{t: t, actor: nostr.Generate(), org: uuid.New(), service: nostr.Generate(), store: store, outbox: outbox, storePath: storePath, outboxPath: outboxPath, provider: &virtualizationTestProvider{}}
	t.Cleanup(func() { _ = h.store.Close(); _ = h.outbox.Close() })
	publish := func(_ context.Context, ev nostr.Event) error {
		h.publishCount++
		_, err := h.outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: repository.NostrPublishTargetControlPlane})
		if err != nil {
			return err
		}
		if h.publishBlocked {
			return errors.New("relay unavailable")
		}
		_, err = h.outbox.CommitPublisherRound(ev.ID, localstore.OutboxRound{Target: repository.NostrPublishTargetControlPlane, Rounds: 1, Relays: map[string]localstore.RelayDelivery{"wss://relay.test": {Accepted: true}}, Delivered: true, Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://relay.test"}, Required: 1}, State: localstore.OutboxPublished})
		if err != nil {
			return err
		}
		_, err = h.store.SaveEvent(ev)
		return err
	}
	h.handler = NewVirtualizationIntentHandler(VirtualizationIntentDependencies{Outbox: outbox, Events: store, Signer: keyer.NewPlainKeySigner(h.service), Target: repository.NostrPublishTargetControlPlane, Publish: publish, Provider: h.provider})
	status := NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error { h.statuses = append(h.statuses, ev); return nil }, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	h.processor = NewIntentProcessor(NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{h.org.String(): h.actor.Public().Hex()})), store, status, IntentProcessorConfig{EnabledDomains: map[string]bool{"virtualization": true}}, zap.NewNop())
	h.processor.RegisterHandler("virtualization", h.handler)
	return h
}

func (h *virtualizationHarness) restart() {
	h.t.Helper()
	require.NoError(h.t, h.store.Close())
	require.NoError(h.t, h.outbox.Close())
	var err error
	h.store, err = localstore.Open(h.storePath)
	require.NoError(h.t, err)
	h.outbox, err = localstore.OpenOutbox(h.outboxPath)
	require.NoError(h.t, err)
	deps := h.handler.runtime.VirtualizationIntentDependencies
	deps.Outbox = h.outbox
	deps.Events = h.store
	h.handler = NewVirtualizationIntentHandler(deps)
	status := NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error { h.statuses = append(h.statuses, ev); return nil }, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	h.processor = NewIntentProcessor(NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{h.org.String(): h.actor.Public().Hex()})), h.store, status, IntentProcessorConfig{EnabledDomains: map[string]bool{"virtualization": true}}, zap.NewNop())
	h.processor.RegisterHandler("virtualization", h.handler)
}

func virtualID(t *testing.T) uuid.UUID { t.Helper(); return uuid.Must(uuid.NewV7()) }

func (h *virtualizationHarness) resource(op string, kind domain.VirtualizationResourceKind, id uuid.UUID, resource any, expected string) *nostr.Event {
	h.t.Helper()
	_, prefix, _, ok := virtualizationFamily(kind)
	require.True(h.t, ok)
	request := virtualizationResourceRequest{ID: id, ResourceKind: kind, ExpectedUpdatedAt: expected}
	if op == "delete" {
		request.Deleted = true
	} else {
		raw, err := json.Marshal(resource)
		require.NoError(h.t, err)
		request.Resource = raw
	}
	content, err := json.Marshal(request)
	require.NoError(h.t, err)
	ev := &nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Content: string(content), Tags: nostr.Tags{{"d", prefix + id.String()}, {"domain", kinds.VirtualizationDomain}, {"schema", kinds.VirtualizationIntentSchema}, {"op", op}, {"org", h.org.String()}, {"intent_id", virtualID(h.t).String()}, {"t", "bahia-intent"}, {"t", kinds.VirtualizationDomain}}}
	require.NoError(h.t, ev.Sign(h.actor))
	return ev
}

func (h *virtualizationHarness) resourceRecord(kind domain.VirtualizationResourceKind, id uuid.UUID) virtualizationCanonicalRecord {
	h.t.Helper()
	r, _, err := h.handler.runtime.current(h.t.Context(), kind, id)
	require.NoError(h.t, err)
	require.NotNil(h.t, r)
	return *r
}

func (h *virtualizationHarness) fixtures() (domain.VirtualizationHost, domain.VMImage, domain.PersistentVMDeployment) {
	idHost, idImage, idVM := virtualID(h.t), virtualID(h.t), virtualID(h.t)
	digest := "sha256:" + strings.Repeat("a", 64)
	host := domain.VirtualizationHost{InstallationID: uuid.New(), LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, Provider: domain.VMProviderLibvirt, ExecutionLocation: domain.VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, Architecture: "amd64", Capacity: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, Quota: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, OperationLimits: domain.DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 90}
	image := domain.VMImage{ManifestDigest: digest, Format: domain.VMImageQCOW2, Architecture: "amd64", OS: domain.VMOSLinux, LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, Firmware: domain.VMFirmwareBIOS, DriverContract: "virtio-v1", AgentProtocolVersion: "1", ReleaseRef: uuid.New(), Provenance: domain.VMProvenance{EventID: strings.Repeat("b", 64), Signer: strings.Repeat("c", 64), Verified: true, VerifiedAt: time.Now().UTC()}, Components: []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: uuid.New(), Digest: digest, SizeBytes: 100 << 30}}}
	vm := domain.PersistentVMDeployment{LifecycleClass: domain.VMLifecyclePersistent, Purpose: domain.VMPurposeDesktop, Provider: domain.VMProviderLibvirt, HostID: idHost, ImageID: idImage, DisplayName: "desktop", DesiredPower: domain.VMDesiredStopped, Allocation: domain.VMCapacity{VCPU: 8, MemoryBytes: 24 << 30, DiskBytes: 100 << 30}, StoragePoolRef: uuid.New(), Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}, Firmware: domain.VMFirmwareBIOS, ConfigDigest: digest}
	vm.Identity = domain.VMResourceIdentity{InstallationID: host.InstallationID, OrgID: h.org, HostID: idHost, DeploymentID: idVM, Provider: domain.VMProviderLibvirt, ProviderResourceID: uuid.New(), LifecycleClass: domain.VMLifecyclePersistent}
	host.ID = idHost
	image.ID = idImage
	vm.ID = idVM // outer request ids are authoritative; these are for test setup only.
	return host, image, vm
}

func TestVirtualizationCanonicalResourceIntentAndConflictWithoutPostgres(t *testing.T) {
	h := newVirtualizationHarness(t)
	host, _, _ := h.fixtures()
	id := host.ID
	host.ID = uuid.Nil
	create := h.resource("create", domain.VirtualizationHostResource, id, host, "")
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), create))
	require.Equal(t, "accepted", extractTag(h.statuses[len(h.statuses)-1], "status"))
	record := h.resourceRecord(domain.VirtualizationHostResource, id)
	require.False(t, record.Deleted)
	require.Equal(t, int64(1), func() int64 {
		var v domain.VirtualizationHost
		require.NoError(t, json.Unmarshal(record.Resource, &v))
		return v.Generation
	}())
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), create))
	require.Equal(t, 1, h.publishCount, "same intent_id may not publish a second state")
	resigned := *create
	resigned.CreatedAt++
	require.NoError(t, resigned.Sign(h.actor))
	require.ErrorContains(t, h.processor.ProcessRelayIntent(t.Context(), &resigned), "conflicts with previously accepted content")
	tampered := *create
	tampered.Content += " "
	require.ErrorContains(t, h.processor.ProcessRelayIntent(t.Context(), &tampered), "signed")
	host.Enabled = false
	stale := h.resource("update", domain.VirtualizationHostResource, id, host, time.Now().Add(-time.Hour).Format(time.RFC3339Nano))
	require.ErrorContains(t, h.processor.ProcessRelayIntent(t.Context(), stale), "revision conflict")
	require.Equal(t, "conflict", extractTag(h.statuses[len(h.statuses)-1], "status"))
	update := h.resource("update", domain.VirtualizationHostResource, id, host, record.UpdatedAt.Format(time.RFC3339Nano))
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), update))
	updated := h.resourceRecord(domain.VirtualizationHostResource, id)
	require.True(t, updated.UpdatedAt.After(record.UpdatedAt))
	deleted := h.resource("delete", domain.VirtualizationHostResource, id, nil, updated.UpdatedAt.Format(time.RFC3339Nano))
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), deleted))
	require.True(t, h.resourceRecord(domain.VirtualizationHostResource, id).Deleted)
	raw, err := h.outbox.GetControlRecord(virtualizationReceiptFamily, virtualizationIntentID(*deleted))
	require.NoError(t, err)
	var receipt virtualizationReceipt
	require.NoError(t, json.Unmarshal(raw, &receipt))
	require.NotNil(t, receipt.Canonical)
	require.NoError(t, h.store.DeleteEvent(receipt.Canonical.ID))
	_, err = h.outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, h.resourceRecord(domain.VirtualizationHostResource, id).Deleted, "proof-bearing receipt restores head after event-cache loss and outbox prune")
}

func TestVirtualizationOperationRefusesMissingCanonicalVM(t *testing.T) {
	h := newVirtualizationHarness(t)
	req := virtualizationOperationRequest{OperationID: virtualID(t), ResourceID: virtualID(t), ResourceKind: domain.PersistentVMResource, Action: domain.VMOperationStart, ExpectedGeneration: 1, IdempotencyKey: virtualID(t).String(), Reason: "operator start"}
	ev := virtualizationSignedRequest(t, h.actor, h.org, req)
	require.ErrorContains(t, h.processor.ProcessRelayIntent(t.Context(), ev), "no canonical VM record")
	require.Zero(t, h.provider.effects)
}

func TestVirtualizationEffectReceiptReplayAndCrashWindow(t *testing.T) {
	h := newVirtualizationHarness(t)
	host, image, vm := h.fixtures()
	hostID, imageID, vmID := host.ID, image.ID, vm.ID
	host.ID = uuid.Nil
	image.ID = uuid.Nil
	vm.ID = uuid.Nil
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), h.resource("create", domain.VirtualizationHostResource, hostID, host, "")))
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), h.resource("create", domain.VMImageResource, imageID, image, "")))
	vmEvent := h.resource("create", domain.PersistentVMResource, vmID, vm, "")
	h.provider.before = func(q domain.VMProviderOperation) {
		raw, err := h.outbox.GetControlRecord(virtualizationReceiptFamily, q.Operation.IdempotencyKey)
		require.NoError(t, err)
		require.NotEmpty(t, raw, "receipt must precede provider effect")
		var receipt virtualizationReceipt
		require.NoError(t, json.Unmarshal(raw, &receipt))
		require.Equal(t, "prepared", receipt.Phase)
		require.Equal(t, q.Operation.ID, receipt.Effect.Operation.ID)
	}
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), vmEvent))
	require.Equal(t, 1, h.provider.effects)
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), vmEvent))
	require.Equal(t, 1, h.provider.effects)
	vmRecord := h.resourceRecord(domain.PersistentVMResource, vmID)
	require.False(t, vmRecord.Deleted)
	req := virtualizationOperationRequest{OperationID: virtualID(t), ResourceID: vmID, ResourceKind: domain.PersistentVMResource, Action: domain.VMOperationStart, ExpectedGeneration: 1, IdempotencyKey: virtualID(t).String(), Reason: "operator start"}
	opEvent := virtualizationSignedRequest(t, h.actor, h.org, req)
	h.publishBlocked = true
	statusesBeforePending := len(h.statuses)
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), opEvent), "pending canonical delivery is not rejection")
	require.Equal(t, 2, h.provider.effects)
	require.False(t, h.processor.IsProcessed(req.IdempotencyKey))
	require.Len(t, h.statuses, statusesBeforePending, "pending delivery must not emit a final status")
	require.ErrorIs(t, h.processor.ProcessInProcess(t.Context(), virtualizationParsedRequest(t, opEvent)), ErrVirtualizationPending)
	require.Equal(t, 2, h.provider.effects)
	h.publishBlocked = false
	h.restart()
	require.NoError(t, h.handler.Recover(t.Context(), h.processor.ProcessRelayIntent))
	require.Equal(t, 2, h.provider.effects, "post-effect publish retry cannot re-execute")
	require.True(t, h.processor.IsProcessed(req.IdempotencyKey))
	require.Equal(t, "accepted", extractTag(h.statuses[len(h.statuses)-1], "status"))
	// A crash after an effect but before staging the signed canonical event is
	// recovered only if Inspect proves the exact operation marker and state.
	req2 := virtualizationOperationRequest{OperationID: virtualID(t), ResourceID: vmID, ResourceKind: domain.PersistentVMResource, Action: domain.VMOperationReboot, ExpectedGeneration: 1, IdempotencyKey: virtualID(t).String(), Reason: "operator reboot"}
	ev2 := virtualizationSignedRequest(t, h.actor, h.org, req2)
	parsed := virtualizationParsedRequest(t, ev2)
	hostCanonical, imageCanonical, err := h.handler.runtime.resolveReferences(t.Context(), hostID, imageID, h.org)
	require.NoError(t, err)
	var canonicalVM domain.PersistentVMDeployment
	require.NoError(t, json.Unmarshal(vmRecord.Resource, &canonicalVM))
	op := h.handler.runtime.providerOperation(parsed, canonicalVM, req2.Action, time.Now().UTC())
	op.Reason = req2.Reason
	prepared := h.handler.runtime.newReceipt(parsed)
	prepared.Phase = "prepared"
	prepared.Effect = &virtualizationEffectInput{Host: hostCanonical, Image: imageCanonical, VM: canonicalVM, Operation: op}
	require.NoError(t, h.handler.runtime.saveReceipt(prepared))
	h.provider.observer.Marker.OperationID = op.ID
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), ev2))
	require.Equal(t, 2, h.provider.effects, "marker recovery cannot call Execute")
	req3 := virtualizationOperationRequest{OperationID: virtualID(t), ResourceID: vmID, ResourceKind: domain.PersistentVMResource, Action: domain.VMOperationReboot, ExpectedGeneration: 1, IdempotencyKey: virtualID(t).String(), Reason: "operator reboot"}
	ev3 := virtualizationSignedRequest(t, h.actor, h.org, req3)
	parsed3 := virtualizationParsedRequest(t, ev3)
	op3 := h.handler.runtime.providerOperation(parsed3, canonicalVM, req3.Action, time.Now().UTC())
	op3.Reason = req3.Reason
	uncertain := h.handler.runtime.newReceipt(parsed3)
	uncertain.Phase = "prepared"
	uncertain.Effect = &virtualizationEffectInput{Host: hostCanonical, Image: imageCanonical, VM: canonicalVM, Operation: op3}
	require.NoError(t, h.handler.runtime.saveReceipt(uncertain))
	err = h.processor.ProcessRelayIntent(t.Context(), ev3)
	var uncertainty *VirtualizationUncertainError
	require.ErrorAs(t, err, &uncertainty)
	require.Contains(t, err.Error(), "bahia-4f10q.1")
	require.Equal(t, 2, h.provider.effects)
}

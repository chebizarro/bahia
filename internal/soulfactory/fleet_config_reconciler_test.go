package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

type fleetReconcilePublishCapture struct {
	mu     sync.Mutex
	events []*nostr.Event
}

func (c *fleetReconcilePublishCapture) publish(_ context.Context, event *nostr.Event, _ []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := *event
	copied.Tags = append(nostr.Tags{}, event.Tags...)
	c.events = append(c.events, &copied)
	return nil
}

func (c *fleetReconcilePublishCapture) byKind(kind int) []*nostr.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*nostr.Event
	for _, event := range c.events {
		if int(event.Kind) == kind {
			out = append(out, event)
		}
	}
	return out
}

type fleetReconcileRuntime struct {
	mu        sync.Mutex
	requests  []RuntimeAdapterRequest
	failApply error
}

func (a *fleetReconcileRuntime) Runtime() domain.RuntimeTarget {
	return domain.RuntimeTargetOpenClaw
}

func (a *fleetReconcileRuntime) DiscoverCapabilities(context.Context, domain.SoulRelayPolicySpec) ([]RuntimeCapability, error) {
	return nil, nil
}

func (a *fleetReconcileRuntime) Execute(_ context.Context, req RuntimeAdapterRequest) (*RuntimeControlResultEnvelope, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	if req.Action != domain.SoulActionRollback && a.failApply != nil {
		return &RuntimeControlResultEnvelope{
			Method: req.Method,
			Status: "failed",
			Error:  &RuntimeControlError{Code: "runtime_error", Message: a.failApply.Error()},
		}, a.failApply
	}
	return &RuntimeControlResultEnvelope{
		Method: req.Method,
		Status: "success",
		Result: map[string]interface{}{"restart": false},
	}, nil
}

func (a *fleetReconcileRuntime) capturedRequests() []RuntimeAdapterRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]RuntimeAdapterRequest(nil), a.requests...)
}

func TestReactorSubscribesToTrustedFleetConfigRevisions(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusBackoff(immediateRelayBusBackoff),
	)
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	reactor := NewReactor(Config{
		Relays:             []string{endpoint.url},
		AuthorizedPubkeys:  []string{signer.pubkey},
		SoulFactoryPubkey:  signer.pubkey,
		FleetConfigEnabled: true,
	}, nil, signer, slog.Default())
	reactor.relayBus = bus
	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- reactor.Run(ctx) }()

	filters := <-endpoint.subscribeCalls
	var fleetFilter *nostr.Filter
	for i := range filters {
		if len(filters[i].Kinds) == 1 && filters[i].Kinds[0] == nostr.Kind(domain.KindSoulFleetConfig) {
			fleetFilter = &filters[i]
			break
		}
	}
	if fleetFilter == nil {
		t.Fatal("reactor subscription omitted kind 31953")
	}
	if len(fleetFilter.Authors) != 1 || fleetFilter.Authors[0].Hex() != signer.pubkey {
		t.Fatalf("fleet authors = %#v", fleetFilter.Authors)
	}
	if got := fleetFilter.Tags[tagParameterizedD]; len(got) != 1 || got[0] != SoulFactoryFleetConfigIdentifier {
		t.Fatalf("fleet d-tag filter = %#v", got)
	}
	if fleetFilter.Limit != 1 {
		t.Fatalf("fleet filter limit = %d, want 1", fleetFilter.Limit)
	}

	cancel()
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
}

func TestFleetConfigReconcilerFansOutToAffectedOpenClawSouls(t *testing.T) {
	oldRevision, newRevision := fleetReconcileSnapshots(t)
	souls := []*domain.AgentSoul{
		fleetReconcileSoul("alpha", oldRevision.EventID),
		fleetReconcileSoul("bravo", oldRevision.EventID),
		fleetReconcileSoul("metiq", oldRevision.EventID),
		fleetReconcileSoul("suspended", oldRevision.EventID),
	}
	souls[2].Runtime.Target = domain.RuntimeTargetMetiq
	souls[3].Status = domain.SoulStatusSuspended

	reactor, runtime, capture := newFleetReconcileTestReactor(t, souls, oldRevision)
	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	requests := runtime.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("runtime request count = %d, want 2", len(requests))
	}
	for _, request := range requests {
		if request.Method != RuntimeMethodConfigReload || request.Action != domain.SoulActionHotReload ||
			request.RequestKind != domain.KindSoulFleetConfig {
			t.Fatalf("runtime request = %#v", request)
		}
		if request.IdempotencyKey == "" {
			t.Fatal("runtime request omitted idempotency key")
		}
		patch := request.Params["patch"].(map[string]interface{})
		applied := patch["fleet_config"].(*FleetConfigSnapshot)
		if applied.EventID != newRevision.EventID {
			t.Fatalf("applied fleet revision = %q", applied.EventID)
		}
	}

	soulEvents := capture.byKind(domain.KindAgentSoul)
	if len(soulEvents) != 2 {
		t.Fatalf("published soul count = %d, want 2", len(soulEvents))
	}
	for _, event := range soulEvents {
		if got := ParseAgentSoulEvent(event).AppliedFleetConfigRevision; got != newRevision.EventID {
			t.Fatalf("applied fleet revision in 31951 = %q", got)
		}
	}
	results := capture.byKind(domain.KindProvisioningResult)
	if len(results) != 2 {
		t.Fatalf("terminal result count = %d, want 2", len(results))
	}
	for _, event := range results {
		if got := tagValue(event.Tags, tagFleetRevision); got != newRevision.EventID {
			t.Fatalf("terminal fleet revision tag = %q", got)
		}
		if got := tagValue(event.Tags, tagSoul); got == "" {
			t.Fatal("terminal result omitted soul ref")
		}
		if got := tagValue(event.Tags, tagStatus); got != "completed" {
			t.Fatalf("terminal status = %q", got)
		}
		if got := tagValue(event.Tags, tagRequestKind); got != "31953" {
			t.Fatalf("terminal request-kind = %q", got)
		}
	}
	if statuses := capture.byKind(domain.KindProvisioningStatus); len(statuses) < 6 {
		t.Fatalf("progress event count = %d, want at least 6", len(statuses))
	}
}

func TestFleetConfigReconcilerSkipsAlreadyAppliedRevision(t *testing.T) {
	_, newRevision := fleetReconcileSnapshots(t)
	soul := fleetReconcileSoul("alpha", newRevision.EventID)
	reactor, runtime, capture := newFleetReconcileTestReactor(t, []*domain.AgentSoul{soul}, nil)

	if err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if requests := runtime.capturedRequests(); len(requests) != 0 {
		t.Fatalf("runtime request count = %d, want 0", len(requests))
	}
	if len(capture.byKind(domain.KindProvisioningStatus)) != 0 ||
		len(capture.byKind(domain.KindProvisioningResult)) != 0 ||
		len(capture.byKind(domain.KindAgentSoul)) != 0 {
		t.Fatal("already-applied soul emitted reconciliation events")
	}
}

func TestFleetConfigReconcilerRollsBackFailedSoul(t *testing.T) {
	oldRevision, newRevision := fleetReconcileSnapshots(t)
	soul := fleetReconcileSoul("alpha", oldRevision.EventID)
	reactor, runtime, capture := newFleetReconcileTestReactor(t, []*domain.AgentSoul{soul}, oldRevision)
	runtime.failApply = errors.New("runtime rejected new fleet config")

	err := reactor.fleetReconciler().Reconcile(t.Context(), newRevision)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want runtime failure")
	}
	requests := runtime.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("runtime request count = %d, want apply + rollback", len(requests))
	}
	if requests[0].Action != domain.SoulActionHotReload || requests[1].Action != domain.SoulActionRollback {
		t.Fatalf("runtime actions = %q, %q", requests[0].Action, requests[1].Action)
	}
	if requests[0].IdempotencyKey == requests[1].IdempotencyKey {
		t.Fatal("apply and rollback reused one idempotency key")
	}
	rollbackPatch := requests[1].Params["patch"].(map[string]interface{})
	rolledBack := rollbackPatch["fleet_config"].(*FleetConfigSnapshot)
	if rolledBack.EventID != oldRevision.EventID {
		t.Fatalf("rollback fleet revision = %q, want %q", rolledBack.EventID, oldRevision.EventID)
	}
	if len(capture.byKind(domain.KindAgentSoul)) != 0 {
		t.Fatal("failed reconciliation published a new 31951")
	}
	results := capture.byKind(domain.KindProvisioningResult)
	if len(results) != 1 {
		t.Fatalf("terminal result count = %d, want 1", len(results))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(results[0].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["fleet_status"] != "failed" || payload["rollback_status"] != "completed" {
		t.Fatalf("terminal payload = %#v", payload)
	}
	progress := capture.byKind(domain.KindProvisioningStatus)
	foundRollback := false
	for _, event := range progress {
		if event.Content == "rolling back fleet config via soulfactory.config.reload" {
			foundRollback = true
		}
	}
	if !foundRollback {
		t.Fatal("rollback progress event was not published")
	}
}

func newFleetReconcileTestReactor(
	t *testing.T,
	souls []*domain.AgentSoul,
	previous *FleetConfigSnapshot,
) (*Reactor, *fleetReconcileRuntime, *fleetReconcilePublishCapture) {
	t.Helper()
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{
		Relays:                    []string{"wss://relay.example"},
		AuthorizedPubkeys:         []string{signer.pubkey},
		SoulFactoryPubkey:         signer.pubkey,
		FleetConfigEnabled:        true,
		FleetReconcileConcurrency: 2,
	}, scriptedGenerator{}, signer, slog.Default())
	capture := &fleetReconcilePublishCapture{}
	reactor.publishFn = capture.publish
	reactor.listSoulsFn = func(context.Context) ([]*domain.AgentSoul, error) {
		return souls, nil
	}
	reactor.getFleetConfigRevisionFn = func(_ context.Context, eventID string) (*FleetConfigSnapshot, error) {
		if previous != nil && previous.EventID == eventID {
			return previous, nil
		}
		return nil, nil
	}
	runtime := &fleetReconcileRuntime{}
	handler := NewLifecycleHandler(reactor, nil, nil, slog.Default())
	handler.SetRuntimeAdapters(map[domain.RuntimeTarget]RuntimeAdapter{
		domain.RuntimeTargetOpenClaw: runtime,
	})
	reactor.lifecycleHandler = handler
	return reactor, runtime, capture
}

func fleetReconcileSnapshots(t *testing.T) (*FleetConfigSnapshot, *FleetConfigSnapshot) {
	t.Helper()
	signer := newFakeSigner(t)
	coordinate := parameterizedCoordinate(domain.KindSoulFleetConfig, signer.pubkey, SoulFactoryFleetConfigIdentifier)
	oldRevision := &FleetConfigSnapshot{
		Coordinate: coordinate,
		EventID:    soulTestID("fleet-old").Hex(),
		Author:     signer.pubkey,
		CreatedAt:  10,
		Document: FleetConfigDocument{
			Schema:   SoulFactoryFleetConfigSchema,
			Template: map[string]interface{}{"logging": map[string]interface{}{"level": "info"}},
		},
	}
	newRevision := &FleetConfigSnapshot{
		Coordinate: coordinate,
		EventID:    soulTestID("fleet-new").Hex(),
		Author:     signer.pubkey,
		CreatedAt:  20,
		Document: FleetConfigDocument{
			Schema:   SoulFactoryFleetConfigSchema,
			Template: map[string]interface{}{"logging": map[string]interface{}{"level": "debug"}},
		},
	}
	return oldRevision, newRevision
}

func fleetReconcileSoul(agentID, appliedRevision string) *domain.AgentSoul {
	return &domain.AgentSoul{
		AgentID:                    agentID,
		Name:                       agentID,
		Status:                     domain.SoulStatusActive,
		SpecHash:                   "sha256:" + agentID,
		AppliedFleetConfigRevision: appliedRevision,
		Runtime: domain.SoulRuntimeSpec{
			Target:        domain.RuntimeTargetOpenClaw,
			RuntimePubkey: soulTestPubKeyHex("runtime-" + agentID),
			State:         "running",
		},
	}
}

package controlplane

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type recordingDNSOperator struct {
	zones        map[string]bool
	reconcileAll int
	reconciled   []string
	policyRepo   *recordingDNSPolicyRepository
}

func (o *recordingDNSOperator) ReconcileAll(context.Context) error {
	o.reconcileAll++
	return nil
}

func (o *recordingDNSOperator) ReconcileZone(_ context.Context, zoneName string) error {
	o.reconciled = append(o.reconciled, zoneName)
	return nil
}

func (o *recordingDNSOperator) HasZone(zoneName string) bool {
	return o.zones[zoneName]
}

func (o *recordingDNSOperator) DNSPolicyRepository() repository.DNSPolicyRepository {
	if o.policyRepo == nil {
		return nil
	}
	return o.policyRepo
}

type recordingDNSPersistentOperator struct {
	*recordingDNSOperator
	zonesCreated     []domain.DNSZone
	overridesCreated []domain.DNSRecordOverride
}

func (o *recordingDNSPersistentOperator) CreateZone(_ context.Context, zone domain.DNSZone) error {
	o.zonesCreated = append(o.zonesCreated, zone)
	if o.zones == nil {
		o.zones = map[string]bool{}
	}
	o.zones[zone.Name] = true
	return nil
}

func (o *recordingDNSPersistentOperator) CreateOverride(_ context.Context, override domain.DNSRecordOverride) error {
	o.overridesCreated = append(o.overridesCreated, override)
	return nil
}

func (o *recordingDNSPersistentOperator) ListOverridesByZone(_ context.Context, zoneName string) ([]domain.DNSRecordOverride, error) {
	var overrides []domain.DNSRecordOverride
	for _, override := range o.overridesCreated {
		if override.ZoneName == zoneName {
			overrides = append(overrides, override)
		}
	}
	return overrides, nil
}

type recordingDNSPolicyRepository struct {
	created []domain.DNSPolicy
}

func (r *recordingDNSPolicyRepository) Create(_ context.Context, policy *domain.DNSPolicy) error {
	r.created = append(r.created, *policy)
	return nil
}
func (r *recordingDNSPolicyRepository) Get(context.Context, uuid.UUID) (*domain.DNSPolicy, error) {
	return nil, nil
}
func (r *recordingDNSPolicyRepository) List(context.Context) ([]domain.DNSPolicy, error) {
	return nil, nil
}
func (r *recordingDNSPolicyRepository) ListEnabled(context.Context) ([]domain.DNSPolicy, error) {
	return nil, nil
}
func (r *recordingDNSPolicyRepository) Update(context.Context, *domain.DNSPolicy) error { return nil }
func (r *recordingDNSPolicyRepository) Delete(context.Context, uuid.UUID) error         { return nil }

func TestDNSDriftRemediateHandlerTriggersReconcile(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	event := &nostr.Event{ID: testNostrID("dns-remediate"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(KindDNSDriftRemediateRequest), Content: `{"zone":"prod.example"}`}

	reactor.handleDNSDriftRemediate(context.Background(), event)

	if len(operator.reconciled) != 1 || operator.reconciled[0] != "prod.example" {
		t.Fatalf("expected zone reconcile for prod.example, got %#v", operator.reconciled)
	}
	assertDNSPublishedKind(t, capture.events, KindDNSOperationStatus)
	result := assertDNSPublishedKind(t, capture.events, KindDNSDriftRemediateResult)
	assertDNSResultStatus(t, result, "success")
}

func TestDNSZoneCreateExistingZoneReturnsSuccess(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	event := &nostr.Event{ID: testNostrID("dns-zone-create"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(KindDNSZoneCreateRequest), Content: `{"zone":"prod.example"}`}

	reactor.handleDNSZoneCreate(context.Background(), event)

	if len(operator.reconciled) != 1 || operator.reconciled[0] != "prod.example" {
		t.Fatalf("expected zone reconcile for existing zone, got %#v", operator.reconciled)
	}
	result := assertDNSPublishedKind(t, capture.events, KindDNSZoneCreateResult)
	assertDNSResultStatus(t, result, "success")
}

func TestDNSZoneCreateUnknownZoneReturnsUnsupported(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	operator.zones = map[string]bool{}
	event := &nostr.Event{ID: testNostrID("dns-zone-create-unknown"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(KindDNSZoneCreateRequest), Content: `{"zone":"unknown.example"}`}

	reactor.handleDNSZoneCreate(context.Background(), event)

	if len(operator.reconciled) != 0 {
		t.Fatalf("expected no reconcile for unknown zone, got %#v", operator.reconciled)
	}
	result := assertDNSPublishedKind(t, capture.events, KindDNSZoneCreateResult)
	assertDNSResultStatus(t, result, "failed")
	assertDNSResultStep(t, result, "unsupported")
}

func TestDNSZoneCreatePersistsZoneWhenRepositoryAvailable(t *testing.T) {
	operator := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{}}}
	reactor, capture, pubkey := newDNSHandlerTestReactorWithOperator(t, operator)
	zone := domain.DNSZone{Name: "edge.example", Visibility: domain.ZoneVisibilityEdge, BackendRef: "edge-dns", TTL: 300}
	content, _ := json.Marshal(zone)
	event := &nostr.Event{ID: testNostrID("dns-zone-create-durable"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(KindDNSZoneCreateRequest), Content: string(content)}

	reactor.handleDNSZoneCreate(context.Background(), event)

	if len(operator.zonesCreated) != 1 || operator.zonesCreated[0].Name != "edge.example" {
		t.Fatalf("expected persisted zone edge.example, got %#v", operator.zonesCreated)
	}
	if len(operator.reconciled) != 1 || operator.reconciled[0] != "edge.example" {
		t.Fatalf("expected reconcile for edge.example, got %#v", operator.reconciled)
	}
	result := assertDNSPublishedKind(t, capture.events, KindDNSZoneCreateResult)
	assertDNSResultStatus(t, result, "success")
	assertDNSResultField(t, result, "zone", "edge.example")
}

func TestDNSRecordOverridePersistsWithOperatorPubkey(t *testing.T) {
	operator := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{"prod.example": true}}}
	reactor, capture, pubkey := newDNSHandlerTestReactorWithOperator(t, operator)
	override := domain.DNSRecordOverride{ZoneName: "prod.example", RecordName: "api", RecordType: domain.DNSRecordTypeA, Value: "192.0.2.10", TTL: 60, Reason: "maintenance drain"}
	content, _ := json.Marshal(override)
	event := &nostr.Event{ID: testNostrID("dns-record-override"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(KindDNSRecordOverrideRequest), Content: string(content)}

	reactor.handleDNSRecordOverride(context.Background(), event)

	if len(operator.overridesCreated) != 1 {
		t.Fatalf("expected one override persisted, got %#v", operator.overridesCreated)
	}
	created := operator.overridesCreated[0]
	if created.OperatorPubkey != pubkey {
		t.Fatalf("operator pubkey = %q, want %q", created.OperatorPubkey, pubkey)
	}
	if created.ID == uuid.Nil {
		t.Fatalf("expected generated override ID")
	}
	if len(operator.reconciled) != 1 || operator.reconciled[0] != "prod.example" {
		t.Fatalf("expected reconcile for prod.example, got %#v", operator.reconciled)
	}
	result := assertDNSPublishedKind(t, capture.events, KindDNSRecordOverrideResult)
	assertDNSResultStatus(t, result, "success")
	assertDNSResultField(t, result, "zone", "prod.example")
	assertDNSResultField(t, result, "override_id", created.ID.String())
}

func TestDNSDurableHandlersInvalidPayloadsReturnErrors(t *testing.T) {
	cases := []struct {
		name       string
		kind       int
		handle     func(*Reactor, context.Context, *nostr.Event)
		resultKind int
		content    string
	}{
		{name: "zone", kind: KindDNSZoneCreateRequest, handle: func(r *Reactor, ctx context.Context, ev *nostr.Event) { r.handleDNSZoneCreate(ctx, ev) }, resultKind: KindDNSZoneCreateResult, content: `{"name":"bad.example","visibility":"private","backend_ref":"edge-dns","ttl":300}`},
		{name: "override", kind: KindDNSRecordOverrideRequest, handle: func(r *Reactor, ctx context.Context, ev *nostr.Event) { r.handleDNSRecordOverride(ctx, ev) }, resultKind: KindDNSRecordOverrideResult, content: `{"zone_name":"prod.example","record_name":"api","record_type":"TXT","value":"bad","ttl":60,"reason":"test"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operator := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{"prod.example": true}}}
			reactor, capture, pubkey := newDNSHandlerTestReactorWithOperator(t, operator)
			event := &nostr.Event{ID: testNostrID("dns-invalid-" + tc.name), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(tc.kind), Content: tc.content}

			tc.handle(reactor, context.Background(), event)

			if len(operator.zonesCreated) != 0 || len(operator.overridesCreated) != 0 || len(operator.reconciled) != 0 {
				t.Fatalf("expected no persistence or reconcile; zones=%#v overrides=%#v reconciled=%#v", operator.zonesCreated, operator.overridesCreated, operator.reconciled)
			}
			result := assertDNSPublishedKind(t, capture.events, tc.resultKind)
			assertDNSResultStatus(t, result, "error")
			assertDNSResultStep(t, result, "validation_error")
		})
	}
}

func TestDNSPolicyApplyValidPayloadPersistsPolicyAndTriggersReconcile(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	operator.policyRepo = &recordingDNSPolicyRepository{}
	ttl := 120
	policy := domain.DNSPolicy{
		Name:    "latency-aware",
		Rules:   []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{TTLOverride: &ttl}}},
		Enabled: true,
	}
	content, _ := json.Marshal(policy)
	event := &nostr.Event{ID: testNostrID("dns-policy-apply"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(KindDNSPolicyApplyRequest), Content: string(content)}

	reactor.handleDNSPolicyApply(context.Background(), event)

	if len(operator.policyRepo.created) != 1 {
		t.Fatalf("expected one policy persisted, got %#v", operator.policyRepo.created)
	}
	created := operator.policyRepo.created[0]
	if created.Name != "latency-aware" {
		t.Fatalf("persisted policy name = %q, want latency-aware", created.Name)
	}
	if created.ID == uuid.Nil {
		t.Fatalf("expected generated policy ID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("expected policy timestamps to be set, got created=%s updated=%s", created.CreatedAt, created.UpdatedAt)
	}
	if operator.reconcileAll != 1 {
		t.Fatalf("expected ReconcileAll once, got %d", operator.reconcileAll)
	}
	assertDNSPublishedKind(t, capture.events, KindDNSOperationStatus)
	result := assertDNSPublishedKind(t, capture.events, KindDNSPolicyApplyResult)
	assertDNSResultStatus(t, result, "success")
	assertDNSResultStep(t, result, "completed")
	assertDNSResultField(t, result, "policy", "latency-aware")
	assertDNSResultField(t, result, "rule_count", float64(1))
}

func TestDNSPolicyApplyInvalidPayloadReturnsValidationError(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	operator.policyRepo = &recordingDNSPolicyRepository{}
	event := &nostr.Event{ID: testNostrID("dns-policy-apply-invalid"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(KindDNSPolicyApplyRequest), Content: `{"name":"invalid","rules":[]}`}

	reactor.handleDNSPolicyApply(context.Background(), event)

	if operator.reconcileAll != 0 {
		t.Fatalf("expected no reconcile for invalid policy, got %d", operator.reconcileAll)
	}
	result := assertDNSPublishedKind(t, capture.events, KindDNSPolicyApplyResult)
	assertDNSResultStatus(t, result, "error")
	assertDNSResultStep(t, result, "validation_error")
}

func TestDNSUnsupportedHandlersPublishDeterministicResults(t *testing.T) {
	reactor, capture, pubkey, _ := newDNSHandlerTestReactor(t)
	cases := []struct {
		kind       int
		resultKind int
	}{
		{kind: KindDNSRecordOverrideRequest, resultKind: KindDNSRecordOverrideResult},
		{kind: KindDNSPolicyApplyRequest, resultKind: KindDNSPolicyApplyResult},
		{kind: KindDNSBackendRegisterRequest, resultKind: KindDNSBackendRegisterResult},
	}
	for _, tc := range cases {
		capture.events = nil
		reactor.handleDNSRequest(context.Background(), &nostr.Event{ID: testNostrID("dns-unsupported"), PubKey: testNostrPubKeyFromHex(t, pubkey), Kind: nostr.Kind(tc.kind), Content: `{}`})
		result := assertDNSPublishedKind(t, capture.events, tc.resultKind)
		assertDNSResultStatus(t, result, "failed")
		assertDNSResultStep(t, result, "unsupported")
	}
}

func newDNSHandlerTestReactor(t *testing.T) (*Reactor, *captureNostrPublisher, string, *recordingDNSOperator) {
	t.Helper()
	operator := &recordingDNSOperator{zones: map[string]bool{"prod.example": true}}
	reactor, capture, pubkey := newDNSHandlerTestReactorWithOperator(t, operator)
	return reactor, capture, pubkey, operator
}

func newDNSHandlerTestReactorWithOperator(t *testing.T, operator DNSControlPlaneOperator) (*Reactor, *captureNostrPublisher, string) {
	t.Helper()
	privateKey, pubkey := testNostrKeypair()
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	capture := &captureNostrPublisher{published: 1}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{pubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithDNSOperator(operator))
	return reactor, capture, pubkey
}

func assertDNSPublishedKind(t *testing.T, events []nostr.Event, kind int) nostr.Event {
	t.Helper()
	legacyKind := strconv.Itoa(kind)
	if isLegacyDNSObservableKind(kind) {
		for _, ev := range events {
			if int(ev.Kind) == kind {
				t.Fatalf("legacy DNS kind %d was published directly; events=%#v", kind, events)
			}
		}
		for _, ev := range events {
			if tagValueNostr(ev.Tags, "legacy_kind") == legacyKind {
				if ok := ev.VerifySignature(); !ok {
					t.Fatalf("canonical event for legacy kind %d signature invalid", kind)
				}
				return ev
			}
		}
		t.Fatalf("canonical event carrying legacy_kind %d not published; events=%#v", kind, events)
	}
	for _, ev := range events {
		if int(ev.Kind) == kind {
			if ok := ev.VerifySignature(); !ok {
				t.Fatalf("kind %d signature invalid", kind)
			}
			return ev
		}
	}
	t.Fatalf("kind %d not published; events=%#v", kind, events)
	return nostr.Event{}
}

func isLegacyDNSObservableKind(kind int) bool {
	switch kind {
	case KindDNSOperationStatus, KindDNSZoneCreateResult, KindDNSPolicyApplyResult, KindDNSRecordOverrideResult, KindDNSDriftRemediateResult, KindDNSBackendRegisterResult:
		return true
	default:
		return false
	}
}

func assertDNSResultStatus(t *testing.T, event nostr.Event, want string) {
	t.Helper()
	if got := tagValueNostr(event.Tags, "status"); got != want {
		t.Fatalf("status tag = %q, want %s; tags=%#v content=%s", got, want, event.Tags, event.Content)
	}
}

func assertDNSResultStep(t *testing.T, event nostr.Event, want string) {
	t.Helper()
	if got := tagValueNostr(event.Tags, "step"); got != want {
		t.Fatalf("step tag = %q, want %s; tags=%#v content=%s", got, want, event.Tags, event.Content)
	}
}

func assertDNSResultField(t *testing.T, event nostr.Event, key string, want any) {
	t.Helper()
	content := dnsResultPayload(t, event)
	if got := content[key]; got != want {
		t.Fatalf("%s = %#v, want %#v; content=%s", key, got, want, event.Content)
	}
}

func dnsResultPayload(t *testing.T, event nostr.Event) map[string]any {
	t.Helper()
	var response struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(event.Content), &response); err != nil {
		t.Fatalf("decode DNS ContextVM result content: %v", err)
	}
	if response.Result == nil {
		t.Fatalf("DNS result content has no JSON-RPC result payload: %s", event.Content)
	}
	return response.Result
}

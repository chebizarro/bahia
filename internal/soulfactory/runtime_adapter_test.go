package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeRuntimeAdapterTransport struct {
	capabilities []*nostr.Event
	nip65        []*nostr.Event

	filters   []nostr.Filter
	published []nostr.Event

	resultStatus string
	resultError  *RuntimeControlError
	wrongFirst   *nostr.Event
	resultSub    *RelayBusSubscription
	resultEvents chan *nostr.Event
}

func (f *fakeRuntimeAdapterTransport) Publish(_ context.Context, event nostr.Event) (int, error) {
	f.published = append(f.published, event)
	if f.resultSub != nil && event.Kind == nostr.Kind(domain.KindRuntimeControlRequest) {
		if f.wrongFirst != nil {
			f.sendResult(f.wrongFirst)
		}
		result := f.buildRuntimeResult(event)
		f.sendResult(result)
	}
	return 1, nil
}

func (f *fakeRuntimeAdapterTransport) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (*RelayBusSubscription, error) {
	f.filters = append(f.filters, filters...)
	if len(filters) == 0 {
		return nil, errors.New("missing filters")
	}
	kind := nostr.Kind(0)
	if len(filters[0].Kinds) > 0 {
		kind = filters[0].Kinds[0]
	}
	switch kind {
	case nostr.Kind(domain.KindRuntimeCapability):
		return bufferedSubscription(f.capabilities...), nil
	case nostr.Kind(kindNIP65RelayListMetadata):
		return bufferedSubscription(f.nip65...), nil
	case nostr.Kind(domain.KindRuntimeControlResult):
		events := make(chan *nostr.Event, 8)
		eose := make(chan struct{})
		close(eose)
		f.resultEvents = events
		f.resultSub = &RelayBusSubscription{Events: events, EndOfStoredEvents: eose, cancel: func() {}}
		return f.resultSub, nil
	default:
		return nil, errors.New("unexpected filter kind")
	}
}

func (f *fakeRuntimeAdapterTransport) Close() {}

func (f *fakeRuntimeAdapterTransport) sendResult(event *nostr.Event) {
	if f.resultEvents == nil || event == nil {
		return
	}
	select {
	case f.resultEvents <- event:
	default:
	}
}

func (f *fakeRuntimeAdapterTransport) buildRuntimeResult(request nostr.Event) *nostr.Event {
	status := f.resultStatus
	if status == "" {
		status = "success"
	}
	envelope, _ := ParseRuntimeControlRequestEvent(&request)
	result := map[string]interface{}{
		"agent_id":        envelope.Target.AgentID,
		"runtime":         envelope.Target.Runtime,
		"runtime_binding": string(envelope.Target.Runtime) + "://agents/" + envelope.Target.AgentID,
		"state":           "running",
		"spec_hash":       envelope.Soul.SpecHash,
		"capability_ref":  tagValue(request.Tags, tagCapability),
		"observed_at":     int64(1715700005),
		"warnings":        []string{},
	}
	if status != "success" {
		result = nil
	}
	content, _ := json.Marshal(RuntimeControlResultEnvelope{
		Schema:               domain.SoulFactoryRuntimeControlSchema,
		Method:               envelope.Method,
		IdempotencyKey:       envelope.IdempotencyKey,
		RequestEvent:         request.ID.Hex(),
		OperatorRequestEvent: envelope.Operator.RequestEvent,
		Status:               status,
		Result:               result,
		Error:                f.resultError,
	})
	event := &nostr.Event{
		Kind:      nostr.Kind(domain.KindRuntimeControlResult),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{tagPubkey, envelope.Controller.Pubkey},
			{tagEvent, request.ID.Hex()},
			{"method", envelope.Method},
			{"idempotency-key", envelope.IdempotencyKey},
			{tagAgentID, envelope.Target.AgentID},
			{tagSoul, envelope.Soul.ID},
			{tagSpecHash, envelope.Soul.SpecHash},
			{tagSchema, domain.SoulFactoryRuntimeControlSchema},
			{tagStatus, status},
		},
		Content: string(content),
	}
	secret, err := nostr.SecretKeyFromHex(runtimeAdapterTestRuntimeSecret)
	if err != nil {
		panic(err)
	}
	_ = event.Sign(secret)
	return event
}

func bufferedSubscription(events ...*nostr.Event) *RelayBusSubscription {
	eventCh := make(chan *nostr.Event)
	eose := make(chan struct{})
	go func() {
		for _, event := range events {
			eventCh <- event
		}
		close(eose)
		close(eventCh)
	}()
	return &RelayBusSubscription{Events: eventCh, EndOfStoredEvents: eose, cancel: func() {}}
}

var (
	runtimeAdapterTestRuntimeSecret string
	runtimeAdapterTestRuntimePubkey string
)

func TestRuntimeCapabilityParsesOpenClawAndMetiqShapes(t *testing.T) {
	controller := newFakeSigner(t)
	runtime := newFakeSigner(t)
	openclaw := signedRuntimeCapabilityEvent(t, runtime, map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            "openclaw",
		"methods":            []string{RuntimeMethodProvision},
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
		"relay_hints": map[string]interface{}{
			"read": []string{"wss://read.example"},
		},
	}, nostr.Tags{
		{tagParameterizedD, "openclaw-main"},
		{tagRuntime, "openclaw"},
		{tagSchema, domain.SoulFactoryRuntimeCapabilitySchema},
		{"control-schema", domain.SoulFactoryRuntimeControlSchema},
		{"method", RuntimeMethodUpdate},
		{"controller", controller.pubkey},
		{"relay", "wss://control.example", "control"},
	})
	metiq := signedRuntimeCapabilityEvent(t, runtime, map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            "metiq",
		"methods":            []string{RuntimeMethodProvision, RuntimeMethodSuspend},
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
		"relay_hints": map[string]interface{}{
			"control": []string{"wss://metiq-control.example"},
		},
	}, nostr.Tags{{tagParameterizedD, runtime.pubkey}, {tagRuntime, "metiq"}, {"relay", "wss://metiq.example"}})

	openclawCap, ok := ParseRuntimeCapabilityEvent(openclaw)
	if !ok {
		t.Fatal("OpenClaw capability did not parse")
	}
	if !openclawCap.Supports(domain.RuntimeTargetOpenClaw, RuntimeMethodUpdate, controller.pubkey) {
		t.Fatalf("OpenClaw capability did not merge tag/content method and controller: %+v", openclawCap)
	}
	if got := openclawCap.RelayHints.Control; !reflect.DeepEqual(got, []string{"wss://control.example"}) {
		t.Fatalf("OpenClaw control hints = %#v", got)
	}
	if openclawCap.Coordinate != "30317:"+runtime.pubkey+":openclaw-main" {
		t.Fatalf("coordinate = %q", openclawCap.Coordinate)
	}

	metiqCap, ok := ParseRuntimeCapabilityEvent(metiq)
	if !ok {
		t.Fatal("Metiq capability did not parse")
	}
	if !metiqCap.Supports(domain.RuntimeTargetMetiq, RuntimeMethodSuspend, controller.pubkey) {
		t.Fatalf("Metiq capability did not support content-only SoulFactory contract: %+v", metiqCap)
	}
	if got := metiqCap.RelayHints.Control; !reflect.DeepEqual(got, []string{"wss://metiq-control.example", "wss://metiq.example"}) {
		t.Fatalf("Metiq control hints = %#v", got)
	}
}

func TestRuntimeAdapterDiscoversCapabilitiesNewestFirst(t *testing.T) {
	controller := newFakeSigner(t)
	oldRuntime := newFakeSigner(t)
	newRuntime := newFakeSigner(t)
	now := int64(nostr.Now())
	oldCapability := signedRuntimeCapabilityEventAt(t, oldRuntime, now-10, map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            "openclaw",
		"methods":            []string{RuntimeMethodProvision},
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
	}, nostr.Tags{{tagParameterizedD, "old"}, {tagRuntime, "openclaw"}})
	newCapability := signedRuntimeCapabilityEventAt(t, newRuntime, now, map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            "openclaw",
		"methods":            []string{RuntimeMethodProvision},
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
	}, nostr.Tags{{tagParameterizedD, "new"}, {tagRuntime, "openclaw"}})
	transport := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{oldCapability, newCapability}}
	adapter, err := NewOpenClawRuntimeAdapter(RuntimeAdapterConfig{ControllerPubkey: controller.pubkey, Signer: controller, Relays: []string{"wss://fallback.example"}, Transport: transport})
	if err != nil {
		t.Fatalf("NewOpenClawRuntimeAdapter error = %v", err)
	}

	capabilities, err := adapter.DiscoverCapabilities(t.Context(), domain.SoulRelayPolicySpec{})
	if err != nil {
		t.Fatalf("DiscoverCapabilities error = %v", err)
	}
	if len(capabilities) != 2 || capabilities[0].Pubkey != newRuntime.pubkey || capabilities[1].Pubkey != oldRuntime.pubkey {
		t.Fatalf("capabilities not sorted newest first: %+v", capabilities)
	}
}

func TestRuntimeAdapterSignsRequestSelectsRelaysAndRequiresCorrelatedResult(t *testing.T) {
	controller := newFakeSigner(t)
	runtime := newFakeSigner(t)
	runtimeAdapterTestRuntimeSecret = runtime.secret
	runtimeAdapterTestRuntimePubkey = runtime.pubkey

	capabilityEvent := signedRuntimeCapabilityEvent(t, runtime, map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            "openclaw",
		"methods":            []string{RuntimeMethodProvision},
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
		"relay_hints": map[string]interface{}{
			"control": []string{"wss://cap-control.example"},
			"read":    []string{"wss://cap-read.example"},
		},
	}, nostr.Tags{{tagParameterizedD, "openclaw-main"}, {tagRuntime, "openclaw"}})
	nip65 := signedNIP65Event(t, runtime, nostr.Tags{{"r", "wss://nip65-read.example", "read"}, {"r", "wss://nip65-write.example", "write"}})
	wrong := signedRuntimeResultForDifferentRequest(t, runtime, controller.pubkey)
	transport := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{capabilityEvent}, nip65: []*nostr.Event{nip65}, wrongFirst: wrong}

	adapter, err := NewOpenClawRuntimeAdapter(RuntimeAdapterConfig{
		ControllerPubkey: controller.pubkey,
		Signer:           controller,
		Relays:           []string{"wss://fallback.example"},
		Transport:        transport,
	})
	if err != nil {
		t.Fatalf("NewOpenClawRuntimeAdapter error = %v", err)
	}

	result, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
		Method: RuntimeMethodProvision,
		Operator: RuntimeOperatorRef{
			Pubkey:       stringsRepeat("a", 64),
			RequestEvent: stringsRepeat("b", 64),
		},
		Soul: RuntimeSoulRef{ID: "scout", Draft: "draft-event", SpecHash: "sha256:spec"},
		Target: RuntimeTargetRef{
			Runtime: domain.RuntimeTargetOpenClaw,
			AgentID: "scout",
		},
		DraftPolicy: domain.SoulRelayPolicySpec{
			Control:        []string{"wss://draft-control.example"},
			NIP65Discovery: true,
		},
		Params:      map[string]interface{}{"identity": map[string]interface{}{"name": "Scout"}},
		RequestKind: domain.KindProvisioningRequest,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.Status != "success" || result.Result["runtime_binding"] != "openclaw://agents/scout" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want 1", len(transport.published))
	}
	request := transport.published[0]
	if request.Kind != nostr.Kind(domain.KindRuntimeControlRequest) || request.PubKey.Hex() != controller.pubkey || !request.CheckID() {
		t.Fatalf("request not signed as controller: kind=%d pubkey=%s id=%s", request.Kind, request.PubKey.Hex(), request.ID.Hex())
	}
	if got := tagValue(request.Tags, tagPubkey); got != runtime.pubkey {
		t.Fatalf("request target p = %q, want runtime pubkey", got)
	}
	for _, tagName := range []string{tagEvent, tagSoul, tagAgentID, "idempotency-key", tagSpecHash, tagSchema, "controller"} {
		if tagValue(request.Tags, tagName) == "" {
			t.Fatalf("request missing required tag %q: %#v", tagName, request.Tags)
		}
	}
	envelope, err := ParseRuntimeControlRequestEvent(&request)
	if err != nil {
		t.Fatalf("ParseRuntimeControlRequestEvent error = %v", err)
	}
	if envelope.Operator.Pubkey == "" || envelope.Operator.RequestEvent == "" || envelope.Soul.SpecHash != "sha256:spec" || envelope.Target.RuntimePubkey != runtime.pubkey {
		t.Fatalf("request envelope missing correlated context: %+v", envelope)
	}
	if got := relayTags(request.Tags); !reflect.DeepEqual(got, []string{"wss://draft-control.example", "wss://cap-control.example", "wss://cap-read.example", "wss://nip65-read.example", "wss://nip65-write.example", "wss://fallback.example"}) {
		t.Fatalf("selected relay tags = %#v", got)
	}
	if !hasFilter(transport.filters, domain.KindRuntimeCapability, nostr.TagMap{tagRuntime: []string{"openclaw"}}) {
		t.Fatalf("missing runtime-scoped capability filter: %#v", transport.filters)
	}
	if !hasFilter(transport.filters, domain.KindRuntimeControlResult, nostr.TagMap{tagEvent: []string{request.ID.Hex()}, tagPubkey: []string{controller.pubkey}, "idempotency-key": []string{envelope.IdempotencyKey}}) {
		t.Fatalf("missing correlated result filter: %#v", transport.filters)
	}
}

func TestRuntimeAdapterReportsRejectedAndFailedRuntimeResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		code   string
	}{
		{name: "rejected unauthorized", status: "rejected", code: "unauthorized_controller"},
		{name: "failed execution", status: "failed", code: "execution_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := newFakeSigner(t)
			runtime := newFakeSigner(t)
			runtimeAdapterTestRuntimeSecret = runtime.secret
			runtimeAdapterTestRuntimePubkey = runtime.pubkey
			capabilityEvent := signedRuntimeCapabilityEvent(t, runtime, map[string]interface{}{
				"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
				"runtime":            "metiq",
				"methods":            []string{RuntimeMethodSuspend},
				"control_schema":     domain.SoulFactoryRuntimeControlSchema,
				"controller_pubkeys": []string{controller.pubkey},
			}, nostr.Tags{{tagParameterizedD, runtime.pubkey}, {tagRuntime, "metiq"}, {"relay", "wss://metiq-control.example"}})
			transport := &fakeRuntimeAdapterTransport{
				capabilities: []*nostr.Event{capabilityEvent},
				resultStatus: tc.status,
				resultError:  &RuntimeControlError{Code: tc.code, Message: "runtime said no", Retryable: false},
			}
			adapter, err := NewMetiqRuntimeAdapter(RuntimeAdapterConfig{ControllerPubkey: controller.pubkey, Signer: controller, Relays: []string{"wss://fallback.example"}, Transport: transport})
			if err != nil {
				t.Fatalf("NewMetiqRuntimeAdapter error = %v", err)
			}
			result, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
				Method:   RuntimeMethodSuspend,
				Operator: RuntimeOperatorRef{Pubkey: stringsRepeat("c", 64), RequestEvent: stringsRepeat("d", 64)},
				Soul:     RuntimeSoulRef{ID: "metiq-soul", SpecHash: "sha256:metiq"},
				Target:   RuntimeTargetRef{Runtime: domain.RuntimeTargetMetiq, AgentID: "metiq-soul"},
				Params:   map[string]interface{}{"reason": "test"},
			})
			if err == nil {
				t.Fatal("Execute error = nil, want runtime response error")
			}
			if result == nil || result.Status != tc.status || result.Error == nil || result.Error.Code != tc.code {
				t.Fatalf("result = %+v, want status/code %s/%s", result, tc.status, tc.code)
			}
		})
	}
}

func signedRuntimeCapabilityEvent(t *testing.T, signer fakeSigner, content map[string]interface{}, tags nostr.Tags) *nostr.Event {
	t.Helper()
	return signedRuntimeCapabilityEventAt(t, signer, int64(nostr.Now()), content, tags)
}

func signedRuntimeCapabilityEventAt(t *testing.T, signer fakeSigner, createdAt int64, content map[string]interface{}, tags nostr.Tags) *nostr.Event {
	t.Helper()
	body, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal capability: %v", err)
	}
	event := &nostr.Event{Kind: nostr.Kind(domain.KindRuntimeCapability), CreatedAt: nostr.Timestamp(createdAt), Tags: tags, Content: string(body)}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign capability: %v", err)
	}
	return event
}

func signedNIP65Event(t *testing.T, signer fakeSigner, tags nostr.Tags) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: nostr.Kind(kindNIP65RelayListMetadata), CreatedAt: nostr.Now(), Tags: tags}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign nip65: %v", err)
	}
	return event
}

func signedRuntimeResultForDifferentRequest(t *testing.T, signer fakeSigner, controllerPubkey string) *nostr.Event {
	t.Helper()
	content, _ := json.Marshal(RuntimeControlResultEnvelope{
		Schema:               domain.SoulFactoryRuntimeControlSchema,
		Method:               RuntimeMethodProvision,
		IdempotencyKey:       "sha256:wrong",
		RequestEvent:         stringsRepeat("e", 64),
		OperatorRequestEvent: stringsRepeat("f", 64),
		Status:               "success",
		Result:               map[string]interface{}{"agent_id": "wrong"},
	})
	event := &nostr.Event{Kind: nostr.Kind(domain.KindRuntimeControlResult), CreatedAt: nostr.Now(), Tags: nostr.Tags{{tagPubkey, controllerPubkey}, {tagEvent, stringsRepeat("e", 64)}, {"idempotency-key", "sha256:wrong"}, {tagSchema, domain.SoulFactoryRuntimeControlSchema}}, Content: string(content)}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign wrong result: %v", err)
	}
	return event
}

func relayTags(tags nostr.Tags) []string {
	var out []string
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "relay" {
			out = append(out, tag[1])
		}
	}
	return out
}

func hasFilter(filters []nostr.Filter, kind int, tags nostr.TagMap) bool {
	for _, filter := range filters {
		if len(filter.Kinds) == 0 || filter.Kinds[0] != nostr.Kind(kind) {
			continue
		}
		matched := true
		for key, want := range tags {
			got := filter.Tags[key]
			if !reflect.DeepEqual(got, want) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func stringsRepeat(value string, count int) string {
	out := ""
	for i := 0; i < count; i++ {
		out += value
	}
	return out
}

package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

func TestOperatorRuntimeDeploySubscribesBeforePublishAndHandlesStatusResultDedup(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	replyKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	artifactID := "artifact-1"

	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		status := signedOperatorReply(t, replyKey, controlplane.KindActionStatus, nostr.Tags{
			{"e", ev.ID, "", "reply"},
			{"p", ev.PubKey},
			{"status", "processing"},
			{"action", "deploy"},
			{"step", "started"},
		}, "Direct runtime action started")
		transport.events <- status
		transport.events <- status // duplicate delivery from another relay must be ignored
		transport.events <- signedOperatorReply(t, replyKey, controlplane.KindActionResult, nostr.Tags{
			{"e", ev.ID, "", "reply"},
			{"p", ev.PubKey},
			{"status", "success"},
			{"action", "deploy"},
		}, `{"action":"deploy","service_id":"svc-1","environment_id":"env-1"}`)
		return 1, nil
	}

	var statuses []OperatorStatusEvent
	result, err := client.DeployServiceRuntimeNostr(context.Background(), "svc-1", "env-1", &artifactID, func(status OperatorStatusEvent) {
		statuses = append(statuses, status)
	})
	if err != nil {
		t.Fatalf("DeployServiceRuntimeNostr() error = %v", err)
	}
	if result.Action != "deploy" || result.ServiceID != "svc-1" || result.EnvironmentID != "env-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(statuses) != 1 {
		t.Fatalf("status callback count = %d, want 1", len(statuses))
	}
	if statuses[0].Status != "processing" || statuses[0].Step != "started" || statuses[0].Message != "Direct runtime action started" {
		t.Fatalf("unexpected status event: %#v", statuses[0])
	}
	if got := transport.calls; len(got) != 2 || got[0] != "subscribe" || got[1] != "publish" {
		t.Fatalf("calls = %#v, want subscribe before publish", got)
	}
	published := transport.onlyPublished(t)
	assertSignedEvent(t, published)
	if published.Kind != controlplane.KindServiceAction {
		t.Fatalf("request kind = %d, want %d", published.Kind, controlplane.KindServiceAction)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(published.Content), &payload); err != nil {
		t.Fatalf("decode request content: %v", err)
	}
	for key, want := range map[string]string{"action": "deploy", "service_id": "svc-1", "environment_id": "env-1", "artifact_id": artifactID} {
		if payload[key] != want {
			t.Fatalf("payload[%s] = %q, want %q (payload=%#v)", key, payload[key], want, payload)
		}
	}
	assertTagValue(t, published.Tags, "action", "deploy")
	assertTagValue(t, published.Tags, "service", "svc-1")
	assertTagValue(t, published.Tags, "environment", "env-1")
	assertTagValue(t, published.Tags, "artifact", artifactID)
	filter := transport.onlyFilter(t)
	if got := filter.Tags["e"]; len(got) != 1 || got[0] != published.ID {
		t.Fatalf("filter #e = %#v, want request id %s", got, published.ID)
	}
	if got := filter.Tags["p"]; len(got) != 1 || got[0] != published.PubKey {
		t.Fatalf("filter #p = %#v, want requester pubkey %s", got, published.PubKey)
	}
}

func TestOperatorRuntimeRestartStopRequestConstruction(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, *OperatorControlPlaneClient) (*RuntimeActionResult, error)
	}{
		{name: "restart", run: func(ctx context.Context, c *OperatorControlPlaneClient) (*RuntimeActionResult, error) {
			return c.RestartServiceRuntimeNostr(ctx, "svc-1", "env-1", nil)
		}},
		{name: "stop", run: func(ctx context.Context, c *OperatorControlPlaneClient) (*RuntimeActionResult, error) {
			return c.StopServiceRuntimeNostr(ctx, "svc-1", "env-1", nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestKey := nostr.GeneratePrivateKey()
			replyKey := nostr.GeneratePrivateKey()
			transport := newFakeOperatorTransport()
			client := newTestOperatorClient(t, requestKey, transport)
			transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
				transport.events <- signedOperatorReply(t, replyKey, controlplane.KindActionResult, nostr.Tags{{"e", ev.ID, "", "reply"}, {"p", ev.PubKey}, {"status", "success"}}, `{"action":"`+tc.name+`","service_id":"svc-1","environment_id":"env-1"}`)
				return 1, nil
			}
			result, err := tc.run(context.Background(), client)
			if err != nil {
				t.Fatalf("runtime action error = %v", err)
			}
			if result.Action != tc.name {
				t.Fatalf("result action = %q, want %q", result.Action, tc.name)
			}
			published := transport.onlyPublished(t)
			var payload map[string]string
			if err := json.Unmarshal([]byte(published.Content), &payload); err != nil {
				t.Fatalf("decode request content: %v", err)
			}
			if payload["action"] != tc.name {
				t.Fatalf("action = %q, want %q", payload["action"], tc.name)
			}
			if _, exists := payload["artifact_id"]; exists {
				t.Fatalf("non-deploy request included artifact_id: %#v", payload)
			}
		})
	}
}

func TestOperatorIgnoresInvalidUncorrelatedAndDuplicateReplies(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	replyKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		invalid := signedOperatorReply(t, replyKey, controlplane.KindActionResult, nostr.Tags{{"e", ev.ID, "", "reply"}, {"p", ev.PubKey}, {"status", "success"}}, `{"action":"restart"}`)
		invalid.Content = `{"action":"tampered"}`
		transport.events <- invalid
		future := signedOperatorReply(t, replyKey, controlplane.KindActionResult, nostr.Tags{{"e", ev.ID, "", "reply"}, {"p", ev.PubKey}, {"status", "success"}}, `{"action":"future"}`)
		future.CreatedAt = nostr.Timestamp(int64(nostr.Now()) + 601)
		if err := future.Sign(replyKey); err != nil {
			t.Fatalf("sign future reply: %v", err)
		}
		transport.events <- future
		transport.events <- signedOperatorReply(t, replyKey, controlplane.KindActionResult, nostr.Tags{{"e", "different", "", "reply"}, {"p", ev.PubKey}, {"status", "success"}}, `{"action":"restart"}`)
		good := signedOperatorReply(t, replyKey, controlplane.KindActionResult, nostr.Tags{{"e", ev.ID, "", "reply"}, {"p", ev.PubKey}, {"status", "success"}}, `{"action":"restart","service_id":"svc-1","environment_id":"env-1"}`)
		transport.events <- good
		transport.events <- good
		return 1, nil
	}
	result, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	if err != nil {
		t.Fatalf("RestartServiceRuntimeNostr() error = %v", err)
	}
	if result.Action != "restart" {
		t.Fatalf("result action = %q, want restart", result.Action)
	}
}

func TestOperatorRuntimeTerminalFailure(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	replyKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedOperatorReply(t, replyKey, controlplane.KindActionResult, nostr.Tags{{"e", ev.ID, "", "reply"}, {"p", ev.PubKey}, {"status", "failed"}, {"error", "runtime denied"}}, `{"status":"failed","error":"json error"}`)
		return 1, nil
	}
	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	if err == nil || !strings.Contains(err.Error(), "runtime denied") {
		t.Fatalf("error = %v, want runtime denied", err)
	}
}

func TestOperatorAdoptionScanAndImportRequestConstructionAndResults(t *testing.T) {
	for _, tc := range []struct {
		name       string
		operation  string
		resultKind int
		run        func(context.Context, *OperatorControlPlaneClient) (any, error)
		content    string
	}{
		{
			name:       "scan",
			operation:  "scan",
			resultKind: controlplane.KindAdoptionScanResult,
			content:    `[{"target":{"name":"prod","endpoint_ref":"prod-docker","environment_name":"production"},"containers":[]}]`,
			run: func(ctx context.Context, c *OperatorControlPlaneClient) (any, error) {
				return c.ScanAdoptionNostr(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker", EnvironmentName: "production"}}}, nil)
			},
		},
		{
			name:       "import",
			operation:  "import",
			resultKind: controlplane.KindAdoptionImportResult,
			content:    `[{"target_name":"prod","container_id":"abc","service_name":"api","status":"created"}]`,
			run: func(ctx context.Context, c *OperatorControlPlaneClient) (any, error) {
				return c.ImportAdoptionNostr(ctx, AdoptionImportRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker", EnvironmentName: "production"}}, Selections: []AdoptionSelection{{TargetName: "prod", ContainerID: "abc", ServiceNameOverride: "api"}}}, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestKey := nostr.GeneratePrivateKey()
			replyKey := nostr.GeneratePrivateKey()
			transport := newFakeOperatorTransport()
			client := newTestOperatorClient(t, requestKey, transport)
			transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
				transport.events <- signedOperatorReply(t, replyKey, tc.resultKind, nostr.Tags{{"e", ev.ID, "", "reply"}, {"p", ev.PubKey}, {"status", "failed"}, {"operation", tc.operation}}, tc.content)
				return 1, nil
			}
			result, err := tc.run(context.Background(), client)
			if err != nil {
				t.Fatalf("%s error = %v", tc.name, err)
			}
			if result == nil {
				t.Fatalf("%s returned nil result", tc.name)
			}
			published := transport.onlyPublished(t)
			assertTagValue(t, published.Tags, "operation", tc.operation)
			assertTagValue(t, published.Tags, "target", "prod")
			assertTagValue(t, published.Tags, "endpoint_ref", "prod-docker")
			assertTagValue(t, published.Tags, "environment_name", "production")
			if strings.Contains(published.Content, "docker_host") {
				t.Fatalf("signer-first adoption request included docker_host: %s", published.Content)
			}
		})
	}
}

func TestOperatorAdoptionErrorEnvelope(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	replyKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedOperatorReply(t, replyKey, controlplane.KindAdoptionScanResult, nostr.Tags{{"e", ev.ID, "", "reply"}, {"p", ev.PubKey}, {"status", "failed"}, {"operation", "scan"}, {"error", "not authorized"}}, `{"status":"failed","error":"not authorized"}`)
		return 1, nil
	}
	_, err := client.ScanAdoptionNostr(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("error = %v, want not authorized", err)
	}
}

func TestOperatorContextCancelAfterPublishIsPostAcceptanceAbort(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	ctx, cancel := context.WithCancel(context.Background())
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		cancel()
		return 1, nil
	}

	_, err := client.RestartServiceRuntimeNostr(ctx, "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if !reqErr.RequestAccepted || reqErr.PublishedRelays != 1 {
		t.Fatalf("RequestAccepted=%v PublishedRelays=%d, want accepted abort", reqErr.RequestAccepted, reqErr.PublishedRelays)
	}
	if !errors.Is(reqErr, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled cause", reqErr)
	}
}

func TestOperatorReplySubscriptionClosedAfterPublishIsPostAcceptanceFailure(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		close(transport.events)
		return 1, nil
	}

	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if !reqErr.RequestAccepted || reqErr.PublishedRelays != 1 {
		t.Fatalf("RequestAccepted=%v PublishedRelays=%d, want accepted post-publish failure", reqErr.RequestAccepted, reqErr.PublishedRelays)
	}
	if !strings.Contains(reqErr.Error(), "reply subscription closed before terminal result") {
		t.Fatalf("error = %v, want explicit subscription closure", reqErr)
	}
}

func TestOperatorPublishNoRelayAcceptedIsPreAcceptanceFailure(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		return 0, errors.New("all relays rejected")
	}
	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if reqErr.RequestAccepted {
		t.Fatalf("RequestAccepted = true, want false")
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want signed publish attempt", len(transport.published))
	}
}

func TestOperatorSubscribeFailureIsPreAcceptanceFailure(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	transport.subscribeErr = errors.New("subscription unavailable")
	client := newTestOperatorClient(t, requestKey, transport)
	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) || reqErr.RequestAccepted {
		t.Fatalf("error = %T %#v, want pre-acceptance ControlPlaneRequestError", err, reqErr)
	}
	if len(transport.published) != 0 {
		t.Fatalf("publish called after subscribe failure")
	}
}

func TestOperatorAdoptionRejectsDockerHostBeforePublish(t *testing.T) {
	requestKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	_, err := client.ScanAdoptionNostr(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: "unix:///var/run/docker.sock"}}}, nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) || reqErr.RequestAccepted {
		t.Fatalf("error = %T %#v, want pre-acceptance validation failure", err, reqErr)
	}
	if len(transport.calls) != 0 || len(transport.published) != 0 {
		t.Fatalf("transport was used for invalid adoption target: calls=%#v published=%d", transport.calls, len(transport.published))
	}
}

type fakeOperatorTransport struct {
	mu           sync.Mutex
	events       chan *nostr.Event
	eose         chan struct{}
	publishFn    func(context.Context, nostr.Event) (int, error)
	subscribeErr error
	published    []nostr.Event
	filters      []nostr.Filter
	calls        []string
	closed       bool
}

func newFakeOperatorTransport() *fakeOperatorTransport {
	return &fakeOperatorTransport{events: make(chan *nostr.Event, 32), eose: make(chan struct{})}
}

func (f *fakeOperatorTransport) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "publish")
	f.published = append(f.published, ev)
	fn := f.publishFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, ev)
	}
	return 1, nil
}

func (f *fakeOperatorTransport) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "subscribe")
	f.filters = append(f.filters, filters...)
	err := f.subscribeErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &nostrpool.MergedSubscription{Events: f.events, EndOfStoredEvents: f.eose}, nil
}

func (f *fakeOperatorTransport) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeOperatorTransport) onlyPublished(t *testing.T) nostr.Event {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.published) != 1 {
		t.Fatalf("published count = %d, want 1", len(f.published))
	}
	return f.published[0]
}

func (f *fakeOperatorTransport) onlyFilter(t *testing.T) nostr.Filter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) != 1 {
		t.Fatalf("filter count = %d, want 1", len(f.filters))
	}
	return f.filters[0]
}

func newTestOperatorClient(t *testing.T, privateKey string, transport operatorRelayTransport) *OperatorControlPlaneClient {
	t.Helper()
	normalized, err := NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("NormalizeNostrPrivateKey() error = %v", err)
	}
	signer, err := controlplane.NewPrivateKeySigner(normalized)
	if err != nil {
		t.Fatalf("NewPrivateKeySigner() error = %v", err)
	}
	pubkey, err := nostr.GetPublicKey(normalized)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}
	return &OperatorControlPlaneClient{relays: []string{"wss://relay.example"}, privateKey: normalized, signer: signer, pubkey: pubkey, transport: transport}
}

func signedOperatorReply(t *testing.T, privateKey string, kind int, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: content}
	if err := event.Sign(privateKey); err != nil {
		t.Fatalf("sign reply event: %v", err)
	}
	return event
}

func assertSignedEvent(t *testing.T, event nostr.Event) {
	t.Helper()
	if !event.CheckID() {
		t.Fatalf("event ID does not match serialized event: %#v", event)
	}
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("event signature ok = %v, err = %v", ok, err)
	}
}

func assertTagValue(t *testing.T, tags nostr.Tags, name, value string) {
	t.Helper()
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return
		}
	}
	t.Fatalf("missing tag %s=%s in %#v", name, value, tags)
}

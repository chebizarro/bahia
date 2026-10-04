package controlplane

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"go.uber.org/zap"
)

// sidecarRelayMaxMessageBytes is the relay sidecar's websocket frame limit
// (khatru MaxMessageSize, NIP-11 max_message_length).
const sidecarRelayMaxMessageBytes = 512000

type relayPublisher struct{ relay *nostr.Relay }

func (p relayPublisher) Publish(ctx context.Context, event nostr.Event) (int, error) {
	if err := p.relay.Publish(ctx, event); err != nil {
		return 0, err
	}
	return 1, nil
}

type contextVMSidecarHarness struct {
	url       string
	requester *nostr.Relay
	daemon    *nostr.Relay
	transport *EncryptedRequestTransport
	inbox     *nostr.Subscription // requester's replies
	requests  *nostr.Subscription // daemon's requests
}

// startContextVMSidecarHarness runs a real relay sidecar with two clients: a
// requester (the web or CLI role) and the daemon, whose ContextVM transport
// answers through the sidecar. Both subscriptions are live (EOSE) before the
// test publishes anything.
func startContextVMSidecarHarness(t *testing.T, ctx context.Context) *contextVMSidecarHarness {
	t.Helper()
	cfg := config.Defaults().Nostr
	cfg.Sidecar.DataDir = t.TempDir()
	sidecar, err := relaysidecar.New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	t.Cleanup(func() { _ = sidecar.Close() })
	server := httptest.NewServer(sidecar.Handler())
	t.Cleanup(server.Close)
	h := &contextVMSidecarHarness{url: "ws" + strings.TrimPrefix(server.URL, "http")}
	connect := func() *nostr.Relay {
		relay, err := nostr.RelayConnect(ctx, h.url, nostr.RelayOptions{})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = relay.Close() })
		return relay
	}
	h.requester, h.daemon = connect(), connect()
	wrapKinds := []nostr.Kind{KindContextVMMessage, KindContextVMGiftWrap, KindContextVMEphemeralWrap}
	subscribe := func(relay *nostr.Relay, pubkey string) *nostr.Subscription {
		sub, err := relay.Subscribe(ctx, nostr.Filter{Kinds: wrapKinds, Tags: nostr.TagMap{"p": {pubkey}}}, nostr.SubscriptionOptions{})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		select {
		case <-sub.EndOfStoredEvents:
		case <-ctx.Done():
			t.Fatal("no EOSE")
		}
		return sub
	}
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey).Hex()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	h.inbox = subscribe(h.requester, requesterPubkey)
	h.requests = subscribe(h.daemon, servicePubkey)
	h.transport = NewEncryptedRequestTransport(nil, newResponder(t, relayPublisher{relay: h.daemon}), []string{requesterPubkey}, zap.NewNop())
	return h
}

// deliverNextRequest hands the next request the daemon receives from the
// sidecar to its ContextVM transport.
func (h *contextVMSidecarHarness) deliverNextRequest(t *testing.T, ctx context.Context) nostr.Event {
	t.Helper()
	select {
	case request := <-h.requests.Events:
		h.transport.HandleEvent(ctx, &request)
		return request
	case <-ctx.Done():
		t.Fatal("the daemon never received the request through the sidecar")
		return nostr.Event{}
	}
}

// nextReply returns the next reply correlated to requestID that is not a
// progress notification.
func (h *contextVMSidecarHarness) nextReply(t *testing.T, ctx context.Context, requestID nostr.ID) nostr.Event {
	t.Helper()
	for {
		select {
		case reply := <-h.inbox.Events:
			if !hasTag(reply.Tags, "e", requestID.Hex()) {
				continue
			}
			inner := reply
			if reply.Kind != KindContextVMMessage {
				inner = unwrapContextVMResponseEvent(t, reply, testRequesterKey)
			}
			if strings.Contains(inner.Content, ContextVMProgressNotificationMethod) {
				continue
			}
			return reply
		case <-ctx.Done():
			t.Fatal("no reply reached the requester through the sidecar")
			return nostr.Event{}
		}
	}
}

// TestContextVMInlineSBOMAtTheLimitCrossesTheSidecar: an sbom/import shaped
// exactly as the web sends it (plaintext 25910, inline payload of
// maxContextVMInlineSBOMBytes) fits one relay message, is relayed by the
// sidecar to the daemon, is enqueued in full, and the reply comes back.
func TestContextVMReplyTooLargeToStoreCrossesTheSidecar(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	h := startContextVMSidecarHarness(t, ctx)
	logs := strings.Repeat("log line\n", 5_000) // 45 KB
	h.transport.RegisterContextVMHandler(ContextVMMethodDeploymentRunLogsGet, func(context.Context, ContextVMRequest) (any, error) {
		return map[string]any{"logs": logs}, nil
	})
	inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"logs","method":"deployments/run-logs-get","params":{"_meta":{"progressToken":"logs"}}}`)
	request := wrapContextVMEvent(t, inner, KindContextVMGiftWrap)
	if err := h.requester.Publish(ctx, *request); err != nil {
		t.Fatalf("publish request: %v", err)
	}
	h.deliverNextRequest(t, ctx)

	reply := h.nextReply(t, ctx, request.ID)
	if reply.Kind != KindContextVMEphemeralWrap || len(reply.Content) <= maxStoredGiftWrapContentBytes {
		t.Fatalf("reply kind %d with %d bytes of content, want an ephemeral wrap over %d", reply.Kind, len(reply.Content), maxStoredGiftWrapContentBytes)
	}
	rpc := contextVMResponse(t, unwrapContextVMResponseEvent(t, reply, testRequesterKey))
	if rpc.Error != nil || !strings.Contains(string(encodeForTest(t, rpc.Result)), "log line") {
		t.Fatalf("reply did not carry the logs: %+v", rpc.Error)
	}

	stored := reply
	stored.Kind = KindContextVMGiftWrap
	if err := stored.Sign(nostr.Generate()); err != nil {
		t.Fatal(err)
	}
	err := h.daemon.Publish(ctx, stored)
	if err == nil || !strings.Contains(err.Error(), "invalid: content is") {
		t.Fatalf("a stored 1059 of that size must be refused with an invalid: reason, got %v", err)
	}
}

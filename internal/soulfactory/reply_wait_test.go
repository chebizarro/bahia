package soulfactory

import (
	"context"
	"errors"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// replyTestTimeout is the configured wait bound under test. No reply is ever
// sent, so the bound itself is what ends each wait.
const replyTestTimeout = 20 * time.Millisecond

func assertNoTerminalResult(t *testing.T, err error, requestID string, cause error) {
	t.Helper()
	var noResult *NoTerminalResultError
	if !errors.As(err, &noResult) || !errors.Is(err, ErrNoTerminalResult) {
		t.Fatalf("error = %v, want *NoTerminalResultError", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cause %v", err, cause)
	}
	if requestID != "" && noResult.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", noResult.RequestID, requestID)
	}
}

// silentRuntimeTransport accepts the control request and never publishes a
// runtime result.
type silentRuntimeTransport struct {
	*fakeRuntimeAdapterTransport
}

func (s silentRuntimeTransport) Publish(_ context.Context, event nostr.Event) (int, error) {
	s.published = append(s.published, event)
	return 1, nil
}

// Reactor handler contexts carry no deadline: the adapter's own configured
// ResultTimeout must end the wait with the distinct no-terminal-result outcome.
func TestRuntimeAdapterExecuteTimesOutWithNoTerminalResult(t *testing.T) {
	controller := newFakeSigner(t)
	runtime := newFakeSigner(t)
	capability := signedRuntimeCapabilityEvent(t, runtime, map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            "openclaw",
		"methods":            []string{RuntimeMethodProvision},
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
	}, nostr.Tags{{tagParameterizedD, "openclaw-main"}, {tagRuntime, "openclaw"}})
	fake := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{capability}}
	transport := silentRuntimeTransport{fake}
	adapter, err := NewOpenClawRuntimeAdapter(RuntimeAdapterConfig{
		ControllerPubkey: controller.pubkey,
		Signer:           controller,
		Relays:           []string{"wss://fallback.example"},
		Transport:        transport,
		ResultTimeout:    replyTestTimeout,
	})
	if err != nil {
		t.Fatalf("NewOpenClawRuntimeAdapter error = %v", err)
	}

	// context.Background: no caller deadline, as in a reactor handler.
	result, err := adapter.Execute(context.Background(), RuntimeAdapterRequest{
		Method:      RuntimeMethodProvision,
		Operator:    RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
		Soul:        RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
		Target:      RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, AgentID: "scout"},
		RequestKind: domain.KindProvisioningRequest,
	})
	if result != nil {
		t.Fatalf("result = %+v, want none", result)
	}
	if len(fake.published) != 1 {
		t.Fatalf("published = %d, want the one control request", len(fake.published))
	}
	assertNoTerminalResult(t, err, fake.published[0].ID.Hex(), context.DeadlineExceeded)
}

func TestNostrClientAwaitProvisioningResultTimesOutWithNoTerminalResult(t *testing.T) {
	clientSigner := newFakeSigner(t)
	client := (&NostrClient{signer: clientSigner, transport: &subscribableSoulFactoryTransport{accepted: 1}}).WithReplyTimeout(replyTestTimeout)
	receipt := &SoulFactoryRequestReceipt{RequestID: "request-id", RequesterPubkey: clientSigner.pubkey}

	run, err := client.AwaitProvisioningResult(context.Background(), receipt, nil)
	if run != nil {
		t.Fatalf("run = %+v, want none", run)
	}
	assertNoTerminalResult(t, err, "request-id", context.DeadlineExceeded)
}

// A soul action whose result arrives after the wait gave up is still picked up
// by AwaitSoulActionResult: the result is a stored event and the new
// subscription's backfill delivers it.
func TestNostrClientSoulActionTimeoutThenLateResultIsPickedUp(t *testing.T) {
	factory := newFakeSigner(t)
	transport := &subscribableSoulFactoryTransport{accepted: 1}
	client := (&NostrClient{signer: newFakeSigner(t), transport: transport}).WithReplyTimeout(replyTestTimeout)

	reply, err := client.ExecuteSoulAction(context.Background(), "scout", domain.SoulActionSuspend, "", "")
	if reply != nil {
		t.Fatalf("reply = %v, want none", reply)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published = %d, want the one soul action", len(transport.published))
	}
	requestID := transport.published[0].ID.Hex()
	assertNoTerminalResult(t, err, requestID, context.DeadlineExceeded)
	var noResult *NoTerminalResultError
	errors.As(err, &noResult)

	late := signedSoulFactoryEventAt(t, factory, domain.KindProvisioningResult, nostr.Now(),
		nostr.Tags{{tagEvent, requestID}, {tagStatus, "success"}, {tagRequestKind, "1950"}}, "")
	transport.events = []*nostr.Event{late}
	picked, err := client.AwaitSoulActionResult(context.Background(), noResult.RequestID)
	if err != nil {
		t.Fatalf("AwaitSoulActionResult() error = %v", err)
	}
	if picked == nil || picked.ID != late.ID {
		t.Fatalf("AwaitSoulActionResult() = %v, want the late result", picked)
	}
}

// The caller owns the wait: an earlier caller deadline or a cancellation ends
// it first, still as the distinct outcome with the caller's cause.
func TestAwaitTerminalReplyKeepsCallerDeadlineAndCancellation(t *testing.T) {
	never := func(*nostr.Event) replyClass { return replyIgnore }
	newSub := func() *RelayBusSubscription {
		return &RelayBusSubscription{Events: make(chan *nostr.Event), EndOfStoredEvents: make(chan struct{}), cancel: func() {}}
	}

	deadlineCtx, cancelDeadline := context.WithTimeout(context.Background(), replyTestTimeout)
	defer cancelDeadline()
	_, err := awaitTerminalReply(deadlineCtx, newSub(), "req", time.Hour, time.Hour, never, nil)
	assertNoTerminalResult(t, err, "req", context.DeadlineExceeded)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = awaitTerminalReply(cancelled, newSub(), "req", time.Hour, time.Hour, never, nil)
	assertNoTerminalResult(t, err, "req", context.Canceled)

	closed := make(chan *nostr.Event)
	close(closed)
	_, err = awaitTerminalReply(context.Background(), &RelayBusSubscription{Events: closed, cancel: func() {}}, "req", time.Hour, time.Hour, never, nil)
	assertNoTerminalResult(t, err, "req", errReplySubscriptionClosed)
}

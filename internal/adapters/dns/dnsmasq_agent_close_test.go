package dns

import (
	"context"
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	bahiaclient "github.com/openagentsinc/bahia/pkg/client"
)

// closableRequester mimics the production ContextVMRequestClient shape:
// Request plus a no-error Close.
type closableRequester struct {
	closeCalls int
}

func (c *closableRequester) Request(context.Context, string, any, nostr.Tags, func(bahiaclient.OperatorStatusEvent)) (*nostr.Event, error) {
	return nil, errors.New("not implemented")
}

func (c *closableRequester) Close() { c.closeCalls++ }

// errorClosableRequester satisfies io.Closer.
type errorClosableRequester struct {
	closeCalls int
	closeErr   error
}

func (c *errorClosableRequester) Request(context.Context, string, any, nostr.Tags, func(bahiaclient.OperatorStatusEvent)) (*nostr.Event, error) {
	return nil, errors.New("not implemented")
}

func (c *errorClosableRequester) Close() error {
	c.closeCalls++
	return c.closeErr
}

// plainRequester exposes no Close method at all.
type plainRequester struct{}

func (plainRequester) Request(context.Context, string, any, nostr.Tags, func(bahiaclient.OperatorStatusEvent)) (*nostr.Event, error) {
	return nil, errors.New("not implemented")
}

func TestDnsmasqAgentBackendCloseClosesInjectedCloserExactlyOnce(t *testing.T) {
	requester := &closableRequester{}
	backend, err := NewDnsmasqAgentBackend(requester)
	if err != nil {
		t.Fatalf("NewDnsmasqAgentBackend: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if requester.closeCalls != 1 {
		t.Fatalf("expected injected closer to be closed exactly once, got %d calls", requester.closeCalls)
	}
}

func TestDnsmasqAgentBackendClosePropagatesIoCloserError(t *testing.T) {
	wantErr := errors.New("close failed")
	requester := &errorClosableRequester{closeErr: wantErr}
	backend, err := NewDnsmasqAgentBackend(requester)
	if err != nil {
		t.Fatalf("NewDnsmasqAgentBackend: %v", err)
	}
	if err := backend.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close error = %v, want %v", err, wantErr)
	}
	if requester.closeCalls != 1 {
		t.Fatalf("expected injected closer to be closed exactly once, got %d calls", requester.closeCalls)
	}
}

func TestDnsmasqAgentBackendCloseIgnoresRequestersWithoutClose(t *testing.T) {
	backend, err := NewDnsmasqAgentBackend(plainRequester{})
	if err != nil {
		t.Fatalf("NewDnsmasqAgentBackend: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close on requester without Close method: %v", err)
	}
}

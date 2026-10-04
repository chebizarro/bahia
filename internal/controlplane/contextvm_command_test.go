package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
)

type captureNostrPublisher struct {
	events    []nostr.Event
	published int
}

func (p *captureNostrPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.events = append(p.events, ev)
	return p.published, nil
}
func assertContextVMCommand(t *testing.T, ev nostr.Event, method string) map[string]any {
	t.Helper()
	if ev.Kind != KindContextVMMessage {
		t.Fatalf("expected ContextVM kind %d, got %d", KindContextVMMessage, ev.Kind)
	}
	if !ev.VerifySignature() {
		t.Fatal("published event signature invalid")
	}
	assertReactorTag(t, ev.Tags, "method", method)
	assertReactorTag(t, ev.Tags, ContextVMRoutingTag, ContextVMWireVersion)
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(ev.Content), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.JSONRPC != "2.0" || rpc.Method != method {
		t.Fatalf("unexpected RPC: %#v", rpc)
	}
	var params map[string]any
	if err := json.Unmarshal(rpc.Params, &params); err != nil {
		t.Fatal(err)
	}
	return params
}

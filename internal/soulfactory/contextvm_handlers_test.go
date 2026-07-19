package soulfactory

import (
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestContextVMProvisioningAdapterPreservesCanonicalCorrelation(t *testing.T) {
	request := contextVMTestRequest(t, ContextVMMethodProvision, `{"agent_id":"ravel","brief":"A careful fleet reviewer","tier":"standard"}`)
	event, err := contextVMProvisioningEvent(request)
	if err != nil {
		t.Fatalf("contextVMProvisioningEvent() error = %v", err)
	}
	if event.Kind != nostr.Kind(domain.KindProvisioningRequest) || event.ID != request.Event.ID || event.PubKey != request.Event.PubKey {
		t.Fatalf("interop event lost request correlation: %#v", event)
	}
	req, err := ParseProvisioningRequestEvent(event)
	if err != nil {
		t.Fatalf("ParseProvisioningRequestEvent() error = %v", err)
	}
	if req.AgentID != "ravel" || req.Brief != "A careful fleet reviewer" || req.EventID != request.Event.ID.Hex() {
		t.Fatalf("provisioning request = %#v", req)
	}
}

func TestContextVMActionAdapterProjectsRequiredTags(t *testing.T) {
	request := contextVMTestRequest(t, ContextVMMethodAction, `{"soul_ref":"ravel","action":"suspend","reason":"maintenance"}`)
	event, err := contextVMActionEvent(request)
	if err != nil {
		t.Fatalf("contextVMActionEvent() error = %v", err)
	}
	action, err := ParseSoulActionEvent(event)
	if err != nil {
		t.Fatalf("ParseSoulActionEvent() error = %v", err)
	}
	if action.SoulRef != "ravel" || action.Action != domain.SoulActionSuspend || action.Reason != "maintenance" || action.EventID != request.Event.ID.Hex() {
		t.Fatalf("soul action = %#v", action)
	}
}

func TestContextVMAdaptersFailClosedOnMalformedParams(t *testing.T) {
	tests := []struct {
		name   string
		method string
		params string
		run    func(controlplane.ContextVMRequest) (*nostr.Event, error)
	}{
		{name: "missing provisioning source", method: ContextVMMethodProvision, params: `{"agent_id":"ravel"}`, run: contextVMProvisioningEvent},
		{name: "missing action", method: ContextVMMethodAction, params: `{"soul_ref":"ravel"}`, run: contextVMActionEvent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.run(contextVMTestRequest(t, tt.method, tt.params)); err == nil {
				t.Fatal("adapter error = nil")
			}
		})
	}
}

func contextVMTestRequest(t *testing.T, method, params string) controlplane.ContextVMRequest {
	t.Helper()
	key := nostr.Generate()
	event := &nostr.Event{Kind: nostr.Kind(controlplane.KindContextVMMessage), CreatedAt: nostr.Now(), Content: `{}`}
	if err := event.Sign(key); err != nil {
		t.Fatalf("sign request: %v", err)
	}
	return controlplane.ContextVMRequest{
		Event: event,
		RPC:   cascontextvm.Request{JSONRPC: "2.0", ID: json.RawMessage(`"request-1"`), Method: method, Params: json.RawMessage(params)},
	}
}

package controlplane

import (
	"reflect"
	"testing"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

func TestDiscoveryAdvertisesOnlyInteractiveContextVMMethods(t *testing.T) {
	want := []string{"assistant/prompt", "assistant/approval", "assistant/cancel", "assistant/reconcile", ContextVMMethodServiceSecretsReveal, ContextVMMethodDeploymentRunLogsGet}
	for _, dnsEnabled := range []bool{false, true} {
		if got := nostrpool.DiscoveryContextVMMethods(dnsEnabled); !reflect.DeepEqual(got, want) {
			t.Fatalf("discovery methods = %v, want %v", got, want)
		}
	}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, &mockEncryptedPublisher{}), nil, zap.NewNop())
	NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{}).Register(transport)
	for _, method := range want[4:] {
		if transport.contextVMHandlers[method] == nil {
			t.Errorf("retained method %q has no handler", method)
		}
	}
	if len(transport.contextVMHandlers) != 2 {
		t.Fatalf("non-interactive ContextVM handlers registered: %v", transport.contextVMHandlers)
	}
}

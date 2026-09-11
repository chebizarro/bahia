package controlplane

import (
	"testing"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

// TestDiscoveryAdvertisesOnlyRegisteredContextVMMethods guards bahia-ubg10:
// every method in discovery control_plane.methods must resolve to a handler on
// the encrypted ContextVM transport, using the same registration groups that
// internal/app wires in production.
func TestDiscoveryAdvertisesOnlyRegisteredContextVMMethods(t *testing.T) {
	transport := NewEncryptedRequestTransport(nil, newResponder(t, &mockEncryptedPublisher{}), nil, zap.NewNop())
	RegisterWorkerContextVMHandlers(transport)
	RegisterDNSContextVMHandlers(transport, nil, false)
	RegisterServiceContextVMHandlers(transport, EncryptedServiceHandlersConfig{})
	RegisterSBOMContextVMHandlers(transport, &fakeSBOMRequestRunner{})

	for _, method := range nostrpool.DiscoveryContextVMMethods(true) {
		if transport.contextVMHandlers[method] == nil {
			t.Errorf("discovery advertises %q but no ContextVM handler is registered", method)
		}
	}
}

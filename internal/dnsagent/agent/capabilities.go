package agent

// Capability tags advertised in NIP-38 health status events.
const (
	// CapabilityZoneSubscribe indicates the agent can receive zone state
	// through REQ subscriptions instead of ContextVM RPC pushes.
	CapabilityZoneSubscribe = "zone-subscribe"

	// CapabilityZoneSubscribeVersion is the protocol version for zone-subscribe.
	CapabilityZoneSubscribeVersion = "1"
)

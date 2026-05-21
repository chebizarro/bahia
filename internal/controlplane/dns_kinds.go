package controlplane

// DNS control-plane kinds are reserved by Phase 0 for future DNS orchestration.
// Phase 0 intentionally does not modify reactor.go subscriptions or handlers.
const (
	KindDNSZoneCreateRequest      = 5941
	KindDNSPolicyApplyRequest     = 5942
	KindDNSRecordOverrideRequest  = 5943
	KindDNSDriftRemediateRequest  = 5944
	KindDNSBackendRegisterRequest = 5945

	KindDNSOperationStatus = 6941

	KindDNSZoneCreateResult      = 7941
	KindDNSPolicyApplyResult     = 7942
	KindDNSRecordOverrideResult  = 7943
	KindDNSDriftRemediateResult  = 7944
	KindDNSBackendRegisterResult = 7945
)

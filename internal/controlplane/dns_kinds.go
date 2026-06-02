package controlplane

import "github.com/openagentsinc/bahia/internal/kinds"

// DNS control-plane kinds are aliases to the canonical internal/kinds catalog.
// Phase 0 intentionally does not modify reactor.go subscriptions or handlers.
const (
	KindDNSZoneCreateRequest      = kinds.DNSZoneCreateRequest
	KindDNSPolicyApplyRequest     = kinds.DNSPolicyApplyRequest
	KindDNSRecordOverrideRequest  = kinds.DNSRecordOverrideRequest
	KindDNSDriftRemediateRequest  = kinds.DNSDriftRemediateRequest
	KindDNSBackendRegisterRequest = kinds.DNSBackendRegisterRequest

	KindDNSOperationStatus = kinds.DNSOperationStatus

	KindDNSZoneCreateResult      = kinds.DNSZoneCreateResult
	KindDNSPolicyApplyResult     = kinds.DNSPolicyApplyResult
	KindDNSRecordOverrideResult  = kinds.DNSRecordOverrideResult
	KindDNSDriftRemediateResult  = kinds.DNSDriftRemediateResult
	KindDNSBackendRegisterResult = kinds.DNSBackendRegisterResult
)

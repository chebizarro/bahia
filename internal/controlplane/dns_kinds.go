package controlplane

import "github.com/openagentsinc/bahia/internal/kinds"

// DNS control-plane kinds are aliases to the canonical internal/kinds catalog.
// These aliases do not modify reactor.go subscriptions or handlers.
const (
	KindDNSZoneCreateRequest      = kinds.DNSZoneCreateRequest
	KindDNSPolicyApplyRequest     = kinds.DNSPolicyApplyRequest
	KindDNSRecordOverrideRequest  = kinds.DNSRecordOverrideRequest
	KindDNSDriftRemediateRequest  = kinds.DNSDriftRemediateRequest
	KindDNSBackendRegisterRequest = kinds.DNSBackendRegisterRequest
	KindDNSOverrideRetireRequest  = kinds.DNSOverrideRetireRequest

	KindDNSOperationStatus = kinds.DNSOperationStatus

	KindDNSZoneCreateResult      = kinds.DNSZoneCreateResult
	KindDNSPolicyApplyResult     = kinds.DNSPolicyApplyResult
	KindDNSRecordOverrideResult  = kinds.DNSRecordOverrideResult
	KindDNSDriftRemediateResult  = kinds.DNSDriftRemediateResult
	KindDNSBackendRegisterResult = kinds.DNSBackendRegisterResult
	KindDNSOverrideRetireResult  = kinds.DNSOverrideRetireResult
)

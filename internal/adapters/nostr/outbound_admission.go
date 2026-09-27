package nostr

import (
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// The outbound admission controller lives in the leaf package nostrout so that
// every gateway — this RelayPool, the SoulFactory relay bus, the Signet
// management client, and NIP-46 signer RPCs — shares one process-wide
// controller without import cycles. These aliases keep the adapter-facing API
// stable; there is no second implementation or wrapper-owned state.
type (
	OutboundAdmission        = nostrout.Admission
	OutboundAdmissionConfig  = nostrout.Config
	OutboundPurpose          = nostrout.Purpose
	OutboundPurposeBudget    = nostrout.PurposeBudget
	OutboundAdmissionMetrics = nostrout.Metrics
	OutboundAdmissionState   = nostrout.State
)

const (
	OutboundPurposePriority = nostrout.PurposePriority
	OutboundPurposeState    = nostrout.PurposeState
	OutboundPurposeGeneral  = nostrout.PurposeGeneral
	OutboundPurposeBulk     = nostrout.PurposeBulk
	OutboundPurposeSigner   = nostrout.PurposeSigner
)

var (
	ErrOutboundBudgetExceeded = nostrout.ErrBudgetExceeded
	ErrOutboundCircuitOpen    = nostrout.ErrCircuitOpen
	ErrOutboundKillSwitch     = nostrout.ErrKillSwitch
	ErrOutboundInFlight       = nostrout.ErrInFlight
)

// DefaultOutboundAdmission returns the process-wide controller.
func DefaultOutboundAdmission() *OutboundAdmission { return nostrout.Default() }

// NewOutboundAdmission constructs an isolated controller. Production wiring
// must use DefaultOutboundAdmission so every gateway shares one budget.
func NewOutboundAdmission(cfg OutboundAdmissionConfig) *OutboundAdmission { return nostrout.New(cfg) }

// DefaultOutboundAdmissionConfig returns the bounded default configuration.
func DefaultOutboundAdmissionConfig() OutboundAdmissionConfig { return nostrout.DefaultConfig() }

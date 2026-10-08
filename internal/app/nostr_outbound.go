package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// nostrOutboundAdmissionConfig maps the configured nostr.outbound section onto
// the process-wide controller configuration. Zero values keep nostrout's
// bounded defaults; no configured value can disable admission, and a kill
// switch file the operator did not configure still resolves from
// BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE for processes whose config did not go
// through internal/config's environment mapping.
func nostrOutboundAdmissionConfig(cfg config.NostrOutboundConfig) nostrout.Config {
	lane := func(c config.NostrOutboundLaneConfig) nostrout.PurposeBudget {
		return nostrout.PurposeBudget{RatePerMinute: c.RatePerMinute, Burst: c.Burst}
	}
	killSwitchFile := strings.TrimSpace(cfg.KillSwitchFile)
	if killSwitchFile == "" {
		killSwitchFile = strings.TrimSpace(os.Getenv(nostrout.KillSwitchEnv))
	}
	return nostrout.Config{
		KillSwitchFile: killSwitchFile,
		Aggregate:      lane(cfg.Aggregate),
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: lane(cfg.Lanes.Priority),
			nostrout.PurposeState:    lane(cfg.Lanes.State),
			nostrout.PurposeGeneral:  lane(cfg.Lanes.General),
			nostrout.PurposeBulk:     lane(cfg.Lanes.Bulk),
			nostrout.PurposeSigner:   lane(cfg.Lanes.Signer),
		},
		RelayWire:         lane(cfg.RelayWire),
		RelayWirePriority: lane(cfg.RelayWirePriority),
		BreakerMin:        cfg.BreakerMin,
		BreakerMax:        cfg.BreakerMax,
		DuplicateTTL:      cfg.DuplicateTTL,
		DuplicateLimit:    cfg.DuplicateLimit,
	}
}

// nostrOutboundAdmissionCheck renders the readiness/health view of the
// process-wide outbound controller. It fails while the kill switch is active
// and warns while the breaker is open or after budget rejections. Its details
// are content-free counters: never event bodies, tags, keys, or relay
// credentials.
func nostrOutboundAdmissionCheck(admission *nostrout.Admission) HealthCheck {
	state := admission.State()
	check := HealthCheck{
		Name:    "nostr_outbound_admission",
		Status:  HealthStatusPass,
		Message: "outbound publication admission is available",
		Details: map[string]string{
			"attempted":            fmt.Sprintf("%d", state.Metrics.Attempted),
			"admitted":             fmt.Sprintf("%d", state.Metrics.Admitted),
			"budget_rejected":      fmt.Sprintf("%d", state.Metrics.BudgetRejected),
			"circuit_rejected":     fmt.Sprintf("%d", state.Metrics.CircuitRejected),
			"duplicates":           fmt.Sprintf("%d", state.Metrics.Duplicates),
			"kill_switch_rejected": fmt.Sprintf("%d", state.Metrics.KillSwitchRejected),
			"relay_rate_limited":   fmt.Sprintf("%d", state.Metrics.RelayRateLimited),
			"in_flight_rejected":   fmt.Sprintf("%d", state.Metrics.InFlightRejected),
			"capacity_rejected":    fmt.Sprintf("%d", state.Metrics.CapacityRejected),
			"queue_rejected":       fmt.Sprintf("%d", state.Metrics.QueueRejected),
			"wire_attempts":        fmt.Sprintf("%d", state.Metrics.WireAttempts),
			"wire_rejected":        fmt.Sprintf("%d", state.Metrics.WireRejected),
			"auth_admitted":        fmt.Sprintf("%d", state.Metrics.AuthAdmitted),
			"opaque_admitted":      fmt.Sprintf("%d", state.Metrics.OpaqueAdmitted),
			"operations_started":   fmt.Sprintf("%d", state.Metrics.OperationsStarted),
			"operations_queued":    fmt.Sprintf("%d", state.Metrics.OperationsQueued),
			"operation_active":     fmt.Sprintf("%t", state.Metrics.OperationActive),
			"active_publications":  fmt.Sprintf("%d", state.Metrics.ActivePublications),
		},
	}
	if state.KillSwitchActive {
		check.Status = HealthStatusFail
		check.Message = "outbound publication kill switch is active"
		if state.KillSwitchError != "" {
			check.Details["kill_switch_error"] = state.KillSwitchError
		}
		return check
	}
	if state.CircuitOpen {
		check.Status = HealthStatusWarn
		check.Message = "outbound publication circuit breaker is open"
		check.Details["breaker_until"] = state.Metrics.BreakerUntil.UTC().Format(time.RFC3339Nano)
	} else if state.Metrics.BudgetRejected > 0 {
		check.Status = HealthStatusWarn
		check.Message = "outbound publication budget has rejected events"
	}
	return check
}

package nostr

import (
	"fmt"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// publicRuntimeFailureMetadata is a closed wire projection. The persisted map
// may contain legacy free-form provider text; copying it into signed 30900
// content would make credentials durable on every relay.
func publicRuntimeFailureMetadata(raw map[string]any) map[string]any {
	safe := make(map[string]any)
	if value, ok := raw["starting_since"].(string); ok {
		if at, err := time.Parse(time.RFC3339, value); err == nil {
			safe["starting_since"] = at.UTC().Format(time.RFC3339)
		}
	}
	if value, ok := raw["observed_health"].(string); ok && publicRuntimeHealth(value) {
		safe["observed_health"] = value
	}
	if value, ok := raw["unhealthy_evidence"].(string); ok {
		if evidence := publicRuntimeEvidence(value); evidence != "" {
			safe["unhealthy_evidence"] = evidence
		}
	}
	if value, ok := raw["reason"].(string); ok {
		switch value {
		case "auto_apply_failed", "environment_apply_lock_contended":
			safe["reason"] = value
		}
	}
	if _, exists := raw["message"]; exists {
		// The old map could hold arbitrary provider stderr. Even a redaction
		// regex cannot prove that an unlabelled secret has been removed.
		safe["message"] = "automatic desired-state application failed"
	}
	if value, ok := raw["failed_at"].(string); ok {
		if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
			safe["failed_at"] = at.UTC().Format(time.RFC3339Nano)
		}
	}
	if value, ok := raw["backoff"].(string); ok {
		if delay, err := time.ParseDuration(value); err == nil && delay > 0 {
			safe["backoff"] = delay.String()
		}
	}
	switch count := raw["failure_count"].(type) {
	case int:
		if count > 0 && count <= 1e9 {
			safe["failure_count"] = count
		}
	case int64:
		if count > 0 && count <= 1e9 {
			safe["failure_count"] = count
		}
	case float64: // Values decoded from legacy JSON maps.
		if count > 0 && count <= 1e9 && count == float64(int64(count)) {
			safe["failure_count"] = int64(count)
		}
	}
	return safe
}

func publicRuntimeHealth(value string) bool {
	switch domain.HealthStatus(value) {
	case domain.HealthStatusUnknown, domain.HealthStatusStarting, domain.HealthStatusHealthy, domain.HealthStatusUnhealthy:
		return true
	default:
		return false
	}
}

func publicRuntimeEvidence(raw string) string {
	const timeoutPrefix = "container did not become healthy within "
	if rest, ok := strings.CutPrefix(raw, timeoutPrefix); ok {
		duration, health, found := strings.Cut(rest, "; last observed health ")
		if found && publicRuntimeHealth(health) {
			if parsed, err := time.ParseDuration(duration); err == nil && parsed > 0 {
				return domain.SanitizeEvidence(fmt.Sprintf("%s%s; last observed health %s", timeoutPrefix, parsed, health))
			}
		}
	}
	const unhealthyPrefix = "desired configuration is applied but the runtime is "
	const unhealthySuffix = "; the deployment is not healthy"
	if rest, ok := strings.CutPrefix(raw, unhealthyPrefix); ok {
		if health, ok := strings.CutSuffix(rest, unhealthySuffix); ok && publicRuntimeHealth(health) {
			return domain.SanitizeEvidence(unhealthyPrefix + health + unhealthySuffix)
		}
	}
	return ""
}
